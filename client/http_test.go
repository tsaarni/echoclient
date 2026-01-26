package client

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewMeasuringHTTPClient(t *testing.T) {
	client := NewMeasuringHTTPClient()

	// Verify the client has a transport
	if client.Transport == nil {
		t.Error("expected transport to be set")
	}

	_, ok := client.Transport.(*MeasuringRoundTripper)
	if !ok {
		t.Error("expected transport to be MeasuringRoundTripper")
	}
}

func TestNewMeasuringRoundTripperWithNil(t *testing.T) {
	rt := NewMeasuringRoundTripper(nil)
	mrt, ok := rt.(*MeasuringRoundTripper)
	if !ok {
		t.Fatal("expected MeasuringRoundTripper type")
	}

	// Should use DefaultTransport when nil is passed
	if mrt.next != http.DefaultTransport {
		t.Error("expected DefaultTransport when nil is passed")
	}
}

func TestMeasuringRoundTripperSuccess(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer server.Close()

	// Create a measuring round tripper
	rt := NewMeasuringRoundTripper(http.DefaultTransport)

	// Create a request
	req, err := http.NewRequest("GET", server.URL, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}

	// Execute the round trip
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
}

// mockFailingTransport is a transport that always returns an error
type mockFailingTransport struct {
	err error
}

func (m *mockFailingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, m.err
}

func TestMeasuringRoundTripperNetworkError(t *testing.T) {
	// Create a mock transport that returns a net.OpError
	opErr := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: errors.New("connection refused"),
	}

	mockTransport := &mockFailingTransport{err: opErr}
	rt := NewMeasuringRoundTripper(mockTransport)

	req, _ := http.NewRequest("GET", "http://localhost:9999", nil)

	resp, err := rt.RoundTrip(req)

	// Should return the error
	if err == nil {
		t.Error("expected error from round trip")
		if resp != nil {
			resp.Body.Close()
		}
	}

	// Error should be the net.OpError we created
	var gotOpErr *net.OpError
	if !errors.As(err, &gotOpErr) {
		t.Errorf("expected net.OpError, got %T", err)
	}
}

func TestMeasuringRoundTripperNonOpError(t *testing.T) {
	// Create a mock transport that returns a generic error (not net.OpError)
	genericErr := errors.New("some generic error")
	mockTransport := &mockFailingTransport{err: genericErr}
	rt := NewMeasuringRoundTripper(mockTransport)

	req, _ := http.NewRequest("GET", "http://localhost:9999", nil)

	_, err := rt.RoundTrip(req)

	if err == nil {
		t.Error("expected error from round trip")
	}

	if err.Error() != "some generic error" {
		t.Errorf("expected 'some generic error', got %v", err)
	}
}

func TestMeasuringRoundTripperWithDifferentMethods(t *testing.T) {
	methods := []string{"GET", "POST", "PUT", "DELETE", "PATCH"}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != method {
					t.Errorf("expected method %s, got %s", method, r.Method)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			rt := NewMeasuringRoundTripper(http.DefaultTransport)
			req, _ := http.NewRequest(method, server.URL, nil)
			resp, err := rt.RoundTrip(req)
			if err != nil {
				t.Fatalf("round trip failed: %v", err)
			}
			resp.Body.Close()
		})
	}
}

func TestMeasuringRoundTripperWithDifferentStatusCodes(t *testing.T) {
	statusCodes := []int{200, 201, 400, 404, 500, 502, 503}

	for _, code := range statusCodes {
		t.Run("status_"+string(rune(code)), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
			}))
			defer server.Close()

			rt := NewMeasuringRoundTripper(http.DefaultTransport)
			req, _ := http.NewRequest("GET", server.URL, nil)
			resp, err := rt.RoundTrip(req)
			if err != nil {
				t.Fatalf("round trip failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != code {
				t.Errorf("expected status %d, got %d", code, resp.StatusCode)
			}
		})
	}
}
