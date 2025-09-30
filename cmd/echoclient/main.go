package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tsaarni/echoclient/metrics"
)

func setupMetricsOnInterrupt() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigs
		fmt.Println("\nInterrupted. Dumping metrics:")
		metrics.DumpMetrics()
		os.Exit(1)
	}()
}

func setupPeriodicMetricsDump(interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		for range ticker.C {
			metrics.DumpMetrics()
		}
	}()
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <subcommand> [options]\n  Subcommands: get, upload\n", os.Args[0])
		os.Exit(1)
	}

	subcommand := os.Args[1]

	setupMetricsOnInterrupt()
	setupPeriodicMetricsDump(5 * time.Second)

	switch subcommand {
	case "get":
		runGet(os.Args[2:])
	case "upload":
		runUpload(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand: %s\n", subcommand)
		os.Exit(1)
	}
}
