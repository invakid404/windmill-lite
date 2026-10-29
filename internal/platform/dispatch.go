package platform

import (
	"context"
	"encoding/json"
)

// DispatchKind is what a dispatch names its target by (WA-15): a script's
// path (`jobs/run/p`), a flow's path (`jobs/run/f`), or a script's hash
// (`jobs/run/h`).
type DispatchKind string

const (
	DispatchScript DispatchKind = "script"
	DispatchFlow   DispatchKind = "flow"
	DispatchHash   DispatchKind = "hash"
)

// Dispatch is one asynchronous run a step submits with runScript*Async,
// runFlowAsync or their synchronous forms (ADR 0007, WA-15).
type Dispatch struct {
	Kind DispatchKind
	// Target is the script's or the flow's path, or the script's hash.
	Target string
	// Args is the request's body, the run's arguments as JSON text.
	Args json.RawMessage
	// ParentJob and RootJob are the query's parent_job and root_job, ""
	// when absent; Tag its tag; ScheduledInSecs its scheduled_in_secs, 0
	// when absent.
	ParentJob, RootJob, Tag string
	ScheduledInSecs         int64
	// Caller is the job variables of the invocation that dispatched it.
	Caller map[string]string
}

// JobResult is the state of a dispatched job, as getResultMaybe and
// waitJob read it (WA-V2): whether it has started, and once it has
// completed whether it succeeded and its result, as stored JSON text (a
// failed job's is its failure value, {"error": …}).
type JobResult struct {
	Started   bool
	Completed bool
	Success   bool
	Result    json.RawMessage
}

// Dispatcher is the dispatcher of ADR 0007: it starts the runs that steps
// dispatch, and answers the result polls for them. The default runs the
// target from the bundle as a separate run of the same runtime
// (engine.Dispatcher); a host's may route elsewhere.
type Dispatcher interface {
	// Dispatch starts the run and returns its job id, a UUID. A target
	// that is not a root of the bundle fails with *NotInBundleError,
	// which fails the dispatching step with UnsupportedPlatformCall
	// (ADR 0001 §5a).
	Dispatch(ctx context.Context, d Dispatch) (string, error)
	// Result returns the state of the job id names, and false when the
	// dispatcher did not start it.
	Result(ctx context.Context, id string) (JobResult, bool, error)
}

// NotInBundleError is a dispatch whose target is not a root of the bundle
// (ADR 0001 §5a, ADR 0002's host contract): the dispatching step fails
// with UnsupportedPlatformCall, `dispatch target not in bundle: <path>`.
type NotInBundleError struct {
	// Target is the path, or the hash, the dispatch named.
	Target string
}

func (e *NotInBundleError) Error() string { return "dispatch target not in bundle: " + e.Target }
