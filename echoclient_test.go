package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tsaarni/echoclient/generator"
	"github.com/tsaarni/echoclient/worker"
)

type recordedCall struct {
	Method      string
	ContentType string
	Path        string
	TotalSize   int64
}

type recordingServer struct {
	calls []recordedCall
	t     *testing.T
}

func newRecordingServer(t *testing.T) (*httptest.Server, *recordingServer) {
	rs := &recordingServer{t: t}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var totalSize int64
		buf := make([]byte, 32*1024)
		for {
			n, err := r.Body.Read(buf)
			totalSize += int64(n)
			if err != nil {
				break
			}
		}
		rc := recordedCall{
			Method:      r.Method,
			ContentType: r.Header.Get("Content-Type"),
			Path:        r.URL.Path,
			TotalSize:   totalSize,
		}
		rs.calls = append(rs.calls, rc)
		w.WriteHeader(http.StatusOK)
	}))
	return srv, rs
}
func TestUpload(t *testing.T) {
	ts, rs := newRecordingServer(t)
	defer ts.Close()

	// Patch doUpload to use the test server URL
	doUpload := func(ctx context.Context) error {
		reader := generator.NewReader(
			generator.WithTotalSize(1*generator.MB),
			generator.WithChunkSize(64*generator.KB),
		)
		resp, err := http.Post(ts.URL+"/upload", "application/octet-stream", reader)
		if err != nil {
			t.Fatal("Worker function returned error: ", err)
		}
		resp.Body.Close()
		return nil
	}

	r := worker.NewWorkerPool(
		doUpload,
		worker.WithConcurrency(2),
		worker.WithRepetitions(1),
	)
	r.Launch().Wait()

	if len(rs.calls) != 2 {
		t.Fatalf("Expected 2 calls to upload endpoint, got %d", len(rs.calls))
	}
	for _, c := range rs.calls {
		if c.Method != http.MethodPost {
			t.Errorf("Expected POST method, got %s", c.Method)
		}
		if c.ContentType != "application/octet-stream" {
			t.Errorf("Expected Content-Type application/octet-stream, got %s", c.ContentType)
		}
		if c.Path != "/upload" {
			t.Errorf("Expected path /upload, got %s", c.Path)
		}
		if c.TotalSize != 1*generator.MB {
			t.Errorf("Expected body size %d, got %d", 1*generator.MB, c.TotalSize)
		}
	}
}
