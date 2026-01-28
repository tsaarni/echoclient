package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/tsaarni/echoclient/client"
	"github.com/tsaarni/echoclient/metrics"
	"github.com/tsaarni/echoclient/worker"
)

func main() {
	// Create an HTTP client instrumented for metrics.
	httpClient := client.NewMeasuringHTTPClient()

	// Define the load function to be executed by each worker in a loop.
	// This example sends a GET request to the target server, but you can implement more complex sequences,
	// such as performing authentication followed by multiple requests.
	loadFunc := func(ctx context.Context, wp *worker.WorkerPool) error {
		req, err := http.NewRequestWithContext(ctx, "GET", "http://localhost:8080/", nil)
		if err != nil {
			return err
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return nil
	}

	// Periodically print metrics to the console.
	ticker := time.NewTicker(5 * time.Second)
	go func() {
		for range ticker.C {
			metrics.DumpMetrics(os.Stdout)
		}
	}()

	// Create and launch a worker pool.
	pool := worker.NewWorkerPool(
		loadFunc,
		worker.WithConcurrency(100),
		worker.WithInfiniteRepetitions(),
	)
	if err := pool.Launch(); err != nil {
		log.Fatal(err)
	}
	pool.Wait()
}
