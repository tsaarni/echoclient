package metrics

import (
	"encoding/json"
	"io"
	"time"
)

type jsonEntry struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Value  float64           `json:"value"`
}

type jsonReport struct {
	Timestamp string      `json:"ts"`
	Metrics   []jsonEntry `json:"metrics"`
}

// DumpMetricsJSON writes current metrics as a single JSON line (JSONL) per dump period.
//
// Each line is a JSON object with the following structure:
//
//	{"ts":"<RFC3339>","metrics":[{"name":"<metric>","labels":{"<key>":"<val>"},"value":<float64>}, ...]}
//
// The "labels" field is omitted when the metric has no labels.
// Histogram metrics emit separate entries for each percentile (p50, p90, p95, p99)
// with the percentile appended to the metric name (e.g. "http_client_request_duration_seconds_p50").
func DumpMetricsJSON(output io.Writer) {
	snap, err := gatherMetrics()
	if err != nil || len(snap.entries) == 0 {
		return
	}

	report := jsonReport{Timestamp: snap.timestamp.Format(time.RFC3339)}
	for _, e := range snap.entries {
		report.Metrics = append(report.Metrics, jsonEntry{Name: e.name, Labels: e.labels, Value: e.value})
	}

	_ = json.NewEncoder(output).Encode(report)
}
