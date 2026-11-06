// Package metrics provides metric collection, formatting, and reporting.
package metrics

import (
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// latencyHistogramOpts returns options for a latency histogram.
//
// Native buckets grow by a factor of about 1.044, so percentiles in DumpMetrics are within
// about 4% at any latency. Classic buckets (100µs to 13s) are kept for text format scrapes.
// When more than 320 native buckets are needed, their resolution is reduced.
func latencyHistogramOpts(name, help string) prometheus.HistogramOpts {
	return prometheus.HistogramOpts{
		Name:                           name,
		Help:                           help,
		Buckets:                        prometheus.ExponentialBuckets(0.0001, 2, 18),
		NativeHistogramBucketFactor:    1.05,
		NativeHistogramMaxBucketNumber: 320,
	}
}

var (
	HttpClientRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_client_requests_total",
			Help: "Total number of HTTP client requests.",
		},
		[]string{"method", "code", "host"},
	)

	HttpClientRequestDurationSeconds = promauto.NewHistogramVec(
		latencyHistogramOpts("http_client_request_duration_seconds", "Duration of HTTP client requests, until the response body is read or closed."),
		[]string{"method", "host"},
	)

	HttpClientResponseHeadersSeconds = promauto.NewHistogramVec(
		latencyHistogramOpts("http_client_response_headers_seconds", "Time until HTTP client receives the response headers."),
		[]string{"method", "host"},
	)

	HttpClientErrorsTotal = promauto.NewCounterVec(
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

	WorkerPoolActiveWorkers = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "worker_pool_active_workers",
			Help: "Current number of active workers in the pool.",
		},
	)

	WorkerCallsTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "worker_calls_total",
			Help: "Number of worker function calls.",
		},
	)

	WorkerErrorsTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "worker_errors_total",
			Help: "Number of errors returned by worker functions.",
		},
	)

	SchedulerRequestLatencySeconds = promauto.NewHistogramVec(
		latencyHistogramOpts("scheduler_request_latency_seconds", "Request latency including wait time when all workers are busy due to slow server responses."),
		[]string{},
	)

	SchedulerSkippedRequestsTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "scheduler_skipped_requests_total",
			Help: "Number of requests that were never sent because the server was too slow and the scheduler skipped ahead to catch up.",
		},
	)
)

// StartPrometheusServer serves metrics at http://addr/metrics in the background.
// It returns after the listener is open. Close the returned server to stop it.
// The endpoint has no authentication, so bind addr to localhost unless remote access is needed.
func StartPrometheusServer(addr string) (*http.Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	srv := &http.Server{Addr: ln.Addr().String(), Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }() // Errors after the listener is open are not reported.
	return srv, nil
}
