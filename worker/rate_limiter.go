package worker

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/tsaarni/echoclient/metrics"
)

const (
	// How far ahead of now a worker may reserve a send time without retrying.
	// Matched to the easing ticker period (100ms) so that at most one
	// tick worth of send times can use a stale RPS value.
	jitLookahead = 100 * time.Millisecond

	// Upper bound on each JIT retry sleep, ensuring context cancellation
	// and RPS changes are noticed promptly.
	jitSleepCap = 50 * time.Millisecond
)

// RequestScheduler spaces requests evenly in time across concurrent workers.
//
// It works by maintaining a single shared timestamp: the time when the next
// request should be sent. This is called TAT (Theoretical Arrival Time) — the
// ideal time when the next request should "arrive" at the server, assuming
// perfect pacing with no delays. Each worker that wants to send a request
// reads the current TAT, adds one interval (1s / rps), and writes back the
// new value using an atomic compare-and-swap (CAS). The old TAT that the
// worker read becomes its own scheduled send time. The worker then sleeps
// until that time, and sends the request.
//
// Example with RPS=100 (interval=10ms) and 3 workers calling Take():
//
//	Worker A: reads TAT=T,       writes T+10ms, will send at T
//	Worker B: reads TAT=T+10ms,  writes T+20ms, will send at T+10ms
//	Worker C: reads TAT=T+20ms,  writes T+30ms, will send at T+20ms
//
// If two workers read the same TAT simultaneously, only one CAS succeeds;
// the other retries and gets the next interval.
//
// Four mechanisms handle edge cases:
//
// 1. Just-In-Time (JIT) waiting prevents workers from grabbing send times
// far in the future. This matters when the target RPS is being ramped up or
// down over time (easing): if 50 workers at RPS=100 all grabbed send times
// immediately, the last one would be scheduled 500ms ahead using the current
// interval, even if the RPS changes before then. Instead, when a worker's
// own scheduled time (oldTAT) would be more than jitLookahead (100ms) ahead
// of now, the worker waits briefly and retries, re-reading the current RPS.
//
// 2. Max catchup bounds the burst after a stall. If the server blocks for 5s
// at RPS=100, TAT falls 500 intervals behind now. Without a cap all 500
// requests would fire at once on recovery. SetMaxCatchup snaps TAT forward so
// at most maxCatchup × rps requests fire immediately (e.g., 100ms → 10
// requests).
//
// 3. Snap-backward bounds the gap when RPS increases. If a CAS at RPS=1
// commits TAT 1s ahead and the rate then ramps to 100, the stale TAT would
// block workers for nearly a second. When oldTAT exceeds
// now + interval + jitLookahead, the scheduler snaps TAT back to now, letting
// subsequent workers pace at the new, higher rate.
//
// 4. Coordinated Omission (CO) tracking captures queuing delay. After a stall,
// each worker's scheduled send time is in the past. The difference between
// when the request was supposed to be sent and when it actually completed
// includes the time the client spent blocked, waiting for the server. This is
// recorded as scheduler_request_latency_seconds in the worker loop.
type RequestScheduler struct {
	tat            atomic.Int64 // when the next request should be sent (nanoseconds since epoch)
	maxCatchupNano atomic.Int64 // max TAT lag before snap-forward; 0 = unlimited
}

// NewRequestScheduler creates a RequestScheduler with TAT initialized to now.
func NewRequestScheduler() *RequestScheduler {
	rl := &RequestScheduler{}
	rl.tat.Store(time.Now().UnixNano())
	return rl
}

// Reset sets TAT to t. Call at step transitions to avoid a burst from
// the previous step's stale TAT.
func (rl *RequestScheduler) Reset(t time.Time) {
	rl.tat.Store(t.UnixNano())
}

// SetMaxCatchup bounds the post-stall burst. After a stall longer than d,
// TAT is snapped forward so at most d × rps requests fire immediately.
// Set to 0 for unlimited catch-up.
func (rl *RequestScheduler) SetMaxCatchup(d time.Duration) {
	rl.maxCatchupNano.Store(int64(d))
}

// Take reserves the next send time and blocks until it arrives. Returns a
// context containing the scheduled send time (see ScheduledTimeFromContext).
// When rps <= 0 (unlimited), returns immediately with the original context.
// The rps parameter is a pointer to an atomic so that JIT retries always
// re-read the latest value.
func (rl *RequestScheduler) Take(ctx context.Context, rps *atomic.Int64) (context.Context, error) {
	var timer *time.Timer // lazily allocated on first sleep
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx, ctx.Err()
		default:
		}

		currentRPS := rps.Load()
		if currentRPS <= 0 {
			return ctx, nil
		}

		now := time.Now().UnixNano()
		oldTAT := rl.tat.Load()
		interval := int64(time.Second) / currentRPS
		newTAT := oldTAT + interval

		// Snap TAT forward if it fell too far behind (bounds post-stall burst).
		// The interval guard (oldTAT < now-maxCatchup-interval) prevents a
		// spin loop: without it, each iteration's time.Now() advances by a
		// few nanoseconds, making TAT perpetually "just behind" the threshold
		// and re-triggering the snap on every loop pass.
		maxCatchup := rl.maxCatchupNano.Load()
		if maxCatchup > 0 && oldTAT < now-maxCatchup-interval {
			snapTarget := now - maxCatchup
			if rl.tat.CompareAndSwap(oldTAT, snapTarget) {
				skipped := (snapTarget - oldTAT) / interval
				if skipped > 0 {
					metrics.SchedulerSkippedRequestsTotal.Add(float64(skipped))
				}
			}
			continue
		}

		// Snap TAT backward if it advanced too far due to a previous lower RPS.
		// This bounds the gap when RPS increases (e.g., during easing ramp-up).
		// Without this, a CAS at RPS=1 commits TAT 1s ahead; if RPS then
		// ramps to 100, workers JIT-wait for the stale TAT instead of pacing
		// at the new rate.
		if oldTAT > now+interval+int64(jitLookahead) {
			rl.tat.CompareAndSwap(oldTAT, now)
			continue
		}

		// Our scheduled time too far ahead — wait and retry with fresh RPS.
		if oldTAT > now+int64(jitLookahead) {
			sleepDur := min(time.Duration(oldTAT-now-int64(jitLookahead))+1, jitSleepCap)
			timer = resetOrNewTimer(timer, sleepDur)
			select {
			case <-timer.C:
			case <-ctx.Done():
				return ctx, ctx.Err()
			}
			continue
		}

		// Try to take this send time. If another worker took it first, retry.
		if !rl.tat.CompareAndSwap(oldTAT, newTAT) {
			continue
		}

		// Sleep until our scheduled time if it hasn't arrived yet.
		scheduledNano := oldTAT
		if scheduledNano > now {
			timer = resetOrNewTimer(timer, time.Duration(scheduledNano-now))
			select {
			case <-timer.C:
			case <-ctx.Done():
				return ctx, ctx.Err()
			}
		}

		return contextWithScheduledTime(ctx, time.Unix(0, scheduledNano)), nil
	}
}

// resetOrNewTimer reuses t (if non-nil) or allocates a new timer.
func resetOrNewTimer(t *time.Timer, d time.Duration) *time.Timer {
	if t == nil {
		return time.NewTimer(d)
	}
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
	return t
}
