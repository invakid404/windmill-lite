// Package conformance holds the vocabulary that every comparison in
// windmill-lite shares: the typed site-path encoding of step identities
// (ADR 0011, answering Q-OR-17), the run and step statuses, and the
// normalised observation of a run (OR-15).
//
// The engine, the harness, the oracle and wltest all import this package, so
// it imports no other windmill-lite package. Code that drives windmill-lite
// to produce an observation lives with its caller, never here.
//
// Fixture loading and the comparator are later milestones (E1, E2).
package conformance
