package worker

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// getActiveWorkers is a test helper to safely read activeWorkers.
func (wp *WorkerPool) getActiveWorkers() int64 {
	return wp.activeWorkers.Load()
}

func TestSetConcurrencyAfterWorkersTerminate(t *testing.T) {
	// Test that SetConcurrency spawns workers even when targetConcurrency
	// is already at the desired value but workers have terminated.
	callCount := atomic.Int64{}
	workerFunc := func(ctx context.Context, wp *WorkerPool) error {
		callCount.Add(1)
		return nil
	}

	profile := []*Step{
		NewStep(
			WithConcurrency(10),
			WithRepetitions(10), // Workers will terminate after 10 calls.
		),
		NewStep(
			WithConcurrency(10), // Same concurrency as step 1.
			WithRepetitions(10),
		),
	}

	wp := NewMultiStepWorkerPool(workerFunc, profile)
	_ = wp.Launch()
	wp.Wait()

	// Should have completed 20 calls total (10 from each step).
	if got := callCount.Load(); got != 20 {
		t.Errorf("expected 20 calls, got %d", got)
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

	_ = wp.Launch()
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
	_ = wp.Launch()
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

	// 5 workers, limit to exactly 5 repetitions so no re-spawning beyond that.
	wp := NewWorkerPool(workerFunc, WithConcurrency(5), WithRepetitions(5))

	_ = wp.Launch()
	wp.Wait()

	count := callCount.Load()
	t.Logf("Total executions: %d", count)

	if count > 5 {
		t.Errorf("Workers did not stop reliably, executed %d times", count)
	}
}
