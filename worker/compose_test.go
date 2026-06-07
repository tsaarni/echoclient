package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestMix(t *testing.T) {
	var countA atomic.Int64
	var countB atomic.Int64

	var workerA WorkerFunc = func(ctx context.Context, wp *WorkerPool) error {
		countA.Add(1)
		return nil
	}
	var workerB WorkerFunc = func(ctx context.Context, wp *WorkerPool) error {
		countB.Add(1)
		return nil
	}

	// Test the .Weighted() helper method using relative weights (sum = 10)
	mixed := Mix(
		workerA.Weighted(7),
		workerB.Weighted(3),
	)

	ctx := context.Background()
	runs := 10000
	for i := 0; i < runs; i++ {
		err := mixed(ctx, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	a := countA.Load()
	b := countB.Load()

	// Expecting ~70% and ~30% with a reasonable tolerance (e.g., +/- 3% or 300 runs)
	expectedA := int64(7000)
	tolerance := int64(300)

	if a < expectedA-tolerance || a > expectedA+tolerance {
		t.Errorf("expected countA to be close to 7000, got %d", a)
	}
	if b < (int64(runs)-expectedA)-tolerance || b > (int64(runs)-expectedA)+tolerance {
		t.Errorf("expected countB to be close to 3000, got %d", b)
	}
}

func TestMixEdgeCases(t *testing.T) {
	// Empty choices should return a no-op that does not fail
	mixedEmpty := Mix()
	if err := mixedEmpty(context.Background(), nil); err != nil {
		t.Errorf("expected no error for empty Mix, got %v", err)
	}

	// Zero/Negative weights should not cause panic or infinite loop
	var dummy WorkerFunc = func(ctx context.Context, wp *WorkerPool) error { return nil }
	mixedZero := Mix(
		dummy.Weighted(0),
		dummy.Weighted(-5),
	)
	if err := mixedZero(context.Background(), nil); err != nil {
		t.Errorf("expected no error for zero/negative weight Mix, got %v", err)
	}
}

func TestCompositionValidation(t *testing.T) {
	t.Run("MixNilFunc", func(t *testing.T) {
		mixed := Mix(Weighted{Weight: 10, Func: nil})
		err := mixed(context.Background(), nil)
		if !errors.Is(err, ErrStopWorker) {
			t.Errorf("expected ErrStopWorker, got %v", err)
		}
	})
}
