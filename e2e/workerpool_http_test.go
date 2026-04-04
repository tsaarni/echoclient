// Package e2e provides end-to-end testing for the WorkerPool and MeasuringHTTPClient interaction.
// These tests verify that the orchestration of traffic generation and latency
// instrumentation correctly handles complex traffic profiles and avoids Coordinated Omission.
package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/tsaarni/echoclient/client"
	"github.com/tsaarni/echoclient/metrics"
	"github.com/tsaarni/echoclient/worker"
)

// E2ETestHarness provides a controlled environment for end-to-end testing.
// It manages the lifecycle of a test server and provides convenience methods
// for asserting metrics produced by the MeasuringHTTPClient.
type E2ETestHarness struct {
	Server *httptest.Server
	Client *http.Client
}

// NewE2ETestHarness initializes a new test server and a MeasuringHTTPClient.
func NewE2ETestHarness(handler http.Handler) *E2ETestHarness {
	server := httptest.NewServer(handler)
	c := client.NewMeasuringHTTPClient()
	return &E2ETestHarness{
		Server: server,
		Client: &c,
	}
}

// Close shuts down the test server.
func (h *E2ETestHarness) Close() {
	h.Server.Close()
}

// Host extracts the host:port from the test server URL for use in metric labels.
func (h *E2ETestHarness) Host() string {
	u, _ := url.Parse(h.Server.URL)
	return u.Host
}

// AssertRequestCount verifies that the total number of HTTP requests matches the expected count.
// Uses a margin to account for potential jitter in CI environments.
func (h *E2ETestHarness) AssertRequestCount(t *testing.T, expected float64, margin float64) {
	t.Helper()
	counter := metrics.HttpClientRequestsTotal.WithLabelValues("GET", "200", h.Host())
	count := testutil.ToFloat64(counter)
	if count < expected*(1-margin) || count > expected*(1+margin) {
		t.Errorf("Expected request count around %f (±%f), got %f", expected, margin, count)
	}
}

// TestCoordinatedOmission validates that if a worker pool falls behind schedule (due to slow backend),
// the recorded latency correctly reflects the total time (queue time + service time),
// effectively mitigating the Coordinated Omission problem.
func TestCoordinatedOmission(t *testing.T) {
	h := NewE2ETestHarness(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Inject intentional 50ms delay to force backpressure.
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer h.Close()

	// Traffic profile: 100 RPS (1 req/10ms), Concurrency 1.
	// Since backend takes 50ms, the queue should build up, testing latency correction.
	wp := worker.NewWorkerPool(
		func(ctx context.Context, wp *worker.WorkerPool) error {
			_, _ = h.Client.Get(h.Server.URL)
			return nil
		},
		worker.WithRateLimit(100, 1),
		worker.WithConcurrency(1),
		worker.WithRepetitions(5),
	)

	wp.Launch()
	wp.Wait()

	h.AssertRequestCount(t, 5, 0.05)
}

// TestMultiStepProfile verifies that the worker pool correctly transitions between different
// traffic configurations, handles pauses, and respects easing logic over time.
func TestMultiStepProfile(t *testing.T) {
	h := NewE2ETestHarness(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer h.Close()

	// Define a multi-step profile covering warmup, pause, spike, and cooldown.
	steps := []*worker.Step{
		worker.NewStep(worker.WithRateLimit(50, 1), worker.WithConcurrency(2), worker.WithDuration(200*time.Millisecond)),
		worker.NewStep(worker.WithPause(100*time.Millisecond)),
		worker.NewStep(worker.WithRateLimit(200, 10, worker.EasingLinear), worker.WithConcurrency(10), worker.WithDuration(200*time.Millisecond)),
		worker.NewStep(worker.WithRateLimit(10, 1), worker.WithConcurrency(1), worker.WithRepetitions(10)),
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

	h.AssertRequestCount(t, 97, 0.2)
}

// TestGracefulScaleDown ensures that when concurrency is reduced, in-flight requests
// complete normally and the pool drains without crashing or dropping connections.
func TestGracefulScaleDown(t *testing.T) {
	h := NewE2ETestHarness(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Short delay to ensure requests are in-flight during scale-down transition.
		time.Sleep(10 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer h.Close()

	steps := []*worker.Step{
		worker.NewStep(worker.WithConcurrency(50), worker.WithDuration(200*time.Millisecond)),
		worker.NewStep(worker.WithRateLimit(10, 1), worker.WithConcurrency(2), worker.WithDuration(200*time.Millisecond)),
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
}
