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
	uploadCmd := flag.NewFlagSet("upload", flag.ExitOnError)
	concurrency := uploadCmd.Int("concurrency", 1, "Number of concurrent workers")
	repetitions := uploadCmd.Int("repetitions", 1, "Number of repetitions per worker")
	totalSize := uploadCmd.String("totalsize", "10MB", "Total size of data to upload per worker (in bytes)")
	chunkSize := uploadCmd.String("chunksize", "64KB", "Chunk size for data generation (in bytes)")
	addr := uploadCmd.String("addr", "http://localhost:8080/upload", "Server URL")

	if err := uploadCmd.Parse(args); err != nil {
		fmt.Printf("Failed to parse flags: %v\n", err)
		return
	}

	reps := "infinite"
	if *repetitions > 0 {
		reps = fmt.Sprintf("%d", *repetitions)
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

	fmt.Printf("Running 'upload' with concurrency=%d, repetitions=%s, addr=%s\n", *concurrency, reps, *addr)
	fmt.Printf("    totalsize=%d bytes, chunksize=%d bytes\n", parsedTotalSize, parsedChunkSize)

	http := metrics.NewMeasuringHTTPClient()

	doUpload := func(ctx context.Context) error {
		reader := generator.NewReader(
			generator.WithTotalSize(parsedTotalSize),
			generator.WithChunkSize(parsedChunkSize),
		)
		resp, err := http.Post(*addr, "application/octet-stream", reader)
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	}

	r := worker.NewWorkerPool(
		doUpload,
		worker.WithConcurrency(*concurrency),
		worker.WithRepetitions(*repetitions),
	)
	r.Launch().Wait()

	metrics.DumpMetrics()
}
