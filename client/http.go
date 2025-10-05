package client

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/tsaarni/echoclient/metrics"
)

type MeasuringRoundTripper struct {
	next http.RoundTripper
}

// NewMeasuringHTTPClient returns a new http.Client which records Prometheus metrics for each request.
func NewMeasuringHTTPClient() http.Client {
	t := &http.Transport{
		Dial: (&net.Dialer{
			Timeout:   2 * time.Second,
			KeepAlive: 30 * time.Second,
		}).Dial,
		ForceAttemptHTTP2:     true,
		MaxConnsPerHost:       10000,
		MaxIdleConns:          10000,
		MaxIdleConnsPerHost:   10000,
		TLSHandshakeTimeout:   2 * time.Second,
		ResponseHeaderTimeout: 2 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return http.Client{
		Transport: NewMeasuringRoundTripper(t),
	}
}

// NewMeasuringRoundTripper returns a new InstrumentedRoundTripper wrapping the provided RoundTripper.
func NewMeasuringRoundTripper(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &MeasuringRoundTripper{next: next}
}

// RoundTrip records metrics for each request.
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
