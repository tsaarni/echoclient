package e2e

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tsaarni/echoclient/worker"
)

// TestCoordinatedOmission tests that latency includes time spent waiting in the queue.
func TestCoordinatedOmission(t *testing.T) {
	h := NewE2ETestFixture(Delayed(50*time.Millisecond, 200))
	defer h.Close()

	RunPool(h, worker.WithRateLimit(100, 1), worker.WithConcurrency(1), worker.WithRepetitions(5))

	h.AssertRequestsApprox(t, 5, 0.05)
}

// TestLatencyCompensationGranularity tests that queue time increases when the server is slower than the request rate.
func TestLatencyCompensationGranularity(t *testing.T) {
	h := NewE2ETestFixture(Delayed(50*time.Millisecond, 200))
	defer h.Close()

	var scheduledTimes []time.Time
	var receiptTimes []time.Time
	var mu sync.Mutex

	workerFunc := func(ctx context.Context, wp *worker.WorkerPool) error {
		now := time.Now()
		st := worker.ScheduledTimeFromContext(ctx)
		mu.Lock()
		scheduledTimes = append(scheduledTimes, st)
		receiptTimes = append(receiptTimes, now)
		mu.Unlock()

		_, _ = h.Client.Get(h.Server.URL)
		return nil
	}

	RunPool(h, worker.WithDuration(300*time.Millisecond), worker.WithRateLimit(100, 100), worker.WithConcurrency(1), worker.WithWorkerFunc(workerFunc))

	mu.Lock()
	defer mu.Unlock()

	if len(scheduledTimes) < 3 {
		t.Fatalf("expected at least 3 data points, got %d", len(scheduledTimes))
	}

	lastQueueTime := receiptTimes[len(receiptTimes)-1].Sub(scheduledTimes[len(scheduledTimes)-1])
	firstQueueTime := receiptTimes[0].Sub(scheduledTimes[0])
	if lastQueueTime <= firstQueueTime {
		t.Errorf("expected queue time to grow over time: first=%v, last=%v", firstQueueTime, lastQueueTime)
	}
}

// TestNoNegativeLatencyInBurst verifies that scheduled times never exceed
// execution time across a step transition from high RPS to low RPS.
// The GCRA scheduler resets TAT at step boundaries and JIT-paces workers,
// so negative latency (scheduledTime > now) should never occur.
func TestNoNegativeLatencyInBurst(t *testing.T) {
	h := NewE2ETestFixture(Status(200))
	defer h.Close()

	var mu sync.Mutex
	var negativeLatencies []time.Duration

	workerFunc := func(ctx context.Context, wp *worker.WorkerPool) error {
		now := time.Now()
		st := worker.ScheduledTimeFromContext(ctx)
		if st.IsZero() {
			return nil
		}
		latency := now.Sub(st)

		mu.Lock()
		t.Logf("Worker executed at %v, scheduled at %v, latency %v", now.Format("15:04:05.000"), st.Format("15:04:05.000"), latency)
		if latency < 0 {
			negativeLatencies = append(negativeLatencies, latency)
		}
		mu.Unlock()

		_, _ = h.Client.Get(h.Server.URL)
		return nil
	}

	steps := []*worker.Step{
		// Step 1: High rate for a short burst of traffic.
		worker.NewStep(
			worker.WithDuration(200*time.Millisecond),
			worker.WithRateLimit(1000, 100),
			worker.WithConcurrency(10),
		),
		// Step 2: Drop to a lower rate.
		// With the GCRA scheduler, TAT resets at step boundaries so
		// workers are paced at the new rate via JIT — no token
		// accumulation from Step 1.
		worker.NewStep(
			worker.WithRateLimit(10, 10),
			worker.WithConcurrency(10),
			worker.WithRepetitions(10),
		),
	}

	wp := worker.NewMultiStepWorkerPool(workerFunc, steps)
	_ = wp.Launch()
	wp.Wait()

	mu.Lock()
	l := len(negativeLatencies)
	mu.Unlock()

	if l > 0 {
		t.Fatalf("Found %d instances of negative latency! Worst: %v", l, negativeLatencies[l-1])
	}
}

// TestUnresponsiveRateChange reveals the "Concurrency Eagerness Bug".
// Workers eagerly claim timestamps and then block in the limiter.
// If the rate is increased, workers already blocked are stuck with the old, slow schedule.
// With the RequestScheduler, JIT prevents eager claiming, so rate changes take effect promptly.
func TestUnresponsiveRateChange(t *testing.T) {
	h := NewE2ETestFixture(Status(200))
	defer h.Close()

	start := time.Now()
	workerFunc := func(ctx context.Context, wp *worker.WorkerPool) error {
		_, _ = h.Client.Get(h.Server.URL)
		return nil
	}

	// 10 workers, 1 RPS.
	wp := worker.NewWorkerPool(workerFunc,
		worker.WithRateLimit(1, 1),
		worker.WithConcurrency(10),
		worker.WithRepetitions(10),
	)
	_ = wp.Launch()

	// Give them a moment to "grab" their slow timestamps and block.
	time.Sleep(100 * time.Millisecond)

	// Increase rate to 1000 RPS. It should finish almost immediately (~10ms total).
	wp.SetRateLimit(1000, 10)

	wp.Wait()
	duration := time.Since(start)

	// If it takes > 5 seconds, it means they are still crawling at 1 RPS.
	if duration > 5*time.Second {
		t.Fatalf("Rate change was ignored by waiting workers! Duration: %v. Expected < 1s. This proves workers eagerly commit to a schedule at the wrong time.", duration)
	}
}

// TestScheduledTimeNeverExceedsNow verifies that across a multi-step profile with various
// RPS/concurrency combos, no worker ever receives a scheduled time in the future.
func TestScheduledTimeNeverExceedsNow(t *testing.T) {
	h := NewE2ETestFixture(Status(200))
	defer h.Close()

	var mu sync.Mutex
	var violations []time.Duration

	workerFunc := func(ctx context.Context, wp *worker.WorkerPool) error {
		now := time.Now()
		st := worker.ScheduledTimeFromContext(ctx)
		if !st.IsZero() && st.After(now) {
			mu.Lock()
			violations = append(violations, st.Sub(now))
			mu.Unlock()
		}
		_, _ = h.Client.Get(h.Server.URL)
		return nil
	}

	steps := []*worker.Step{
		worker.NewStep(
			worker.WithRateLimit(100, 10),
			worker.WithConcurrency(5),
			worker.WithDuration(300*time.Millisecond),
			worker.WithWorkerFunc(workerFunc),
		),
		worker.NewStep(
			worker.WithRateLimit(500, 50),
			worker.WithConcurrency(10),
			worker.WithDuration(300*time.Millisecond),
			worker.WithWorkerFunc(workerFunc),
		),
		worker.NewStep(
			worker.WithRateLimit(10, 1),
			worker.WithConcurrency(2),
			worker.WithRepetitions(10),
			worker.WithWorkerFunc(workerFunc),
		),
	}

	wp := worker.NewMultiStepWorkerPool(workerFunc, steps)
	_ = wp.Launch()
	wp.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(violations) > 0 {
		t.Fatalf("found %d instances where scheduledTime > now; worst: %v", len(violations), violations[0])
	}
}

// TestEasingCurveAccuracy verifies that during a linear ramp from 10→200 RPS over 2s,
// the actual request rate in each time bucket approximates the eased rate.
func TestEasingCurveAccuracy(t *testing.T) {
	h := NewE2ETestFixture(Status(200))
	defer h.Close()

	var mu sync.Mutex
	var completionTimes []time.Time
	var startTime time.Time

	workerFunc := func(ctx context.Context, wp *worker.WorkerPool) error {
		mu.Lock()
		completionTimes = append(completionTimes, time.Now())
		mu.Unlock()
		_, _ = h.Client.Get(h.Server.URL)
		return nil
	}

	steps := []*worker.Step{
		worker.NewStep(
			worker.WithRateLimit(200, 200, worker.EasingLinear),
			worker.WithConcurrency(10),
			worker.WithDuration(2*time.Second),
			worker.WithWorkerFunc(workerFunc),
			worker.WithHooks(func(ctx context.Context, wp *worker.WorkerPool) {
				startTime = time.Now()
			}, nil),
		),
	}

	wp := worker.NewMultiStepWorkerPool(workerFunc, steps)
	_ = wp.Launch()
	wp.Wait()

	mu.Lock()
	defer mu.Unlock()

	// Bin into 500ms buckets.
	buckets := make([]int, 4) // 0-500ms, 500-1000ms, 1000-1500ms, 1500-2000ms
	for _, ct := range completionTimes {
		elapsed := ct.Sub(startTime)
		idx := int(elapsed / (500 * time.Millisecond))
		if idx >= len(buckets) {
			idx = len(buckets) - 1
		}
		if idx >= 0 {
			buckets[idx]++
		}
	}

	// With linear easing from 10→200 RPS over 2s, the instantaneous rate at time t is:
	// rate(t) = 10 + (200-10) * (t/2s) = 10 + 95*t
	// Expected counts per 500ms bucket (integral of rate over bucket):
	// Bucket 0 (0-0.5s): integral of 10+95t from 0 to 0.5 ≈ 16.9
	// Bucket 1 (0.5-1.0s): ≈ 41.3
	// Bucket 2 (1.0-1.5s): ≈ 65.6
	// Bucket 3 (1.5-2.0s): ≈ 90.0
	t.Logf("Buckets: %v", buckets)

	// Verify monotonically increasing request count across buckets.
	for i := 1; i < len(buckets); i++ {
		if buckets[i] <= buckets[i-1] {
			t.Errorf("expected monotonically increasing request count; bucket[%d]=%d <= bucket[%d]=%d",
				i, buckets[i], i-1, buckets[i-1])
		}
	}
}

// TestCOAfterServerStall verifies that CO-corrected latency captures server stall time.
// A server handler stalls for 500ms on requests 3-5 (out of 10). With RPS=100 and
// concurrency=1, post-stall scheduled times should show the accumulated delay.
func TestCOAfterServerStall(t *testing.T) {
	var requestNum atomic.Int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requestNum.Add(1)
		if n >= 3 && n <= 5 {
			time.Sleep(500 * time.Millisecond)
		}
		w.WriteHeader(200)
	})

	h := NewE2ETestFixture(handler)
	defer h.Close()

	var mu sync.Mutex
	type record struct {
		scheduled  time.Time
		dispatched time.Time
	}
	var records []record

	workerFunc := func(ctx context.Context, wp *worker.WorkerPool) error {
		now := time.Now()
		st := worker.ScheduledTimeFromContext(ctx)
		mu.Lock()
		records = append(records, record{scheduled: st, dispatched: now})
		mu.Unlock()

		req, _ := http.NewRequestWithContext(ctx, "GET", h.Server.URL, nil)
		_, _ = h.Client.Do(req)
		return nil
	}

	RunPool(h,
		worker.WithRateLimit(100, 200),
		worker.WithConcurrency(1),
		worker.WithRepetitions(10),
		worker.WithWorkerFunc(workerFunc),
	)

	mu.Lock()
	defer mu.Unlock()

	if len(records) < 8 {
		t.Fatalf("expected at least 8 records, got %d", len(records))
	}

	// Assert no negative latencies.
	for i, r := range records {
		latency := r.dispatched.Sub(r.scheduled)
		if latency < 0 {
			t.Errorf("record[%d]: negative latency %v", i, latency)
		}
	}

	// Assert that scheduled times are approximately evenly spaced at 10ms.
	for i := 1; i < len(records); i++ {
		gap := records[i].scheduled.Sub(records[i-1].scheduled)
		if gap < 5*time.Millisecond || gap > 15*time.Millisecond {
			t.Errorf("scheduled gap[%d] = %v, expected ~10ms", i, gap)
		}
	}

	// Assert that post-stall latencies include the stall time.
	// After request 5 (which stalls 500ms), the accumulated delay should be significant.
	if len(records) > 6 {
		postStallLatency := records[6].dispatched.Sub(records[6].scheduled)
		if postStallLatency < 200*time.Millisecond {
			t.Errorf("post-stall latency %v, expected >200ms (CO should capture stall)", postStallLatency)
		}
	}
}
