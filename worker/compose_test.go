package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
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

func TestRetryConstantDelay(t *testing.T) {
	t.Run("SuccessOnFirstAttempt", func(t *testing.T) {
		var calls int
		var f WorkerFunc = func(ctx context.Context, wp *WorkerPool) error {
			calls++
			return nil
		}

		retried := f.Retry(3, 1*time.Millisecond)
		err := retried(context.Background(), nil)
		if err != nil {
			t.Errorf("expected success, got err: %v", err)
		}
		if calls != 1 {
			t.Errorf("expected 1 call, got %d", calls)
		}
	})

	t.Run("SuccessAfterFailure", func(t *testing.T) {
		var calls int
		var f WorkerFunc = func(ctx context.Context, wp *WorkerPool) error {
			calls++
			if calls < 3 {
				return errors.New("fail")
			}
			return nil
		}

		retried := f.Retry(5, 1*time.Millisecond)
		err := retried(context.Background(), nil)
		if err != nil {
			t.Errorf("expected eventual success, got err: %v", err)
		}
		if calls != 3 {
			t.Errorf("expected 3 calls, got %d", calls)
		}
	})

	t.Run("AllAttemptsFail", func(t *testing.T) {
		var calls int
		expectedErr := errors.New("persistent error")
		var f WorkerFunc = func(ctx context.Context, wp *WorkerPool) error {
			calls++
			return expectedErr
		}

		retried := f.Retry(3, 1*time.Millisecond)
		err := retried(context.Background(), nil)
		if !errors.Is(err, expectedErr) {
			t.Errorf("expected error %v, got %v", expectedErr, err)
		}
		if calls != 3 {
			t.Errorf("expected 3 calls, got %d", calls)
		}
	})

	t.Run("ContextCancellation", func(t *testing.T) {
		var f WorkerFunc = func(ctx context.Context, wp *WorkerPool) error {
			return errors.New("fail")
		}

		ctx, cancel := context.WithCancel(context.Background())
		retried := f.Retry(5, 100*time.Millisecond)

		go func() {
			time.Sleep(20 * time.Millisecond)
			cancel()
		}()

		err := retried(ctx, nil)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled error, got %v", err)
		}
	})
}

func TestRetryWithBackoff(t *testing.T) {
	t.Run("BackoffJitterLimits", func(t *testing.T) {
		var calls int
		var callTimes []time.Time
		var f WorkerFunc = func(ctx context.Context, wp *WorkerPool) error {
			calls++
			callTimes = append(callTimes, time.Now())
			return errors.New("fail")
		}

		minDelay := 10 * time.Millisecond
		maxDelay := 30 * time.Millisecond
		retried := f.RetryWithBackoff(4, minDelay, maxDelay)

		_ = retried(context.Background(), nil)

		if calls != 4 {
			t.Fatalf("expected 4 calls, got %d", calls)
		}

		// Delays are random between 0 and currentLimit.
		// We verify they are strictly below the upper bounds (plus a small scheduling latency margin of 5ms):
		d1 := callTimes[1].Sub(callTimes[0]) // limit 10ms
		d2 := callTimes[2].Sub(callTimes[1]) // limit 20ms
		d3 := callTimes[3].Sub(callTimes[2]) // limit 30ms (capped)

		if d1 > 10*time.Millisecond+5*time.Millisecond {
			t.Errorf("expected delay 1 to be capped at 10ms (plus margin), got %v", d1)
		}
		if d2 > 20*time.Millisecond+5*time.Millisecond {
			t.Errorf("expected delay 2 to be capped at 20ms (plus margin), got %v", d2)
		}
		if d3 > 30*time.Millisecond+5*time.Millisecond {
			t.Errorf("expected delay 3 to be capped at 30ms (plus margin), got %v", d3)
		}
	})

	t.Run("ContextCancellation", func(t *testing.T) {
		var f WorkerFunc = func(ctx context.Context, wp *WorkerPool) error {
			return errors.New("fail")
		}

		ctx, cancel := context.WithCancel(context.Background())
		retried := f.RetryWithBackoff(5, 100*time.Millisecond, 500*time.Millisecond)

		go func() {
			time.Sleep(20 * time.Millisecond)
			cancel()
		}()

		err := retried(ctx, nil)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled error, got %v", err)
		}
	})
}

func TestCompositionValidation(t *testing.T) {
	// 1. Mix validation
	t.Run("MixNilFunc", func(t *testing.T) {
		mixed := Mix(Weighted{Weight: 10, Func: nil})
		err := mixed(context.Background(), nil)
		if !errors.Is(err, ErrStopWorker) {
			t.Errorf("expected ErrStopWorker, got %v", err)
		}
	})

	// 2. Retry validation
	t.Run("RetryNilFunc", func(t *testing.T) {
		var f WorkerFunc
		retried := f.Retry(3, 10*time.Millisecond)
		err := retried(context.Background(), nil)
		if !errors.Is(err, ErrStopWorker) {
			t.Errorf("expected ErrStopWorker, got %v", err)
		}
	})

	t.Run("RetryZeroAttempts", func(t *testing.T) {
		var dummy WorkerFunc = func(ctx context.Context, wp *WorkerPool) error { return nil }
		retried := dummy.Retry(0, 10*time.Millisecond)
		err := retried(context.Background(), nil)
		if !errors.Is(err, ErrStopWorker) {
			t.Errorf("expected ErrStopWorker, got %v", err)
		}
	})

	t.Run("RetryNegativeDelay", func(t *testing.T) {
		var dummy WorkerFunc = func(ctx context.Context, wp *WorkerPool) error { return nil }
		retried := dummy.Retry(3, -5*time.Millisecond)
		err := retried(context.Background(), nil)
		if !errors.Is(err, ErrStopWorker) {
			t.Errorf("expected ErrStopWorker, got %v", err)
		}
	})

	// 3. RetryWithBackoff validation
	t.Run("RetryWithBackoffNilFunc", func(t *testing.T) {
		var f WorkerFunc
		retried := f.RetryWithBackoff(3, 10*time.Millisecond, 100*time.Millisecond)
		err := retried(context.Background(), nil)
		if !errors.Is(err, ErrStopWorker) {
			t.Errorf("expected ErrStopWorker, got %v", err)
		}
	})

	t.Run("RetryWithBackoffZeroAttempts", func(t *testing.T) {
		var dummy WorkerFunc = func(ctx context.Context, wp *WorkerPool) error { return nil }
		retried := dummy.RetryWithBackoff(0, 10*time.Millisecond, 100*time.Millisecond)
		err := retried(context.Background(), nil)
		if !errors.Is(err, ErrStopWorker) {
			t.Errorf("expected ErrStopWorker, got %v", err)
		}
	})

	t.Run("RetryWithBackoffNegativeMinDelay", func(t *testing.T) {
		var dummy WorkerFunc = func(ctx context.Context, wp *WorkerPool) error { return nil }
		retried := dummy.RetryWithBackoff(3, -1*time.Second, 1*time.Second)
		err := retried(context.Background(), nil)
		if !errors.Is(err, ErrStopWorker) {
			t.Errorf("expected ErrStopWorker, got %v", err)
		}
	})

	t.Run("RetryWithBackoffMaxLessThanMin", func(t *testing.T) {
		var dummy WorkerFunc = func(ctx context.Context, wp *WorkerPool) error { return nil }
		retried := dummy.RetryWithBackoff(3, 100*time.Millisecond, 10*time.Millisecond)
		err := retried(context.Background(), nil)
		if !errors.Is(err, ErrStopWorker) {
			t.Errorf("expected ErrStopWorker, got %v", err)
		}
	})
}

func TestRetryZeroDelay(t *testing.T) {
	var calls int
	var f WorkerFunc = func(ctx context.Context, wp *WorkerPool) error {
		calls++
		if calls < 3 {
			return errors.New("fail")
		}
		return nil
	}

	retried := f.Retry(5, 0)
	err := retried(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected success with zero delay, got err: %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestRetryWithBackoffZeroDelay(t *testing.T) {
	var calls int
	var f WorkerFunc = func(ctx context.Context, wp *WorkerPool) error {
		calls++
		if calls < 3 {
			return errors.New("fail")
		}
		return nil
	}

	retried := f.RetryWithBackoff(5, 0, 0)
	err := retried(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected success with zero delay, got err: %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestRetryPreCancelledContext(t *testing.T) {
	var calls int
	var f WorkerFunc = func(ctx context.Context, wp *WorkerPool) error {
		calls++
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	retried := f.Retry(3, 10*time.Millisecond)
	err := retried(ctx, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if calls != 0 {
		t.Errorf("expected 0 calls on pre-cancelled context, got %d", calls)
	}
}

