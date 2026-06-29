package metrics

import (
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// desiredPercentiles is the set of histogram percentiles reported by all formatters.
var desiredPercentiles = []float64{0.5, 0.9, 0.95, 0.99}

// metricEntry is the shared intermediate representation for a single metric data point.
type metricEntry struct {
	name   string
	labels map[string]string
	value  float64
}

// metricSnapshot holds all metrics collected during a single dump period.
type metricSnapshot struct {
	timestamp time.Time
	entries   []metricEntry
}

// Previous metric values for rate calculation:
// These rate metrics are generated client-side for console display, since there is no Prometheus server to perform the calculation. Normally this would be done e.g. by Grafana.
// map[metricName][labels]prevValue
var prevMetricValues = map[string]map[string]float64{
	"http_client_requests_total": {},
	"http_client_errors_total":   {},
}

var prevMetricsDumpTime time.Time = time.Now()

// gatherMetrics collects and returns a snapshot of all current metrics.
func gatherMetrics() (*metricSnapshot, error) {
	runtimeSeconds.Set(time.Since(startTime).Seconds())
	currentTime.Set(float64(time.Now().Unix()))

	metricFamilies, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return nil, err
	}
	synthetizeRateMetrics(&metricFamilies)

	snap := &metricSnapshot{timestamp: time.Now()}

	for _, family := range metricFamilies {
		name := family.GetName()
		if skipMetric(name) {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := labelsToMap(metric.GetLabel())
			switch family.GetType() {
			case dto.MetricType_COUNTER:
				snap.entries = append(snap.entries, metricEntry{name, labels, metric.GetCounter().GetValue()})
			case dto.MetricType_GAUGE:
				snap.entries = append(snap.entries, metricEntry{name, labels, metric.GetGauge().GetValue()})
			case dto.MetricType_HISTOGRAM:
				for _, p := range desiredPercentiles {
					val := percentileFromHistogram(metric.GetHistogram(), p)
					pName := fmt.Sprintf("%s_p%.0f", name, p*100)
					snap.entries = append(snap.entries, metricEntry{pName, labels, val})
				}
			}
		}
	}

	sort.Slice(snap.entries, func(i, j int) bool {
		if snap.entries[i].name == snap.entries[j].name {
			return formatLabelsSorted(snap.entries[i].labels) < formatLabelsSorted(snap.entries[j].labels)
		}
		return snap.entries[i].name < snap.entries[j].name
	})

	return snap, nil
}

// DumpMetrics logs the current values of all registered metrics in tabular format.
func DumpMetrics(output io.Writer) {
	snap, err := gatherMetrics()
	if err != nil {
		fmt.Printf("failed to gather metrics: %v\n", err)
		return
	}
	if len(snap.entries) == 0 {
		fmt.Println("No Prometheus metrics to display.")
		return
	}
	tabularDump(output, snap)
}

// formatLabelsSorted formats a label map into a sorted display string.
func formatLabelsSorted(labels map[string]string) string {
	if len(labels) == 0 {
		return "—"
	}
	parts := make([]string, 0, len(labels))
	for k, v := range labels {
		parts = append(parts, fmt.Sprintf("%s=\"%s\"", k, v))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func labelsToMap(pairs []*dto.LabelPair) map[string]string {
	if len(pairs) == 0 {
		return nil
	}
	m := make(map[string]string, len(pairs))
	for _, p := range pairs {
		m[p.GetName()] = p.GetValue()
	}
	return m
}

// skipMetric returns true if the given metric name should be skipped.
func skipMetric(name string) bool {
	skipPrefixes := []string{
		"go_gc", "go_memstats", "process_virtual_memory",
	}
	for _, prefix := range skipPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}

	skipExact := []string{
		"go_sched_gomaxprocs_threads", "process_max_fds", "go_info",
		"process_network_receive_bytes_total", "process_network_transmit_bytes_total",
	}
	return slices.Contains(skipExact, name)
}

// percentileFromHistogram returns the upper bound value for the given percentile.
func percentileFromHistogram(histogram *dto.Histogram, percentile float64) float64 {
	total := histogram.GetSampleCount()
	if total == 0 || len(histogram.Bucket) == 0 {
		return 0
	}

	// How many samples should be below this percentile.
	target := uint64(float64(total) * percentile)

	// Find the first bucket where the cumulative count meets or exceeds the target.
	for _, b := range histogram.Bucket {
		if b.GetCumulativeCount() >= target {
			return b.GetUpperBound()
		}
	}

	// If not found, return the highest bucket.
	return histogram.Bucket[len(histogram.Bucket)-1].GetUpperBound()
}

// humanizeMetric returns a human-readable string for metrics.
func humanizeMetric(name string, val float64) string {
	switch {
	case name == "process_resident_memory_bytes":
		return humanize.Bytes(uint64(val))
	case name == "process_start_time_seconds" || name == "current_time":
		return time.Unix(int64(val), 0).Format("2006-01-02 15:04:05 MST")
	case name == "process_open_fds" || name == "go_goroutines" || name == "go_threads" || name == "worker_pool_active_workers":
		return humanize.Comma(int64(val))
	case strings.HasPrefix(name, "http_client_request_duration_seconds") ||
		strings.HasPrefix(name, "scheduler_request_latency_seconds") ||
		name == "runtime_seconds" || name == "process_cpu_seconds_total":
		return time.Duration(val * float64(time.Second)).Round(time.Millisecond).String()
	case name == "http_client_requests_total" || name == "http_client_errors_total" ||
		name == "http_client_requests_per_second" || name == "http_client_errors_per_second" ||
		name == "scheduler_skipped_requests_total":
		return humanize.Comma(int64(val))
	default:
		return fmt.Sprintf("%v", val)
	}
}

// synthetizeRateMetrics computes and adds per-second rate gauge metrics for http_client_requests_total and http_client_errors_total.
func synthetizeRateMetrics(metricFamilies *[]*dto.MetricFamily) {
	fromMetrics := []struct {
		fromName string
		rateName string
	}{
		{"http_client_requests_total", "http_client_requests_per_second"},
		{"http_client_errors_total", "http_client_errors_per_second"},
	}

	now := time.Now()
	elapsed := now.Sub(prevMetricsDumpTime).Seconds()

	for _, m := range fromMetrics {
		fromFamily := findMetricFamily(*metricFamilies, m.fromName)
		if fromFamily == nil {
			continue
		}
		rateFamily := buildRateMetricFamily(fromFamily, m.fromName, m.rateName, elapsed)
		*metricFamilies = append(*metricFamilies, rateFamily)
	}
	prevMetricsDumpTime = now
}

// findMetricFamily returns the pointer to the metric family with the given name, or nil if not found.
func findMetricFamily(families []*dto.MetricFamily, name string) *dto.MetricFamily {
	for _, fam := range families {
		if fam.GetName() == name {
			return fam
		}
	}
	return nil
}

// buildRateMetricFamily builds a new metric family for the per-second rate.
func buildRateMetricFamily(fromFamily *dto.MetricFamily, fromName, rateName string, elapsed float64) *dto.MetricFamily {
	rateFamily := &dto.MetricFamily{
		Name: &rateName,
		Type: dto.MetricType_GAUGE.Enum(),
	}
	for _, metric := range fromFamily.GetMetric() {
		labels := formatLabelsSorted(labelsToMap(metric.GetLabel()))
		val := metric.GetCounter().GetValue()
		prevVal := prevMetricValues[fromName][labels]
		rate := (val - prevVal) / elapsed
		newMetric := &dto.Metric{
			Label: metric.Label,
			Gauge: &dto.Gauge{Value: &rate},
		}
		rateFamily.Metric = append(rateFamily.Metric, newMetric)
		prevMetricValues[fromName][labels] = val
	}
	return rateFamily
}
