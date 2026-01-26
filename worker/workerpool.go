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

// WorkerPool manages concurrent execution of WorkerFuncs.
type WorkerPool struct {
	// targetConcurrency is the desired number of workers.
	targetConcurrency atomic.Int64

	// activeWorkers is the current number of running workers.
	activeWorkers atomic.Int64

	wg       sync.WaitGroup // Tracks profile execution and worker goroutines.
	ctx      context.Context
	cancel   context.CancelFunc
	worker   WorkerFunc   // The worker function to execute for generating traffic.
	workerMu sync.RWMutex // Protects worker field.

	// limiter is the rate limiter for the worker pool.
	limiter *rate.Limiter

	// profile holds the traffic profile steps for execution.
	profile []*Step

	// remainingReps is the global repetition counter.
	// -1 means unlimited.
	remainingReps atomic.Int64
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
		// targetConcurrency and activeWorkers default to 0 (atomic.Int64 zero value).
		// remainingReps defaults to 0, which we set to -1 (unlimited) below.
	}
	wp.remainingReps.Store(-1)

	return wp
}

// Launch starts the worker pool and executes all steps in the traffic profile.
// Use Wait() to block until all workers complete.
func (wp *WorkerPool) Launch() (*WorkerPool, error) {
	return wp.LaunchWithContext(context.Background())
}

// LaunchWithContext starts the worker pool with the provided context and executes all steps in the traffic profile.
func (wp *WorkerPool) LaunchWithContext(ctx context.Context) (*WorkerPool, error) {
	// Check that profile is not empty.
	if len(wp.profile) == 0 {
		return nil, errors.New("worker pool was initialized with no traffic profile steps")
	}

	// Check that all steps that have easing functions also have non-zero duration.
	for i, st := range wp.profile {
		if (st.concurrencyEasing != nil || st.rpsEasing != nil) && st.duration == 0 {
			return nil, errors.New("traffic profile step " + strconv.Itoa(i) + " has easing functions but zero duration")
		}
	}

	wp.ctx, wp.cancel = context.WithCancel(ctx)

	// Track profile step execution goroutine.
	wp.wg.Add(1)
	go func() {
		defer wp.wg.Done()
		wp.runProfileSteps()
	}()

	return wp, nil
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

	// Track whether this worker has already decremented activeWorkers.
	// This ensures we only decrement once, regardless of exit path.
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

		// If activeWorkers > targetConcurrency, decrement activeWorkers and exit.
		for {
			active := wp.activeWorkers.Load()
			target := wp.targetConcurrency.Load()
			if active <= target {
				// No scale down needed, continue working.
				break
			}
			// Try to claim exit slot by decrementing activeWorkers.
			if wp.activeWorkers.CompareAndSwap(active, active-1) {
				// Successfully claimed exit slot.
				decrementedActive = true
				metrics.WorkerPoolActiveWorkers.Dec()
				return
			}
			// CAS failed, another worker modified activeWorkers. Retry.
		}

		// Check global repetition limit using atomic operations.
		// remainingReps == -1 means unlimited repetitions.
		remainingReps := wp.remainingReps.Load()
		if remainingReps >= 0 {
			// Finite repetitions: try to claim one
			remaining := wp.remainingReps.Add(-1)
			if remaining < 0 {
				// No more repetitions available, restore and exit
				wp.remainingReps.Add(1)
				return
			}
		}

		// Wait for rate limiter.
		if err := wp.limiter.Wait(wp.ctx); err != nil {
			return
		}

		worker := wp.GetWorker()
		_ = worker(wp.ctx, wp)
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
		return wp.executeImmediateStep(st, ticker)
	}
	return wp.executeTimedStep(st, ticker, startRate, startConcurrency)
}

// executeImmediateStep runs a step with zero duration.
// It applies the target configuration immediately and waits for completion (if repetitions > 0)
// or runs indefinitely until cancellation.
func (wp *WorkerPool) executeImmediateStep(st *Step, ticker *time.Ticker) (int, int) {
	targetRate := st.rps
	targetBurst := st.burst
	targetConcurrency := st.concurrency

	// Apply target configuration immediately.
	wp.SetRateLimit(targetRate, targetBurst)
	wp.SetConcurrency(targetConcurrency)

	if st.repetitions > 0 {
		// Wait for all global repetitions to complete.
		for {
			active := wp.activeWorkers.Load()

			remaining := wp.remainingReps.Load()
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

		t := float64(elapsed) / float64(st.duration)

		// Calculate current RPS.
		currentRPS := targetRate
		if st.rpsEasing != nil {
			easedRPS := st.rpsEasing(t)
			currentRPS = int(float64(startRate) + easedRPS*float64(targetRate-startRate))
		}

		// Calculate current concurrency.
		currentConcurrency := targetConcurrency
		if st.concurrencyEasing != nil {
			easedConcurrency := st.concurrencyEasing(t)
			currentConcurrency = int(float64(startConcurrency) + easedConcurrency*float64(targetConcurrency-startConcurrency))
		}

		// Burst is not subject to easing.
		currentBurst := targetBurst

		// Ensure minimums.
		if currentRPS < 1 && targetRate > 0 {
			currentRPS = 1
		}
		if currentConcurrency < 1 && targetConcurrency > 0 {
			currentConcurrency = 1
		}

		// Update pool.
		wp.SetRateLimit(currentRPS, currentBurst)
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
