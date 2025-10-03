package worker

import (
	"context"
	"sync"

	"golang.org/x/time/rate"
)

// WorkerFunc defines the function signature for work to be performed by the pool.
type WorkerFunc func(ctx context.Context) error

// WorkerPool manages concurrent execution of WorkerFuncs.
type WorkerPool struct {
	concurrency int
	repetitions int // 0 means unlimited repetitions
	wg          sync.WaitGroup
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

// NewWorkerPool creates a new WorkerPool with the given worker function and options.
func NewWorkerPool(worker WorkerFunc, opts ...WorkerPoolOption) *WorkerPool {
	wp := &WorkerPool{
		concurrency: 1,
		repetitions: 0,
		worker:      worker,
	}
	for _, o := range opts {
		o(wp)
	}
	return wp
}

// Launch starts the worker pool, running the worker function in parallel.
func (pool *WorkerPool) Launch() *WorkerPool {
	ctx, cancel := context.WithCancel(context.Background())
	pool.cancel = cancel
	for i := 0; i < pool.concurrency; i++ {
		pool.wg.Add(1)
		go func() {
			defer pool.wg.Done()
			for j := 0; pool.repetitions == 0 || j < pool.repetitions; j++ {
				if pool.limiter != nil {
					if err := pool.limiter.Wait(ctx); err != nil {
						return
					}
				} else {
					select {
					case <-ctx.Done():
						return
					default:
					}
				}
				_ = pool.worker(ctx)
			}
		}()
	}
	return pool
}

func (pool *WorkerPool) Wait() {
	pool.wg.Wait()
}

func (pool *WorkerPool) Stop() {
	pool.cancel()
}

// SetRateLimit updates the rate limiter's rate and burst at runtime.
func (pool *WorkerPool) SetRateLimit(rps int, burst int) {
	if pool.limiter != nil && rps > 0 {
		pool.limiter.SetLimit(rate.Limit(rps))
		pool.limiter.SetBurst(burst)
	}
}
