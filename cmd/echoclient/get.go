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
	repetitions := cmd.Int("repetitions", 0, "Number of repetitions per worker (0 = infinite repetitions)")
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

	doGet := func(ctx context.Context) error {
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

	rpsInitial := *rps
	var rampUpFunc func(w *worker.WorkerPool)

	if *rps > 0 && *rampUpPeriod > 0 {
		fmt.Printf("  rps=%d, ramp-up-period=%s\n", *rps, *rampUpPeriod)
		rpsInitial = 1
		rampUpFunc = func(w *worker.WorkerPool) {
			start := time.Now()
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()

			for {
				elapsed := time.Since(start)
				if elapsed >= *rampUpPeriod {
					break
				}

				progress := float64(elapsed) / float64(*rampUpPeriod)
				target := int(float64(*rps) * progress)
				w.SetRateLimit(target, target / *concurrency)
				<-ticker.C
			}
			w.SetRateLimit(*rps, *rps / *concurrency)
		}
	}

	w := worker.NewWorkerPool(
		doGet,
		worker.WithConcurrency(*concurrency),
		worker.WithRepetitions(*repetitions),
		worker.WithRateLimit(rpsInitial, rpsInitial / *concurrency),
		worker.WithTimeout(*duration),
	)

	if rampUpFunc != nil {
		go rampUpFunc(w)
	}

	w.Launch().Wait()
}
