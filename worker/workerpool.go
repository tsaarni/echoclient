package worker

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// WorkerFunc defines the function signature for work to be performed by the pool.
type WorkerFunc func(ctx context.Context) error

// WorkerPool manages concurrent execution of WorkerFuncs.
type WorkerPool struct {
	concurrency int
	repetitions int // 0 means unlimited repetitions
	wg          sync.WaitGroup
	ctx         context.Context
	cancel      context.CancelFunc
	worker      WorkerFunc
	limiter     *rate.Limiter
}

type WorkerPoolOption func(*WorkerPool)

// WithConcurrency sets the number of concurrent workers.
func WithConcurrency(n int) WorkerPoolOption {
	return func(wp *WorkerPool) {
		if n > 0 {
			wp.concurrency = n
		}
	}
}

// WithRepetitions sets the number of repetitions per worker.
// If not set or set to 0, workers will run indefinitely until stopped.
func WithRepetitions(n int) WorkerPoolOption {
	return func(wp *WorkerPool) {
		if n > 0 {
			wp.repetitions = n
		}
	}
}

// WithInfiniteRepetitions configures the worker pool to run indefinitely.
func WithInfiniteRepetitions() WorkerPoolOption {
	return func(wp *WorkerPool) {
		wp.repetitions = 0
	}
}

// WithRateLimit sets the global number of operations per second across the workers in the pool.
func WithRateLimit(rps, burst int) WorkerPoolOption {
	return func(wp *WorkerPool) {
		if rps > 0 {
			wp.limiter = rate.NewLimiter(rate.Limit(rps), burst)
		}
	}
}

// WithTimeout sets a timeout for the entire worker pool operation.
func WithTimeout(timeout time.Duration) WorkerPoolOption {
	return func(wp *WorkerPool) {
		if timeout > 0 {
			wp.ctx, wp.cancel = context.WithTimeout(context.Background(), timeout)
		}
	}
}

// NewWorkerPool creates a new worker pool with the given options.
func NewWorkerPool(worker WorkerFunc, opts ...WorkerPoolOption) *WorkerPool {
	wp := &WorkerPool{
		concurrency: 1,
		worker:      worker,
	}
	for _, o := range opts {
		o(wp)
	}

	return wp
}

// Launch starts the worker pool with the given context and runs the worker function in parallel.
func (wp *WorkerPool) Launch() *WorkerPool {
	if wp.ctx == nil {
		wp.ctx, wp.cancel = context.WithCancel(context.Background())
	}

	for i := 0; i < wp.concurrency; i++ {
		wp.wg.Add(1)
		go func() {
			defer wp.wg.Done()
			for j := 0; wp.repetitions == 0 || j < wp.repetitions; j++ {
				select {
				case <-wp.ctx.Done():
					return
				default:
					if wp.limiter != nil {
						if err := wp.limiter.Wait(wp.ctx); err != nil {
							return
						}
					}
					_ = wp.worker(wp.ctx)
				}
			}
		}()
	}
	return wp
}

// Wait blocks until all workers have completed.
func (wp *WorkerPool) Wait() {
	wp.wg.Wait()
}

// Stop signals all workers to stop after their current operation.
func (wp *WorkerPool) Stop() {
	if wp.cancel != nil {
		wp.cancel()
	}
}

// SetRateLimit updates the rate limiter's rate and burst at runtime.
func (wp *WorkerPool) SetRateLimit(rps int, burst int) {
	if wp.limiter != nil && rps > 0 {
		wp.limiter.SetLimit(rate.Limit(rps))
		wp.limiter.SetBurst(burst)
	}
}
