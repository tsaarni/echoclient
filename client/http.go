// Package client provides HTTP client functionality for the echoclient.
package client

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/tsaarni/echoclient/metrics"
)

// MeasuringRoundTripper is a wrapper for http.RoundTripper that records Prometheus metrics for each request.
type MeasuringRoundTripper struct {
	next http.RoundTripper
}

// NewMeasuringHTTPClient returns a new http.Client which records Prometheus metrics for each request.
//
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
// The request duration is recorded when the response body is read to the end or closed.
func (rt *MeasuringRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	host := req.URL.Host
	resp, err := rt.next.RoundTrip(req)
	if err != nil {
		countError(err, host)
		return resp, err
	}
	metrics.HttpClientResponseHeadersSeconds.WithLabelValues(req.Method, host).Observe(time.Since(start).Seconds())
	metrics.HttpClientRequestsTotal.WithLabelValues(req.Method, strconv.Itoa(resp.StatusCode), host).Inc()

	// end records the request when it ends with err: the duration if it completed, otherwise the error.
	end := func(err error) {
		if err == nil || err == io.EOF {
			metrics.HttpClientRequestDurationSeconds.WithLabelValues(req.Method, host).Observe(time.Since(start).Seconds())
		} else {
			countError(err, host)
		}
	}

	// Without a body, or when the body is a connection (101 Switching Protocols), the request ends here.
	if resp.Body == http.NoBody || resp.StatusCode == http.StatusSwitchingProtocols {
		end(nil)
	} else {
		resp.Body = &measuringBody{ReadCloser: resp.Body, end: end}
	}
	return resp, nil
}

// measuringBody calls end once, when the body is read to the end, fails, or is closed.
type measuringBody struct {
	io.ReadCloser
	end  func(error)
	once sync.Once
}

func (b *measuringBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.once.Do(func() { b.end(err) })
	}
	return n, err
}

func (b *measuringBody) Close() error {
	b.once.Do(func() { b.end(nil) })
	return b.ReadCloser.Close()
}

// countError counts err in http_client_errors_total.
// context.Canceled is not counted, since requests are canceled when the worker pool stops.
// For net.OpError the label omits the addresses.
func countError(err error, host string) {
	if errors.Is(err, context.Canceled) {
		return
	}
	label := err.Error()
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		label = opErr.Err.Error()
	}
	metrics.HttpClientErrorsTotal.WithLabelValues(label, host).Inc()
}
