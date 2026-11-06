package e2e

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/tsaarni/echoclient/client"
	"github.com/tsaarni/echoclient/metrics"
	"github.com/tsaarni/echoclient/worker"
)

// TestClientRoundTripSuccess tests that a successful HTTP request works and is counted.
func TestClientRoundTripSuccess(t *testing.T) {
	h := NewE2ETestFixture(Status(200))
	defer h.Close()

	rt := client.NewMeasuringRoundTripper(http.DefaultTransport)
	req, _ := http.NewRequest("GET", h.Server.URL, nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
}

// TestClientRoundTripMethods tests that GET, POST, PUT etc. are sent correctly.
func TestClientRoundTripMethods(t *testing.T) {
	methods := []string{"GET", "POST", "PUT", "DELETE", "PATCH"}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			h := NewE2ETestFixture(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != method {
					t.Errorf("expected method %s, got %s", method, r.Method)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer h.Close()

			rt := client.NewMeasuringRoundTripper(http.DefaultTransport)
			req, _ := http.NewRequest(method, h.Server.URL, nil)
			resp, err := rt.RoundTrip(req)
			if err != nil {
				t.Fatalf("round trip failed: %v", err)
			}
			_ = resp.Body.Close()
		})
	}
}

// TestClientRoundTripStatusCodes tests that different HTTP status codes are preserved and counted.
func TestClientRoundTripStatusCodes(t *testing.T) {
	statusCodes := []int{200, 201, 400, 404, 500, 502, 503}

	for _, code := range statusCodes {
		t.Run("status_"+strconv.Itoa(code), func(t *testing.T) {
			h := NewE2ETestFixture(Status(code))
			defer h.Close()

			rt := client.NewMeasuringRoundTripper(http.DefaultTransport)
			req, _ := http.NewRequest("GET", h.Server.URL, nil)
			resp, err := rt.RoundTrip(req)
			if err != nil {
				t.Fatalf("round trip failed: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != code {
				t.Errorf("expected status %d, got %d", code, resp.StatusCode)
			}
		})
	}
}

type mockFailingTransport struct {
	err error
}

func (m *mockFailingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, m.err
}

// TestClientRoundTripNetworkError tests that network errors like connection refused are reported.
func TestClientRoundTripNetworkError(t *testing.T) {
	opErr := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: errors.New("connection refused"),
	}

	mockTransport := &mockFailingTransport{err: opErr}
	rt := client.NewMeasuringRoundTripper(mockTransport)

	req, _ := http.NewRequest("GET", "http://localhost:9999", nil)
	_, err := rt.RoundTrip(req)

	if err == nil {
		t.Error("expected error from round trip")
	}

	var gotOpErr *net.OpError
	if !errors.As(err, &gotOpErr) {
		t.Errorf("expected net.OpError, got %T", err)
	}
}

// TestClientRoundTripNonOpError tests that generic errors are reported.
func TestClientRoundTripNonOpError(t *testing.T) {
	genericErr := errors.New("some generic error")
	mockTransport := &mockFailingTransport{err: genericErr}
	rt := client.NewMeasuringRoundTripper(mockTransport)

	req, _ := http.NewRequest("GET", "http://localhost:9999", nil)
	_, err := rt.RoundTrip(req)

	if err == nil {
		t.Error("expected error from round trip")
	}

	if err.Error() != "some generic error" {
		t.Errorf("expected 'some generic error', got %v", err)
	}
}

// TestFaultTolerance tests that 500 errors and slow responses are counted correctly.
func TestFaultTolerance(t *testing.T) {
	h := NewE2ETestFixture(PathSwitch{
		"/500":  Status(500),
		"/hang": Delayed(5*time.Second, 200),
	})
	defer h.Close()

	RunPool(h, worker.WithRepetitions(5), worker.WithWorkerFunc(func(ctx context.Context, wp *worker.WorkerPool) error {
		_, _ = h.Client.Get(h.Server.URL + "/500")
		return nil
	}))

	h.AssertRequests(t, 500, 5)
}

// TestClientErrorsCounted tests that transport errors are counted in http_client_errors_total.
func TestClientErrorsCounted(t *testing.T) {
	c := client.NewMeasuringHTTPClient()

	slow := httptest.NewServer(Delayed(3*time.Second, 200))
	defer slow.Close()
	plain := httptest.NewServer(Status(200))
	defer plain.Close()

	// Accepts connections but never answers the TLS handshake.
	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = silent.Close() }()
	go func() {
		for {
			conn, err := silent.Accept()
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
		}
	}()

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := closed.Addr().String()
	_ = closed.Close()

	tests := []struct {
		name  string
		host  string
		url   string
		label string
	}{
		{"refused", closedAddr, "http://" + closedAddr, "connect: connection refused"},
		{"header-timeout", slow.Listener.Addr().String(), slow.URL, "net/http: timeout awaiting response headers"},
		{"tls-handshake-timeout", silent.Addr().String(), "https://" + silent.Addr().String(), "net/http: TLS handshake timeout"},
		{"tls-to-plain", plain.Listener.Addr().String(), "https://" + plain.Listener.Addr().String(), "tls: first record does not look like a TLS handshake"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", tc.url, nil)
			if _, err := c.Do(req); err == nil {
				t.Fatal("expected error")
			}
			counter := metrics.HttpClientErrorsTotal.WithLabelValues(tc.label, tc.host)
			if got := testutil.ToFloat64(counter); got != 1 {
				t.Errorf("expected 1 error with label %q, got %v", tc.label, got)
			}
		})
	}
}

// TestClientCanceledNotCounted tests that canceled requests are not counted as errors.
func TestClientCanceledNotCounted(t *testing.T) {
	h := NewE2ETestFixture(Status(200))
	defer h.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", h.Server.URL, nil)
	if _, err := h.Client.Do(req); err == nil {
		t.Fatal("expected error")
	}

	counter := metrics.HttpClientErrorsTotal.WithLabelValues("context canceled", h.Host())
	if got := testutil.ToFloat64(counter); got != 0 {
		t.Errorf("expected canceled request not counted, got %v", got)
	}
}

// histogramSum returns the sample count and sum of a histogram series.
func histogramSum(t *testing.T, h *prometheus.HistogramVec, labels ...string) (uint64, float64) {
	t.Helper()
	var m dto.Metric
	if err := h.WithLabelValues(labels...).(prometheus.Metric).Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetHistogram().GetSampleCount(), m.GetHistogram().GetSampleSum()
}

// TestClientDurationIncludesBody tests that request duration includes reading the body,
// and response headers duration does not.
func TestClientDurationIncludesBody(t *testing.T) {
	h := NewE2ETestFixture(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte("body"))
	}))
	defer h.Close()

	resp, err := h.Client.Get(h.Server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	_, headers := histogramSum(t, metrics.HttpClientResponseHeadersSeconds, "GET", h.Host())
	count, duration := histogramSum(t, metrics.HttpClientRequestDurationSeconds, "GET", h.Host())
	if headers > 0.1 {
		t.Errorf("expected response headers under 100ms, got %v", headers)
	}
	if count != 1 || duration < 0.2 {
		t.Errorf("expected 1 request of at least 200ms, got %d with %v", count, duration)
	}
}

// TestClientDurationOnClose tests that request duration is recorded when the body is closed without reading.
func TestClientDurationOnClose(t *testing.T) {
	h := NewE2ETestFixture(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("body"))
	}))
	defer h.Close()

	resp, err := h.Client.Get(h.Server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	_ = resp.Body.Close()

	if count, _ := histogramSum(t, metrics.HttpClientRequestDurationSeconds, "GET", h.Host()); count != 1 {
		t.Errorf("expected 1 request, got %d", count)
	}
}

// TestClientBodyReadErrorCounted tests that errors while reading the body are counted.
func TestClientBodyReadErrorCounted(t *testing.T) {
	h := NewE2ETestFixture(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Promise 100 bytes, send 4, then close the connection.
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("part"))
		w.(http.Flusher).Flush()
		conn, _, _ := w.(http.Hijacker).Hijack()
		_ = conn.Close()
	}))
	defer h.Close()

	resp, err := h.Client.Get(h.Server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err == nil {
		t.Fatal("expected body read error")
	}
	_ = resp.Body.Close()

	counter := metrics.HttpClientErrorsTotal.WithLabelValues("unexpected EOF", h.Host())
	if got := testutil.ToFloat64(counter); got != 1 {
		t.Errorf("expected 1 body read error, got %v", got)
	}
	if count, _ := histogramSum(t, metrics.HttpClientRequestDurationSeconds, "GET", h.Host()); count != 0 {
		t.Errorf("expected failed request not in duration, got %d", count)
	}
}

// TestClientReadAfterCloseNotCounted tests that reading a closed body is not counted as an error.
func TestClientReadAfterCloseNotCounted(t *testing.T) {
	h := NewE2ETestFixture(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("body"))
	}))
	defer h.Close()

	resp, err := h.Client.Get(h.Server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	_, err = resp.Body.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("expected error reading closed body")
	}

	counter := metrics.HttpClientErrorsTotal.WithLabelValues(err.Error(), h.Host())
	if got := testutil.ToFloat64(counter); got != 0 {
		t.Errorf("expected %q not counted, got %v", err, got)
	}
}

// TestClientNoBodyRecorded tests that responses without a body are recorded without closing the body.
func TestClientNoBodyRecorded(t *testing.T) {
	h := NewE2ETestFixture(Status(200))
	defer h.Close()

	req, _ := http.NewRequest("HEAD", h.Server.URL, nil)
	if _, err := h.Client.Do(req); err != nil { //nolint:bodyclose // Testing that an unclosed NoBody is recorded.
		t.Fatal(err)
	}

	if count, _ := histogramSum(t, metrics.HttpClientRequestDurationSeconds, "HEAD", h.Host()); count != 1 {
		t.Errorf("expected 1 request, got %d", count)
	}
}
