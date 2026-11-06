package metrics

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestFormatLabelsSorted(t *testing.T) {
	if got := formatLabelsSorted(nil); got != "—" {
		t.Errorf("expected —, got %s", got)
	}

	labels := map[string]string{"method": "GET", "status": "200"}
	expected := `method="GET", status="200"`
	if got := formatLabelsSorted(labels); got != expected {
		t.Errorf("expected %s, got %s", expected, got)
	}
}

func TestHumanizeMetric(t *testing.T) {
	tests := []struct {
		name string
		val  float64
		want string
	}{
		{"process_resident_memory_bytes", 1024 * 1024, "1.0 MB"},
		{"process_start_time_seconds", 1700000000, time.Unix(1700000000, 0).Format("2006-01-02 15:04:05 MST")},
		{"go_goroutines", 1234, "1,234"},
		{"runtime_seconds", 12.3456, "12.346s"},
		{"http_client_request_duration_seconds_p50", 0.0000579123, "57.9µs"},
		{"http_client_request_duration_seconds_p99", 1.23456, "1.23s"},
		{"op_duration_seconds_p50", 0.000000195021, "195ns"},
		{"op_wait_seconds", 0.0123456, "12.3ms"},
		{"process_start_time_seconds", 1700000000, time.Unix(1700000000, 0).Format("2006-01-02 15:04:05 MST")},
		{"op_seconds_total", 2.5, "2.5"},
		{"http_client_requests_total", 5678, "5,678"},
		{"unknown_metric", 99.9, "99.9"},
	}

	for _, tt := range tests {
		got := humanizeMetric(tt.name, tt.val)
		if got != tt.want {
			t.Errorf("humanizeMetric(%s, %v) = %q, want %q", tt.name, tt.val, got, tt.want)
		}
	}
}

func TestPercentileFromHistogram(t *testing.T) {
	hEmpty := &dto.Histogram{}
	if got := percentileFromHistogram(hEmpty, 0.5); got != 0 {
		t.Errorf("expected 0 for empty histogram, got %v", got)
	}

	count := uint64(10)
	sum := 15.0
	b1 := float64(1.0)
	c1 := uint64(2)
	b2 := float64(2.0)
	c2 := uint64(8)
	b3 := float64(3.0)
	c3 := uint64(10)
	h := &dto.Histogram{
		SampleCount: &count,
		SampleSum:   &sum,
		Bucket: []*dto.Bucket{
			{UpperBound: &b1, CumulativeCount: &c1},
			{UpperBound: &b2, CumulativeCount: &c2},
			{UpperBound: &b3, CumulativeCount: &c3},
		},
	}

	// Rank 5 is in bucket (1, 2] with 6 samples after 2: 1 + 3/6.
	if got := percentileFromHistogram(h, 0.5); got != 1.5 {
		t.Errorf("expected p50 to be 1.5, got %v", got)
	}

	// Rank 9 is in bucket (2, 3] with 2 samples after 8: 2 + 1/2.
	if got := percentileFromHistogram(h, 0.9); got != 2.5 {
		t.Errorf("expected p90 to be 2.5, got %v", got)
	}
}

func TestPercentileFromNativeHistogram(t *testing.T) {
	tests := []struct {
		name       string
		values     []float64
		percentile float64
		want       float64
	}{
		{"uniform-p50", seq(0.001, 1, 1000), 0.5, 0.5},
		{"uniform-p99", seq(0.001, 1, 1000), 0.99, 0.99},
		{"sub-ms", seq(0.00005, 0.0001, 100), 0.5, 0.000075},
		{"two-clusters-low", append(seq(0.001, 0.0011, 50), seq(1, 1.1, 50)...), 0.25, 0.00105},
		{"two-clusters-high", append(seq(0.001, 0.0011, 50), seq(1, 1.1, 50)...), 0.75, 1.05},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := prometheus.NewHistogram(latencyHistogramOpts("test_seconds", "test"))
			for _, v := range tt.values {
				h.Observe(v)
			}
			var m dto.Metric
			if err := h.Write(&m); err != nil {
				t.Fatal(err)
			}
			got := percentileFromHistogram(m.GetHistogram(), tt.percentile)
			if math.Abs(got-tt.want)/tt.want > 0.05 {
				t.Errorf("expected %v within 5%%, got %v", tt.want, got)
			}
		})
	}
}

// seq returns n values evenly spaced from start to end.
func seq(start, end float64, n int) []float64 {
	values := make([]float64, n)
	for i := range values {
		values[i] = start + (end-start)*float64(i)/float64(n-1)
	}
	return values
}

func TestSynthetizeRateMetrics(t *testing.T) {
	nameRequests := "http_client_requests_total"
	typeCounter := dto.MetricType_COUNTER
	valRequests := 100.0
	k := "status"
	v := "200"
	requestsFamily := &dto.MetricFamily{
		Name: &nameRequests,
		Type: &typeCounter,
		Metric: []*dto.Metric{
			{
				Label:   []*dto.LabelPair{{Name: &k, Value: &v}},
				Counter: &dto.Counter{Value: &valRequests},
			},
		},
	}

	prevMetricValues = map[string]map[string]float64{
		"http_client_requests_total": {},
		"http_client_errors_total":   {},
	}
	prevMetricsDumpTime = time.Now().Add(-10 * time.Second)

	families := []*dto.MetricFamily{requestsFamily}
	synthetizeRateMetrics(&families)

	if len(families) != 2 {
		t.Fatalf("expected 2 families after rate synthesis, got %d", len(families))
	}

	rateFamily := families[1]
	if rateFamily.GetName() != "http_client_requests_per_second" {
		t.Errorf("expected rate family name, got %s", rateFamily.GetName())
	}

	val := rateFamily.Metric[0].GetGauge().GetValue()
	if val < 9.9 || val > 10.1 {
		t.Errorf("expected rate close to 10.0, got %f", val)
	}
}

func TestTabularDump(t *testing.T) {
	var buf bytes.Buffer
	snap := &metricSnapshot{
		timestamp: time.Now(),
		entries: []metricEntry{
			{"metric_a", nil, 1.0},
			{"metric_b", map[string]string{"k": "v"}, 2.0},
		},
	}
	tabularDump(&buf, snap)

	out := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("metric_a")) {
		t.Errorf("expected output to contain metric_a, got:\n%s", out)
	}
	if bytes.Contains(buf.Bytes(), []byte("\033")) {
		t.Errorf("expected no colors when output is not a terminal, got:\n%q", out)
	}
}

type failingCollector struct{ desc *prometheus.Desc }

func (c failingCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }
func (c failingCollector) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.NewInvalidMetric(c.desc, errors.New("collector failed"))
}

func TestDumpWithFailingCollector(t *testing.T) {
	c := failingCollector{prometheus.NewDesc("failing_metric", "", nil, nil)}
	prometheus.MustRegister(c)
	defer prometheus.Unregister(c)

	var buf bytes.Buffer
	DumpMetrics(&buf)
	out := buf.String()
	if !strings.Contains(out, "worker_pool_active_workers") {
		t.Errorf("expected other metrics in output, got:\n%s", out)
	}
	if !strings.Contains(out, "collector failed") {
		t.Errorf("expected error in output, got:\n%s", out)
	}

	buf.Reset()
	DumpMetricsJSON(&buf)
	var report jsonReport
	if err := json.Unmarshal(buf.Bytes(), &report); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(report.Metrics) == 0 || !strings.Contains(report.Error, "collector failed") {
		t.Errorf("expected metrics and error in JSON, got:\n%s", buf.String())
	}
}
