package worker

import (
	"context"
	"errors"
	"math/rand/v2"
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

// Mix selects and executes one of the provided choices based on their relative weights.
//
// Weights do not need to sum to 100 (though they can if desired). Only the relative relation
// (ratio) between the weights matters. For example, weights of 4 and 1 yield the exact same
// 80/20 distribution as weights of 80 and 20.
//
// If choices are empty or the total weight is <= 0, it returns a no-op WorkerFunc.
func Mix(choices ...Weighted) WorkerFunc {
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
	return func(ctx context.Context, wp *WorkerPool) error {
		var lastErr error
		for i := 0; i < attempts; i++ {
			err := f(ctx, wp)
			if err == nil {
				return nil
			}
			lastErr = err

			if i < attempts-1 {
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					return ctx.Err()
				case <-timer.C:
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
	return func(ctx context.Context, wp *WorkerPool) error {
		var lastErr error
		currentLimit := minDelay

		for i := 0; i < attempts; i++ {
			err := f(ctx, wp)
			if err == nil {
				return nil
			}
			lastErr = err

			if i < attempts-1 {
				// Full Jitter: select a random duration between 0 and currentLimit.
				var jitteredDelay time.Duration
				if currentLimit > 0 {
					jitteredDelay = rand.N(currentLimit)
				}

				timer := time.NewTimer(jitteredDelay)
				select {
				case <-ctx.Done():
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					return ctx.Err()
				case <-timer.C:
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
