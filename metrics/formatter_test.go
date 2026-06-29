package metrics

import (
	"bytes"
	"testing"
	"time"

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

	if got := percentileFromHistogram(h, 0.5); got != 2.0 {
		t.Errorf("expected p50 to be 2.0, got %v", got)
	}

	if got := percentileFromHistogram(h, 0.9); got != 3.0 {
		t.Errorf("expected p90 to be 3.0, got %v", got)
	}
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
}
