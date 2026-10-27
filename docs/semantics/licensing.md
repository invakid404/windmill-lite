# Licensing and the clean room

This area records which parts of the Windmill repository and its published
artefacts windmill-lite may read, depend on, copy or run, and which stay
closed to the build team. It also records the decision to use the Windmill
Community Edition (CE) image as a test oracle. The oracle harness itself is
described in [oracle.md](oracle.md), and the client libraries windmill-lite
can depend on are described in [client-api.md](client-api.md).

Two roles keep the clean room. The **semantics team** may read Windmill's
AGPL source at the pinned release and writes this reference in its own words.
The **build team** (everyone who designs, reviews or implements windmill-lite)
never opens AGPL or proprietary Windmill source. The binding contributor
rules are in [AGENTS.md](../../AGENTS.md); this file gives the evidence and the
directory-by-directory map behind them. Source pointers below are for the
semantics team; build-team members do not follow them into AGPL or
proprietary files.

**Pinned version:** Windmill `v1.767.0` (commit `6de4ec0f665ad856002495c4d86269105ec28ad7`).
Source pointers are `path:line @ v1.767.0` relative to the Windmill repository
root. Status, scope and confidence tags are defined in the [index](README.md#legend).

**ID prefixes in this file:** `LIC-` licence map and clean-room boundary. This
area has no findings added during validation. `OR-` findings cited here are in
[oracle.md](oracle.md).

## Summary for the design

1. **Windmill is AGPLv3 unless a part says otherwise.** The root `LICENSE`
   makes the OpenAPI files (the OpenFlow schema and
   `backend/windmill-api/openapi.yaml`, although it sits under `backend/`),
   `python-client/`, `go-client/` and `powershell-client/` Apache-2.0.
   `typescript-client/` is treated as Apache-2.0 by project rule. These are
   the build team's allowed Windmill inputs ([LIC-1](#lic-1)).
2. **Excluded for the build team:** AGPL and proprietary implementation code.
   That is the backend Rust sources (which do contain enterprise-gated
   proprietary snippets), `frontend/`, `cli/src/` and the other AGPL-default
   directories. The `windmill-parser-wasm-*` npm packages are AGPL-derived and
   are never a dependency ([LIC-1](#lic-1)).
3. **One boundary is open.** `windmill-yaml-validator`,
   `cli/windmill-utils-internal`, `rust-client` and the published
   `windmill-cli` carry an Apache label only in their own package metadata.
   Whether the build team may read, depend on or copy them is a human decision
   ([Q-OF-7](#q-of-7)(a)). Behaviour that this reference describes can be
   implemented independently either way ([LIC-1](#lic-1), [LIC-2](#lic-2)).
4. **Black-box use is not affected by the open questions.** The semantics team
   reads AGPL source at the pin, and anyone may run the published `wmill` CLI
   and the unmodified CE image as tools ([LIC-1](#lic-1),
   [Decision](#oracle-image)).
5. **Public CI may pull and run the unmodified CE image as a test oracle.**
   windmill-lite never redistributes, mirrors, bakes, modifies, wraps or
   exposes it as a service. The CE image is not pure AGPL: it is built with
   proprietary code ([Decision](#oracle-image), [OR-3](oracle.md#or-3)).
6. **Whether hybrid dispatch to a user's own Windmill counts as "wrapping"**
   under the CE terms is a separate, open human decision
   ([Q-OF-7](#q-of-7)(b)).
7. **The sync-folder format** comes to the build team from the Apache OpenFlow
   schema plus the `SY-` findings in [openflow.md](openflow.md) and the public
   CLI and Sync documentation ([LIC-2](#lic-2)).
8. **Windmill ships no Go OpenFlow types.** windmill-lite needs its own, checked
   against the vendored schema by a coverage test ([LIC-3](#lic-3)).
9. **Two repository checks enforce the boundary:** a manifest guard
   ([LIC-1](#lic-1)) and a lint that forbids derived or re-pushed Windmill
   images ([Decision](#oracle-image)).

## A. Licence map

<a id="lic-1"></a>
### LIC-1 · Licence map and the build-team boundary

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **Governing text.** The root `LICENSE` governs the repository.
    - Everything is AGPLv3 "unless otherwise specified".
    - Backend code under the `enterprise` feature flag and licence-checked
      frontend code are proprietary.
    - `python-client/`, `deno-client/`, `go-client/`, `powershell-client/`
      and "the openapi files, including the OpenFlow spec" are Apache-2.0.
    - The CE image and release binaries carry their own terms (last row of
      the map).
  - **Headers.** No file has an SPDX header. About a fifth of the backend
    `.rs` files carry a prose AGPL header; the rest rely on the root rule.
  - **`deno-client/` no longer exists.** It was removed before v1.450, and
    Deno users get `typescript-client` through JSR.
  - **Proprietary code is present in the tree.** The private `*_ee.rs` files
    are absent (ignored by `backend/.gitignore`, with `*_oss.rs` stubs in
    their place), but `cfg(feature = "enterprise")` snippets sit in public
    files, and the root `LICENSE` makes them proprietary. The checkout is
    therefore not free of proprietary code.
  - **Package metadata is an observation, not a permission.** An "Apache 2.0"
    field in a package's own metadata, for a directory the root `LICENSE`
    does not name, does not establish a right to read, depend on or copy it.
    A missing licence field does not remove the AGPL from a package built
    from AGPL code.

  **Use classes.**

  | Class | Basis | Build team | windmill-lite artefacts |
  |---|---|---|---|
  | Apache | Named Apache-2.0 by the root `LICENSE`, or `typescript-client/` by project rule ([AGENTS.md](../../AGENTS.md)) | May read | May depend on it, generate code from it, or copy it with its licence notice kept. Never relabel it as Unlicense. |
  | Apache, metadata only | The package's own `package.json`, `jsr.json` or Cargo metadata says Apache 2.0; the root `LICENSE` does not name the directory | Read, depend on or copy: open, [Q-OF-7](#q-of-7)(a) | Not a dependency and not copied until Q-OF-7(a) is decided. Behaviour described in this reference may be implemented independently. Running the published `wmill` CLI as a black-box tool is separate and allowed. |
  | AGPL | Root default | Never opens. The semantics team reads it at the pin and describes behaviour in its own words. | Never copied or translated, never a dependency. |
  | Proprietary (`EE`) | `enterprise`-gated code, the code the `private` feature pulls in at release build ([OR-3](oracle.md#or-3)), licence-checked frontend code, private packages | Never opens | Out of scope unless observable on CE. |
  | Built product | CE image and release binaries, root `LICENSE:23-31` | Black-box use only | Never shipped, modified or wrapped. See [Decision](#oracle-image). |

  **The map.**

  | Path or package | What it is | Licence evidence | Class | What windmill-lite may do |
  |---|---|---|---|---|
  | `openflow.openapi.yaml` | OpenFlow schema, `info.version: 1.767.0` | `openflow.openapi.yaml:11-13`; root `LICENSE:16-17` | Apache | Vendor it (with notice) and generate or check Go types against it. It is the source of field names and descriptions, combined with the decode rules in [openflow.md](openflow.md#of-1). |
  | `backend/windmill-api/openapi.yaml` | Full REST API schema; re-exports the OpenFlow schemas (`:23454-23503`) | `:12-14`; root `LICENSE:15-17` | Apache (explicit exception inside `backend/`) | Generate an API client for the oracle harness ([oracle.md](oracle.md)) and for hybrid dispatch ([client-api.md](client-api.md#wa-51)). |
  | `backend/windmill-api/openapi-deref.{yaml,json}`, `backend/openapi-bundled.yaml`, `backend/openapi-deref.yaml` | Dereferenced bundles | The deref files say version 1.740.0; the two `backend/` files are empty | Apache | Do not use: stale or empty. Bundle from the source YAML instead. |
  | `go-client/` | Thin hand-written Go wrapper (`windmill.go`, about 200 lines: variables, resources, state, resume URLs) over a client generated at release time by oapi-codegen v2.4.1 from the API schema. The generated `api/` is gitignored and published as the separate module `github.com/windmill-labs/windmill-go-client`. | `go-client/LICENSE`; `go-client/build.sh`; `go-client/go.mod` | Apache | May depend on the published module for API calls. Its OpenFlow types are a poor base for windmill-lite's own ([LIC-3](#lic-3)). |
  | `python-client/` | `wmill` package; the generated `windmill-api` is built at release | `python-client/LICENSE`; `python-client/wmill/pyproject.toml:5` (`license = "Apache-2.0"`) | Apache | Depend on it or shim it ([client-api.md](client-api.md)). |
  | `powershell-client/` | PowerShell module | root `LICENSE:15` | Apache | Allowed; not needed. |
  | `deno-client/` | Named in the root `LICENSE`, absent at the pin | root `LICENSE` | Apache | Nothing to use. |
  | `typescript-client/` | npm `windmill-client`, JSR `@windmill/windmill`. Hand-written `client.ts`, `s3Types.ts`, `sqlUtils.ts`; the generated `src/` (hey-api openapi-ts 0.43.0) is gitignored. | `package.json:6` "Apache 2.0"; `jsr.json:8` "Apache-2.0"; no `LICENSE` file; not named in the root `LICENSE` | Apache (project rule) | Depend on the published package; alias or shim it inside the Bun harness ([client-api.md](client-api.md)). The root-`LICENSE` wording gap is kept as an observation only. |
  | `windmill-yaml-validator/` | npm `windmill-yaml-validator` 1.1.1: ajv validation of `flow.yaml`, schedules and triggers against schemas generated from the two OpenAPI files ([OF-19](openflow.md#of-19)) | `package.json:18` "Apache 2.0"; published on npm; not named in the root `LICENSE` | Apache, metadata only | Open ([Q-OF-7](#q-of-7)(a)). It is not a substitute for the server's parse rules in any case. |
  | `cli/windmill-utils-internal/` | npm `windmill-utils-internal` 1.8.2: inline-script extraction and replacement, inline-script file naming, the language-to-extension table, signature-to-JSON-Schema conversion (`argSigToJsonSchemaType`), deploy helpers. Used by the CLI, the VS Code extension and the frontend. | `package.json:31` "Apache 2.0"; published on npm; not named in the root `LICENSE` | Apache, metadata only | Open ([Q-OF-7](#q-of-7)(a)). |
  | `cli/` apart from `cli/windmill-utils-internal/` (including `cli/src/`) | `wmill` CLI source; published to npm as `windmill-cli` | `cli/package.json:2-3` is the private, unlicensed `wmill-dev`. `cli/build-npm.ts:61` stamps "Apache 2.0" on the published package, but `:53` copies the root mixed `LICENSE` into it. The root `LICENSE` does not name `cli/`. | AGPL (source); Apache, metadata only (published `windmill-cli`) | The build team never opens `cli/src/`. Reading, depending on or copying the published package is open ([Q-OF-7](#q-of-7)(a)). Running the published `wmill` binary as a black-box tool is allowed ([OR-8](oracle.md#or-8)). |
  | `rust-client/` | Rust `wmill` crate plus a generated `windmill-api` | `Cargo.proto.toml:6` Apache-2.0; not named in the root `LICENSE` | Apache, metadata only | Open ([Q-OF-7](#q-of-7)(a)); not needed. |
  | `backend/**` apart from the OpenAPI files (all crates: `windmill-types`, `windmill-common`, `windmill-worker`, `windmill-queue`, `windmill-api*`, `windmill-jseval`, `windmill-runtime-nativets`, `windmill-dep-map`, and the rest) | Server, workers, flow model, evaluator | root `LICENSE:7-10`; `backend/NOTICE:5`; prose headers (for example `backend/windmill-common/src/flows.rs:4`) | AGPL, plus proprietary code under the `enterprise` and `private` features | The semantics team reads it; the build team never opens it. Never copied, never linked. `windmill-types` has no per-file header but is AGPL by the root rule. |
  | `backend/parsers/*` (22 `windmill-parser-*` crates), published to npm as `windmill-parser-wasm-*` | Script signature and import parsers | No licence field in any `Cargo.toml`; AGPL headers (for example `windmill-parser-ts/src/lib.rs:4`); the npm entries carry no licence | AGPL | Never a dependency. Signature parsing is reimplemented from [languages.md](languages.md). |
  | `frontend/` | Svelte UI | `frontend/LICENSE`, `frontend/NOTICE:3`, `package.json` AGPL-3.0; licence-checked UI is proprietary | AGPL and proprietary | Do not use. Its committed `openFlow.json` is a stale (1.756.0) copy of the Apache schema; use the root schema instead. |
  | `react-sdk/` | README only; the SDK itself is private | `react-sdk/README.md:3` | Proprietary | Nothing to use. |
  | `lsp/`, `wm-ts-nav/`, `system_prompts/`, `docs/`, `examples/`, `fixtures/`, `integration_tests/`, `benchmarks/`, `docker/`, `sandbox-image/`, `ai_evals/`, `debugger/`, `multiplayer/`, `functions/`, `ephemeral-backends/` | Tooling, prompts, internal docs, fixtures, deployment files | No licence of their own; root default | AGPL | Never copy fixtures or examples; write synthetic ones ([AGENTS.md](../../AGENTS.md)). |
  | Docker image `ghcr.io/windmill-labs/windmill` (CE) and release binaries | Built product that includes non-public code | root `LICENSE:23-31` | Built product | Free use of CE features within the in-software limits and quotas, and redistribution "as is". No selling, reselling, managed service, modification or "wrap under any form" without an agreement. Pulling and running the unmodified image as a test oracle is use ([Decision](#oracle-image)). Never ship it inside a windmill-lite artefact. Hybrid dispatch: [Q-OF-7](#q-of-7)(b). |

  - **Unaffected by the open questions:**
    - the semantics team reads AGPL source at the pin;
    - running published tools as black boxes is allowed: the `wmill` CLI and
      the unmodified CE image as a test oracle.
  - **The only open boundary** is build-team access to the four
    metadata-only packages: `windmill-yaml-validator`,
    `cli/windmill-utils-internal`, `rust-client` and the published
    `windmill-cli` ([Q-OF-7](#q-of-7)(a)).
- **Evidence:** root `LICENSE:1-31` (default, proprietary and Apache parts
  `:7-17`, with the Apache list at `:15-17`; built-product terms `:23-31`);
  `NOTICE:1-10` (it refers to
  "LICENSE-ALPH", a typo for `LICENSE-AGPL`);
  `backend/windmill-api/openapi.yaml:12-14`; `backend/.gitignore:10`;
  enterprise snippets at `backend/windmill-api-flows/src/flows.rs:362-379`
  (CE stub and enterprise implementation side by side) and
  `backend/windmill-worker/src/worker_flow.rs:1819`; the per-row pointers in
  the map; the project's classification of `typescript-client/` in
  [AGENTS.md](../../AGENTS.md).
- **Conformance:** [LIC-1/a, LIC-1/b](conformance.md#lic-1) (repository
  checks on windmill-lite's own manifests).
- **Note:** The manifest guard concerns Windmill-derived inputs only, not
  unrelated third-party dependencies. The guard against derived or re-pushed
  Windmill images is in [OR-3](oracle.md#or-3) and summarised under
  [Decision](#oracle-image).

<a id="lic-2"></a>
### LIC-2 · Where the build team learns the sync format without AGPL code

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:** Most of the `wmill sync` folder format is defined in Apache or
  Apache-labelled Windmill sources; the rest is in AGPL code. Read against the
  classes of [LIC-1](#lic-1):
  - **The flow model:** `openflow.openapi.yaml` (Apache, allowed).
  - **Which files are flows, schedules and triggers:**
    `windmill-yaml-validator` (Apache, metadata only).
  - **Inline-script file naming, the `!inline` reference syntax, the
    language-to-extension table and lock-file naming:**
    `windmill-utils-internal` (Apache, metadata only).
  - **Everything else** (script metadata layout, `wmill.yaml`,
    `wmill-lock.yaml`, resources, variables, path mapping) lives in the
    AGPL-default `cli/src/`. It reaches the build team only through the `SY-`
    findings in [openflow.md](openflow.md#sy-1) and the public "CLI" and
    "Sync" pages of the [Windmill documentation](https://www.windmill.dev/docs).

  Build-team access to the two metadata-only packages waits for
  [Q-OF-7](#q-of-7)(a); until then, what they define also reaches the build
  team through the `SY-` findings. This reference does not itself authorise
  reading any source whose licence is unsettled.
- **Evidence:** `cli/windmill-utils-internal/src/index.ts:1-15` (exports),
  `cli/windmill-utils-internal/src/path-utils/path-assigner.ts`,
  `cli/windmill-utils-internal/src/inline-scripts/{extractor,replacer}.ts`;
  `windmill-yaml-validator/src/validation/yaml-validator.ts:49-73`.
- **Conformance:** none (framing finding).

<a id="lic-3"></a>
### LIC-3 · No usable Go OpenFlow type set exists

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:** Windmill commits no Go OpenFlow types. `go-client/` generates its
  API client at release time from the bundled OpenAPI schema with
  oapi-codegen 2.4.1 and does not commit the generated `api/` directory.
  Types generated that way come from the OpenAPI schema, so they cannot carry
  the effective-model rules: defaults, aliases, raw-number shortcuts, integer
  ranges, null-versus-omission differences and the required
  `skip_if_stopped` ([OF-1](openflow.md#of-1), [OF-V1](openflow.md#of-v1),
  [OF-V2](openflow.md#of-v2)). windmill-ts has no OpenFlow types either
  ([PA-2](openflow.md#pa-2)).
- **Evidence:** `go-client/build.sh`; `go-client/.gitignore:1` (`api/` not
  committed).
- **Conformance:** [LIC-3/a](conformance.md#lic-3) (a check on windmill-lite's
  own types).
- **Note:** Statements about the contents of the published Go module are
  inference: no released artefact was inspected. Generated from the schema's
  `oneOf`, its OpenFlow types would be unions wrapping raw JSON.
- **Design note (not Windmill behaviour):** hand-write (or generate and then
  annotate) Go types from `openflow.openapi.yaml`. Encode the decode rules of
  [OF-5](openflow.md#of-5) to [OF-16](openflow.md#of-16),
  [OF-V1](openflow.md#of-v1) and [OF-V2](openflow.md#of-v2) explicitly. Add a
  test that every schema property is either mapped or explicitly ignored, so
  that a schema change at a new pin fails the test.

<a id="oracle-image"></a>
## Decision: the Windmill CE image as a test oracle

**Status:** DECIDED (human decision).

**Public CI MAY pull and run the unmodified Windmill CE image as a test
oracle.** That is use, which the image's terms allow. windmill-lite never
redistributes the image, mirrors or re-pushes it, bakes it into its own
images, modifies it, wraps it, or exposes it as a service.

What [OR-3](oracle.md#or-3) records about the image and its terms:

- **It is not pure AGPL.** The published CE image is built with the `ce`
  feature set. `ce` includes `ce_core`, which combines `oss_core` with the
  `private` and `operator` features, and release CI checks out and copies a
  private enterprise repository before building. The image's
  `org.opencontainers.image.licenses` label is `NOASSERTION`
  ([OR-1](oracle.md#or-1)).
- **Its terms** (root `LICENSE`):
  - free use of all CE features, subject only to the in-software limits and
    quotas;
  - a right to distribute it "as is";
  - no right to "sell, resell, serve as a managed service, modify or wrap
    under any form" without an agreement.
- **The README** adds that a commercial licence is needed to "build a feature
  on top of" CE that is sold or embedded in a distributable product.
  windmill-lite does neither: it only tests against CE.
- **What windmill-lite does with it:** pulls it from `ghcr.io` at test time,
  pinned by version and digest ([OR-1](oracle.md#or-1)); runs it unmodified,
  configured only through environment variables and API calls; and uses it
  only as a black-box test oracle.
- **What windmill-lite must not do:** build derived images
  (`FROM ghcr.io/windmill-labs/windmill…`), mirror or re-push it, ship it
  inside a windmill-lite artefact, or expose it as a service.
- **Repository lint:** no `FROM ghcr.io/windmill-labs/` in any Dockerfile,
  and no `docker push` or `docker tag` of a Windmill image in any workflow.
- **Enterprise images are not used in public CI:** they need a licence key,
  which would be a secret ([OR-19](oracle.md#or-19)).
- **The CLI is a separate tool.** The harness pushes fixtures with
  `windmill-cli@1.767.0` from npm (licence field "Apache 2.0", binary `wmill`),
  run on the harness host, not the CLI inside the image
  ([OR-8](oracle.md#or-8)). Running it is use. Its npm licence field is not
  evidence that CLI source may be copied into an Unlicense implementation
  ([OR-3](oracle.md#or-3), [LIC-1](#lic-1)).
- **An AGPL-only fallback exists but is optional.** A binary built from
  source with the `oss` feature set (`oss_core` + `all_languages` +
  `no_auth`) contains no proprietary code. It costs a full Rust and frontend
  build. Whether it behaves like the CE image for core flow semantics is
  unverified ([Q-OR-1](oracle.md#q-or-1)).

Evidence (from [OR-3](oracle.md#or-3)): root `LICENSE:1-31`; `README.md:237`;
`backend/Cargo.toml:180-201` (`oss_core`, `ce_core`, `oss`, `ce`, `ee`);
`.github/workflows/docker-image.yml:49-75` (private checkout and code
substitution before the CE build).

The decision does not cover hybrid dispatch, where windmill-lite sends steps
to a user's own Windmill instance: that is [Q-OF-7](#q-of-7)(b).

## Licensing facts recorded in other areas

| Fact | Source |
|---|---|
| `typescript-client/`, `python-client/`, `go-client/` and `backend/windmill-api/openapi.yaml` are Apache-2.0 and may be depended on or copied with their notices kept. The Python licence is declared in `python-client/wmill/pyproject.toml` (`license = "Apache-2.0"`). | [client-api.md](client-api.md) (area introduction) |
| The Apache-2.0 Go client `github.com/windmill-labs/windmill-go-client` (generated from `openapi.yaml` with oapi-codegen, plus thin helpers that read `BASE_INTERNAL_URL`, `WM_WORKSPACE` and `WM_TOKEN`) can be a dependency for delegated steps; windmill-lite can also generate its own client from `openapi.yaml`. | [WA-51](client-api.md#wa-51) |
| The S3 job-helper server implementation is not in the public source: `backend/windmill-api/src/job_helpers_oss.rs` is a stub that re-exports the enterprise module under the `private` feature, and a pure OSS build registers no job-helper routes. CE server behaviour for those routes is knowable only from the oracle. | [WA-16](client-api.md#wa-16), [Q-WA-3](client-api.md#q-wa-3) |
| Bun's long-lived "dedicated worker" mode is gated behind the proprietary `private` feature. It is `EE` and not observable on CE. | [WR-36](languages.md#wr-36) |
| windmill-ts (`github.com/invakid404/windmill-ts`, pinned at `af2b1e325320`) is released under the Unlicense, the same licence as windmill-lite, so its code may be reused. Its loader behaviours are prior-art policies, not Windmill semantics. | [WA-60](client-api.md#wa-60), [PA-1](openflow.md#pa-1) |
| The published `windmill-cli@1.767.0` npm package declares "Apache 2.0" and is run as a black-box tool. | [OR-8](oracle.md#or-8) |

The execution, control-flow and expression areas state no licensing facts
beyond the clean-room rules above.

## Open questions

<a id="q-of-7"></a>
- **Q-OF-7** · affects [LIC-1](#lic-1), [LIC-2](#lic-2),
  [Decision](#oracle-image) · **(human decision)** Two questions:
  - **(a)** May the build team read, depend on, or copy source from the
    packages whose Apache label rests on their own metadata alone:
    `windmill-yaml-validator`, `cli/windmill-utils-internal`, `rust-client`
    and the published `windmill-cli`? The source shows only the metadata and
    the root `LICENSE` wording; it cannot establish a legal permission.
  - **(b)** Does hybrid dispatch, where windmill-lite sends steps to a user's
    own Windmill instance through its API ([WA-50](client-api.md#wa-50),
    [WA-51](client-api.md#wa-51)), count as "wrap under any form" under the
    CE terms (`LICENSE:23-31`)? It looks like ordinary API use, but that is a
    legal reading, not a source fact.

  *Settled by:* a human decision. Neither part blocks this reference:
  behaviour described here can be implemented independently, and the CE
  oracle use decided above stands either way.

Related question in another area:

- **[Q-OR-1](oracle.md#q-or-1)** · Does a source build with only the `oss`
  feature set give the same normalised observations as the CE image? If so,
  it is an AGPL-only oracle. Optional, since public CI may use the CE image.
  *Settled by:* building the `oss` image, running every oracle fixture on
  both images, and comparing the normalised observations.
