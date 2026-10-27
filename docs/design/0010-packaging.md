# 0010 · Packaging, build identity and drift guard

**Status:** proposed · **Topic:** the bundle format, the single executable
for single-harness selections, the OCI image, build identity, and the guards
that keep what runs equal to what was built.

## Context

Once flows are data, a deployed worker must prove which flows, scripts,
locks and runtimes it carries. A Windmill job runs on a pinned Bun
(`1.3.10` in the reference image, [WR-3](../semantics/languages.md#wr-3)) and
bundles each script with fixed settings ([WR-27](../semantics/languages.md#wr-27)).
Practices that worked for a compiled Bun worker carry over: refuse to build on
an unpinned Bun, keep the bundler's metafile, write a release manifest with
digests, answer health checks and a build-info call, and let the host pin the
executable by digest.

## Decision

### The bundle

A bundle is a directory, or the same tree as one `.wlb` file (a tar archive):

| Path | Content |
|---|---|
| `manifest.json` | format version; windmill-lite version; the digest of every other file; the source inventory (every input file of the sync folder with its digest, locks included); the Bun version; readings and deviations in effect ([0001](0001-equivalence-contract.md)); module-state mode; harness runtime digest; required engine version |
| `ir.json` | the compiled program ([0002](0002-ir-and-loader.md)) |
| `scripts/bun/<id>.mjs` | one bundled module per Bun script, plus `<id>.meta.json` (the bundler's metafile and the parsed signature) |
| `scripts/bun/shared/` | the `shared` build, when that mode is selected ([0006](0006-module-state.md)) |
| `data/` | resources, resource types and non-secret variables from the folder |
| `plugins/` | host plugins, when the host ships any ([0008](0008-host-hooks.md)) |

Secrets are never written into a bundle unless the host passes them
explicitly.

### The build

`windmill-lite build <folder> -o <bundle>`:

1. loads and compiles the folder ([0002](0002-ir-and-loader.md));
2. runs the **Bun build helper** on the pinned Bun, refusing any other
   version unless `--bun-mismatch` is given, which marks the manifest
   `nonconformant_runtime`;
3. bundles each script with Windmill's settings: target `bun`, ESM,
   syntax-only minification, `electron` external
   ([WR-27](../semantics/languages.md#wr-27)); pinned-version removal and the
   `.ts` import rewrite as textual passes ([WR-26](../semantics/languages.md#wr-26),
   [WR-28](../semantics/languages.md#wr-28)); the inline import base
   ([WR-29](../semantics/languages.md#wr-29)); relative imports resolved from
   the folder; codebase scripts with their `wmill.yaml` settings
   ([SY-8](../semantics/openflow.md#sy-8)); `//nobundling` scripts bundled
   too (deviation D7);
4. parses each script's signature and annotations into its metafile
   ([WR-6](../semantics/languages.md#wr-6), [WR-8](../semantics/languages.md#wr-8),
   [WR-31](../semantics/languages.md#wr-31), [WR-32](../semantics/languages.md#wr-32),
   [WR-39](../semantics/languages.md#wr-39)), and checks native scripts for
   imports and globals the native runtime lacks (D5);
5. rewrites `Bun.env` and `import.meta.env` to `process.env` for
   per-invocation environments ([WA-31](../semantics/client-api.md#wa-31));
6. writes the manifest last.

The same inputs and pins produce the same bundle digest; CI builds a fixture
bundle twice and compares.

### Build identity

The **bundle digest** is the SHA-256 of the canonical `manifest.json`. Since
the manifest lists the digest of the IR, of every built script, of every
source file and lock, and of the harness runtime, any change to a flow (one
`stop_after_if`, say), a script, a lock, a pin or a reading changes the
digest. It appears in:

- `GetBuildInfo` and `/healthz`, with engine, harness and Bun versions;
- the harness handshake, which fails on a mismatch ([0004](0004-harness-protocol.md));
- every run trace;
- generated typed clients ([0009](0009-host-adapters.md)).

### Single executable

For a bundle with one harness class, `windmill-lite pack --single` writes one
file per platform (`linux/amd64`, `linux/arm64`, `darwin/arm64`):

- the Go engine with the bundle appended;
- the Bun harness compiled with `bun build --compile`, the script modules
  embedded, appended as well.

On start the executable extracts the harness into a private directory
(mode 0700), checks its digest against the manifest, and spawns it. `pack
--single --host bun` builds the mirror image for host mode: a compiled Bun
host program that carries the engine binary and starts it
([0009](0009-host-adapters.md)).

### OCI image

`windmill-lite image <bundle>` writes an OCI image layout (or pushes it) with
a pure-Go image library, so no Docker daemon is needed. The image holds the
single executable and its manifest on an empty base, with the chosen adapter
as entrypoint. No windmill-lite image is ever based on, or contains, a
Windmill image ([OR-3](../semantics/oracle.md#or-3)); a CI lint enforces it
([0011](0011-differential-testing.md)).

### Drift guard

1. The build refuses an unpinned Bun.
2. The harness and the engine refuse each other on a protocol, runtime or
   bundle-digest mismatch.
3. `windmill-lite verify <artifact>` recomputes every digest against a
   release manifest, for release jobs and for hosts that pin artifacts.
4. A host that pins the executable by digest (go-plugin `SecureConfig`) and
   compares `GetBuildInfo` with its release record catches a swapped binary
   and a swapped bundle.

## Consequences

- A deployed worker can prove which flows it runs, not just which code.
- The pinned Bun is the reference's; a host on another Bun gets a visible
  `nonconformant_runtime` flag, not a silent difference.
- One-file deployment is available for the common case of a single language.

## Spec IDs relied on

WR-3, WR-6, WR-8, WR-26 to WR-29, WR-31, WR-32, WR-39, WA-31, SY-8, OR-3.
