![Logo Light](./examples/echoclient-light.png#gh-light-mode-only)
![Logo Dark](./examples/echoclient-dark.png#gh-dark-mode-only)

**Echoclient** is a programmable load testing package for Go.
The library provides an API that can be used to create deterministic traffic scenarios. It supports features like:

- **Workload modeling** using traffic profiles to define load patterns.
- **Traffic shaping** with easing functions to simulate realistic load transitions.
- **Observability** via built-in metrics for real-time performance monitoring.

It also includes a command-line tool for quick HTTP load tests.

![Demo](./examples/demo.gif)

### Introduction

Install the package:

```bash
go get github.com/tsaarni/echoclient
```

**Simple Usage:**

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

This example sends HTTP GET requests to `http://localhost:8080` for 10 seconds using 10 concurrent workers.
Each worker repeatedly executes `loadFunc`, which can be customized for any type of load generation, not just HTTP.


**Multi-Step Traffic Profile:**

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

This example creates a traffic profile that ramps up to 100 RPS over 5 seconds, maintains it for 10 seconds while scaling workers from 10 to 20, and then ramps down to zero over the final 5 seconds using an easing function for smooth transitions.


**Metrics-aware HTTP Client:**

```go
httpClient := client.NewMeasuringHTTPClient()
resp, err := httpClient.Get("http://localhost:8080")

metrics.DumpMetrics(os.Stdout)

go metrics.StartPrometheusServer(":9090")
```

This client automatically tracks per-request metrics and system resource usage, and can output them to the console or expose them to Prometheus.

The `DumpMetrics()` function writes a snapshot of the currently collected metrics.
You can call it during a load test for live monitoring or after the test to inspect the results.

Optionally, the `StartPrometheusServer()` function can be used to expose metrics on an HTTP endpoint for Prometheus scraping.

**Test Data Generator:**

```go
body := generator.NewReader(
    generator.WithRandom(),
    generator.WithTotalSize(1*humanize.GiByte),
)

req, _ := http.NewRequest("POST", "http://localhost:8080/upload", body)
```

This generates a stream of random data of the specified size without loading it all into memory at once, making it suitable for testing upload endpoints.

### Collected Metrics

The following metrics are automatically collected and exposed by the `MeasuringHTTPClient` and the worker pool:

- **Requests**: Duration, count (by method/status/host), and rate.
- **Errors**: Total error count and error rate.
- **Workers**: Number of active concurrent workers.
- **System**: CPU usage (user/system), Memory usage (RSS).
- **Network**: Bytes received and transmitted.
- **Resources**: Open file descriptors, OS threads, and goroutines.

### Command-Line Tool

Install and run:

```bash
go install github.com/tsaarni/echoclient/cmd/echoclient@latest

echoclient get -url http://localhost:8080 -concurrency 10 -duration 30s -rps 100
```

**Subcommands:**
- `get` - Send HTTP GET requests with concurrency, rate limiting, ramp-up
- `upload` - Upload generated data with configurable size and chunk size

Run `echoclient <subcommand> -help` for all options.

### Documentation

- [Package documentation (pkg.go.dev)](https://pkg.go.dev/github.com/tsaarni/echoclient)
- [Examples](examples/)
