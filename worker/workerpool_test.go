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
	// Test the low-level SetConcurrency API directly.
	wp := NewMultiStepWorkerPool(
		func(ctx context.Context, wp *WorkerPool) error {
			time.Sleep(10 * time.Millisecond)
			return nil
		},
		[]*Step{
			NewStep(
				WithDuration(0), // Infinite duration, so runProfileSteps won't overwrite concurrency.
				WithConcurrency(1),
			),
		},
	)

	wp.Launch()
	defer wp.Stop()

	// Wait for step to start workers.
	time.Sleep(150 * time.Millisecond)

	// Check initial state.
	if got := wp.getActiveWorkers(); got != 1 {
		t.Errorf("expected 1 worker, got %d", got)
	}
	if got := wp.getTargetConcurrency(); got != 1 {
		t.Errorf("expected concurrency 1, got %d", got)
	}

	// Increase to 5 workers.
	wp.SetConcurrency(5)
	time.Sleep(50 * time.Millisecond)
	if got := wp.getActiveWorkers(); got != 5 {
		t.Errorf("expected 5 workers, got %d", got)
	}

	// Decrease to 2 workers.
	wp.SetConcurrency(2)
	time.Sleep(50 * time.Millisecond)
	if got := wp.getActiveWorkers(); got != 2 {
		t.Errorf("expected 2 workers, got %d", got)
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

	if st.rpsEasing(0.5) != 0.25 { // EaseIn(0.5) = 0.5*0.5 = 0.25.
		t.Errorf("expected easingRPS(0.5) to be 0.25, got %v", st.rpsEasing(0.5))
	}
	if st.concurrencyEasing(0.5) != 0.75 { // EaseOut(0.5) = 0.5*(2-0.5) = 0.75.
		t.Errorf("expected easingConcurrency(0.5) to be 0.75, got %v", st.concurrencyEasing(0.5))
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

	// Check final state.
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
	// Case 1: Repetitions run out before Duration.
	t.Run("RepetitionsPrecedence", func(t *testing.T) {
		var callCount int64
		wp := NewWorkerPool(
			func(ctx context.Context, wp *WorkerPool) error {
				atomic.AddInt64(&callCount, 1)
				return nil
			},
			WithDuration(5*time.Second), // Long duration.
			WithRepetitions(10),         // Only 10 reps.
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

	// Case 2: Duration runs out before Repetitions.
	t.Run("DurationPrecedence", func(t *testing.T) {
		var callCount int64
		wp := NewWorkerPool(
			func(ctx context.Context, wp *WorkerPool) error {
				atomic.AddInt64(&callCount, 1)
				// Small sleep to ensure we don't accidentally burn through reps very fast (though 10M is safe).
				time.Sleep(1 * time.Microsecond)
				return nil
			},
			WithDuration(200*time.Millisecond),
			WithRepetitions(1000000), // Huge number.
			WithConcurrency(10),
		)

		start := time.Now()
		wp.Launch()
		wp.Wait()
		elapsed := time.Since(start)

		// Allow some slack in timing.
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

func TestWithInfiniteRepetitions(t *testing.T) {
	var callCount int64

	wp := NewWorkerPool(
		func(ctx context.Context, wp *WorkerPool) error {
			atomic.AddInt64(&callCount, 1)
			return nil
		},
		WithInfiniteRepetitions(),
		WithDuration(100*time.Millisecond),
		WithConcurrency(2),
		WithRateLimit(100, 100),
	)

	wp.Launch()
	wp.Wait()

	got := atomic.LoadInt64(&callCount)
	// With infinite repetitions and 100ms duration at ~100 RPS, should have ~10+ calls
	if got < 5 {
		t.Errorf("expected at least 5 calls with infinite repetitions, got %d", got)
	}
}

func TestWithWorkerFunc(t *testing.T) {
	var defaultCalls, overrideCalls int64

	defaultWorker := func(ctx context.Context, wp *WorkerPool) error {
		atomic.AddInt64(&defaultCalls, 1)
		return nil
	}

	overrideWorker := func(ctx context.Context, wp *WorkerPool) error {
		atomic.AddInt64(&overrideCalls, 1)
		return nil
	}

	steps := []*Step{
		NewStep(
			WithDuration(50*time.Millisecond),
			WithConcurrency(1),
			WithRateLimit(50, 50),
		),
		NewStep(
			WithDuration(50*time.Millisecond),
			WithConcurrency(1),
			WithRateLimit(50, 50),
			WithWorkerFunc(overrideWorker),
		),
	}

	wp := NewMultiStepWorkerPool(defaultWorker, steps)
	wp.Launch()
	wp.Wait()

	if atomic.LoadInt64(&defaultCalls) == 0 {
		t.Error("expected default worker to be called in first step")
	}
	if atomic.LoadInt64(&overrideCalls) == 0 {
		t.Error("expected override worker to be called in second step")
	}
}

func TestWithHooks(t *testing.T) {
	var onStartCalled, onEndCalled bool

	onStart := func(ctx context.Context, wp *WorkerPool) {
		onStartCalled = true
	}

	onEnd := func(ctx context.Context, wp *WorkerPool) {
		onEndCalled = true
	}

	wp := NewWorkerPool(
		func(ctx context.Context, wp *WorkerPool) error {
			return nil
		},
		WithDuration(50*time.Millisecond),
		WithConcurrency(1),
		WithHooks(onStart, onEnd),
	)

	wp.Launch()
	wp.Wait()

	if !onStartCalled {
		t.Error("expected onStart hook to be called")
	}
	if !onEndCalled {
		t.Error("expected onEnd hook to be called")
	}
}

func TestSetWorker(t *testing.T) {
	var worker1Calls, worker2Calls int64

	worker1 := func(ctx context.Context, wp *WorkerPool) error {
		atomic.AddInt64(&worker1Calls, 1)
		time.Sleep(10 * time.Millisecond)
		return nil
	}

	worker2 := func(ctx context.Context, wp *WorkerPool) error {
		atomic.AddInt64(&worker2Calls, 1)
		time.Sleep(10 * time.Millisecond)
		return nil
	}

	wp := NewMultiStepWorkerPool(
		worker1,
		[]*Step{
			NewStep(
				WithDuration(0),
				WithConcurrency(1),
			),
		},
	)

	wp.Launch()
	time.Sleep(50 * time.Millisecond)

	// Change the worker function
	wp.SetWorker(worker2)
	time.Sleep(50 * time.Millisecond)

	wp.Stop()
	wp.Wait()

	if atomic.LoadInt64(&worker1Calls) == 0 {
		t.Error("expected worker1 to be called")
	}
	if atomic.LoadInt64(&worker2Calls) == 0 {
		t.Error("expected worker2 to be called after SetWorker")
	}
}

func TestLaunchWithContextEmptyProfile(t *testing.T) {
	wp := &WorkerPool{
		profile: []*Step{},
	}

	err := wp.Launch()
	if err == nil {
		t.Error("expected error for empty profile")
	}
	if err.Error() != "worker pool was initialized with no traffic profile steps" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestLaunchWithContextEasingWithoutDuration(t *testing.T) {
	wp := NewWorkerPool(
		func(ctx context.Context, wp *WorkerPool) error { return nil },
		WithConcurrency(10, EasingLinear),
		WithDuration(0), // Zero duration with easing should error.
	)

	err := wp.Launch()
	if err == nil {
		t.Error("expected error for easing without duration")
	}
}

func TestCalculateEasedValue(t *testing.T) {
	tests := []struct {
		name     string
		start    int
		target   int
		progress float64
		easing   EasingFunc
		want     int
	}{
		// Edge & Logic Checks.
		{"NilEasing", 0, 100, 0.5, nil, 100},
		{"MinValueClamp", 100, 1, 0.0, EasingLinear, 100},
		{"MinValueClampPositiveTarget", 0, 10, 0.05, EasingLinear, 1}, // Result would be 0.5, clamped to 1.
		{"NegativeToPositive", -10, 10, 0.5, EasingLinear, 1},         // Result would be 0, but clamped to 1 since target > 0.

		// Curve Verification (Start=0, Target=1000 allows verifying 3 decimal places).
		// Linear
		{"Linear-0", 0, 1000, 0, EasingLinear, 1},       // Clamped to 1
		{"Linear-0.5", 0, 1000, 0.5, EasingLinear, 500}, // 0.5 * 1000
		{"Linear-1", 0, 1000, 1, EasingLinear, 1000},
		// EaseIn (t^2)
		{"EaseIn-0", 0, 1000, 0, EasingIn, 1},       // Clamped to 1
		{"EaseIn-0.5", 0, 1000, 0.5, EasingIn, 250}, // 0.5^2 = 0.25 * 1000
		{"EaseIn-1", 0, 1000, 1, EasingIn, 1000},
		// EaseOut (t * (2-t))
		{"EaseOut-0", 0, 1000, 0, EasingOut, 1},       // Clamped to 1
		{"EaseOut-0.5", 0, 1000, 0.5, EasingOut, 750}, // 0.5 * 1.5 = 0.75 * 1000
		{"EaseOut-1", 0, 1000, 1, EasingOut, 1000},
		// EaseInOut
		{"EaseInOut-0", 0, 1000, 0, EasingInOut, 1},         // Clamped to 1
		{"EaseInOut-0.25", 0, 1000, 0.25, EasingInOut, 125}, // 2*0.25^2 = 0.125 * 1000
		{"EaseInOut-0.5", 0, 1000, 0.5, EasingInOut, 500},   // Transition point
		{"EaseInOut-0.75", 0, 1000, 0.75, EasingInOut, 875}, // -1 + (4-1.5)*0.75 = 0.875 * 1000
		{"EaseInOut-1", 0, 1000, 1, EasingInOut, 1000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateEasedValue(tt.start, tt.target, tt.progress, tt.easing)
			if got != tt.want {
				t.Errorf("calculateEasedValue(%d, %d, %f) = %d, want %d",
					tt.start, tt.target, tt.progress, got, tt.want)
			}
		})
	}
}

func TestSetConcurrencyNegative(t *testing.T) {
	wp := NewWorkerPool(
		func(ctx context.Context, wp *WorkerPool) error {
			time.Sleep(10 * time.Millisecond)
			return nil
		},
		WithDuration(0),
		WithConcurrency(5),
	)

	wp.Launch()
	time.Sleep(50 * time.Millisecond)

	// Set negative concurrency should be ignored.
	wp.SetConcurrency(-5)
	time.Sleep(20 * time.Millisecond)

	// Should still have 5 workers.
	if got := wp.getActiveWorkers(); got != 5 {
		t.Errorf("expected 5 workers after negative SetConcurrency, got %d", got)
	}

	wp.Stop()
	wp.Wait()
}

func TestLaunchWithContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var callCount int64
	wp := NewWorkerPool(
		func(ctx context.Context, wp *WorkerPool) error {
			atomic.AddInt64(&callCount, 1)
			time.Sleep(10 * time.Millisecond)
			return nil
		},
		WithDuration(0), // Infinite duration.
		WithConcurrency(5),
	)

	wp.LaunchWithContext(ctx)
	time.Sleep(50 * time.Millisecond)

	// Cancel the context.
	cancel()

	// Should finish quickly.
	done := make(chan struct{})
	go func() {
		wp.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Good, finished as expected.
	case <-time.After(2 * time.Second):
		t.Error("worker pool did not stop after context cancellation")
	}

	// Should have done some work.
	if atomic.LoadInt64(&callCount) == 0 {
		t.Error("expected some work to be done before cancellation")
	}
}

func TestStopWithoutLaunch(t *testing.T) {
	wp := NewWorkerPool(
		func(ctx context.Context, wp *WorkerPool) error { return nil },
		WithConcurrency(1),
	)

	// Calling Stop without Launch should succeed.
	wp.Stop()
}

func TestWorkerStop(t *testing.T) {
	var callCount atomic.Int64
	var activeWorkers atomic.Int64

	// A worker that runs once and returns ErrStopWorker.
	workerFunc := func(ctx context.Context, wp *WorkerPool) error {
		callCount.Add(1)
		activeWorkers.Store(wp.activeWorkers.Load()) // Capture active count
		time.Sleep(10 * time.Millisecond)
		return ErrStopWorker
	}

	wp := NewWorkerPool(workerFunc, WithConcurrency(1), WithDuration(100*time.Millisecond))

	// Launch and wait.
	wp.Launch()
	wp.Wait()

	count := callCount.Load()
	t.Logf("Worker executed %d times", count)

	if count > 1 {
		t.Fatalf("Worker did not stop, executed %d times", count)
	}
}

// TestWorkerStopConcurrent verifies multiple workers stopping independently.
func TestWorkerStopConcurrent(t *testing.T) {
	var callCount atomic.Int64

	// Worker stops immediately.
	workerFunc := func(ctx context.Context, wp *WorkerPool) error {
		callCount.Add(1)
		return ErrStopWorker
	}

	// 5 workers, long duration.
	wp := NewWorkerPool(workerFunc, WithConcurrency(5), WithDuration(100*time.Millisecond))

	wp.Launch()
	wp.Wait()

	count := callCount.Load()
	t.Logf("Total executions: %d", count)

	if count > 5 {
		t.Errorf("Workers did not stop reliably, executed %d times", count)
	}
}

func TestLongRunningWorker(t *testing.T) {
	workerFunc := func(ctx context.Context, wp *WorkerPool) error {
		time.Sleep(500 * time.Millisecond) // Takes 500ms to complete.
		return nil
	}

	wp := NewWorkerPool(workerFunc, WithRepetitions(1), WithDuration(0))

	start := time.Now()
	wp.Launch()
	wp.Wait()
	duration := time.Since(start)
	// Should have taken at least 500ms.
	if duration < 500*time.Millisecond {
		t.Errorf("worker pool did not run for at least 500ms, got %s", duration)
	}
}
