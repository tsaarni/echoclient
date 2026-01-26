package worker

import (
	"time"
)

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
	onStart           LifeCycleFunc
	onEnd             LifeCycleFunc
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
