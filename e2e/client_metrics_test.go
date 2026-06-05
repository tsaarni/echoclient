package e2e

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/tsaarni/echoclient/client"
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
