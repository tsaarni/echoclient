// Package worker provides core logic for the worker pool and task orchestration.
package worker

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tsaarni/echoclient/metrics"
)

// ErrStopWorker is returned by a WorkerFunc to signal that the worker should stop.
var ErrStopWorker = errors.New("stop worker")

// contextKeyScheduledTime is a context key for the scheduled request time.
type contextKeyScheduledTime struct{}

// ScheduledTimeFromContext extracts the scheduled time from the context.
// If no scheduled time is present (e.g., unlimited rate, or direct usage), returns zero time.
func ScheduledTimeFromContext(ctx context.Context) time.Time {
	if t, ok := ctx.Value(contextKeyScheduledTime{}).(time.Time); ok {
		return t
	}
	return time.Time{}
}

// contextWithScheduledTime returns a new context with the scheduled time set.
func contextWithScheduledTime(ctx context.Context, t time.Time) context.Context {
	return context.WithValue(ctx, contextKeyScheduledTime{}, t)
}

// WorkerPool manages concurrent execution of traffic generating WorkerFuncs.
type WorkerPool struct {
	targetConcurrency atomic.Int64   // targetConcurrency is the desired number of workers.
	activeWorkers     atomic.Int64   // activeWorkers is the current number of running workers.
	wg                sync.WaitGroup // Tracks traffic profile execution and worker goroutines.
	worker            WorkerFunc     // The worker function to execute for generating traffic.
	workerMu          sync.RWMutex   // Protects fields that can be updated at runtime.
	scheduler         *RequestScheduler // scheduler is the GCRA-based request scheduler with CO tracking.
	profile           []*Step        // profile holds the traffic profile steps for execution.
	isUnlimited       atomic.Bool    // isUnlimited indicates if the current step has unlimited repetitions.
	remainingReps     atomic.Int64   // remainingReps is remaining worker function calls (when isUnlimited is false).
	ctx               context.Context
	cancel            context.CancelFunc

	rpsAtom atomic.Int64 // Current RPS; 0 means unlimited (read by RequestScheduler.Take)
}

// NewWorkerPool creates a new worker pool.
// The worker function is required. Options configure the traffic profile.
// This is equivalent to NewMultiStepWorkerPool with a single traffic profile step.
func NewWorkerPool(worker WorkerFunc, opts ...Option) *WorkerPool {
	return NewMultiStepWorkerPool(worker, []*Step{NewStep(opts...)})
}

// NewMultiStepWorkerPool creates a new worker pool with multiple traffic profile steps.
// The worker function is required, but can be overridden in each step with worker.WithWorkerFunc() option.
func NewMultiStepWorkerPool(worker WorkerFunc, steps []*Step) *WorkerPool {
	wp := &WorkerPool{
		worker:    worker,
		scheduler: NewRequestScheduler(),
		profile:   steps,
	}
	wp.isUnlimited.Store(true)

	return wp
}

// Launch starts the worker pool and executes all steps in the traffic profile.
// Use Wait() to block until all workers complete.
func (wp *WorkerPool) Launch() error {
	return wp.LaunchWithContext(context.Background())
}

// LaunchWithContext starts the worker pool with the provided context and executes all steps in the traffic profile.
func (wp *WorkerPool) LaunchWithContext(ctx context.Context) error {
	// Check that profile is not empty.
	if len(wp.profile) == 0 {
		return errors.New("worker pool was initialized with no traffic profile steps")
	}

	// Check that each step has valid configuration.
	for i, st := range wp.profile {
		if (st.concurrencyEasing != nil || st.rpsEasing != nil) && st.duration == 0 {
			return errors.New("traffic profile step " + strconv.Itoa(i) + " has easing functions but zero duration")
		}
		if !st.pause && st.concurrency <= 0 {
			return errors.New("traffic profile step " + strconv.Itoa(i) + " has non-positive concurrency")
		}
		if st.rps < 0 {
			return errors.New("traffic profile step " + strconv.Itoa(i) + " has negative RPS")
		}
	}

	wp.ctx, wp.cancel = context.WithCancel(ctx)

	wp.wg.Go(func() {
		wp.runProfileSteps()
	})

	return nil
}

// Wait blocks until all workers and traffic profile executor has completed.
func (wp *WorkerPool) Wait() {
	wp.wg.Wait()
}

// Stop signals all workers to stop immediately.
func (wp *WorkerPool) Stop() {
	if wp.cancel != nil {
		wp.cancel()
	}
}

// SetRateLimit updates the rate limiter's rate and burst at runtime.
func (wp *WorkerPool) SetRateLimit(rps int, burst int) {
	if rps > 0 {
		wp.rpsAtom.Store(int64(rps))
		maxCatchup := time.Duration(float64(max(burst, 1)) / float64(rps) * float64(time.Second))
		wp.scheduler.SetMaxCatchup(maxCatchup)
	} else {
		wp.rpsAtom.Store(0)
	}
}

// SetConcurrency changes the number of concurrent workers at runtime.
// When scaling up, new workers are spawned immediately.
// When scaling down, running workers will self-terminate on their next iteration
func (wp *WorkerPool) SetConcurrency(n int) {
	if n < 0 {
		return
	}

	target := int64(n)
	wp.targetConcurrency.Store(target)

	// Only spawn workers if the pool has been launched (ctx is initialized).
	if wp.ctx == nil {
		return
	}

	current := wp.activeWorkers.Load()
	toSpawn := target - current
	for range toSpawn {
		wp.activeWorkers.Add(1)
		metrics.WorkerPoolActiveWorkers.Inc()
		wp.wg.Add(1)
		go wp.runWorker()
	}
}

// SetWorker updates the worker function.
func (wp *WorkerPool) SetWorker(f WorkerFunc) {
	wp.workerMu.Lock()
	wp.worker = f
	wp.workerMu.Unlock()
}

// GetWorker retrieves the current worker function.
func (wp *WorkerPool) GetWorker() WorkerFunc {
	wp.workerMu.RLock()
	defer wp.workerMu.RUnlock()
	return wp.worker
}

// runWorker executes the worker loop.
func (wp *WorkerPool) runWorker() {
	defer wp.wg.Done()

	// Ensure activeWorkers is decremented on exit only once.
	decrementedActive := false
	defer func() {
		if !decrementedActive {
			wp.activeWorkers.Add(-1)
			metrics.WorkerPoolActiveWorkers.Dec()
		}
	}()

	for {
		// Check for cancellation.
		select {
		case <-wp.ctx.Done():
			return
		default:
		}

		// Handle scale down if needed.
		if wp.tryScaleDown(&decrementedActive) {
			return
		}

		// Try to claim a repetition. If no more repetitions remain, exit.
		if !wp.shouldContinue() {
			return
		}

		// Take() handles both pacing and context enrichment.
		// When rate-limited, it blocks until the scheduled time and returns a context
		// with the scheduled time embedded. When unlimited, it returns ctx unchanged.
		workerCtx, err := wp.scheduler.Take(wp.ctx, &wp.rpsAtom)
		if err != nil {
			return // Context cancelled.
		}

		worker := wp.GetWorker()
		err = worker(workerCtx, wp)

		// Record CO-corrected latency at the worker level.
		if st := ScheduledTimeFromContext(workerCtx); !st.IsZero() {
			metrics.SchedulerRequestLatencySeconds.WithLabelValues().Observe(time.Since(st).Seconds())
		}

		if errors.Is(err, ErrStopWorker) {
			return
		}
	}
}

// tryScaleDown handles scaling down the worker pool when activeWorkers > targetConcurrency.
// Returns true if the worker should exit, false otherwise.
func (wp *WorkerPool) tryScaleDown(decrementedActive *bool) bool {
	for {
		active := wp.activeWorkers.Load()
		target := wp.targetConcurrency.Load()
		if active <= target {
			// No scale down needed.
			return false
		}
		// Try to claim exit slot by decrementing activeWorkers.
		if wp.activeWorkers.CompareAndSwap(active, active-1) {
			// Successfully claimed exit slot.
			*decrementedActive = true
			metrics.WorkerPoolActiveWorkers.Dec()
			return true
		}
		// CAS failed, another worker modified activeWorkers. Retry.
	}
}

// shouldContinue decides whether the worker should continue executing based on the remaining repetitions.
func (wp *WorkerPool) shouldContinue() bool {
	if wp.isUnlimited.Load() {
		return true
	}

	if wp.remainingReps.Add(-1) < 0 {
		return false
	}

	return true
}

// runProfileSteps executes the traffic profile steps on the worker pool.
// It iterates over each step, gradually adjusting RPS and concurrency according to the easing function.
func (wp *WorkerPool) runProfileSteps() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	startRate := 1        // Start with 1 RPS unless overridden by step.
	startConcurrency := 1 // Start with 1 worker unless overridden by step.

	for _, st := range wp.profile {
		startRate, startConcurrency = wp.executeStep(st, ticker, startRate, startConcurrency)
		if wp.ctx.Err() != nil {
			return
		}
	}

	// After all steps complete, stop the pool
	wp.Stop()
}

// executeStep runs a single traffic profile step.
// It returns the final RPS and concurrency, which serve as start values for the next step.
func (wp *WorkerPool) executeStep(st *Step, ticker *time.Ticker, startRate, startConcurrency int) (int, int) {
	if st.onStart != nil {
		st.onStart(wp.ctx, wp)
	}
	defer func() {
		if st.onEnd != nil {
			st.onEnd(wp.ctx, wp)
		}
	}()

	// Handle pause steps: just wait for the duration.
	if st.pause {
		select {
		case <-time.After(st.duration):
		case <-wp.ctx.Done():
		}
		return startRate, startConcurrency
	}

	// Apply step configuration: set global repetitions counter.
	if st.repetitions > 0 {
		wp.isUnlimited.Store(false)
		wp.remainingReps.Store(int64(st.repetitions))
	} else {
		wp.isUnlimited.Store(true)
	}

	if st.workerFunc != nil {
		wp.SetWorker(st.workerFunc)
	}

	// Reset per-step coordinated omission state.
	wp.scheduler.Reset(time.Now())

	if st.duration == 0 {
		return wp.executeUneasedStep(st, ticker)
	}
	return wp.executeTimedStep(st, ticker, startRate, startConcurrency)
}

// executeUneasedStep runs a step with zero duration and no easing functions.
// It applies the target configuration immediately and waits for completion (if repetitions > 0)
// or runs indefinitely until cancellation.
func (wp *WorkerPool) executeUneasedStep(st *Step, ticker *time.Ticker) (int, int) {
	targetRate := st.rps
	targetBurst := st.burst
	targetConcurrency := st.concurrency

	// Apply target configuration immediately.
	wp.SetRateLimit(targetRate, targetBurst)
	wp.SetConcurrency(targetConcurrency)

	if st.repetitions > 0 {
		// Infinite duration but finite repetitions.
		for wp.remainingReps.Load() > 0 || wp.activeWorkers.Load() > 0 {
			select {
			case <-wp.ctx.Done():
				return targetRate, targetConcurrency
			case <-ticker.C:
			}
		}
	} else {
		// Infinite duration and infinite repetitions: run until cancelled.
		<-wp.ctx.Done()
	}

	return targetRate, targetConcurrency
}

// executeTimedStep runs a step with a specific duration, applying easing if configured.
func (wp *WorkerPool) executeTimedStep(st *Step, ticker *time.Ticker, startRate, startConcurrency int) (int, int) {
	targetRate := st.rps
	targetBurst := st.burst
	targetConcurrency := st.concurrency
	startTime := time.Now()

	for {
		now := time.Now()
		elapsed := now.Sub(startTime)

		// Check if repetitions satisfied and workers finished.
		// TODO: use workgroup instead of polling?
		if st.repetitions > 0 && wp.remainingReps.Load() <= 0 && wp.activeWorkers.Load() == 0 {
			return targetRate, targetConcurrency
		}

		if elapsed >= st.duration {
			// Duration ended: ensure we hit the exact target at the end.
			wp.SetRateLimit(targetRate, targetBurst)
			wp.SetConcurrency(targetConcurrency)
			return targetRate, targetConcurrency
		}

		progress := float64(elapsed) / float64(st.duration)

		// Calculate and apply current parameters with easing.
		currentRPS := calculateEasedValue(startRate, targetRate, progress, st.rpsEasing)
		currentConcurrency := calculateEasedValue(startConcurrency, targetConcurrency, progress, st.concurrencyEasing)

		wp.SetRateLimit(currentRPS, targetBurst)
		wp.SetConcurrency(currentConcurrency)

		select {
		case <-wp.ctx.Done():
			return currentRPS, currentConcurrency
		case <-ticker.C:
		}
	}
}

// calculateEasedValue computes the current value between start and target using an easing function.
func calculateEasedValue(start, target int, progress float64, easingFunc func(float64) float64) int {
	if easingFunc == nil {
		return target
	}
	easedProgress := easingFunc(progress)
	value := int(float64(start) + easedProgress*float64(target-start))
	// Ensure minimum of 1 if target is positive.
	if value < 1 && target > 0 {
		return 1
	}
	return value
}
