# echoclient load testing tool

Echoclient is a simple HTTP load testing tool written in Go.
It can be used in two ways:

- As a [command-line tool](#using-the-command-line-tool) for running load tests against [echoserver](https://github.com/tsaarni/echoserver) or any HTTP server.
- As a [Go library](#using-in-own-projects) for building your own custom load testing tools.

### Using the Command-line Tool

Compile the tool with:

```bash
go install github.com/tsaarni/echoclient/cmd/echoclient@latest
```

Then run it with:

```bash
echoclient <subcommand> [flags]
```

Or alternatively run directly with Go compiler

```bash
go run github.com/tsaarni/echoclient/cmd/echoclient@latest <subcommand> [flags]
```

#### `get` subcommand

Sends HTTP GET requests to the target server.
Each worker performs the specified number of repetitions.

Flags:

| Flag              | Default               | Description                                                         |
| ----------------- | --------------------- | ------------------------------------------------------------------- |
| `-url`            | http://localhost:8080 | Server URL                                                          |
| `-concurrency`    | 1                     | Number of concurrent workers                                        |
| `-repetitions`    | 0                     | Total number of repetitions across all workers<br>_(0 = infinite repetitions)_      |
| `-duration`       | 0                     | Duration of the load test<br>_(0 = run until repetitions complete)_ |
| `-rps`            | 0                     | Requests per second allowed across all workers<br>_(0 = no limit)_  |
| `-ramp-up-period` | 0                     | Ramp-up period to reach target rps<br>_(0 = no ramp-up)_            |

You can specify `-duration` and `-ramp-up-period` with values such as `1h`, `30m`, or `15s`.
If both `-duration` and `-repetitions` are set, the test will end when either limit is reached first.

Note that requests executed during the `-ramp-up-period` do not count towards the `-repetitions` limit. The repetitions limit applies only to the steady state phase.

#### `upload` subcommand

Uploads generated data to the target server using HTTP POST requests.
Each worker uploads the specified total size, split into chunks.

Flags:

| Flag           | Default                      | Description                                                         |
| -------------- | ---------------------------- | ------------------------------------------------------------------- |
| `-concurrency` | 1                            | Number of concurrent workers                                        |
| `-repetitions` | 1                            | Total number of repetitions across all workers<br>_(0 = infinite repetitions)_      |
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

Echoclient can be imported as a Go library to build custom load testing tools.

#### Basic Usage

Create a `WorkerPool` with a custom Load Function and options for concurrency, duration, or repetitions.

```go
package main

import (
    "context"
    "net/http"
    "time"
    "github.com/tsaarni/echoclient/worker"
    "github.com/tsaarni/echoclient/client"
)

func main() {
    // 1. Define the work to be done
    loadFunc := func(ctx context.Context, wp *worker.WorkerPool) error {
        // ... perform one unit of work (e.g. HTTP request) ...
        return nil
    }

    // 2. Create the worker pool with desired options
    pool := worker.NewWorkerPool(
        loadFunc,
        worker.WithConcurrency(10), // 10 concurrent workers
        worker.WithDuration(10*time.Second), // Run for 10 seconds
    )

    // 3. Launch and wait
    pool.Launch()
    pool.Wait()
}
```

See [examples/simple/main.go](examples/simple/main.go) for a complete runnable example including metrics printing.

#### Traffic Profiles

For more complex scenarios, you can define a "Profile" consisting of multiple "Steps". Each step can have its own duration, concurrency, rate limits, and even easing functions for smooth transitions (ramp-up/ramp-down).

```go
// Define a traffic profile with varying load characteristics
profile := []*worker.Step{
    // Step 1: Ramp up to 100 RPS
    worker.NewStep(
        worker.WithDuration(5*time.Second),
        worker.WithRateLimit(100, 100, worker.EasingLinear),
    ),
    // Step 2: Consistent load
    worker.NewStep(
        worker.WithDuration(10*time.Second),
        worker.WithRateLimit(100, 100),
    ),
}

// Create a MultiStepWorkerPool
pool := worker.NewMultiStepWorkerPool(loadFunc, profile)
pool.Launch()
pool.Wait()
```

See [examples/steps/main.go](examples/steps/main.go) for a comprehensive example demonstrating:
- Multi-step execution
- Linear, EaseIn, and EaseOut transitions
- Lifecycle hooks (before/after steps)
- Changing worker behavior per step

See the [package documentation](https://pkg.go.dev/github.com/tsaarni/echoclient) for complete API reference.
