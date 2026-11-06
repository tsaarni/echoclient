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

The run ends when the duration or repetitions are used up. It can also be ended early with `Stop`, or with `Abort(err)`, in which case `Wait` returns `err`.

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

Composes multiple tasks with relative weights (e.g. 80% reads, 20% writes).

```go
var readFunc worker.WorkerFunc = func(ctx context.Context, wp *worker.WorkerPool) error { /* ... */ }
var writeFunc worker.WorkerFunc = func(ctx context.Context, wp *worker.WorkerPool) error { /* ... */ }

// Mix read (weight 4) and write (weight 1)
composedWorker := worker.Mix(
    readFunc.Weighted(4),
    writeFunc.Weighted(1),
)

pool := worker.NewWorkerPool(composedWorker, worker.WithConcurrency(50))
pool.Launch()
pool.Wait()
```

### Worker State

Opens a resource once per worker, such as a connection, and closes it when the worker exits.

```go
type connKey struct{}

pool := worker.NewWorkerPool(func(ctx context.Context, wp *worker.WorkerPool) error {
    conn := ctx.Value(connKey{}).(net.Conn)
    // Use conn...
    return nil
}, worker.WithConcurrency(10), worker.WithDuration(10*time.Second))

pool.SetWorkerHooks(
    func(ctx context.Context, wp *worker.WorkerPool) (context.Context, error) {
        conn, err := net.Dial("tcp", "localhost:9000")
        if err != nil {
            return ctx, err
        }
        return context.WithValue(ctx, connKey{}, conn), nil
    },
    func(ctx context.Context, wp *worker.WorkerPool) {
        _ = ctx.Value(connKey{}).(net.Conn).Close()
    },
)
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

// Or expose to Prometheus at http://localhost:9090/metrics
srv, err := metrics.StartPrometheusServer("localhost:9090")
if err != nil {
    log.Fatal(err)
}
defer srv.Close()
```

The collected metrics include:

- **Requests**: Count, rate (by method, host, and status code), duration until the body is read or closed, and time until response headers (by method and host).
- **Errors**: Total count and rate.
- **Workers**: Active worker count, worker function calls, and errors.
- **System**: User/system CPU usage and RSS memory usage.
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
