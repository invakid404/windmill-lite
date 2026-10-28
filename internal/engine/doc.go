// Package engine is windmill-lite's flow interpreter (ADR 0003): one Go
// implementation of Windmill's decision list, containers, retries, waits,
// failure modules and cancellation.
//
// The interpreter itself is not implemented yet (milestone N1a onwards).
// This package holds the contracts it is built against (milestone K1):
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
