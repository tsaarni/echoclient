package metrics

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestStartPrometheusServer(t *testing.T) {
	get := func(srv *http.Server, path string) (int, string) {
		t.Helper()
		resp, err := http.Get("http://" + srv.Addr + path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	// Two servers in the same process.
	srv, err := StartPrometheusServer("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	srv2, err := StartPrometheusServer("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv2.Close() }()

	if _, body := get(srv2, "/metrics"); !strings.Contains(body, "worker_pool_active_workers") {
		t.Errorf("expected metrics, got:\n%s", body)
	}

	// Handlers on http.DefaultServeMux are not exposed.
	http.HandleFunc("/test-default-mux", func(w http.ResponseWriter, r *http.Request) {})
	if code, _ := get(srv, "/test-default-mux"); code != http.StatusNotFound {
		t.Errorf("expected 404 for default mux handler, got %d", code)
	}

	if _, err := StartPrometheusServer(srv.Addr); err == nil {
		t.Error("expected error when address is in use")
	}
}
