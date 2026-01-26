package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/tsaarni/echoclient/client"
	"github.com/tsaarni/echoclient/metrics"
	"github.com/tsaarni/echoclient/worker"
)

func main() {
	httpClient := client.NewMeasuringHTTPClient()

	// Start a test server.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
	}))
	defer ts.Close()

	fmt.Printf("Targeting server at %s\n", ts.URL)

	// Work function.
	loadFuncGet := func(ctx context.Context, wp *worker.WorkerPool) error {
		req, err := http.NewRequestWithContext(ctx, "GET", ts.URL, nil)
		if err != nil {
			return err
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	}

	// Alternative work function for demonstration.
	loadFuncPost := func(ctx context.Context, wp *worker.WorkerPool) error {
		req, err := http.NewRequestWithContext(ctx, "POST", ts.URL, nil)
		if err != nil {
			return err
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	}

	// Helper for hooks to match LifeCycleFunc signature
	hook := func(msg string) worker.LifeCycleFunc {
		return func(ctx context.Context, wp *worker.WorkerPool) {
			printSeparator(msg)
		}
	}

	// Define traffic profile with step-specific options.
	profile := []*worker.Step{
		worker.NewStep(
			worker.WithDuration(5*time.Second),
			worker.WithRateLimit(100, 100, worker.EasingLinear),
			worker.WithConcurrency(10),
			worker.WithHooks(
				hook("Step 1: Linear ramp up to 100 RPS and 10 concurrent workers"),
				hook("Step 1: Finished"),
			),
		),
		worker.NewStep(
			worker.WithDuration(5*time.Second),
			worker.WithRateLimit(100, 100),
			worker.WithConcurrency(20, worker.EasingLinear),
			worker.WithHooks(
				hook("Step 2: Steady state at 100 RPS, increasing concurrency to 20 linearly"),
				hook("Step 2: Finished"),
			),
		),
		worker.NewStep(
			worker.WithDuration(3*time.Second),
			worker.WithRateLimit(500, 500, worker.EasingIn),
			worker.WithConcurrency(50, worker.EasingLinear),
			worker.WithHooks(
				hook("Step 3: Ramp up to 500 RPS and 50 concurrent workers with ease in acceleration"),
				hook("Step 3: Finished"),
			),
		),
		// This step uses a different worker function
		worker.NewStep(
			worker.WithDuration(2*time.Second),
			worker.WithWorkerFunc(loadFuncPost), // Override worker for this step
			worker.WithRateLimit(10, 10),
			worker.WithConcurrency(5),
			worker.WithHooks(
				hook("Step 4: Health checks at 10 RPS and 5 concurrent workers"),
				hook("Step 4: Finished"),
			),
		),
		worker.NewStep(
			worker.WithDuration(5*time.Second),
			worker.WithRateLimit(10, 10, worker.EasingOut),
			worker.WithConcurrency(1, worker.EasingOut),
			worker.WithHooks(
				hook("Step 5: Ramp down to 10 RPS and 1 concurrent worker with ease out deceleration"),
				hook("Step 5: Finished"),
			),
		),
	}

	// Create multi-step worker pool with the profile.
	pool := worker.NewMultiStepWorkerPool(loadFuncGet, profile)

	fmt.Println("Starting traffic profile steps...")

	// Periodically print metrics to the console.
	ticker := time.NewTicker(5 * time.Second)
	go func() {
		for range ticker.C {
			metrics.DumpMetrics()
		}
	}()

	// Launch and wait for completion.
	if err := pool.Launch(); err != nil {
		panic(err)
	}
	pool.Wait()

	fmt.Println("Traffic profile steps completed.")
}

func printSeparator(msg string) {
	fmt.Printf("\n\033[32m\033[4m=== %s ===\033[0m\n", msg)
}
