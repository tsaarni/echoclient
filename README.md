# Echoclient

Echoclient is a load testing package for Go. It supports:

- **Workload modeling** to define load patterns.
- **Traffic shaping** with easing functions.
- **Metrics** for real-time performance monitoring.

It also includes a command-line tool for quick HTTP load tests.

![Demo](./examples/demo.gif)

## Installation

```bash
go get github.com/tsaarni/echoclient
```

## Examples

### Simple Usage

Runs a customized load function (not limited to HTTP) using 10 concurrent workers for 10 seconds.

```go
loadFunc := func(ctx context.Context, wp *worker.WorkerPool) error {
    resp, err := http.Get("http://localhost:8080")
    if err == nil {
        resp.Body.Close()
    }
    return err
}

pool := worker.NewWorkerPool(
    loadFunc,
    worker.WithConcurrency(10),
    worker.WithDuration(10*time.Second),
)
pool.Launch()
pool.Wait()
```

### Multi-Step Traffic Profile

Ramps up to 100 RPS over 5 seconds, holds it for 10 seconds while scaling workers from 10 to 20, and ramps down to zero over 5 seconds.

```go
profile := []*worker.Step{
    worker.NewStep(
        worker.WithDuration(5*time.Second),
        worker.WithRateLimit(100, 100, worker.EasingLinear),
        worker.WithConcurrency(10),
    ),
    worker.NewStep(
        worker.WithDuration(10*time.Second),
        worker.WithRateLimit(100, 100),
        worker.WithConcurrency(20, worker.EasingLinear),
    ),
    worker.NewStep(
        worker.WithDuration(5*time.Second),
        worker.WithRateLimit(0, 0, worker.EasingOut),
    ),
}
pool := worker.NewMultiStepWorkerPool(loadFunc, profile)
pool.Launch()
pool.Wait()
```

### Worker Composition

Composes multiple tasks with relative weights (e.g. 80% reads, 20% writes) and retry behaviors.

```go
var readFunc worker.WorkerFunc = func(ctx context.Context, wp *worker.WorkerPool) error { /* ... */ }
var writeFunc worker.WorkerFunc = func(ctx context.Context, wp *worker.WorkerPool) error { /* ... */ }

// Mix read (weight 4) and write (weight 1)
composedWorker := worker.Mix(
    readFunc.Weighted(4),
    writeFunc.Retry(3, 100*time.Millisecond).Weighted(1),
)

pool := worker.NewWorkerPool(composedWorker, worker.WithConcurrency(50))
pool.Launch()
pool.Wait()
```

### Test Data Generator

Streams random data of a specific size without loading all of it into memory.

```go
body := generator.NewReader(
    generator.WithRandom(),
    generator.WithTotalSize(1*humanize.GiByte),
)

req, _ := http.NewRequest("POST", "http://localhost:8080/upload", body)
```

## Metrics & Observability

Echoclient automatically tracks metrics when using the `MeasuringHTTPClient` or `WorkerPool`.

```go
httpClient := client.NewMeasuringHTTPClient()
resp, err := httpClient.Get("http://localhost:8080")

// Print snapshot to standard out
metrics.DumpMetrics(os.Stdout)

// Or expose to Prometheus
go metrics.StartPrometheusServer(":9090")
```

The collected metrics include:

- **Requests**: Count, rate, and duration (by method, host, and status code).
- **Errors**: Total count and rate.
- **Workers**: Active concurrent worker count.
- **System**: User/system CPU usage and RSS memory usage.
- **Network**: Bytes transmitted and received.
- **Resources**: Open file descriptors, OS threads, and goroutines.

## Command-Line Tool

### Installation

```bash
go install github.com/tsaarni/echoclient/cmd/echoclient@latest
```

### Usage

```bash
echoclient get -url http://localhost:8080 -concurrency 10 -duration 30s -rps 100
```

Subcommands:
- `get` - Send GET requests with concurrency, rate limiting, and ramp-up.
- `upload` - Upload generated data.

Run `echoclient <subcommand> -help` for options.

## Documentation

- [Package API Documentation](https://pkg.go.dev/github.com/tsaarni/echoclient)
- [Examples](./examples/)
