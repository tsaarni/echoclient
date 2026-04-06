package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/tsaarni/echoclient/client"
	"github.com/tsaarni/echoclient/metrics"
	"github.com/tsaarni/echoclient/worker"
)

// E2ETestFixture holds an HTTP test server and a measuring client.
type E2ETestFixture struct {
	Server *httptest.Server
	Client *http.Client
}

// NewE2ETestFixture creates a fixture with a test server using the given handler and an instrumented client.
func NewE2ETestFixture(handler http.Handler) *E2ETestFixture {
	server := httptest.NewServer(handler)
	c := client.NewMeasuringHTTPClient()
	return &E2ETestFixture{
		Server: server,
		Client: &c,
	}
}

// Status returns a handler that writes the given HTTP status code.
func Status(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
	}
}

// Delayed returns a handler that sleeps for the duration d before writing the status code.
func Delayed(d time.Duration, code int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(d)
		w.WriteHeader(code)
	}
}

// PathSwitch is a handler that routes requests to other handlers based on the URL path.
type PathSwitch map[string]http.HandlerFunc

// ServeHTTP implements the http.Handler interface for PathSwitch by routing the request r to a handler.
func (ps PathSwitch) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h, ok := ps[r.URL.Path]; ok {
		h(w, r)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// Close shuts down the test server.
func (h *E2ETestFixture) Close() {
	h.Server.Close()
}

// Host returns the host and port of the test server.
func (h *E2ETestFixture) Host() string {
	u, _ := url.Parse(h.Server.URL)
	return u.Host
}

// RunPool runs a worker pool on the fixture h with the given options.
func RunPool(h *E2ETestFixture, opts ...worker.Option) {
	wp := worker.NewWorkerPool(
		func(ctx context.Context, wp *worker.WorkerPool) error {
			req, _ := http.NewRequestWithContext(ctx, "GET", h.Server.URL, nil)
			_, _ = h.Client.Do(req)
			return nil
		},
		opts...,
	)
	wp.Launch()
	wp.Wait()
}

// RunMultiStepPool runs a worker pool on the fixture h that follows the given sequence of steps.
func RunMultiStepPool(h *E2ETestFixture, steps []*worker.Step) {
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

// AssertRequests verifies that the client recorded the expected number of requests for the given status code.
func (h *E2ETestFixture) AssertRequests(t *testing.T, code int, expected float64) {
	t.Helper()
	counter := metrics.HttpClientRequestsTotal.WithLabelValues("GET", strconv.Itoa(code), h.Host())
	if got := testutil.ToFloat64(counter); got != expected {
		t.Errorf("Expected %f requests with status %d, got %f", expected, code, got)
	}
}

// AssertRequestsApprox verifies that the recorded 200 OK requests are within the margin of the expected count.
func (h *E2ETestFixture) AssertRequestsApprox(t *testing.T, expected float64, margin float64) {
	t.Helper()
	counter := metrics.HttpClientRequestsTotal.WithLabelValues("GET", "200", h.Host())
	count := testutil.ToFloat64(counter)
	if count < expected*(1-margin) || count > expected*(1+margin) {
		t.Errorf("Expected request count around %f (±%f), got %f", expected, margin, count)
	}
}

// AssertActiveWorkers verifies that the worker pool reports the expected number of active workers.
func (h *E2ETestFixture) AssertActiveWorkers(t *testing.T, expected float64) {
	t.Helper()
	if got := testutil.ToFloat64(metrics.WorkerPoolActiveWorkers); got != expected {
		t.Errorf("Expected %f active workers, got %f", expected, got)
	}
}
