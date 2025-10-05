package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/tsaarni/echoclient/metrics"
)

func runSubcommand(subcommand string, subArgs []string) {
	// Print metrics on when interrupted.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigs
		fmt.Println("\nInterrupted. Dumping metrics:")
		metrics.DumpMetrics()
		os.Exit(1)
	}()

	// Print metrics every 5 seconds.
	ticker := time.NewTicker(5 * time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ticker.C:
				metrics.DumpMetrics()
			case <-ctx.Done():
				return
			}
		}
	}()

	switch subcommand {
	case "get":
		runGet(subArgs)
	case "upload":
		runUpload(subArgs)
	}
	ticker.Stop()
	cancel()
	wg.Wait()
	metrics.DumpMetrics()
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <subcommand> [options]\n  Subcommands: get, upload\n", os.Args[0])
		os.Exit(1)
	}

	subcommand := os.Args[1]
	subArgs := os.Args[2:]

	switch subcommand {
	case "get", "upload":
		runSubcommand(subcommand, subArgs)
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand: %s\n", subcommand)
		os.Exit(1)
	}
}
