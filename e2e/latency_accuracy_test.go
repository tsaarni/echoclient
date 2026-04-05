package e2e

import (
	"context"
	"sync"
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
