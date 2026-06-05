// Package main implements the 'compose' example for the echoclient.
package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"time"

	"github.com/tsaarni/echoclient/client"
	"github.com/tsaarni/echoclient/metrics"
	"github.com/tsaarni/echoclient/worker"
)

func main() {
	httpClient := client.NewMeasuringHTTPClient()

	// 1. Start a mock server representing our target application.
	// /read: Always succeeds (HTTP 200)
	// /write: Fails 2 times, then succeeds on the 3rd attempt (simulating transient write failures).
	var writeAttempts atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/read":
			w.WriteHeader(http.StatusOK)
		case "/write":
			attempt := writeAttempts.Add(1)
			if attempt%3 != 0 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	fmt.Printf("Mock server targeting: %s\n", ts.URL)

	// 2. Define the operations as worker.WorkerFunc types
	var readAction worker.WorkerFunc = func(ctx context.Context, wp *worker.WorkerPool) error {
		req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+"/read", nil)
		if err != nil {
			return err
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		return nil
	}

	var writeAction worker.WorkerFunc = func(ctx context.Context, wp *worker.WorkerPool) error {
		req, err := http.NewRequestWithContext(ctx, "POST", ts.URL+"/write", nil)
		if err != nil {
			return err
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode >= 500 {
			return fmt.Errorf("transient write error: HTTP %d", resp.StatusCode)
		}
		return nil
	}

	// 3. Compose: 
	// - wrap readAction with simple constant retries (2 attempts, 50ms delay)
	// - wrap writeAction with exponential backoff and Full Jitter (3 attempts, 10-100ms)
	// - mix readAction (80%) and writeAction (20%) using methods on WorkerFunc.
	composedWorker := worker.Mix(
		readAction.Retry(2, 50*time.Millisecond).Weighted(80),
		writeAction.RetryWithBackoff(3, 10*time.Millisecond, 100*time.Millisecond).Weighted(20),
	)

	// 4. Periodically dump metrics to console
	ticker := time.NewTicker(2 * time.Second)
	stopTicker := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				fmt.Println("\n--- Metrics Snapshot ---")
				metrics.DumpMetrics(os.Stdout)
			case <-stopTicker:
				ticker.Stop()
				return
			}
		}
	}()

	// 5. Run the worker pool with the composed workload
	fmt.Println("Launching composed worker pool (80% Reads / 20% Writes)...")
	pool := worker.NewWorkerPool(
		composedWorker,
		worker.WithConcurrency(10),
		worker.WithRepetitions(200), // Run 200 total composed actions
	)

	if err := pool.Launch(); err != nil {
		panic(err)
	}
	pool.Wait()

	close(stopTicker)
	fmt.Println("\nLoad test completed.")
}
