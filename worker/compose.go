package worker

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"sync"
	"time"
)

// Weighted groups a function with its relative selection weight.
type Weighted struct {
	Weight int
	Func   WorkerFunc
}

// Weighted wraps this WorkerFunc with its relative selection weight for Mix().
func (f WorkerFunc) Weighted(weight int) Weighted {
	return Weighted{
		Weight: weight,
		Func:   f,
	}
}

// configErrorFunc returns a WorkerFunc that logs the configuration error once,
// stops the WorkerPool, and returns ErrStopWorker to prevent further iterations.
func configErrorFunc(err error) WorkerFunc {
	var logOnce sync.Once
	return func(ctx context.Context, wp *WorkerPool) error {
		logOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "Worker configuration error: %v\n", err)
			if wp != nil {
				wp.Stop()
			}
		})
		return ErrStopWorker
	}
}

// sleepCtx sleeps for duration d while respecting context cancellation.
// Bypasses timer creation if d <= 0.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Mix selects and executes one of the provided choices based on their relative weights.
//
// Weights do not need to sum to 100 (though they can if desired). Only the relative relation
// (ratio) between the weights matters.
//
// If choices are empty or the total weight is <= 0, it returns a no-op WorkerFunc.
func Mix(choices ...Weighted) WorkerFunc {
	for _, choice := range choices {
		if choice.Func == nil {
			return configErrorFunc(errors.New("worker.Mix: nil WorkerFunc provided"))
		}
	}

	if len(choices) == 0 {
		return func(ctx context.Context, wp *WorkerPool) error {
			return nil
		}
	}

	var totalWeight int
	for _, choice := range choices {
		if choice.Weight < 0 {
			continue // Ignore negative weights
		}
		totalWeight += choice.Weight
	}

	return func(ctx context.Context, wp *WorkerPool) error {
		if totalWeight <= 0 {
			return nil
		}

		r := rand.IntN(totalWeight)
		var current int
		for _, choice := range choices {
			if choice.Weight <= 0 {
				continue
			}
			current += choice.Weight
			if r < current {
				return choice.Func(ctx, wp)
			}
		}
		return nil
	}
}

// Retry executes this WorkerFunc up to 'attempts' times.
// It pauses for 'delay' between attempts, and respects context cancellation.
func (f WorkerFunc) Retry(attempts int, delay time.Duration) WorkerFunc {
	if f == nil {
		return configErrorFunc(errors.New("worker: cannot retry nil WorkerFunc"))
	}
	if attempts <= 0 {
		return configErrorFunc(errors.New("worker: retry attempts must be greater than zero"))
	}
	if delay < 0 {
		return configErrorFunc(errors.New("worker: retry delay must be non-negative"))
	}

	return func(ctx context.Context, wp *WorkerPool) error {
		var lastErr error
		for i := 0; i < attempts; i++ {
			if err := ctx.Err(); err != nil {
				return err
			}

			err := f(ctx, wp)
			if err == nil {
				return nil
			}
			lastErr = err

			if i < attempts-1 {
				if err := sleepCtx(ctx, delay); err != nil {
					return err
				}
			}
		}
		if lastErr != nil {
			return lastErr
		}
		return errors.New("retry attempts exhausted with no execution")
	}
}

// RetryWithBackoff executes this WorkerFunc up to 'attempts' times with exponential backoff and jitter.
// The delay starts at 'minDelay', doubles on each failure, and is capped at 'maxDelay'.
// A randomized jitter is applied so the actual delay is a random duration between 0 and the current backoff limit.
// It respects context cancellation.
func (f WorkerFunc) RetryWithBackoff(attempts int, minDelay, maxDelay time.Duration) WorkerFunc {
	if f == nil {
		return configErrorFunc(errors.New("worker: cannot retry nil WorkerFunc"))
	}
	if attempts <= 0 {
		return configErrorFunc(errors.New("worker: retry attempts must be greater than zero"))
	}
	if minDelay < 0 {
		return configErrorFunc(errors.New("worker: minDelay must be non-negative"))
	}
	if maxDelay < minDelay {
		return configErrorFunc(errors.New("worker: maxDelay must be greater than or equal to minDelay"))
	}

	return func(ctx context.Context, wp *WorkerPool) error {
		var lastErr error
		currentLimit := minDelay

		for i := 0; i < attempts; i++ {
			if err := ctx.Err(); err != nil {
				return err
			}

			err := f(ctx, wp)
			if err == nil {
				return nil
			}
			lastErr = err

			if i < attempts-1 {
				var jitteredDelay time.Duration
				if currentLimit > 0 {
					jitteredDelay = rand.N(currentLimit)
				}

				if err := sleepCtx(ctx, jitteredDelay); err != nil {
					return err
				}

				// Double the limit for the next attempt, capped at maxDelay.
				nextLimit := float64(currentLimit) * 2.0
				if nextLimit >= float64(maxDelay) {
					currentLimit = maxDelay
				} else {
					currentLimit = time.Duration(nextLimit)
				}
			}
		}
		if lastErr != nil {
			return lastErr
		}
		return errors.New("retry attempts exhausted with no execution")
	}
}
