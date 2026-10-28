// Package openflow holds windmill-lite's OpenFlow types and the effective
// decoder: the rules by which Windmill v1.767.0 accepts, rejects and
// defaults a flow value, which differ from its schema in both directions
// (OF-1).
//
// The decoder reads JSON text, the form in which the `wmill` CLI sends a
// flow to the server. DecodeFlowValue decodes a flow's `value` in Windmill's
// two stages and runs its two server checks (OF-2); DecodeFlow decodes a
// whole flow document. Decoded values keep static transform values and
// other free-form JSON verbatim, keep maps in input order, and apply the
// decoder's defaults; the deploy-time normalisation that a successful deploy
// adds (OF-3) is the compiler's job, not this package's.
//
// The vendored schema under schema/ is Windmill's Apache-2.0
// openflow.openapi.yaml. A test checks that every property it declares is
// either decoded here or listed as ignored (LIC-3). Spec ids in comments
// refer to docs/semantics.
package openflow
