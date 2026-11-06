package metrics

import (
	"fmt"
	"io"
	"math"
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
// If some metrics fail, it returns the others together with the error.
func gatherMetrics() (*metricSnapshot, error) {
	runtimeSeconds.Set(time.Since(startTime).Seconds())
	currentTime.Set(float64(time.Now().Unix()))

	metricFamilies, err := prometheus.DefaultGatherer.Gather()
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

	return snap, err
}

// DumpMetrics logs the current values of all registered metrics in tabular format.
// Colors are used only when output is a terminal.
func DumpMetrics(output io.Writer) {
	snap, err := gatherMetrics()
	if len(snap.entries) == 0 {
		_, _ = fmt.Fprintln(output, "No Prometheus metrics to display.")
	} else {
		tabularDump(output, snap)
	}
	if err != nil {
		_, _ = fmt.Fprintf(output, "Failed to gather some metrics: %v\n", err)
	}
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

// percentileFromHistogram estimates the given percentile from a histogram.
// Native histogram buckets are used when present, otherwise classic buckets.
func percentileFromHistogram(histogram *dto.Histogram, percentile float64) float64 {
	total := histogram.GetSampleCount()
	if total == 0 {
		return 0
	}
	buckets := classicBuckets(histogram)
	if histogram.Schema != nil {
		buckets = nativeBuckets(histogram)
	}

	// Interpolate linearly within the bucket that holds the rank.
	rank := float64(total) * percentile
	cumulative, last := 0.0, 0.0
	for _, b := range buckets {
		if b.count > 0 && cumulative+b.count >= rank {
			return b.lower + (b.upper-b.lower)*(rank-cumulative)/b.count
		}
		cumulative, last = cumulative+b.count, b.upper
	}
	return last
}

// bucket is a histogram bucket that holds count samples in (lower, upper].
type bucket struct{ lower, upper, count float64 }

// classicBuckets returns the classic buckets of a histogram.
// Samples above the highest bucket are not included.
func classicBuckets(histogram *dto.Histogram) []bucket {
	var buckets []bucket
	lower, prevCount := 0.0, 0.0
	for _, b := range histogram.GetBucket() {
		count := float64(b.GetCumulativeCount())
		buckets = append(buckets, bucket{lower, b.GetUpperBound(), count - prevCount})
		lower, prevCount = b.GetUpperBound(), count
	}
	return buckets
}

// nativeBuckets returns the positive native buckets of a histogram.
// Samples in the zero bucket and negative buckets are put in a first bucket at 0.
func nativeBuckets(histogram *dto.Histogram) []bucket {
	// Native bucket i holds samples in (base^(i-1), base^i].
	base := math.Exp2(math.Exp2(-float64(histogram.GetSchema())))
	other := float64(histogram.GetSampleCount())
	var buckets []bucket
	var index int32
	var count int64
	deltas := histogram.GetPositiveDelta()
	for _, span := range histogram.GetPositiveSpan() {
		index += span.GetOffset()
		for range span.GetLength() {
			count += deltas[0]
			deltas = deltas[1:]
			buckets = append(buckets, bucket{math.Pow(base, float64(index-1)), math.Pow(base, float64(index)), float64(count)})
			other -= float64(count)
			index++
		}
	}
	return append([]bucket{{0, 0, other}}, buckets...)
}

// roundSignificant rounds d to 3 significant digits.
func roundSignificant(d time.Duration) time.Duration {
	r := time.Duration(1)
	for d >= 1000*r {
		r *= 10
	}
	return d.Round(r)
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
		strings.HasPrefix(name, "scheduler_request_latency_seconds"):
		return roundSignificant(time.Duration(val * float64(time.Second))).String()
	case name == "runtime_seconds" || name == "process_cpu_seconds_total":
		return time.Duration(val * float64(time.Second)).Round(time.Millisecond).String()
	case name == "http_client_requests_total" || name == "http_client_errors_total" ||
		name == "http_client_requests_per_second" || name == "http_client_errors_per_second" ||
		name == "scheduler_skipped_requests_total" || name == "worker_errors_total":
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
