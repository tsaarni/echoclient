package e2e

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/tsaarni/echoclient/metrics"
	"github.com/tsaarni/echoclient/worker"
)

func TestFaultTolerance(t *testing.T) {
	h := NewE2ETestHarness(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/500":
			w.WriteHeader(http.StatusInternalServerError)
		case "/hang":
			time.Sleep(5 * time.Second)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer h.Close()

	wp := worker.NewWorkerPool(
		func(ctx context.Context, wp *worker.WorkerPool) error {
			_, _ = h.Client.Get(h.Server.URL + "/500")
			return nil
		},
		worker.WithRepetitions(5),
	)
	wp.Launch()
	wp.Wait()

	counter := metrics.HttpClientRequestsTotal.WithLabelValues("GET", "500", h.Host())
	if testutil.ToFloat64(counter) != 5 {
		t.Errorf("Expected 5 requests with 500 status, got %f", testutil.ToFloat64(counter))
	}
}

func TestExactRepetitionCounting(t *testing.T) {
	h := NewE2ETestHarness(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer h.Close()

	wp := worker.NewWorkerPool(
		func(ctx context.Context, wp *worker.WorkerPool) error {
			_, _ = h.Client.Get(h.Server.URL)
			return nil
		},
		worker.WithConcurrency(50),
		worker.WithRepetitions(100),
	)
	wp.Launch()
	wp.Wait()

	h.AssertRequestCount(t, 100, 0)
}

func TestContextCancellation(t *testing.T) {
	h := NewE2ETestHarness(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer h.Close()

	ctx, cancel := context.WithCancel(context.Background())
	wp := worker.NewWorkerPool(
		func(ctx context.Context, wp *worker.WorkerPool) error {
			_, _ = h.Client.Get(h.Server.URL)
			return nil
		},
		worker.WithConcurrency(10),
	)

	start := time.Now()
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	wp.LaunchWithContext(ctx)
	wp.Wait()
	duration := time.Since(start)

	if duration < 200*time.Millisecond || duration > 500*time.Millisecond {
		t.Errorf("Expected cancellation in ~300ms, took %v", duration)
	}
}

func TestLifecycleHooks(t *testing.T) {
	h := NewE2ETestHarness(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer h.Close()

	var events []string
	var mu sync.Mutex

	onStart := func(ctx context.Context, wp *worker.WorkerPool) {
		mu.Lock()
		events = append(events, "Step_Start")
		mu.Unlock()
	}
	onEnd := func(ctx context.Context, wp *worker.WorkerPool) {
		mu.Lock()
		events = append(events, "Step_End")
		mu.Unlock()
	}

	steps := []*worker.Step{
		worker.NewStep(worker.WithRepetitions(1), worker.WithHooks(onStart, onEnd)),
		worker.NewStep(worker.WithRepetitions(1), worker.WithHooks(onStart, onEnd)),
	}

	wp := worker.NewMultiStepWorkerPool(
		func(ctx context.Context, wp *worker.WorkerPool) error {
			_, _ = h.Client.Get(h.Server.URL)
			return nil
		},
		steps,
	)

	wp.Launch()
	wp.Wait()

	expected := []string{"Step_Start", "Step_End", "Step_Start", "Step_End"}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != len(expected) {
		t.Fatalf("Expected %v, got %v", expected, events)
	}
	for i := range events {
		if events[i] != expected[i] {
			t.Errorf("At index %d: expected %s, got %s", i, expected[i], events[i])
		}
	}
}
