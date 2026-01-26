package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/dustin/go-humanize"
	"github.com/tsaarni/echoclient/client"
	"github.com/tsaarni/echoclient/generator"
	"github.com/tsaarni/echoclient/worker"
)

func runUpload(args []string) {
	cmd := flag.NewFlagSet("upload", flag.ExitOnError)
	url := cmd.String("url", "http://localhost:8080/upload", "Server URL")
	concurrency := cmd.Int("concurrency", 1, "Number of concurrent workers")
	repetitions := cmd.Int("repetitions", 1, "Total number of repetitions across all workers (0 = infinite repetitions)")
	duration := cmd.Duration("duration", 0, "Duration of the load test (0 = run until repetitions complete)")
	totalSize := cmd.String("size", "10MB", "Total size of data to upload per worker, specified in bytes")
	chunkSize := cmd.String("chunk", "64KB", "Chunk size for data generation, specified in bytes")

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

	dur := "no time limit"
	if *duration > 0 {
		dur = duration.String()
	}

	if parsedChunkSize > parsedTotalSize {
		parsedTotalSize = parsedChunkSize
	}

	fmt.Printf("Running 'upload' with url=%s, concurrency=%d, repetitions=%s, duration=%s, size=%s, chunk=%s\n",
		*url, *concurrency, reps, dur, humanize.Bytes(parsedTotalSize), humanize.Bytes(parsedChunkSize))

	client := client.NewMeasuringHTTPClient()

	doUpload := func(ctx context.Context, wp *worker.WorkerPool) error {
		reader := generator.NewReader(
			generator.WithTotalSize(parsedTotalSize),
			generator.WithChunkSize(parsedChunkSize),
		)
		req, err := http.NewRequestWithContext(ctx, "POST", *url, reader)
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/octet-stream")

		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	}

	opts := []worker.Option{
		worker.WithConcurrency(*concurrency),
		worker.WithDuration(*duration),
		worker.WithRepetitions(*repetitions),
	}

	w := worker.NewWorkerPool(doUpload, opts...)

	if _, err := w.Launch(); err != nil {
		fmt.Printf("Failed to launch worker pool: %v\n", err)
		os.Exit(1)
	}
	w.Wait()
}
