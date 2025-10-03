package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/tsaarni/echoclient/metrics"
	"github.com/tsaarni/echoclient/worker"
)

func runGet(args []string) {
	cmd := flag.NewFlagSet("get", flag.ExitOnError)
	url := cmd.String("url", "http://localhost:8080", "Server URL")
	concurrency := cmd.Int("concurrency", 1, "Number of concurrent workers")
	repetitions := cmd.Int("repetitions", 0, "Number of repetitions per worker (0 = infinite repetitions)")
	rps := cmd.Int("rps", 0, "Requests per second allowed across all workers (0 = no limit)")
	rampUpPeriod := cmd.Int("ramp-up-period", 0, "Ramp-up period in seconds to reach target rps (0 means no ramp-up)")

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

	fmt.Printf("Running 'get' with url=%s, concurrency=%d, repetitions=%s\n", *url, *concurrency, reps)

	http := metrics.NewMeasuringHTTPClient()

	doGet := func(ctx context.Context) error {
		resp, err := http.Get(*url)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return nil
	}

	// If ramp-up period is specified, start with a lower RPS and gradually increase to the target RPS.
	rpsInitial := *rps
	var rampUpFunc func(w *worker.WorkerPool)
	if *rps > 0 {
		fmt.Printf("  rps=%d, ramp-up-period=%d seconds\n", *rps, *rampUpPeriod)

		if *rampUpPeriod > 0 {
			rpsInitial = max(*rps/10, 1)
			rampUpFunc = func(w *worker.WorkerPool) {
				steps := *rampUpPeriod * 10 // 10 steps per second (100ms interval)
				for i := 1; i <= steps; i++ {
					target := *rps * i / steps
					w.SetRateLimit(target, target / *concurrency)
					time.Sleep(100 * time.Millisecond)
				}
				// Ensure final rate is set
				w.SetRateLimit(*rps, *rps / *concurrency)
			}
		}
	}

	w := worker.NewWorkerPool(
		doGet,
		worker.WithConcurrency(*concurrency),
		worker.WithRepetitions(*repetitions),
		worker.WithRateLimit(rpsInitial, rpsInitial / *concurrency),
	)

	if rampUpFunc != nil {
		go rampUpFunc(w)
	}

	w.Launch().Wait()

	metrics.DumpMetrics()
}
