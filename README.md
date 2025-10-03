# echoclient load testing tool

Echoclient is a simple HTTP load testing tool written in Go.
It can be used in two ways:

- As a command-line tool for running load tests against [echoserver](https://github.com/tsaarni/echoserver) or any HTTP server.
- As a Go library for building your own custom load testing tools.

### Using the Command-line Tool

```bash
echoclient <subcommand> [flags]
```

#### `get` subcommand

Sends HTTP GET requests to the target server.
Each worker performs the specified number of repetitions.

Flags:

| Flag              | Default               | Description                                                         |
| ----------------- | --------------------- | ------------------------------------------------------------------- |
| `-url`            | http://localhost:8080 | Server URL                                                          |
| `-concurrency`    | 1                     | Number of concurrent workers                                        |
| `-repetitions`    | 0                     | Number of repetitions per worker<br>_(0 = infinite repetitions)_    |
| `-duration`       | 0                     | Duration of the load test<br>_(0 = run until repetitions complete)_ |
| `-rps`            | 0                     | Requests per second allowed across all workers<br>_(0 = no limit)_  |
| `-ramp-up-period` | 0                     | Ramp-up period to reach target rps<br>_(0 = no ramp-up)_            |

You can specify `-duration` and `-ramp-up-period` with values such as `1h`, `30m`, or `15s`.
If both `-duration` and `-repetitions` are set, the test will end when either limit is reached first.

#### `upload` subcommand

Uploads generated data to the target server using HTTP POST requests.
Each worker uploads the specified total size, split into chunks.

Flags:

| Flag           | Default                      | Description                                                         |
| -------------- | ---------------------------- | ------------------------------------------------------------------- |
| `-concurrency` | 1                            | Number of concurrent workers                                        |
| `-repetitions` | 1                            | Number of repetitions per worker<br>_(0 = infinite repetitions)_    |
| `-duration`    | 0                            | Duration of the load test<br>_(0 = run until repetitions complete)_ |
| `-size`        | 10MB                         | Total size of data to upload per worker, specified in bytes         |
| `-chunk`       | 64KB                         | Chunk size for data generation, specified in bytes                  |
| `-url`         | http://localhost:8080/upload | Server URL                                                          |

You can specify `-duration` with values such as `1h`, `30m`, or `15s`.
If both `-duration` and `-repetitions` are set, the test will end when either limit is reached first.

You can specify `-size` and `-chunk` using values like `1GB`, `10MB`, or `64KB`.

#### Example

![image of echoclient output](https://github.com/user-attachments/assets/1683651c-b083-418f-93f3-4413632b959f)

### Using in Own Projects

Echoclient can be imported as a Go module to build custom load testing tools. It provides:

- A worker pool for concurrent execution of load functions.
- Metrics collection and reporting, with HTTP endpoint which Prometheus can scrape.
- Payload data generation utilities.

#### Example: Custom Load Test

```go
package main

import (
	"context"
	"time"

	"github.com/tsaarni/echoclient/metrics"
	"github.com/tsaarni/echoclient/worker"
)

func main() {
	// Create an HTTP client instrumented for metrics
	http := metrics.NewMeasuringHTTPClient()

	// Define the load function (e.g., HTTP GET)
	loadFunc := func(ctx context.Context) error {
		resp, err := http.Get("http://localhost:8080/")
		if resp != nil {
			resp.Body.Close()
		}
		return err
	}

	// Periodically print metrics to the console
	ticker := time.NewTicker(5 * time.Second)
	go func() {
		for range ticker.C {
			metrics.DumpMetrics()
		}
	}()

	// Create and launch a worker pool
	pool := worker.NewWorkerPool(
		loadFunc,
		worker.WithConcurrency(100),
		worker.WithInfiniteRepetitions(),
	)
	pool.Launch().Wait()
}
```

You can customize the `loadFunc` to perform any operation: sequence of requests, custom payloads, etc.

See the [package documentation](https://pkg.go.dev/github.com/tsaarni/echoclient) for more advanced usage.
