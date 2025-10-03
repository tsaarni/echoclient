package metrics

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

type tableRow struct {
	metric string
	labels string
	value  string
}

// Previous metric values for rate calculation:
// These rate metrics are generated client-side for console display, since there is no Prometheus server to perform the calculation.
// map[metricName][labels]prevValue
var prevMetricValues = map[string]map[string]float64{
	"http_client_requests_total": {},
	"http_client_errors_total":   {},
}

var prevMetricsDumpTime time.Time = time.Now()

// DumpMetrics logs the current values of all registered metrics.
func DumpMetrics() {
	runtimeSeconds.Set(time.Since(startTime).Seconds())
	currentTime.Set(float64(time.Now().Unix()))

	metricFamilies, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		fmt.Printf("failed to gather metrics: %v\n", err)
		return
	}

	synthetizeRateMetrics(&metricFamilies)

	rows := buildMetricRows(metricFamilies)
	if len(rows) == 0 {
		fmt.Println("No Prometheus metrics to display.")
		return
	}

	tabularDump(rows)
}

// buildMetricRows constructs table rows for metrics.
func buildMetricRows(families []*dto.MetricFamily) []tableRow {
	rows := make([]tableRow, 0)

	for _, family := range families {
		metricName := family.GetName()

		if skipMetric(metricName) {
			continue
		}

		for _, metric := range family.GetMetric() {
			labels := formatLabels(metric.GetLabel())

			switch family.GetType() {
			case dto.MetricType_COUNTER:
				val := metric.GetCounter().GetValue()
				rows = append(rows, tableRow{metricName, labels, humanizeMetric(metricName, val)})
			case dto.MetricType_GAUGE:
				val := metric.GetGauge().GetValue()
				rows = append(rows, tableRow{metricName, labels, humanizeMetric(metricName, val)})
			case dto.MetricType_HISTOGRAM:
				rows = append(rows, buildHistogramRow(metricName, labels, metric.GetHistogram()))
			}
		}
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].metric == rows[j].metric {
			return rows[i].labels < rows[j].labels
		}
		return rows[i].metric < rows[j].metric
	})

	return rows
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
	}
	return slices.Contains(skipExact, name)
}

// buildHistogramRow builds a table row for selected percentiles.
func buildHistogramRow(name, labels string, histogram *dto.Histogram) tableRow {
	desiredPercentiles := []float64{0.5, 0.9, 0.95, 0.99}
	parts := []string{}
	for _, p := range desiredPercentiles {
		upper := percentileFromHistogram(histogram, p)
		// Example: p90=123ms
		percentileLabel := fmt.Sprintf("p%.0f=%s", p*100, humanizeMetric(name, upper))
		parts = append(parts, percentileLabel)
	}
	return tableRow{name, labels, strings.Join(parts, ", ")}
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

// formatLabels formats label pairs into single string.
func formatLabels(pairs []*dto.LabelPair) string {
	if len(pairs) == 0 {
		return "—"
	}

	labels := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		labels = append(labels, fmt.Sprintf("%s=\"%s\"", pair.GetName(), pair.GetValue()))
	}
	sort.Strings(labels)
	return strings.Join(labels, ", ")
}

// humanizeMetric returns a human-readable string for metrics.
func humanizeMetric(name string, val float64) string {
	switch name {
	case "process_network_receive_bytes_total", "process_network_transmit_bytes_total", "process_resident_memory_bytes", "process_virtual_memory_bytes", "process_virtual_memory_max_bytes":
		return humanize.Bytes(uint64(val))
	case "process_start_time_seconds", "current_time":
		t := time.Unix(int64(val), 0)
		return t.Format("2006-01-02 15:04:05 MST")
	case "process_max_fds", "process_open_fds", "go_goroutines", "go_threads":
		return humanize.Comma(int64(val))
	case "runtime_seconds", "http_client_request_duration_seconds", "process_cpu_seconds_total":
		d := time.Duration(val * float64(time.Second)).Round(time.Millisecond)
		return d.String()
	case "http_client_requests_total", "http_client_errors_total", "http_client_requests_per_second", "http_client_errors_per_second":
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
		labels := formatLabels(metric.GetLabel())
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
