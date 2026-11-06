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

	onWorkerStart WorkerStartFunc // Protected by workerMu.
	onWorkerExit  WorkerExitFunc  // Protected by workerMu.

	resultMu sync.Mutex
	finished bool  // Stop or Abort was called. Protected by resultMu.
	result   error // Error passed to Abort. Protected by resultMu.

	pauseMu sync.Mutex
	resume  chan struct{} // Closed when a pause ends. Nil when not paused. Protected by pauseMu.
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
// It returns the error passed to Abort, or nil.
func (wp *WorkerPool) Wait() error {
	wp.wg.Wait()
	wp.resultMu.Lock()
	defer wp.resultMu.Unlock()
	return wp.result
}

// Stop ends the run. Wait returns nil.
// In-flight worker function calls are canceled through their context.
func (wp *WorkerPool) Stop() {
	wp.finish(nil)
}

// Abort ends the run because of err. Wait returns err.
// The first call to Stop or Abort decides the result returned by Wait.
func (wp *WorkerPool) Abort(err error) {
	wp.finish(err)
}

func (wp *WorkerPool) finish(err error) {
	if wp.cancel == nil {
		return
	}
	wp.resultMu.Lock()
	defer wp.resultMu.Unlock()
	if wp.finished {
		return
	}
	wp.finished, wp.result = true, err
	wp.cancel()
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

// SetWorkerHooks sets callbacks that run when each worker starts and exits.
// Call it before Launch. Either callback may be nil.
func (wp *WorkerPool) SetWorkerHooks(onStart WorkerStartFunc, onExit WorkerExitFunc) {
	wp.workerMu.Lock()
	wp.onWorkerStart = onStart
	wp.onWorkerExit = onExit
	wp.workerMu.Unlock()
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

	wp.workerMu.RLock()
	onStart, onExit := wp.onWorkerStart, wp.onWorkerExit
	wp.workerMu.RUnlock()

	ctx := wp.ctx
	if onStart != nil {
		startCtx, err := onStart(ctx, wp)
		if err != nil {
			if wp.ctx.Err() == nil {
				metrics.WorkerErrorsTotal.Inc()
			}
			return
		}
		if startCtx != nil {
			ctx = startCtx
		}
	}
	if onExit != nil {
		defer onExit(ctx, wp)
	}

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

		// During a pause, wait and then check again.
		if wp.waitWhilePaused() {
			continue
		}

		// Try to claim a repetition. If no more repetitions remain, exit.
		if !wp.shouldContinue() {
			return
		}

		// Take() handles both pacing and context enrichment.
		// When rate-limited, it blocks until the scheduled time and returns a context
		// with the scheduled time embedded. When unlimited, it returns ctx unchanged.
		workerCtx, err := wp.scheduler.Take(ctx, &wp.rpsAtom)
		if err != nil {
			return // Context cancelled.
		}

		worker := wp.GetWorker()
		err = worker(workerCtx, wp)
		if wp.ctx.Err() != nil {
			return // Calls interrupted by the end of the run are not counted.
		}
		metrics.WorkerCallsTotal.Inc()
		if err != nil {
			metrics.WorkerErrorsTotal.Inc()
		}

		// Record CO-corrected latency at the worker level.
		if st := ScheduledTimeFromContext(workerCtx); !st.IsZero() {
			metrics.SchedulerRequestLatencySeconds.WithLabelValues().Observe(time.Since(st).Seconds())
		}
	}
}

// setPaused pauses or resumes all workers.
func (wp *WorkerPool) setPaused(paused bool) {
	wp.pauseMu.Lock()
	defer wp.pauseMu.Unlock()
	if paused && wp.resume == nil {
		wp.resume = make(chan struct{})
	} else if !paused && wp.resume != nil {
		close(wp.resume)
		wp.resume = nil
	}
}

// waitWhilePaused blocks until the pause ends or the pool stops.
// It returns true if it waited.
func (wp *WorkerPool) waitWhilePaused() bool {
	wp.pauseMu.Lock()
	resume := wp.resume
	wp.pauseMu.Unlock()
	if resume == nil {
		return false
	}
	select {
	case <-resume:
	case <-wp.ctx.Done():
	}
	return true
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

	// Pause: workers stay alive but do not call the worker function.
	if st.pause {
		wp.setPaused(true)
		defer wp.setPaused(false)
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

	return wp.runStep(st, ticker, startRate, startConcurrency)
}

// runStep applies the rate and concurrency of a step until the step ends.
// The step ends when its duration has passed, or when its repetitions are used
// and all workers have exited. Without duration and repetitions it runs until the pool stops.
//
// On every tick, eased values are updated and workers that have exited are replaced.
// Values without easing are set once, so SetRateLimit and SetConcurrency can change them.
func (wp *WorkerPool) runStep(st *Step, ticker *time.Ticker, startRate, startConcurrency int) (int, int) {
	startTime := time.Now()

	// Final values of the step. Easing to rps 0 ends at 1 rps.
	finalRate := calculateEasedValue(startRate, st.rps, 1, st.rpsEasing)
	finalConcurrency := calculateEasedValue(startConcurrency, st.concurrency, 1, st.concurrencyEasing)

	wp.SetRateLimit(calculateEasedValue(startRate, st.rps, 0, st.rpsEasing), st.burst)
	wp.SetConcurrency(calculateEasedValue(startConcurrency, st.concurrency, 0, st.concurrencyEasing))

	for {
		select {
		case <-wp.ctx.Done():
			return finalRate, finalConcurrency
		case <-ticker.C:
		}

		elapsed := time.Since(startTime)
		if st.duration > 0 && elapsed >= st.duration {
			return finalRate, finalConcurrency
		}

		// When repetitions are used, wait for workers to exit and do not replace them.
		if st.repetitions > 0 && wp.remainingReps.Load() <= 0 {
			if wp.activeWorkers.Load() == 0 {
				return finalRate, finalConcurrency
			}
			continue
		}

		// Easing requires a duration, so progress is only used when duration > 0.
		progress := 1.0
		if st.duration > 0 {
			progress = float64(elapsed) / float64(st.duration)
		}
		if st.rpsEasing != nil {
			wp.SetRateLimit(calculateEasedValue(startRate, st.rps, progress, st.rpsEasing), st.burst)
		}
		concurrency := int(wp.targetConcurrency.Load())
		if st.concurrencyEasing != nil {
			concurrency = calculateEasedValue(startConcurrency, st.concurrency, progress, st.concurrencyEasing)
		}
		wp.SetConcurrency(concurrency)
	}
}

// calculateEasedValue computes the current value between start and target using an easing function.
func calculateEasedValue(start, target int, progress float64, easingFunc func(float64) float64) int {
	if easingFunc == nil {
		return target
	}
	easedProgress := easingFunc(progress)
	value := int(float64(start) + easedProgress*float64(target-start))
	// Keep the value at 1 or more while easing from or to a positive value.
	// Zero means unlimited RPS.
	if value < 1 && (target > 0 || start > 0) {
		return 1
	}
	return value
}
