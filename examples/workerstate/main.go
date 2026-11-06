// Package main implements the 'workerstate' example for the echoclient.
// Each worker keeps its own TCP connection to an echo server.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/tsaarni/echoclient/metrics"
	"github.com/tsaarni/echoclient/worker"
)

// connKey is the context key for the connection of a worker.
type connKey struct{}

// echoConn is the connection of a worker.
type echoConn struct {
	conn   net.Conn
	reader *bufio.Reader
}

// roundTripSeconds records the latency of one echo round trip.
var roundTripSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
	Name:    "echo_round_trip_seconds",
	Help:    "Latency of one echo round trip.",
	Buckets: prometheus.ExponentialBuckets(0.00005, 2, 16),
})

func main() {
	// 1. Start a TCP echo server.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	// 2. Send a line and wait for the echo, using the connection of this worker.
	loadFunc := func(ctx context.Context, wp *worker.WorkerPool) error {
		c := ctx.Value(connKey{}).(*echoConn)
		start := time.Now()
		if _, err := c.conn.Write([]byte("hello\n")); err != nil {
			return err
		}
		if _, err := c.reader.ReadString('\n'); err != nil {
			return err
		}
		roundTripSeconds.Observe(time.Since(start).Seconds())
		return nil
	}

	pool := worker.NewWorkerPool(loadFunc,
		worker.WithConcurrency(10),
		worker.WithRateLimit(1000, 1000),
		worker.WithDuration(3*time.Second),
	)

	// 3. Open one connection per worker and close it when the worker exits.
	pool.SetWorkerHooks(
		func(ctx context.Context, wp *worker.WorkerPool) (context.Context, error) {
			var d net.Dialer
			conn, err := d.DialContext(ctx, "tcp", ln.Addr().String())
			if err != nil {
				return ctx, err
			}
			return context.WithValue(ctx, connKey{}, &echoConn{conn, bufio.NewReader(conn)}), nil
		},
		func(ctx context.Context, wp *worker.WorkerPool) {
			_ = ctx.Value(connKey{}).(*echoConn).conn.Close()
		},
	)

	if err := pool.Launch(); err != nil {
		log.Fatal(err)
	}
	pool.Wait()

	fmt.Println("Final metrics:")
	metrics.DumpMetrics(os.Stdout)
}
