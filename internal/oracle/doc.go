// Package oracle is the oracle harness of ADR 0011: it runs synthetic flow
// fixtures on an unmodified Windmill CE v1.767.0, captures each run over the
// Apache-2.0 REST API, walks the job tree into typed sites, normalises the
// values, and writes recordings with provenance (OR-1 to OR-31, OR-V1 to
// OR-V9 in docs/semantics/oracle.md).
//
// The oracle is a black box. The harness pulls the pinned images, starts
// them with docker compose, configures them only through environment
// variables and API calls, pushes fixtures with the published wmill CLI run
// as a tool, and tears everything down. It never builds, tags or pushes a
// Windmill image (OR-3); the repository checks in lint.go enforce that for
// the whole repository.
package oracle
