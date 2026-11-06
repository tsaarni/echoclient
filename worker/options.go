// Package worker provides core logic for the worker pool and task orchestration.
package worker

import (
	"context"
	"time"
)

// EasingFunc defines a function that maps time t (0.0 to 1.0) to a value (0.0 to 1.0).
type EasingFunc func(t float64) float64

// Easing functions for interpolating values in traffic profiles from one value to another.
var (
	// EasingLinear: interpolates linearly from start to target value.
	EasingLinear EasingFunc = func(t float64) float64 {
		return t
	}

	// EasingIn: interpolates from start to target value, accelerating (Quadratic).
	EasingIn EasingFunc = func(t float64) float64 {
		return t * t
	}

	// EasingOut: interpolates from start to target value, decelerating (Quadratic).
	EasingOut EasingFunc = func(t float64) float64 {
		return t * (2 - t)
	}

	// EasingInOut: interpolates from start to target value, accelerating then decelerating (Quadratic).
	EasingInOut EasingFunc = func(t float64) float64 {
		if t < 0.5 {
			return 2 * t * t
		}
		return -1 + (4-2*t)*t
	}
)

// WorkerFunc defines the function signature for work to be performed by the pool.
// The function receives a context for cancellation and a reference to the WorkerPool.
// Calls are counted in worker_calls_total and returned errors in worker_errors_total.
// Calls interrupted by the end of the run are not counted.
// To end the run, call wp.Stop or wp.Abort.
type WorkerFunc func(ctx context.Context, wp *WorkerPool) error

// StepHookFunc is called when a step starts or ends.
type StepHookFunc func(ctx context.Context, wp *WorkerPool)

// WorkerStartFunc is called when a worker starts.
// The returned context is passed to every call of the worker function by this worker.
// It must be derived from ctx. If an error is returned, the worker exits and is replaced
// on the next tick (every 100ms). Call wp.Abort to end the run instead.
type WorkerStartFunc func(ctx context.Context, wp *WorkerPool) (context.Context, error)

// WorkerExitFunc is called when a worker exits, if WorkerStartFunc succeeded.
// It receives the context returned by WorkerStartFunc. The context may be canceled.
type WorkerExitFunc func(ctx context.Context, wp *WorkerPool)

// Option configures a Step.
type Option func(*Step)

// WithConcurrency sets the number of concurrent workers, with optional easing.
// If not set, defaults to 1.
//
// When easing is provided, concurrency interpolates from the previous value
// to the new value over the step's duration.
func WithConcurrency(n int, easing ...EasingFunc) Option {
	return func(c *Step) {
		c.concurrency = n
		if len(easing) > 0 {
			c.concurrencyEasing = easing[0]
		}
	}
}

// WithRateLimit sets the rate limit (requests per second) and burst size, with optional easing.
//
// rps: The steady-state rate in requests per second.
// burst: The maximum number of requests permitted instantaneously (token bucket size).
//
// When easing is provided, RPS interpolates from the previous value
// to the new value over the step's duration.
//
// Burst Usage Guidelines:
//   - Burst = 1: Enforces strict pacing (interval = 1/rps).
//     Downside: Very sensitive to scheduling jitter or system pauses. If the generator stutters,
//     tokens are lost immediately, causing the observed rate to drop below the target rps.
//   - Burst = RPS: Allows the system to catch up after pauses of up to 1 second.
//     Recommended for most load tests to ensure the average target rate is maintained.
//   - Burst >= Concurrency: Allows all workers to start immediately.
//
// If WithRateLimit is not called, the pool runs with unlimited rate.
func WithRateLimit(rps, burst int, easing ...EasingFunc) Option {
	return func(c *Step) {
		c.rps = rps
		c.burst = burst
		if len(easing) > 0 {
			c.rpsEasing = easing[0]
		}
	}
}

// WithDuration sets the duration of execution.
// When used with a traffic profile step, this controls how long the step runs before transitioning to the next.
//
// If Repetitions is also set, the step moves to the next step when EITHER the duration elapses
// OR the repetitions are exhausted, whichever happens first.
//
// If duration is 0, the execution runs indefinitely (until cancelled), or until repetitions are exhausted.
func WithDuration(d time.Duration) Option {
	return func(c *Step) {
		c.duration = d
	}
}

// WithRepetitions sets the total number of repetitions (worker function invocations) for the step.
//
// All workers compete to execute from this shared pool of repetitions.
// For example, with 100 repetitions and 10 workers, each worker will execute ~10 times on average, depending on the rate limiter configuration.
//
// If Duration is also set, the step moves to the next step when EITHER the repetitions are exhausted
// OR the duration elapses, whichever happens first.
//
// If repetitions is 0 (default), workers run indefinitely (until cancelled or step duration ends).
func WithRepetitions(n int) Option {
	return func(c *Step) {
		c.repetitions = n
	}
}

// WithPause creates a pause step. Workers stay alive but do not call the worker function
// until the pause ends. The next step continues from the rate and concurrency before the pause.
func WithPause(d time.Duration) Option {
	return func(c *Step) {
		c.duration = d
		c.pause = true
	}
}

// WithInfiniteRepetitions sets unlimited repetitions.
// This is same as WithRepetitions(0).
//
// If Duration is also set, the step moves to the next step when the duration elapses.
func WithInfiniteRepetitions() Option {
	return func(c *Step) {
		c.repetitions = 0
	}
}

// WithWorkerFunc sets an optional worker function override.
// This overrides the default worker function provided in the constructor.
func WithWorkerFunc(f WorkerFunc) Option {
	return func(c *Step) {
		c.workerFunc = f
	}
}

// WithHooks sets optional callbacks to run at the start and end of a step.
func WithHooks(onStart, onEnd StepHookFunc) Option {
	return func(c *Step) {
		c.onStart = onStart
		c.onEnd = onEnd
	}
}

// Step holds configuration for a traffic profile step.
type Step struct {
	concurrency       int
	concurrencyEasing EasingFunc
	rps               int
	burst             int
	rpsEasing         EasingFunc
	duration          time.Duration
	repetitions       int
	workerFunc        WorkerFunc
	onStart           StepHookFunc
	onEnd             StepHookFunc
	pause             bool
}

// NewStep creates a new Step with the given options.
func NewStep(opts ...Option) *Step {
	s := &Step{
		concurrency: 1, // Default to 1 worker.
		rps:         0, // Default to unlimited RPS.
		duration:    0, // Default to infinite duration.
		repetitions: 0, // Default to infinite repetitions.
	}

	for _, o := range opts {
		o(s)
	}

	return s
}
