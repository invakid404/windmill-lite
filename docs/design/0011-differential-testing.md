# 0011 · Differential testing and performance measurement

**Status:** proposed · **Topic:** checking windmill-lite against a real
Windmill (the oracle), the step identity encoding shared by every comparison,
CI, and how performance is measured.

## Context

Almost every expectation in the reference is source-derived; only the
oracle area has recorded observations (36 cases, run 1). The reference
designs the oracle harness ([OR-1](../semantics/oracle.md#or-1) to
[OR-31](../semantics/oracle.md#or-31)) and leaves two core items open: a
collision-free identity for compared steps ([Q-OR-17](../semantics/oracle.md#q-or-17))
and an executable reconstruction of the probe catalogue
([Q-OR-16](../semantics/oracle.md#q-or-16)). Public CI may pull and run the
unmodified CE image as a test oracle; windmill-lite never redistributes,
modifies, wraps or bakes it ([LIC-1](../semantics/licensing.md#lic-1),
[OR-3](../semantics/oracle.md#or-3)). The oracle is a semantic reference,
never a latency baseline ([OR-17](../semantics/oracle.md#or-17)).

## Decision

### The oracle harness

The `oracle` package implements the reference's design as written:

1. **Topology** (image pins [OR-1](../semantics/oracle.md#or-1) and
   [OR-2](../semantics/oracle.md#or-2); topology [OR-4](../semantics/oracle.md#or-4);
   lifecycle and port binding [OR-18](../semantics/oracle.md#or-18)):
   `ghcr.io/windmill-labs/windmill:1.767.0` pinned by its full index digest,
   in `MODE=standalone`, next to `postgres:16-alpine` pinned by digest, data
   on tmpfs, memory limits, ports on `127.0.0.1`; a preflight that requires
   `/api/version` to be exactly `CE v1.767.0`. Fixtures that declare
   `workers: 2` get a second worker container ([OR-5](../semantics/oracle.md#or-5)).
2. **Bootstrap and push** ([OR-6](../semantics/oracle.md#or-6),
   [OR-8](../semantics/oracle.md#or-8), [OR-9](../semantics/oracle.md#or-9),
   [OR-31](../semantics/oracle.md#or-31)): a fresh workspace per fixture run;
   the sync tree pushed with `windmill-cli@1.767.0` from npm on the runner,
   never the CLI inside the image; setup applied after the push; the
   deployment gate polls each lock job to success.
3. **Run and capture** ([OR-10](../semantics/oracle.md#or-10),
   [OR-V1](../semantics/oracle.md#or-v1), [OR-V2](../semantics/oracle.md#or-v2)):
   asynchronous submission and `get_result_maybe` polling, never
   `run_wait_result`; full arguments and results only from `get_args` and
   `get_result_maybe`; status from job metadata.
4. **Walk** ([OR-11](../semantics/oracle.md#or-11) topology,
   [OR-V7](../semantics/oracle.md#or-v7)): by `flow_jobs` children in index
   order, `failed_retries` as attempts, generated `-v` steps omitted, the
   collapse rules never relied on.
5. **Normalise** ([OR-14](../semantics/oracle.md#or-14),
   [OR-V3](../semantics/oracle.md#or-v3), [OR-V5](../semantics/oracle.md#or-v5)):
   only walked job ids replaced; stacks masked only with runtime-error
   provenance; the NUL `InternalErr` location masked and nothing broader;
   provenance kept apart from the compared observation. These rules govern
   oracle-to-oracle comparisons unchanged. Comparing windmill-lite with the
   oracle adds one rule: the Rust location suffix is stripped from every
   message, because windmill-lite cannot reproduce it
   ([0001](0001-equivalence-contract.md) §2).
6. **Effects** ([OR-16](../semantics/oracle.md#or-16), [OR-V6](../semantics/oracle.md#or-v6)):
   an HTTP sink on the runner, grouped by `WM_ROOT_JOB_ID` against the
   submitted root. windmill-lite injects the same sink variable.
7. **Disk** ([OR-30](../semantics/oracle.md#or-30)): the fail-closed policy
   for local runs (pull only with at least 8 GiB free and 5 GiB left after
   the image; stop below 5 GiB; no automatic resume).

### Step identity encoding (answers Q-OR-17)

A **site path** is a sequence of typed segments, written as text by joining
segments with `/`. Each segment is `<kind>:<value>`:

| Kind | Value | Example |
|---|---|---|
| `m` | a module id in a module list | `m:a` |
| `i` | a loop iteration index | `i:2` |
| `b` | a branch: `default` or an index for `branchone`, an index for `branchall` | `b:0` |
| `s` | a special slot: `failure` or `preprocessor` | `s:failure` |
| `r` | an attempt number of a retried module, from 0 | `r:1` |

Values are percent-escaped for `%`, `/` and `:` (`%25`, `%2F`, `%3A`), so the
escape character itself is escaped. Examples: `m:outer/i:1/m:inner/i:0/m:s`,
`m:a/r:0` (the failed first attempt of `a`), `s:failure`, and `m:failure`
for an ordinary module that happens to be named `failure`.

The encoding is injective: the kind separates ordinary modules from slots,
and escaping covers the escape character. A collapsed loop's iterations
appear as `m:<loop>/i:<n>/m:<body>`, the same path a non-collapsed loop gives,
so the shape of Windmill's job tree never leaks into identities. The same
encoding is used by job-id placeholders (`<job:m:a/i:1/m:s>`), host-hook
sites ([0008](0008-host-hooks.md)), run traces, and any replay service keyed
by site. Cases OR-V8/a–c become negative tests of the encoder.

### Fixtures and verbs

Flow fixtures use the format of [OR-15](../semantics/oracle.md#or-15)
(`fixture.yaml`, `windmill/`, `setup.yaml`, `runs/*.yaml`, `recorded/*.json`)
under `conformance/flows/` ([0003](0003-semantics-placement.md)). The
`wltest` command has three verbs:

- `record`: run fixtures on the oracle and write `recorded/` with provenance;
- `check`: run fixtures on windmill-lite and compare with `expect`, or with
  `recorded` where `expect` is absent; a failure prints JSON-pointer diffs
  (case OR-15/b);
- `diff`: run both live and compare.

Exact comparison is the gate. Step and expression cases are lifted into
generated one-step flow fixtures for `record`.

### Stub mode

`wltest stub <folder>` rewrites a sync tree so that every executed script
body is replaced by a recorder stub that keeps the original `main` signature
(so hydration is unchanged) and asks a run-scoped replay service for its
outcome, keyed by the outer root (`WM_ROOT_JOB_ID`), the site path and the
attempt. On the oracle, the service resolves the site by walking the job
tree; on windmill-lite it reads the run trace. Orchestration of flows whose
real steps need private systems can then be compared on the oracle without
those systems; the flows and tables stay with whoever owns them.

### CI

1. **Every pull request** (no Docker): `wltest check` on all levels, with
   JSON differential mode on ([0005](0005-json-step-boundaries.md)); the
   **image lint** (no Dockerfile `FROM ghcr.io/windmill-labs/`, no
   `docker push`/`docker tag` of a Windmill image); the **manifest guard**,
   which rejects AGPL-derived Windmill packages (for example
   `windmill-parser-wasm-*`) and, until [Q-OF-7](../semantics/openflow.md#q-of-7)(a)
   is decided, the metadata-only Apache packages, in windmill-lite's own
   manifests (cases LIC-1/a, LIC-1/b).
2. **Oracle workflow** (nightly, on a `oracle` label, and on changes under
   `conformance/`): `wltest record` on `ubuntu-latest` amd64, failing when a
   recording differs from the committed one. A changed recording is
   committed by a pull request that also updates the case's provenance and,
   where it settles a question, the reading defaults
   ([0001](0001-equivalence-contract.md) §6).

### Performance

1. **Micro-benchmarks** (milestone G1): the boundary costs of
   [0005](0005-json-step-boundaries.md), the protocol round trip of
   [0004](0004-harness-protocol.md), and the expression engine with and
   without pre-warmed contexts ([0003](0003-semantics-placement.md)).
2. **Run spans.** Every run trace carries durations per module, per
   invocation (queue, harness, serialisation), per expression evaluation and
   per platform call, plus every requested wait.
3. **Comparing against a baseline implementation** (a host's existing code,
   never the oracle): run both on the same seeds with the `record` clock
   (H10), so deliberate waits are recorded rather than slept; report elapsed
   time, CPU and peak RSS at p50 and p95 over at least 50 runs after warm-up,
   with request counts. Equal request counts and equal recorded waits are
   checked first: a difference there is a semantic difference, not a
   performance one.

## Consequences

- Every comparison, placeholder, hook and trace uses one identity encoding.
- The oracle runs in public CI as a black box and only there, never inside a
  windmill-lite artefact.
- Disputed findings get settled by recordings, through a reviewed pull
  request that changes one default.

## Spec IDs relied on

OR-1 to OR-31, OR-V1 to OR-V8, Q-OR-16, Q-OR-17, LIC-1, Q-OF-7, and the
conformance catalogue.
