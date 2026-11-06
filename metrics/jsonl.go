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
	Error     string      `json:"error,omitempty"`
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
// If some metrics could not be gathered, the "error" field holds the error.
func DumpMetricsJSON(output io.Writer) {
	snap, err := gatherMetrics()
	if len(snap.entries) == 0 && err == nil {
		return
	}

	report := jsonReport{Timestamp: snap.timestamp.Format(time.RFC3339)}
	if err != nil {
		report.Error = err.Error()
	}
	for _, e := range snap.entries {
		report.Metrics = append(report.Metrics, jsonEntry{Name: e.name, Labels: e.labels, Value: e.value})
	}

	_ = json.NewEncoder(output).Encode(report)
}
