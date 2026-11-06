package worker

import (
	"context"
	"errors"
	"math/rand/v2"
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

// configErrorFunc returns a WorkerFunc that aborts the WorkerPool with err.
func configErrorFunc(err error) WorkerFunc {
	return func(ctx context.Context, wp *WorkerPool) error {
		wp.Abort(err)
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
