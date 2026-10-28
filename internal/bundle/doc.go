// Package bundle builds windmill-lite bundles (ADR 0010). Milestone C2a
// provides the parts shared by both bundlers and the Bun side of the build:
//
//   - the env rewrite, one define table and one prelude passed to every
//     bundler (EnvRewrite; WA-31);
//   - the shim generator for host-provided modules (Shim; D12);
//   - the import base of an inline script (ImportBase; WR-29);
//   - BuildBun, which runs the Bun build helper (harnesses/bun/build) on the
//     pinned Bun: it bundles every Bun script with Windmill's settings
//     (WR-26 to WR-28), parses the signatures, annotations and
//     workflow-as-code markers (WR-6 to WR-8, WR-31, WR-32, WR-39), scans
//     the exports of host-provided modules and refuses live bindings, and
//     writes scripts/bun/<id>.mjs with its <id>.meta.json.
//
// The bundle format, its manifest and digest, and the `windmill-lite build`
// command are milestone F4; codebase bundling through esbuild is C2b.
package bundle
