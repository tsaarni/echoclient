package metrics

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	httpClientRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_client_requests_total",
			Help: "Total number of HTTP client requests.",
		},
		[]string{"method", "code", "host"},
	)

	httpClientRequestDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_client_request_duration_seconds",
			Help:    "Histogram of latencies for HTTP client requests.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "host"},
	)

	httpClientErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_client_errors_total",
			Help: "Total number of HTTP client errors.",
		},
		[]string{"error", "host"},
	)

	// startTime records the time when the application started.
	startTime = time.Now()

	runtimeSeconds = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "runtime_seconds",
			Help: "Total runtime of the application in seconds.",
		},
	)

	currentTime = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "current_time",
			Help: "Current timestamp in seconds since epoch.",
		},
	)
)

type MeasuringRoundTripper struct {
	next http.RoundTripper
}

// NewMeasuringHTTPClient returns a new http.Client which records Prometheus metrics for each request.
func NewMeasuringHTTPClient() http.Client {
	return http.Client{
		Transport: NewMeasuringRoundTripper(nil),
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
			httpClientErrorsTotal.WithLabelValues(opErr.Err.Error(), req.URL.Host).Inc()
		}
	} else {
		duration := time.Since(start).Seconds()
		httpClientRequestDurationSeconds.WithLabelValues(req.Method, req.URL.Host).Observe(duration)
		httpClientRequestsTotal.WithLabelValues(req.Method, strconv.Itoa(resp.StatusCode), req.URL.Host).Inc()
	}
	return resp, err
}

// StartPrometheusServer starts an HTTP server exposing Prometheus metrics at /metrics.
// Call will block, so it should be run in a separate goroutine.
func StartPrometheusServer(addr string) {
	http.Handle("/metrics", promhttp.Handler())
	fmt.Printf("Starting Prometheus metrics server at %s/metrics\n", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		fmt.Printf("Prometheus metrics server failed: %v\n", err)
	}
}
