package worker

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tsaarni/echoclient/metrics"
	"golang.org/x/time/rate"
)

// ErrStopWorker is returned by a WorkerFunc to signal that the worker should stop.
var ErrStopWorker = errors.New("stop worker")

// WorkerPool manages concurrent execution of traffic generating WorkerFuncs.
type WorkerPool struct {
	targetConcurrency atomic.Int64   // targetConcurrency is the desired number of workers.
	activeWorkers     atomic.Int64   // activeWorkers is the current number of running workers.
	wg                sync.WaitGroup // Tracks traffic profile execution and worker goroutines.
	worker            WorkerFunc     // The worker function to execute for generating traffic.
	workerMu          sync.RWMutex   // Protects fields that can be updated at runtime.
	limiter           *rate.Limiter  // limiter is the rate limiter for the worker pool.
	profile           []*Step        // profile holds the traffic profile steps for execution.
	remainingReps     atomic.Int64   // remainingReps is remaining worker function calls (or -1 for unlimited).
	ctx               context.Context
	cancel            context.CancelFunc
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
		worker:  worker,
		limiter: rate.NewLimiter(rate.Inf, 0), // Start with no rate limit.
		profile: steps,
	}
	wp.remainingReps.Store(-1)

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

	// Check that all steps that have easing functions also have non-zero duration.
	for i, st := range wp.profile {
		if (st.concurrencyEasing != nil || st.rpsEasing != nil) && st.duration == 0 {
			return errors.New("traffic profile step " + strconv.Itoa(i) + " has easing functions but zero duration")
		}
	}

	wp.ctx, wp.cancel = context.WithCancel(ctx)

	wp.wg.Add(1)
	go func() {
		defer wp.wg.Done()
		wp.runProfileSteps()
	}()

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
	wp.workerMu.Lock()
	defer wp.workerMu.Unlock()
	if rps > 0 {
		wp.limiter.SetLimit(rate.Limit(rps))
		wp.limiter.SetBurst(max(burst, 1))
	} else {
		// Disable limit
		wp.limiter.SetLimit(rate.Inf)
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
	oldTarget := wp.targetConcurrency.Swap(target)

	for i := oldTarget; i < target; i++ {
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

		// Try to claim a repetition slot.
		if !wp.tryClaimRepetition() {
			return
		}

		// Wait for rate limiter.
		if err := wp.limiter.Wait(wp.ctx); err != nil {
			return
		}

		worker := wp.GetWorker()
		if err := worker(wp.ctx, wp); errors.Is(err, ErrStopWorker) {
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

// tryClaimRepetition attempts to claim a repetition slot.
// Returns true if the worker should continue, false if repetitions are exhausted.
func (wp *WorkerPool) tryClaimRepetition() bool {
	for {
		remainingReps := wp.remainingReps.Load()
		// remainingReps == -1 means unlimited repetitions.
		if remainingReps < 0 {
			return true // Unlimited repetitions, continue.
		}
		if remainingReps == 0 {
			return false // No more repetitions available.
		}
		// Try to atomically claim one repetition.
		if wp.remainingReps.CompareAndSwap(remainingReps, remainingReps-1) {
			return true
		}
		// CAS failed, another worker modified remainingReps. Retry.
	}
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

	// Apply step configuration: set global repetitions counter.
	// Convert 0 (user-facing "unlimited") to -1 (internal sentinel).
	if st.repetitions > 0 {
		wp.remainingReps.Store(int64(st.repetitions))
	} else {
		wp.remainingReps.Store(-1)
	}

	if st.workerFunc != nil {
		wp.SetWorker(st.workerFunc)
	}

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
		for {
			active := wp.activeWorkers.Load()
			remaining := wp.remainingReps.Load()

			// Check if all repetitions are done.
			// TODO: move this to workgroup instead of busy-waiting?
			if active == 0 || remaining <= 0 {
				break
			}

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

		// Check if repetitions satisfied.
		if st.repetitions > 0 && wp.remainingReps.Load() <= 0 {
			return currentRPS, currentConcurrency
		}

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
