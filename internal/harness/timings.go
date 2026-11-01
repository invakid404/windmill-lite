package harness

import (
	"sync/atomic"
	"time"
)

// InvocationTimes are the engine side's clock readings of one invocation
// that completed, for the boundary benchmarks of ADR 0005: each stage's
// duration is the difference of two of them, all on this process's
// monotonic clock. The harness's own readings of the same invocation
// come from the harness runtime (harnesses/bun README, Benchmark timings).
type InvocationTimes struct {
	// ID is the invocation's id, its job's WM_JOB_ID.
	ID string
	// Start is when the process began the invocation: before the checks,
	// the script's arguments and the invoke's payload.
	Start time.Time
	// Encoded is when the invoke's payload was ready.
	Encoded time.Time
	// ArgsWritten is when args.json was written.
	ArgsWritten time.Time
	// Queued is when the invoke was queued on the connection, whose writer
	// sends it.
	Queued time.Time
	// Received is when the reader had read the last frame of the result.
	Received time.Time
	// Decoded is when the reader had decoded the result.
	Decoded time.Time
	// Done is when the invocation's outcome reached the caller of Execute,
	// after its settlement and Reporter calls.
	Done time.Time
}

// timesHook receives the readings of every invocation that completes in
// any Process of this program while it is set.
var timesHook atomic.Pointer[func(InvocationTimes)]

// SetTimesHook makes every Process report each invocation that completes
// with an outcome to f, from the goroutine that called Execute, until it
// is called again; nil turns the reports off. It is for benchmarks: while
// it is unset, a Process reads no clock for it, and each invocation and
// each frame it receives cost one atomic load.
func SetTimesHook(f func(InvocationTimes)) {
	if f == nil {
		timesHook.Store(nil)
		return
	}
	timesHook.Store(&f)
}
