package e2e

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/tsaarni/echoclient/metrics"
	"github.com/tsaarni/echoclient/worker"
)

func TestE2ECompositionMix(t *testing.T) {
	// Setup switch handler for routing requests based on path
	var pathACalls atomic.Int64
	var pathBCalls atomic.Int64

	handler := PathSwitch{
		"/pathA": func(w http.ResponseWriter, r *http.Request) {
			pathACalls.Add(1)
			w.WriteHeader(http.StatusOK)
		},
		"/pathB": func(w http.ResponseWriter, r *http.Request) {
			pathBCalls.Add(1)
			w.WriteHeader(http.StatusOK)
		},
	}

	h := NewE2ETestFixture(handler)
	defer h.Close()

	// 1. Define worker functions performing HTTP requests
	var workerA worker.WorkerFunc = func(ctx context.Context, wp *worker.WorkerPool) error {
		req, _ := http.NewRequestWithContext(ctx, "GET", h.Server.URL+"/pathA", nil)
		resp, err := h.Client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}

	var workerB worker.WorkerFunc = func(ctx context.Context, wp *worker.WorkerPool) error {
		req, _ := http.NewRequestWithContext(ctx, "GET", h.Server.URL+"/pathB", nil)
		resp, err := h.Client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}

	// 2. Mix worker functions using the Weighted helper method (3:1 ratio, sum = 4)
	mixed := worker.Mix(
		workerA.Weighted(3),
		workerB.Weighted(1),
	)

	// 3. Run worker pool executing the composed function
	wp := worker.NewWorkerPool(mixed, worker.WithRepetitions(1000), worker.WithConcurrency(5))
	_ = wp.Launch()
	wp.Wait()

	callsA := pathACalls.Load()
	callsB := pathBCalls.Load()

	if callsA+callsB != 1000 {
		t.Fatalf("expected 1000 total requests, got %d", callsA+callsB)
	}

	// Verify distributions within 5% tolerance
	if callsA < 700 || callsA > 800 {
		t.Errorf("expected pathA calls to be close to 750 (75%%), got %d", callsA)
	}
	if callsB < 200 || callsB > 300 {
		t.Errorf("expected pathB calls to be close to 250 (25%%), got %d", callsB)
	}

	// Check Prom metrics are recorded correctly
	counterA := metrics.HttpClientRequestsTotal.WithLabelValues("GET", "200", h.Host())
	if got := testutil.ToFloat64(counterA); got != 1000 {
		t.Errorf("expected Prom counter to match 1000 requests, got %f", got)
	}
}


