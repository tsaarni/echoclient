package worker

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRequestSchedulerBasicPacing(t *testing.T) {
	rl := NewRequestScheduler()
	var rps atomic.Int64
	rps.Store(100) // 10ms interval

	ctx := context.Background()
	start := time.Now()

	var scheduledTimes []time.Time
	for range 10 {
		retCtx, err := rl.Take(ctx, &rps)
		if err != nil {
			t.Fatalf("Take failed: %v", err)
		}
		st := ScheduledTimeFromContext(retCtx)
		if st.IsZero() {
			t.Fatal("expected scheduled time in context")
		}
		scheduledTimes = append(scheduledTimes, st)
	}

	elapsed := time.Since(start)
	// 10 calls at 100 RPS: 9 intervals of 10ms = ~90ms minimum.
	if elapsed < 80*time.Millisecond || elapsed > 200*time.Millisecond {
		t.Errorf("unexpected elapsed time: %v (expected ~90ms)", elapsed)
	}

	// Verify ~10ms spacing between scheduled times.
	for i := 1; i < len(scheduledTimes); i++ {
		gap := scheduledTimes[i].Sub(scheduledTimes[i-1])
		if gap < 8*time.Millisecond || gap > 12*time.Millisecond {
			t.Errorf("gap[%d] = %v, expected ~10ms", i, gap)
		}
	}
}

func TestRequestSchedulerMonotonicScheduledTimes(t *testing.T) {
	rl := NewRequestScheduler()
	var rps atomic.Int64
	rps.Store(1000) // 1ms interval

	ctx := context.Background()
	const goroutines = 10
	const callsPerGoroutine = 10

	var mu sync.Mutex
	var allTimes []time.Time

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range callsPerGoroutine {
				retCtx, err := rl.Take(ctx, &rps)
				if err != nil {
					t.Errorf("Take failed: %v", err)
					return
				}
				st := ScheduledTimeFromContext(retCtx)
				mu.Lock()
				allTimes = append(allTimes, st)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	sort.Slice(allTimes, func(i, j int) bool { return allTimes[i].Before(allTimes[j]) })

	for i := 1; i < len(allTimes); i++ {
		if !allTimes[i].After(allTimes[i-1]) {
			t.Errorf("non-monotonic at index %d: %v <= %v", i, allTimes[i], allTimes[i-1])
		}
	}
}

func TestRequestSchedulerNoFutureScheduledTimes(t *testing.T) {
	rl := NewRequestScheduler()
	var rps atomic.Int64
	rps.Store(10) // 100ms interval

	ctx := context.Background()
	const goroutines = 10

	var mu sync.Mutex
	var violations []string

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			retCtx, err := rl.Take(ctx, &rps)
			if err != nil {
				return
			}
			now := time.Now()
			st := ScheduledTimeFromContext(retCtx)
			if st.After(now) {
				mu.Lock()
				violations = append(violations, st.Sub(now).String())
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(violations) > 0 {
		t.Fatalf("found %d future scheduled times: %v", len(violations), violations)
	}
}

func TestRequestSchedulerDynamicRPSResponsiveness(t *testing.T) {
	rl := NewRequestScheduler()
	var rps atomic.Int64
	rps.Store(1) // Start slow: 1 RPS

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const goroutines = 5
	const totalCalls = 5

	var callCount atomic.Int64
	var wg sync.WaitGroup

	// After 200ms, increase to 1000 RPS.
	go func() {
		time.Sleep(200 * time.Millisecond)
		rps.Store(1000)
	}()

	start := time.Now()
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for callCount.Add(1) <= totalCalls {
				_, err := rl.Take(ctx, &rps)
				if err != nil {
					return
				}
			}
		}()
	}
	wg.Wait()

	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("took %v, expected < 2s (rate change not responsive)", elapsed)
	}
}

func TestRequestSchedulerCOPreservation(t *testing.T) {
	rl := NewRequestScheduler()
	var rps atomic.Int64
	rps.Store(100) // 10ms interval

	ctx := context.Background()

	// Do 3 normal Takes.
	for range 3 {
		_, err := rl.Take(ctx, &rps)
		if err != nil {
			t.Fatalf("Take failed: %v", err)
		}
	}

	// Simulate a stall: sleep 500ms.
	time.Sleep(500 * time.Millisecond)

	// Do 3 more Takes. Post-stall scheduled times should be in the past.
	start := time.Now()
	for i := range 3 {
		retCtx, err := rl.Take(ctx, &rps)
		if err != nil {
			t.Fatalf("Take failed: %v", err)
		}
		st := ScheduledTimeFromContext(retCtx)
		age := time.Since(st)
		if age < 100*time.Millisecond {
			t.Errorf("post-stall Take[%d]: scheduled time age %v, expected >100ms in the past", i, age)
		}
	}
	postStallElapsed := time.Since(start)

	// Post-stall Takes should return near-instantly (catching up).
	if postStallElapsed > 100*time.Millisecond {
		t.Errorf("post-stall Takes took %v, expected near-instant", postStallElapsed)
	}
}

func TestRequestSchedulerMaxCatchupClamps(t *testing.T) {
	rl := NewRequestScheduler()
	rl.SetMaxCatchup(100 * time.Millisecond)
	var rps atomic.Int64
	rps.Store(100) // 10ms interval

	ctx := context.Background()

	// Establish TAT with one Take.
	_, err := rl.Take(ctx, &rps)
	if err != nil {
		t.Fatalf("Take failed: %v", err)
	}

	// Stall for 1 second.
	time.Sleep(1 * time.Second)

	// Next Take: scheduled time should be at most ~100ms in the past (not 1s).
	retCtx, err := rl.Take(ctx, &rps)
	if err != nil {
		t.Fatalf("Take failed: %v", err)
	}
	st := ScheduledTimeFromContext(retCtx)
	age := time.Since(st)
	if age > 200*time.Millisecond {
		t.Errorf("scheduled time age %v, expected ≤ ~100ms (maxCatchup clamp)", age)
	}
}

func TestRequestSchedulerContextCancellation(t *testing.T) {
	rl := NewRequestScheduler()
	var rps atomic.Int64
	rps.Store(1) // 1 RPS = 1 second interval, so next TAT is far ahead.

	// Take once to advance TAT.
	_, _ = rl.Take(context.Background(), &rps)

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after 50ms.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := rl.Take(ctx, &rps)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("cancellation took %v, expected < 200ms", elapsed)
	}
}

func TestRequestSchedulerReset(t *testing.T) {
	rl := NewRequestScheduler()
	var rps atomic.Int64
	rps.Store(100)

	now := time.Now()
	rl.Reset(now)

	ctx := context.Background()
	retCtx, err := rl.Take(ctx, &rps)
	if err != nil {
		t.Fatalf("Take failed: %v", err)
	}
	st := ScheduledTimeFromContext(retCtx)
	diff := st.Sub(now).Abs()
	if diff > 2*time.Millisecond {
		t.Errorf("scheduled time diff from reset time: %v, expected < 2ms", diff)
	}
}

// TestRequestSchedulerFirstRequestNotDelayed verifies that the first Take()
// after a Reset fires immediately even at very low RPS. Previously, the JIT
// check gated on newTAT (= oldTAT + interval), which prevented the CAS when
// interval > jitLookahead, delaying the first request by nearly one full interval.
func TestRequestSchedulerFirstRequestNotDelayed(t *testing.T) {
	rl := NewRequestScheduler()
	var rps atomic.Int64
	rps.Store(1) // 1 RPS → 1s interval, much larger than jitLookahead (100ms)

	ctx := context.Background()

	start := time.Now()
	retCtx, err := rl.Take(ctx, &rps)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Take failed: %v", err)
	}

	// The first request's scheduled time should be ~now (within a few ms),
	// not delayed by ~900ms of JIT busy-waiting.
	st := ScheduledTimeFromContext(retCtx)
	latency := time.Since(st)
	if elapsed > 50*time.Millisecond {
		t.Errorf("first Take() took %v, expected < 50ms (no JIT delay)", elapsed)
	}
	if latency > 50*time.Millisecond {
		t.Errorf("first request latency %v, expected < 50ms", latency)
	}
}

// TestRequestSchedulerSnapBackwardOnRPSIncrease verifies that when RPS increases
// (e.g., during easing ramp-up), a stale TAT committed at the old lower RPS is
// snapped backward to now. Without this, workers would JIT-wait for the stale
// TAT instead of pacing at the new higher rate.
func TestRequestSchedulerSnapBackwardOnRPSIncrease(t *testing.T) {
	rl := NewRequestScheduler()
	var rps atomic.Int64
	rps.Store(1) // Start at 1 RPS → 1s interval

	ctx := context.Background()

	// First Take commits TAT to now + 1s.
	_, err := rl.Take(ctx, &rps)
	if err != nil {
		t.Fatalf("Take failed: %v", err)
	}

	// Increase RPS to 1000. The committed TAT (now + ~1s) is far too
	// far ahead for the new 1ms interval. The snap-backward mechanism
	// should reset TAT to now.
	rps.Store(1000)

	start := time.Now()
	_, err = rl.Take(ctx, &rps)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Take failed: %v", err)
	}

	// Without snap-backward, the second Take would JIT-wait ~900ms for
	// the stale TAT. With it, Take returns almost immediately.
	if elapsed > 200*time.Millisecond {
		t.Errorf("second Take() after RPS increase took %v, expected < 200ms (stale TAT should be snapped)", elapsed)
	}
}

func TestRequestSchedulerUnlimitedReturnsOriginalContext(t *testing.T) {
	rl := NewRequestScheduler()
	var rps atomic.Int64
	rps.Store(0) // Unlimited

	ctx := context.Background()
	retCtx, err := rl.Take(ctx, &rps)
	if err != nil {
		t.Fatalf("Take failed: %v", err)
	}

	st := ScheduledTimeFromContext(retCtx)
	if !st.IsZero() {
		t.Errorf("expected zero scheduled time for unlimited rate, got %v", st)
	}
}
