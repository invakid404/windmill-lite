// Package engine is windmill-lite's flow interpreter (ADR 0003): one Go
// implementation of Windmill's decision list, containers, retries, waits,
// failure modules and cancellation.
//
// Engine runs the flows of a compiled program (ir.Program): milestone N1a's
// engine core runs a flow's top-level modules in sequence, with input
// transforms, the results routes of expressions, skip_if, mock, identity,
// stop_after_if and a static step timeout. Later milestones add the
// failure machinery (N1b), waits and retries (N2), containers (N3) and
// subflows (N3c); until then New refuses what they implement, with
// ErrUnsupported.
//
// The contracts it is built against (milestone K1):
//
//   - StepExecutor, Invocation and StepOutcome: how the interpreter runs a
//     leaf step in a language harness (ADR 0004);
//   - Hooks and NoHooks: the internal hook seams with no-op defaults
//     (ADR 0008), the Run handle hooks latch through, the Latch, and Clock;
//   - Start, Completion and AfterContinue: the precedence of host signals
//     over Windmill's own checks, which yields the latched, canceled and
//     halted outcomes;
//   - Trace and StepRecord: the run trace every run returns (H9).
//
// Site identities are conformance.SitePath values and run statuses are
// conformance.RunStatus values, so traces, hooks and observations share one
// vocabulary (ADR 0011).
package engine
