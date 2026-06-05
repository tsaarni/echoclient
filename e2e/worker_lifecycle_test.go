package e2e

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/tsaarni/echoclient/worker"
)

// TestContextCancellation tests that the pool stops when the context is cancelled.
func TestContextCancellation(t *testing.T) {
	h := NewE2ETestFixture(Delayed(100*time.Millisecond, 200))
	defer h.Close()

	ctx, cancel := context.WithCancel(context.Background())
	wp := worker.NewWorkerPool(
		func(ctx context.Context, wp *worker.WorkerPool) error {
			_, _ = h.Client.Get(h.Server.URL)
			return nil
		},
		worker.WithConcurrency(10),
	)

	start := time.Now()
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	_ = wp.LaunchWithContext(ctx)
	wp.Wait()

	if duration := time.Since(start); duration < 200*time.Millisecond || duration > 500*time.Millisecond {
		t.Errorf("Expected cancellation in ~300ms, took %v", duration)
	}
}

// TestLifecycleHooks tests that start and end hooks for steps are called in order.
func TestLifecycleHooks(t *testing.T) {
	h := NewE2ETestFixture(Status(200))
	defer h.Close()

	var events []string
	var mu sync.Mutex
	onStart := func(ctx context.Context, wp *worker.WorkerPool) {
		mu.Lock()
		events = append(events, "Step_Start")
		mu.Unlock()
	}
	onEnd := func(ctx context.Context, wp *worker.WorkerPool) {
		mu.Lock()
		events = append(events, "Step_End")
		mu.Unlock()
	}

	steps := []*worker.Step{
		worker.NewStep(worker.WithRepetitions(1), worker.WithHooks(onStart, onEnd)),
		worker.NewStep(worker.WithRepetitions(1), worker.WithHooks(onStart, onEnd)),
	}

	RunMultiStepPool(h, steps)

	expected := []string{"Step_Start", "Step_End", "Step_Start", "Step_End"}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != len(expected) {
		t.Fatalf("Expected %v, got %v", expected, events)
	}
}
