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
// It keeps one shared value: the send time of the last reserved request. Each
// worker computes its own send time as last + interval (1s / current rps),
// reserves it with an atomic compare-and-swap (CAS), sleeps until that time,
// and sends the request. If two workers read the same value, only one CAS
// succeeds; the other retries and gets the next interval.
//
// Example with RPS=100 (interval=10ms) and 3 workers calling Take():
//
//	Worker A: reads last=T,       reserves T+10ms
//	Worker B: reads last=T+10ms,  reserves T+20ms
//	Worker C: reads last=T+20ms,  reserves T+30ms
//
// Because the interval is computed from the current rps when reserving, a rate
// change applies to the next request. Reserved send times are never given out again.
//
// Three mechanisms handle edge cases:
//
// 1. Just-In-Time (JIT) waiting prevents workers from reserving send times far
// in the future. When the next send time is more than jitLookahead (100ms)
// ahead, the worker waits briefly and retries with the current rps. This keeps
// reservations close to now while the rate is being eased.
//
// 2. Max catchup bounds the burst after a stall. If the server blocks for 5s at
// RPS=100, the next send time falls 500 intervals behind now. Without a cap all
// 500 requests would fire at once on recovery. With SetMaxCatchup, send times
// older than maxCatchup are skipped, so at most maxCatchup × rps requests fire
// immediately (e.g., 100ms → 10 requests). Skipped requests are counted in
// scheduler_skipped_requests_total.
//
// 3. Coordinated Omission (CO) tracking captures queuing delay. After a stall,
// each worker's send time is in the past. The difference between when the
// request was supposed to be sent and when it completed includes the time the
// client spent blocked, waiting for the server. This is recorded as
// scheduler_request_latency_seconds in the worker loop.
type RequestScheduler struct {
	// last is the send time of the last reserved request (nanoseconds since epoch).
	// After Reset(t) it is -t: nothing is reserved yet and the next send time is t.
	last           atomic.Int64
	maxCatchupNano atomic.Int64 // max lag of the next send time behind now; 0 = unlimited
}

// NewRequestScheduler creates a RequestScheduler whose first send time is now.
func NewRequestScheduler() *RequestScheduler {
	rl := &RequestScheduler{}
	rl.Reset(time.Now())
	return rl
}

// Reset makes t the next send time. Call at step transitions to avoid a burst
// from the previous step.
func (rl *RequestScheduler) Reset(t time.Time) {
	rl.last.Store(-t.UnixNano())
}

// SetMaxCatchup bounds the post-stall burst. Send times more than d behind now
// are skipped, so at most d × rps requests fire immediately.
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
		interval := int64(time.Second) / currentRPS
		last := rl.last.Load()
		next := last + interval
		if last < 0 {
			next = -last // Nothing reserved since Reset.
		}

		// Next send time too far ahead: wait and retry with the current rps.
		if next > now+int64(jitLookahead) {
			sleepDur := min(time.Duration(next-now-int64(jitLookahead))+1, jitSleepCap)
			timer = resetOrNewTimer(timer, sleepDur)
			select {
			case <-timer.C:
			case <-ctx.Done():
				return ctx, ctx.Err()
			}
			continue
		}

		// Skip send times that are more than maxCatchup behind now.
		skipped := int64(0)
		if maxCatchup := rl.maxCatchupNano.Load(); maxCatchup > 0 && next < now-maxCatchup {
			skipped = (now - maxCatchup - next) / interval
			next = now - maxCatchup
		}

		// Reserve the send time. If another worker reserved first, retry.
		if !rl.last.CompareAndSwap(last, next) {
			continue
		}
		if skipped > 0 {
			metrics.SchedulerSkippedRequestsTotal.Add(float64(skipped))
		}

		// Sleep until the send time if it has not arrived yet.
		if next > now {
			timer = resetOrNewTimer(timer, time.Duration(next-now))
			select {
			case <-timer.C:
			case <-ctx.Done():
				return ctx, ctx.Err()
			}
		}

		return contextWithScheduledTime(ctx, time.Unix(0, next)), nil
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
