package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/dustin/go-humanize"
	"github.com/tsaarni/echoclient/generator"
	"github.com/tsaarni/echoclient/metrics"
	"github.com/tsaarni/echoclient/worker"
)

func runUpload(args []string) {
	cmd := flag.NewFlagSet("upload", flag.ExitOnError)
	url := cmd.String("url", "http://localhost:8080/upload", "Server URL")
	concurrency := cmd.Int("concurrency", 1, "Number of concurrent workers")
	repetitions := cmd.Int("repetitions", 1, "Number of repetitions per worker (0 = infinite repetitions)")
	totalSize := cmd.String("size", "10MB", "Total size of data to upload per worker (in bytes)")
	chunkSize := cmd.String("chunk", "64KB", "Chunk size for data generation (in bytes)")

	if err := cmd.Parse(args); err != nil {
		fmt.Printf("Failed to parse flags: %v\n", err)
		return
	}

	parsedTotalSize, err := humanize.ParseBytes(*totalSize)
	if err != nil {
		fmt.Printf("Invalid totalsize: %v\n", err)
		return
	}
	parsedChunkSize, err := humanize.ParseBytes(*chunkSize)
	if err != nil {
		fmt.Printf("Invalid chunksize: %v\n", err)
		return
	}

	reps := "infinite"
	if *repetitions > 0 {
		reps = fmt.Sprintf("%d", *repetitions)
	}

	fmt.Printf("Running 'upload' with concurrency=%d, repetitions=%s, url=%s, totalsize=%d bytes, chunksize=%d bytes\n",
		*concurrency, reps, *url, parsedTotalSize, parsedChunkSize)

	http := metrics.NewMeasuringHTTPClient()

	doUpload := func(ctx context.Context) error {
		reader := generator.NewReader(
			generator.WithTotalSize(parsedTotalSize),
			generator.WithChunkSize(parsedChunkSize),
		)
		resp, err := http.Post(*url, "application/octet-stream", reader)
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	}

	w := worker.NewWorkerPool(
		doUpload,
		worker.WithConcurrency(*concurrency),
		worker.WithRepetitions(*repetitions),
	)

	w.Launch().Wait()

	metrics.DumpMetrics()
}
