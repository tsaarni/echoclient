package client

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/tsaarni/echoclient/metrics"
)

// MeasuringRoundTripper is a wrapper for http.RoundTripper that records Prometheus metrics for each request.
type MeasuringRoundTripper struct {
	next http.RoundTripper
}

// NewMeasuringHTTPClient returns a new http.Client which records Prometheus metrics for each request.
// The client uses following settings:
// - 2 second timeout for connecting, TLS handshake and response header to avoid slow connections.
// - 10000 max connections per host to allow for many concurrent connections.
// - 10000 max idle connections to avoid closing connections too early.
// - Disable TLS server certificate verification to allow self-signed certificates.
func NewMeasuringHTTPClient() http.Client {
	t := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   2 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxConnsPerHost:       10000,
		MaxIdleConns:          10000,
		MaxIdleConnsPerHost:   10000,
		TLSHandshakeTimeout:   2 * time.Second,
		ResponseHeaderTimeout: 2 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
	}

	return http.Client{
		Transport: NewMeasuringRoundTripper(t),
		// Timeout:   10 * time.Second,  // Total request timeout.
	}
}

// NewMeasuringRoundTripper returns a new wrapped http.RoundTripper that records Prometheus metrics for each request.
// This allows for custom transport settings to be used instead of the settings defined in NewMeasuringHTTPClient.
func NewMeasuringRoundTripper(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &MeasuringRoundTripper{next: next}
}

// RoundTrip is called for each HTTP request and records Prometheus metrics for it.
func (rt *MeasuringRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := rt.next.RoundTrip(req)
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) {
			metrics.HttpClientErrorsTotal.WithLabelValues(opErr.Err.Error(), req.URL.Host).Inc()
		}
	} else {
		duration := time.Since(start).Seconds()
		metrics.HttpClientRequestDurationSeconds.WithLabelValues(req.Method, req.URL.Host).Observe(duration)
		metrics.HttpClientRequestsTotal.WithLabelValues(req.Method, strconv.Itoa(resp.StatusCode), req.URL.Host).Inc()
	}
	return resp, err
}
