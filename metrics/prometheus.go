// Package metrics provides metric collection, formatting, and reporting.
package metrics

import (
	"fmt"
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
		latencyHistogramOpts("http_client_request_duration_seconds", "Histogram of latencies for HTTP client requests."),
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

// StartPrometheusServer starts an HTTP server exposing Prometheus metrics at /metrics.
// Call will block, so it should be run in a separate goroutine.
func StartPrometheusServer(addr string) {
	http.Handle("/metrics", promhttp.Handler())
	fmt.Printf("Starting Prometheus metrics server at %s/metrics\n", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		fmt.Printf("Prometheus metrics server failed: %v\n", err)
	}
}
