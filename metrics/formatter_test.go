package metrics

import (
	"bytes"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
)

func TestFormatLabels(t *testing.T) {
	if got := formatLabels(nil); got != "—" {
		t.Errorf("expected —, got %s", got)
	}

	k1 := "method"
	v1 := "GET"
	k2 := "status"
	v2 := "200"
	pairs := []*dto.LabelPair{
		{Name: &k1, Value: &v1},
		{Name: &k2, Value: &v2},
	}
	expected := `method="GET", status="200"`
	if got := formatLabels(pairs); got != expected {
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

func TestBuildMetricRows(t *testing.T) {
	nameCounter := "http_client_requests_total"
	typeCounter := dto.MetricType_COUNTER
	valCounter := 42.0
	counterFamily := &dto.MetricFamily{
		Name: &nameCounter,
		Type: &typeCounter,
		Metric: []*dto.Metric{
			{Counter: &dto.Counter{Value: &valCounter}},
		},
	}

	nameGauge := "worker_pool_active_workers"
	typeGauge := dto.MetricType_GAUGE
	valGauge := 5.0
	gaugeFamily := &dto.MetricFamily{
		Name: &nameGauge,
		Type: &typeGauge,
		Metric: []*dto.Metric{
			{Gauge: &dto.Gauge{Value: &valGauge}},
		},
	}

	nameHistogram := "http_client_request_duration_seconds"
	typeHistogram := dto.MetricType_HISTOGRAM
	count := uint64(10)
	sum := 1.5
	b1 := float64(0.1)
	c1 := uint64(5)
	b2 := float64(0.5)
	c2 := uint64(10)
	histogramFamily := &dto.MetricFamily{
		Name: &nameHistogram,
		Type: &typeHistogram,
		Metric: []*dto.Metric{
			{
				Histogram: &dto.Histogram{
					SampleCount: &count,
					SampleSum:   &sum,
					Bucket: []*dto.Bucket{
						{UpperBound: &b1, CumulativeCount: &c1},
						{UpperBound: &b2, CumulativeCount: &c2},
					},
				},
			},
		},
	}

	nameGo := "go_gc_duration_seconds"
	typeGo := dto.MetricType_COUNTER
	goFamily := &dto.MetricFamily{
		Name: &nameGo,
		Type: &typeGo,
	}

	families := []*dto.MetricFamily{counterFamily, gaugeFamily, histogramFamily, goFamily}
	rows := buildMetricRows(families)

	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}

	if rows[0].metric != "http_client_request_duration_seconds" {
		t.Errorf("expected row 0 to be duration, got %s", rows[0].metric)
	}
	if rows[1].metric != "http_client_requests_total" {
		t.Errorf("expected row 1 to be requests, got %s", rows[1].metric)
	}
	if rows[2].metric != "worker_pool_active_workers" {
		t.Errorf("expected row 2 to be workers, got %s", rows[2].metric)
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
				Label: []*dto.LabelPair{{Name: &k, Value: &v}},
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
	rows := []tableRow{
		{"metric_a", "label_a", "val_a"},
		{"metric_b", "label_b", "val_b"},
	}
	tabularDump(&buf, rows)

	out := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("metric_a")) {
		t.Errorf("expected output to contain metric_a, got:\n%s", out)
	}
}
