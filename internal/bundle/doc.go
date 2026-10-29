// Package bundle builds windmill-lite bundles (ADR 0010).
//
//   - Build loads and compiles a sync folder with its host contract
//     (internal/syncfolder, internal/ir), bundles its Bun scripts and
//     host-provided modules with BuildBun and its codebase scripts with
//     BuildCodebase, refuses workflow-as-code and scripts that import a
//     package without a lock, and writes the bundle: ir.json, scripts/bun/,
//     assets/, data/ and manifest.json, last.
//   - The manifest (Manifest) lists the digest of every bundle file and of
//     every source file the build read; the bundle digest is the SHA-256 of
//     its canonical encoding (Canonicalize). WriteArchive writes a bundle as
//     a .wlb archive, one bundle giving one archive; Verify checks a bundle
//     or archive against its manifest or a release manifest.
//   - The parts both bundlers share: the env rewrite, one define table and
//     one support module (EnvRewrite; WA-31); the shim generator for
//     host-provided modules (Shim; D12); the import base of an inline
//     script (ImportBase; WR-29).
//   - BuildBun runs the Bun build helper (harnesses/bun/build) on the pinned
//     Bun: it bundles every Bun script with Windmill's settings (WR-26 to
//     WR-28), parses the signatures, annotations and workflow-as-code
//     markers (WR-6 to WR-8, WR-31, WR-32, WR-39), scans the exports of
//     host-provided modules and refuses live bindings, and writes
//     scripts/bun/<id>.mjs with its <id>.meta.json.
//   - BuildCodebase bundles codebase scripts (SY-8) with esbuild 0.28.0
//     through its Go API, with the pinned CLI's settings (CodebaseSettings,
//     WR-47) plus the env rewrite and the shims, and carries their assets
//     (WR-49): scripts/bun/<id>.cjs (or .mjs) and assets/<to>.
package bundle
