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

var (
	HttpClientRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_client_requests_total",
			Help: "Total number of HTTP client requests.",
		},
		[]string{"method", "code", "host"},
	)

	HttpClientRequestDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_client_request_duration_seconds",
			Help:    "Histogram of latencies for HTTP client requests.",
			Buckets: prometheus.DefBuckets,
		},
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

	SchedulerRequestLatencySeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "scheduler_request_latency_seconds",
			Help:    "Request latency including wait time when all workers are busy due to slow server responses.",
			Buckets: prometheus.DefBuckets,
		},
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
