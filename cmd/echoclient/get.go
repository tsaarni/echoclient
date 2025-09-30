package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/tsaarni/echoclient/metrics"
	"github.com/tsaarni/echoclient/worker"
)

func runGet(args []string) {
	getCmd := flag.NewFlagSet("get", flag.ExitOnError)
	concurrency := getCmd.Int("concurrency", 1, "Number of concurrent workers")
	repetitions := getCmd.Int("repetitions", 0, "Number of repetitions per worker")
	addr := getCmd.String("addr", "http://localhost:8080", "Server URL")

	if err := getCmd.Parse(args); err != nil {
		fmt.Printf("Failed to parse flags: %v\n", err)
		return
	}

	reps := "infinite"
	if *repetitions > 0 {
		reps = fmt.Sprintf("%d", *repetitions)
	}

	fmt.Printf("Running 'get' with concurrency=%d, repetitions=%s, addr=%s\n", *concurrency, reps, *addr)

	http := metrics.NewMeasuringHTTPClient()

	doGet := func(ctx context.Context) error {
		resp, err := http.Get(*addr)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return nil
	}

	r := worker.NewWorkerPool(
		doGet,
		worker.WithConcurrency(*concurrency),
		worker.WithRepetitions(*repetitions),
	)
	r.Launch().Wait()

	metrics.DumpMetrics()
}
