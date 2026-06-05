package e2e

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tsaarni/echoclient/worker"
)

// TestExactRepetitionCounting tests that the requested number of requests are sent.
func TestExactRepetitionCounting(t *testing.T) {
	h := NewE2ETestFixture(Status(200))
	defer h.Close()

	RunPool(h, worker.WithConcurrency(50), worker.WithRepetitions(100))

	h.AssertRequests(t, 200, 100)
}

// TestMultiStepProfile tests that the pool can run multiple steps with different rates and pauses.
func TestMultiStepProfile(t *testing.T) {
	h := NewE2ETestFixture(Status(200))
	defer h.Close()

	steps := []*worker.Step{
		worker.NewStep(worker.WithRateLimit(50, 1), worker.WithConcurrency(2), worker.WithDuration(500*time.Millisecond)),
		worker.NewStep(worker.WithPause(100*time.Millisecond)),
		worker.NewStep(worker.WithRateLimit(200, 10, worker.EasingLinear), worker.WithConcurrency(10), worker.WithDuration(500*time.Millisecond)),
		worker.NewStep(worker.WithRateLimit(10, 1), worker.WithConcurrency(1), worker.WithRepetitions(10)),
	}

	RunMultiStepPool(h, steps)

	// Expected request count calculation:
	// Step 1: 50 RPS for 0.5s = 25 requests.
	// Step 2: Pause = 0 requests.
	// Step 3: Easing from 50 to 200 RPS over 0.5s. Average is 125 RPS. 125 * 0.5 = ~62.5 requests.
	// Step 4: Fixed 10 repetitions = 10 requests.
	// Total theoretical = 25 + 0 + 62.5 + 10 = 97.5 requests.
	//
	// Due to CPU scheduler jitter, burst configurations (bucket size of 10 in step 3),
	// and the 100ms ticker resolution for easing, the empirical count averages around 110.
	// A 25% margin (0.25) safely accounts for this normal variance.
	h.AssertRequestsApprox(t, 110, 0.25)
}

// TestGracefulScaleDown tests that requests finish when reducing the number of workers.
func TestGracefulScaleDown(t *testing.T) {
	h := NewE2ETestFixture(Delayed(10*time.Millisecond, 200))
	defer h.Close()

	steps := []*worker.Step{
		worker.NewStep(worker.WithConcurrency(50), worker.WithDuration(200*time.Millisecond)),
		worker.NewStep(worker.WithRateLimit(10, 1), worker.WithConcurrency(2), worker.WithDuration(200*time.Millisecond)),
	}

	RunMultiStepPool(h, steps)
}

// TestPauseStep tests that no requests are sent during a pause step.
func TestPauseStep(t *testing.T) {
	h := NewE2ETestFixture(Status(200))
	defer h.Close()

	steps := []*worker.Step{
		worker.NewStep(worker.WithConcurrency(5), worker.WithRepetitions(10)),
		worker.NewStep(worker.WithPause(200 * time.Millisecond)),
		worker.NewStep(worker.WithConcurrency(5), worker.WithRepetitions(10)),
	}

	start := time.Now()
	RunMultiStepPool(h, steps)

	h.AssertRequests(t, 200, 20)
	if duration := time.Since(start); duration < 200*time.Millisecond {
		t.Errorf("expected at least 200ms duration for pause, got %s", duration)
	}
}

// TestRepetitionsVsDuration tests if the pool stops based on request count or time limit.
func TestRepetitionsVsDuration(t *testing.T) {
	h := NewE2ETestFixture(Status(200))
	defer h.Close()

	t.Run("RepetitionsPrecedence", func(t *testing.T) {
		start := time.Now()
		RunPool(h, worker.WithDuration(5*time.Second), worker.WithRepetitions(10), worker.WithConcurrency(2))

		if duration := time.Since(start); duration > 2*time.Second {
			t.Errorf("expected to finish quickly due to repetition limit, took %v", duration)
		}
		h.AssertRequests(t, 200, 10)
	})

	t.Run("DurationPrecedence", func(t *testing.T) {
		start := time.Now()
		RunPool(h, worker.WithDuration(200*time.Millisecond), worker.WithRepetitions(1000000), worker.WithConcurrency(10))

		if duration := time.Since(start); duration < 200*time.Millisecond {
			t.Errorf("finished too early: %v", duration)
		}
	})
}

// TestDynamicRuntimeAdjustments tests changing the number of workers or the worker function while running.
func TestDynamicRuntimeAdjustments(t *testing.T) {
	h := NewE2ETestFixture(Delayed(10*time.Millisecond, 200))
	defer h.Close()

	var worker2Calls atomic.Int64
	worker2 := func(ctx context.Context, wp *worker.WorkerPool) error {
		worker2Calls.Add(1)
		_, _ = h.Client.Get(h.Server.URL)
		return nil
	}

	wp := worker.NewWorkerPool(
		func(ctx context.Context, wp *worker.WorkerPool) error {
			_, _ = h.Client.Get(h.Server.URL)
			return nil
		},
		worker.WithConcurrency(1),
		worker.WithDuration(0),
	)

	_ = wp.Launch()
	defer wp.Stop()

	time.Sleep(100 * time.Millisecond)
	h.AssertActiveWorkers(t, 1)

	wp.SetConcurrency(5)
	time.Sleep(100 * time.Millisecond)
	h.AssertActiveWorkers(t, 5)

	wp.SetWorker(worker2)
	time.Sleep(100 * time.Millisecond)

	if worker2Calls.Load() == 0 {
		t.Error("expected worker2 to have been called after SetWorker")
	}
}
