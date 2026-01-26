package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/tsaarni/echoclient/client"
	"github.com/tsaarni/echoclient/worker"
)

func runGet(args []string) {
	cmd := flag.NewFlagSet("get", flag.ExitOnError)
	url := cmd.String("url", "http://localhost:8080", "Server URL")
	concurrency := cmd.Int("concurrency", 1, "Number of concurrent workers")
	repetitions := cmd.Int("repetitions", 0, "Total number of repetitions across all workers (0 = infinite repetitions)")
	duration := cmd.Duration("duration", 0, "Duration of the load test (0 = run until repetitions complete)")
	rps := cmd.Int("rps", 0, "Requests per second allowed across all workers (0 = no limit)")
	rampUpPeriod := cmd.Duration("ramp-up-period", 0, "Ramp-up period to reach target rps (0 = no ramp-up)")

	if err := cmd.Parse(args); err != nil {
		fmt.Printf("Failed to parse flags: %v\n", err)
		return
	}

	if *rampUpPeriod > 0 && *rps == 0 {
		fmt.Println("Ramp-up period specified without a target RPS. Ignoring ramp-up.")
		os.Exit(1)
	}

	reps := "infinite"
	if *repetitions > 0 {
		reps = fmt.Sprintf("%d", *repetitions)
	}

	dur := "no time limit"
	if *duration > 0 {
		dur = duration.String()
	}

	fmt.Printf("Running 'get' with url=%s, concurrency=%d, repetitions=%s, duration=%s\n", *url, *concurrency, reps, dur)

	client := client.NewMeasuringHTTPClient()

	doGet := func(ctx context.Context, wp *worker.WorkerPool) error {
		req, err := http.NewRequestWithContext(ctx, "GET", *url, nil)
		if err != nil {
			return err
		}

		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return nil
	}

	var steps []*worker.Step

	// Ramp-up (optional).
	if *rampUpPeriod > 0 {
		steps = append(steps, worker.NewStep(
			worker.WithDuration(*rampUpPeriod),
			worker.WithConcurrency(*concurrency),
			worker.WithRateLimit(*rps, *rps / *concurrency, worker.EasingLinear),
		))
	}

	// Steady traffic.
	steadyDuration := time.Duration(0) // 0 = infinite.
	if *duration > *rampUpPeriod {
		steadyDuration = *duration - *rampUpPeriod // Remaining time after ramp-up.
	}

	steps = append(steps, worker.NewStep(
		worker.WithDuration(steadyDuration),
		worker.WithConcurrency(*concurrency),
		worker.WithRateLimit(*rps, *rps / *concurrency),
		worker.WithRepetitions(*repetitions),
	))

	w := worker.NewMultiStepWorkerPool(doGet, steps)

	if err := w.Launch(); err != nil {
		fmt.Printf("Failed to launch worker pool: %v\n", err)
		os.Exit(1)
	}
	w.Wait()
}
