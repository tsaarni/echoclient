package worker

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// getActiveWorkers is a test helper to safely read activeWorkers.
func (wp *WorkerPool) getActiveWorkers() int64 {
	return wp.activeWorkers.Load()
}

// getTargetConcurrency is a test helper to safely read targetConcurrency.
func (wp *WorkerPool) getTargetConcurrency() int64 {
	return wp.targetConcurrency.Load()
}

func TestSetConcurrency(t *testing.T) {
	// Test the low-level SetConcurrency API directly
	wp := NewMultiStepWorkerPool(
		func(ctx context.Context, wp *WorkerPool) error {
			time.Sleep(10 * time.Millisecond)
			return nil
		},
		[]*Step{
			NewStep(
				WithDuration(0), // Infinite duration, so runProfileSteps won't overwrite concurrency
				WithConcurrency(1),
			),
		},
	)

	wp.Launch()
	defer wp.Stop()

	// Wait for step to start workers
	time.Sleep(150 * time.Millisecond)

	// Check initial state
	if got := wp.getActiveWorkers(); got != 1 {
		t.Errorf("expected 1 worker, got %d", got)
	}
	if got := wp.getTargetConcurrency(); got != 1 {
		t.Errorf("expected concurrency 1, got %d", got)
	}

	// Increase to 5 workers
	wp.SetConcurrency(5)
	time.Sleep(50 * time.Millisecond)
	if got := wp.getActiveWorkers(); got != 5 {
		t.Errorf("expected 5 workers, got %d", got)
	}

	// Decrease to 2 workers
	wp.SetConcurrency(2)
	time.Sleep(50 * time.Millisecond)
	if got := wp.getActiveWorkers(); got != 2 {
		t.Errorf("expected 2 workers, got %d", got)
	}
}

func TestProfileEasing(t *testing.T) {
	tests := []struct {
		name string
		f    EasingFunc
		t    float64
		want float64
	}{
		{"Linear-0", EasingLinear, 0, 0},
		{"Linear-0.5", EasingLinear, 0.5, 0.5},
		{"Linear-1", EasingLinear, 1, 1},
		{"EaseIn-0", EasingIn, 0, 0},
		{"EaseIn-0.5", EasingIn, 0.5, 0.25},
		{"EaseIn-1", EasingIn, 1, 1},
		{"EaseOut-0", EasingOut, 0, 0},
		{"EaseOut-0.5", EasingOut, 0.5, 0.75},
		{"EaseOut-1", EasingOut, 1, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.f(tt.t); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRunProfile(t *testing.T) {
	wp := NewWorkerPool(
		func(ctx context.Context, wp *WorkerPool) error {
			return nil
		},
		WithDuration(100*time.Millisecond),
		WithRateLimit(20, 20),
		WithConcurrency(2),
	)

	wp.Launch()
	wp.Wait()

	// Check final state
	if got := wp.getTargetConcurrency(); got != 2 {
		t.Errorf("expected final concurrency 2, got %d", got)
	}
	if wp.limiter.Limit() != rate.Limit(20) {
		t.Errorf("expected final RPS 20, got %v", wp.limiter.Limit())
	}
}

func TestRateLimitWithEasing(t *testing.T) {
	st := NewStep(
		WithDuration(time.Second),
		WithRateLimit(100, 100, EasingIn),
		WithConcurrency(10, EasingOut),
	)

	if st.rpsEasing(0.5) != 0.25 { // EaseIn(0.5) = 0.5*0.5 = 0.25
		t.Errorf("expected easingRPS(0.5) to be 0.25, got %v", st.rpsEasing(0.5))
	}
	if st.concurrencyEasing(0.5) != 0.75 { // EaseOut(0.5) = 0.5*(2-0.5) = 0.75
		t.Errorf("expected easingConcurrency(0.5) to be 0.75, got %v", st.concurrencyEasing(0.5))
	}
}

func TestDefaultEasing(t *testing.T) {
	st := NewStep(
		WithDuration(time.Second),
	)

	// Default should be nil, handled as linear by runner
	if st.rpsEasing != nil {
		t.Errorf("expected default rpsEasing to be nil")
	}
	if st.concurrencyEasing != nil {
		t.Errorf("expected default concurrencyEasing to be nil")
	}
}

func TestMultiStepWorkerPool(t *testing.T) {
	callCount := int32(0)

	worker := func(ctx context.Context, wp *WorkerPool) error {
		atomic.AddInt32(&callCount, 1)
		return nil
	}

	steps := []*Step{
		NewStep(
			WithDuration(50*time.Millisecond),
			WithConcurrency(2),
			WithRateLimit(100, 100),
		),
		NewStep(
			WithDuration(50*time.Millisecond),
			WithConcurrency(4),
			WithRateLimit(200, 200),
		),
	}

	wp := NewMultiStepWorkerPool(worker, steps)
	wp.Launch()
	wp.Wait()

	// Check final state
	if got := wp.getTargetConcurrency(); got != 4 {
		t.Errorf("expected final concurrency 4, got %d", got)
	}
	if wp.limiter.Limit() != rate.Limit(200) {
		t.Errorf("expected final RPS 200, got %v", wp.limiter.Limit())
	}
	if atomic.LoadInt32(&callCount) == 0 {
		t.Error("expected worker to be called at least once")
	}
}

func TestGlobalRepetitions(t *testing.T) {
	var callCount int64

	worker := func(ctx context.Context, wp *WorkerPool) error {
		atomic.AddInt64(&callCount, 1)
		return nil
	}

	wp := NewWorkerPool(
		worker,
		WithRepetitions(50),
		WithConcurrency(10),
	)

	wp.Launch()
	wp.Wait()

	got := atomic.LoadInt64(&callCount)
	if got != 50 {
		t.Errorf("expected exactly 50 calls with global repetitions, got %d", got)
	}
}

func TestRepetitionsAndDuration(t *testing.T) {
	// Case 1: Repetitions run out before Duration
	t.Run("RepetitionsPrecedence", func(t *testing.T) {
		var callCount int64
		// High duration, low repetitions
		// Should finish quickly
		wp := NewWorkerPool(
			func(ctx context.Context, wp *WorkerPool) error {
				atomic.AddInt64(&callCount, 1)
				return nil
			},
			WithDuration(5*time.Second), // Long duration
			WithRepetitions(10),         // Only 10 reps
			WithConcurrency(2),
		)

		start := time.Now()
		wp.Launch()
		wp.Wait()
		elapsed := time.Since(start)

		if elapsed > 2*time.Second {
			t.Errorf("expected to finish quickly due to repetition limit, took %v", elapsed)
		}

		got := atomic.LoadInt64(&callCount)
		if got != 10 {
			t.Errorf("expected 10 calls, got %d", got)
		}
	})

	// Case 2: Duration runs out before Repetitions
	t.Run("DurationPrecedence", func(t *testing.T) {
		var callCount int64
		// Short duration, high repetitions
		// Should finish at duration
		wp := NewWorkerPool(
			func(ctx context.Context, wp *WorkerPool) error {
				atomic.AddInt64(&callCount, 1)
				// Small sleep to ensure we don't accidentally burn through reps very fast (though 10M is safe)
				time.Sleep(1 * time.Microsecond)
				return nil
			},
			WithDuration(200*time.Millisecond),
			WithRepetitions(1000000), // Huge number
			WithConcurrency(10),
		)

		start := time.Now()
		wp.Launch()
		wp.Wait()
		elapsed := time.Since(start)

		// Allow some slack in timing
		if elapsed < 200*time.Millisecond {
			t.Errorf("finished too early: %v", elapsed)
		}

		got := atomic.LoadInt64(&callCount)
		if got < 100 {
			t.Errorf("expected some work done, got %d", got)
		}
		if got >= 1000000 {
			t.Errorf("should not have finished all repetitions")
		}
	})
}
