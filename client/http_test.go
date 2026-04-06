package client

import (
	"net/http"
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
