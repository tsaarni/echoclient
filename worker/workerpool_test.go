package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/tsaarni/echoclient/metrics"
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
	_ = wp.Wait()
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
		{"RampDownToZero", 100, 0, 0.995, EasingLinear, 1},            // Result would be 0, clamped to 1.
		{"RampDownToZeroEnd", 100, 0, 1, EasingOut, 1},                // Clamped to 1.
		{"UnlimitedToUnlimited", 0, 0, 0.5, EasingLinear, 0},

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

func TestRampDownToZeroStaysRateLimited(t *testing.T) {
	// Easing RPS to 0 keeps the rate limit until the end of the step.
	var calls atomic.Int64
	wp := NewMultiStepWorkerPool(func(ctx context.Context, wp *WorkerPool) error {
		calls.Add(1)
		return nil
	}, []*Step{
		NewStep(WithDuration(500*time.Millisecond), WithRateLimit(100, 100)),
		NewStep(WithDuration(time.Second), WithRateLimit(0, 0, EasingOut)),
	})
	_ = wp.Launch()
	_ = wp.Wait()
	// Expected ~50 (step 1) + ~33 (step 2).
	if got := calls.Load(); got > 200 {
		t.Errorf("expected rate-limited calls (~83), got %d", got)
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
	_ = wp.Wait()
}

func TestStopWithoutLaunch(t *testing.T) {
	wp := NewWorkerPool(
		func(ctx context.Context, wp *WorkerPool) error { return nil },
		WithConcurrency(1),
	)

	// Calling Stop without Launch should succeed.
	wp.Stop()
}

func TestSetWorker(t *testing.T) {
	var callCountA atomic.Int64
	var callCountB atomic.Int64

	workerA := func(ctx context.Context, wp *WorkerPool) error {
		callCountA.Add(1)
		wp.SetWorker(func(ctx context.Context, wp *WorkerPool) error {
			callCountB.Add(1)
			return nil
		})
		return nil
	}

	wp := NewWorkerPool(workerA, WithConcurrency(1), WithRepetitions(2))
	_ = wp.Launch()
	_ = wp.Wait()
	if callCountA.Load() != 1 {
		t.Errorf("expected workerA to run 1 time, got %d", callCountA.Load())
	}
	if callCountB.Load() != 1 {
		t.Errorf("expected workerB to run 1 time, got %d", callCountB.Load())
	}
}

func TestWithInfiniteRepetitions(t *testing.T) {
	st := NewStep(WithInfiniteRepetitions())
	if st.repetitions != 0 {
		t.Errorf("expected repetitions to be 0, got %d", st.repetitions)
	}
}

func TestSetConcurrencyBeforeLaunch(t *testing.T) {
	wp := NewWorkerPool(func(ctx context.Context, wp *WorkerPool) error { return nil })
	wp.SetConcurrency(5)
	if wp.targetConcurrency.Load() != 5 {
		t.Errorf("expected targetConcurrency to be 5, got %d", wp.targetConcurrency.Load())
	}
	if wp.getActiveWorkers() != 0 {
		t.Errorf("expected activeWorkers to be 0, got %d", wp.getActiveWorkers())
	}
}

func TestExecuteUneasedStepInfinite(t *testing.T) {
	var callCount atomic.Int64
	wp := NewWorkerPool(func(ctx context.Context, wp *WorkerPool) error {
		callCount.Add(1)
		time.Sleep(10 * time.Millisecond)
		return nil
	}, WithConcurrency(1))

	_ = wp.Launch()
	time.Sleep(50 * time.Millisecond)
	wp.Stop()
	_ = wp.Wait()
	if callCount.Load() == 0 {
		t.Error("expected at least some calls to run in infinite step")
	}
}

func TestWorkerErrorsCounted(t *testing.T) {
	before := testutil.ToFloat64(metrics.WorkerErrorsTotal)

	var calls atomic.Int64
	wp := NewWorkerPool(func(ctx context.Context, wp *WorkerPool) error {
		if calls.Add(1)%2 == 0 {
			return errors.New("failed")
		}
		return nil
	}, WithConcurrency(2), WithRepetitions(10))
	_ = wp.Launch()
	_ = wp.Wait()
	if got := testutil.ToFloat64(metrics.WorkerErrorsTotal) - before; got != 5 {
		t.Errorf("expected 5 errors, got %v", got)
	}
}

func TestWorkerErrorsNotCountedOnStop(t *testing.T) {
	before := testutil.ToFloat64(metrics.WorkerErrorsTotal)

	wp := NewWorkerPool(func(ctx context.Context, wp *WorkerPool) error {
		<-ctx.Done()
		return ctx.Err()
	}, WithConcurrency(3))
	_ = wp.Launch()
	time.Sleep(50 * time.Millisecond)
	wp.Stop()
	_ = wp.Wait()

	if got := testutil.ToFloat64(metrics.WorkerErrorsTotal) - before; got != 0 {
		t.Errorf("expected 0 errors, got %v", got)
	}
}

type testStateKey struct{}

func TestWorkerHooks(t *testing.T) {
	var starts, exits, calls, missing atomic.Int64
	wp := NewWorkerPool(func(ctx context.Context, wp *WorkerPool) error {
		calls.Add(1)
		if _, ok := ctx.Value(testStateKey{}).(int64); !ok {
			missing.Add(1)
		}
		time.Sleep(time.Millisecond)
		return nil
	}, WithConcurrency(3))
	wp.SetWorkerHooks(
		func(ctx context.Context, wp *WorkerPool) (context.Context, error) {
			return context.WithValue(ctx, testStateKey{}, starts.Add(1)), nil
		},
		func(ctx context.Context, wp *WorkerPool) {
			if _, ok := ctx.Value(testStateKey{}).(int64); ok {
				exits.Add(1)
			}
		},
	)
	_ = wp.Launch()
	time.Sleep(50 * time.Millisecond)
	wp.Stop()
	_ = wp.Wait()
	if starts.Load() != 3 || exits.Load() != 3 {
		t.Errorf("expected 3 starts and 3 exits, got %d and %d", starts.Load(), exits.Load())
	}
	if calls.Load() == 0 || missing.Load() != 0 {
		t.Errorf("expected all %d calls to see worker state, %d did not", calls.Load(), missing.Load())
	}
}

func TestWorkerHooksScaleDown(t *testing.T) {
	var starts, exits atomic.Int64
	wp := NewMultiStepWorkerPool(func(ctx context.Context, wp *WorkerPool) error {
		time.Sleep(time.Millisecond)
		return nil
	}, []*Step{
		NewStep(WithConcurrency(5), WithDuration(200*time.Millisecond)),
		NewStep(WithConcurrency(1), WithDuration(400*time.Millisecond)),
	})
	wp.SetWorkerHooks(
		func(ctx context.Context, wp *WorkerPool) (context.Context, error) {
			starts.Add(1)
			return ctx, nil
		},
		func(ctx context.Context, wp *WorkerPool) {
			exits.Add(1)
		},
	)
	_ = wp.Launch()
	time.Sleep(400 * time.Millisecond) // Step 2 starts within 100ms after 200ms.
	if got := exits.Load(); got != 4 {
		t.Errorf("expected 4 exits after scale down, got %d", got)
	}
	_ = wp.Wait()
	if starts.Load() != 5 || exits.Load() != 5 {
		t.Errorf("expected 5 starts and 5 exits, got %d and %d", starts.Load(), exits.Load())
	}
}

func TestWorkerHooksStartError(t *testing.T) {
	before := testutil.ToFloat64(metrics.WorkerErrorsTotal)

	var calls, exits atomic.Int64
	wp := NewWorkerPool(func(ctx context.Context, wp *WorkerPool) error {
		calls.Add(1)
		return nil
	}, WithConcurrency(2), WithDuration(250*time.Millisecond))
	wp.SetWorkerHooks(
		func(ctx context.Context, wp *WorkerPool) (context.Context, error) {
			return ctx, errors.New("connect failed")
		},
		func(ctx context.Context, wp *WorkerPool) {
			exits.Add(1)
		},
	)
	_ = wp.Launch()
	_ = wp.Wait()
	if calls.Load() != 0 || exits.Load() != 0 {
		t.Errorf("expected no calls and no exits, got %d and %d", calls.Load(), exits.Load())
	}
	if got := testutil.ToFloat64(metrics.WorkerErrorsTotal) - before; got < 2 {
		t.Errorf("expected at least 2 errors, got %v", got)
	}
}

func TestAbort(t *testing.T) {
	reason := errors.New("upstream broken")
	var canceled atomic.Bool
	wp := NewWorkerPool(func(ctx context.Context, wp *WorkerPool) error {
		select {
		case <-ctx.Done():
			canceled.Store(true)
		case <-time.After(5 * time.Second):
		}
		return nil
	}, WithConcurrency(2))
	_ = wp.Launch()
	time.Sleep(20 * time.Millisecond)
	wp.Abort(reason)
	wp.Abort(errors.New("second"))
	wp.Stop()

	if err := wp.Wait(); !errors.Is(err, reason) {
		t.Errorf("expected %v, got %v", reason, err)
	}
	if !canceled.Load() {
		t.Error("expected in-flight calls to be canceled")
	}
}

func TestStopBeforeAbort(t *testing.T) {
	wp := NewWorkerPool(func(ctx context.Context, wp *WorkerPool) error {
		wp.Stop()
		wp.Abort(errors.New("ignored"))
		return nil
	}, WithConcurrency(1))
	_ = wp.Launch()
	if err := wp.Wait(); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestWorkerCallsCounted(t *testing.T) {
	before := testutil.ToFloat64(metrics.WorkerCallsTotal)
	wp := NewWorkerPool(func(ctx context.Context, wp *WorkerPool) error {
		return nil
	}, WithConcurrency(3), WithRepetitions(30))
	_ = wp.Launch()
	_ = wp.Wait()
	if got := testutil.ToFloat64(metrics.WorkerCallsTotal) - before; got != 30 {
		t.Errorf("expected 30 calls, got %v", got)
	}
}

func TestStepWithoutDurationReplacesExitedWorkers(t *testing.T) {
	// The start hook fails for the first 3 workers. A step without duration
	// must replace them and still complete its repetitions.
	var starts, calls atomic.Int64
	wp := NewWorkerPool(func(ctx context.Context, wp *WorkerPool) error {
		calls.Add(1)
		return nil
	}, WithConcurrency(2), WithRepetitions(10))
	wp.SetWorkerHooks(func(ctx context.Context, wp *WorkerPool) (context.Context, error) {
		if starts.Add(1) <= 3 {
			return ctx, errors.New("connect failed")
		}
		return ctx, nil
	}, nil)
	_ = wp.Launch()

	done := make(chan struct{})
	go func() { _ = wp.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		wp.Stop()
		<-done
		t.Fatal("pool did not finish")
	}
	if calls.Load() != 10 {
		t.Errorf("expected 10 calls, got %d", calls.Load())
	}
}

func TestNoWorkersStartedAfterRepetitions(t *testing.T) {
	var starts atomic.Int64
	wp := NewMultiStepWorkerPool(func(ctx context.Context, wp *WorkerPool) error {
		return nil
	}, []*Step{
		NewStep(WithConcurrency(2), WithRepetitions(4), WithDuration(500*time.Millisecond)),
	})
	wp.SetWorkerHooks(func(ctx context.Context, wp *WorkerPool) (context.Context, error) {
		starts.Add(1)
		return ctx, nil
	}, nil)
	_ = wp.Launch()
	_ = wp.Wait()
	if starts.Load() != 2 {
		t.Errorf("expected 2 worker starts, got %d", starts.Load())
	}
}

func TestPauseKeepsWorkersIdle(t *testing.T) {
	var calls, starts, exits atomic.Int64
	wp := NewMultiStepWorkerPool(func(ctx context.Context, wp *WorkerPool) error {
		calls.Add(1)
		return nil
	}, []*Step{
		NewStep(WithConcurrency(2), WithRateLimit(100, 100), WithDuration(300*time.Millisecond)),
		NewStep(WithPause(500 * time.Millisecond)),
		NewStep(WithConcurrency(2), WithRateLimit(100, 100), WithDuration(300*time.Millisecond)),
	})
	wp.SetWorkerHooks(
		func(ctx context.Context, wp *WorkerPool) (context.Context, error) {
			starts.Add(1)
			return ctx, nil
		},
		func(ctx context.Context, wp *WorkerPool) { exits.Add(1) },
	)
	_ = wp.Launch()

	time.Sleep(500 * time.Millisecond) // Pause starts within 100ms after 300ms.
	before := calls.Load()
	time.Sleep(200 * time.Millisecond)
	if got := calls.Load() - before; got != 0 {
		t.Errorf("expected no calls during pause, got %d", got)
	}
	if exits.Load() != 0 {
		t.Errorf("expected workers to stay alive during pause, got %d exits", exits.Load())
	}

	_ = wp.Wait()
	if calls.Load() <= before {
		t.Error("expected calls after pause")
	}
	if starts.Load() != 2 {
		t.Errorf("expected 2 worker starts, got %d", starts.Load())
	}
}

func TestSetConcurrencyInTimedStep(t *testing.T) {
	wp := NewWorkerPool(func(ctx context.Context, wp *WorkerPool) error {
		time.Sleep(time.Millisecond)
		return nil
	}, WithConcurrency(1), WithDuration(time.Second))
	_ = wp.Launch()
	defer func() { wp.Stop(); _ = wp.Wait() }()

	time.Sleep(50 * time.Millisecond)
	wp.SetConcurrency(4)
	time.Sleep(300 * time.Millisecond)
	if got := wp.getActiveWorkers(); got != 4 {
		t.Errorf("expected 4 workers, got %d", got)
	}
}
