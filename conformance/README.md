# conformance

**Current state:** ten levels of the shared conformance suite exist: the
OpenFlow decode cases in `openflow/`, the stored-form cases in `jsonb/`, the
sync-folder load cases in `syncfolder/`, the compile cases in `ir/`, the
harness protocol cases in `protocol/`, the Bun build cases in `build/`, the
flow fixtures in `flows/`, the Bun step cases in `steps/`, the
context-free expression cases in `exprs/`, and the host-hook cases in
`hooks/` (milestone F2b). `wltest record` records the flow fixtures and the expression cases
on the Windmill CE oracle: the oracle smoke fixture, the fixtures of
recording batches E3c (flow execution errors), E3b (flow execution core)
and E3d (loops, branches and the result cache), the stub-mode acceptance
cases, those of recording batch E3a (the expression cases of the JE
catalogue, and CF-19 and CF-V6), and those of recording batch E3e (FX-36's
HTTP responses, the contested `later` cases, codebase scripts, and the
probe catalogue rebuilt as fixtures). This README covers `conformance/`.
`wltest check` runs the flow fixtures on windmill-lite, through the `wl`
library in `warm` and `fresh` (milestone F1), on the engine core
(milestones E1, N1a, N1b, N3a and N3b), which runs flows of
Bun steps, their failure modules, loops and branches; it also runs the
expression cases end to end, as the generated one-step flows `wltest
record` lifts them into. The runs it cannot run yet
are reported as unsupported, and the runs it must pass are listed in
`cmd/wltest/testdata/flowsuite.json`
([cmd/wltest](../cmd/wltest/README.md#tests)). The tests of the expression
pipeline ([internal/expr](../internal/expr/README.md)) run the expression
cases and the flow fixtures' unit cases at unit level: through its
shortcuts and textual stages on a fake engine (milestone B2a), and on
QuickJS-ng against what the oracle recorded, apart from the cases they list
as skipped or as deviations (B2b). The step cases in `steps/` run on
windmill-lite's Bun harness through `wltest check` (milestone C3a), in
`warm` and, since milestone C3b, in `fresh` mode; those of a CommonJS
codebase script also run on Node, where Windmill runs them (milestone C2b,
deviation D13); `steps/bun/wr-40.yaml` to `wr-45.yaml` and `d5.yaml` are
the native cases of milestone C3n, and `build/d5.json` their build.

The suite pins each implementation to the
[semantics reference](../docs/semantics/README.md). Every case cites the
catalogue case it encodes (`docs/semantics/conformance.md`), or the finding
whose rule it encodes, and fixtures are synthetic: invented for this
repository to exercise one behaviour each.

## Levels

| Level | Directory | Status |
|---|---|---|
| decode | `openflow/<finding>.json` | in use; run by `go test ./internal/openflow` |
| jsonb | `jsonb/<finding>.yaml` | in use; run by `go test ./internal/jsonval`, and against PostgreSQL by CI's `jsonb (PostgreSQL)` job |
| load | `syncfolder/<finding>.json` | in use; run by `go test ./internal/syncfolder` |
| compile | `ir/<finding>.json` | in use; run by `go test ./internal/ir` |
| protocol | `protocol/frames.json`, `protocol/exchanges/<case>.json`, `protocol/host/workdir.json` | in use; run by `go test ./internal/harness` (the exchanges need the pinned Bun) and, for the codec and host cases, by `bun test` in `harnesses/bun` |
| build | `build/<finding>.json` | in use; run by `go test ./internal/bundle` through the Bun build helper on the pinned Bun, a whole folder through the bundle build |
| hooks | `hooks/<topic>.yaml` | in use (milestone F2b): `hooks/latch-precedence.yaml`, the latch-precedence cases of [ADR 0008](../docs/design/0008-host-hooks.md): the latch (H5), cancellation and deadlines (H4), fatal error names (H7), halts (H8) and the clock (H10) against retry, `continue_on_error`, `skip_failures` and the failure module, each case a synthetic flow of identity modules, the host's signals and what the run must show; windmill-lite's rules (provenance `policy`), never recorded; run by `go test ./wl` (`TestHookCases`) through the public hooks |
| flow | `flows/<area>/<case>/` | in use: `flows/oracle/smoke`, `flows/execution/fx-*` (E3c, E3b, E3d, N2), `flows/control-flow/cf-*` (E3d, N3c, NI) and `flows/control-flow/waits` (NI), the stub-mode cases under `flows/stub/` (E4a, E4b, N5, E4c), `flows/je/*` and `flows/cf/cf-19` (E3a), `flows/languages/*` and `flows/client-api/*` (E1, PF1, PF2), and the windmill-lite policy cases under `flows/host/` (F2a), which hold expects only; recorded on the oracle by `wltest record`; their recordings are held to their expectations and claims by `go test ./internal/oracle`; run on windmill-lite by `wltest check` and `go test ./cmd/wltest`, through `wl` in `warm` and `fresh` (F1), on the engine core (E1, N1a, N1b, N2, N3a, N3b, N3c, NI, N4, N5), with the platform providers (PF2), the effect sink and stub mode (E4b) |
| step | `steps/<lang>/<finding>.yaml` | in use; run by `wltest check` and `go test ./cmd/wltest` on the pinned Bun, in `warm` and `fresh` |
| expr | `exprs/<finding>.yaml`, `exprs/recorded/<finding>/<case>.json` (the finding in lower case) | in use; lifted into generated one-step flows and recorded on the oracle by `wltest record`; held to their recordings by `go test ./internal/oracle`; run at unit level by `go test ./internal/expr` (B2a, B2b), and end to end on windmill-lite as the same generated flows by `wltest check` and `go test ./cmd/wltest` (N1a, N1b) |

## Flow fixtures

A flow fixture is a directory in the format of
[OR-15](../docs/semantics/oracle.md#or-15): `fixture.yaml`, a `windmill/`
sync tree, an optional `setup.yaml`, one `runs/<name>.yaml` per run with its
`expect`, and `recorded/<name>.json` once the oracle has run it. The format
is described in [internal/conformance](../internal/conformance/README.md#fixtures),
and recording in [internal/oracle](../internal/oracle/README.md).

| Fixture | Encodes |
|---|---|
| `flows/oracle/smoke` | [OR-20](../docs/semantics/oracle.md#or-20): a path script doubles `x`, an inline Bun step formats it and throws above 100. Runs `ok` (`{"x":3}`, OR-11/a) and `fail` (`{"x":60}`, OR-12/a). |
| `flows/execution/fx-6` | [FX-6](../docs/semantics/execution.md#fx-6): step 0 sees the flow input; the step after a tolerated failure sees the wrapped error; a retry re-derives `previous_result` as the string `"{}"`. |
| `flows/execution/fx-7` | [FX-7](../docs/semantics/execution.md#fx-7): JavaScript and static transforms, `params`, `undefined`, an unbound `result`. |
| `flows/execution/fx-8` | [FX-8](../docs/semantics/execution.md#fx-8): `result` and `previous_result` in `stop_after_if`; no `results` in `sleep`. |
| `flows/execution/fx-9` | [FX-9](../docs/semantics/execution.md#fx-9): one `skip_if` per value the rule lists. |
| `flows/execution/fx-10` | [FX-10](../docs/semantics/execution.md#fx-10): a failure module reads an error-stopped step three ways; unknown and not-yet-run ids; which engine reads of the preceding id use memory, without a failure module ([Q-FX-15](../docs/semantics/execution.md#q-fx-15)). |
| `flows/execution/fx-16` | [FX-16](../docs/semantics/execution.md#fx-16): identity with direct input, and as a passthrough. |
| `flows/execution/fx-17` | [FX-17](../docs/semantics/execution.md#fx-17): a skipped step's passthrough; a step skipped only on its retry (FX-17/b, F1's: its step writes a variable through the job API); no `stop_after_if` on a skipped step. |
| `flows/execution/fx-18` | [FX-18](../docs/semantics/execution.md#fx-18) (`later`): a mocked step; `stop_after_if` on the mock value; a false `skip_if` beside a mock. |
| `flows/execution/fx-19` | [FX-19](../docs/semantics/execution.md#fx-19): the clean stops, the error-stop's value, and its matrix of retry, `continue_on_error` and the failure module. |
| `flows/execution/fx-20` | [FX-20](../docs/semantics/execution.md#fx-20): a clean stop and an error-stop in a `branchone` body. |
| `flows/execution/fx-21` | [FX-21](../docs/semantics/execution.md#fx-21): a throwing `stop_after_if`, with and without a failure module. |
| `flows/execution/fx-42` | [FX-42](../docs/semantics/execution.md#fx-42)/h ([Q-FX-16](../docs/semantics/execution.md#q-fx-16)): the arguments file after `$jsonvar:` resolution, and what a JavaScript consumer sees of it. |
| `flows/execution/fx-12` … `fx-38`, `fx-v6` | Recording batch E3c (ADR 0012): the catalogue cases of [FX-12](../docs/semantics/execution.md#fx-12) to [FX-15](../docs/semantics/execution.md#fx-15) and [FX-30](../docs/semantics/execution.md#fx-30) to [FX-38](../docs/semantics/execution.md#fx-38) (FX-36's executor part), FX-V6/a and FX-10/a. Covered: error values, chaining errors, `continue_on_error`, the failure module (what it sees, its retry, `recover`, inside loop bodies), the preprocessor, final-result selection, `early_return` and cancellation. One fixture per finding, `fx-12-deno` for the Deno case. Each run's `expect` states the catalogue's expectation. It holds the recorded value instead where the catalogue asserts only a prefix of a text, says nothing about a field, or is contradicted by the oracle: one case, FX-34/a's handler id, listed in the batch manifest and in [0001](../docs/design/0001-equivalence-contract.md) §6. The batch's coverage, deferrals and prefix checks are in `internal/oracle/testdata/batches/e3c.json` ([internal/oracle](../internal/oracle/README.md)). |
| `flows/control-flow/cf-2` to `cf-15` | Batch E3d: the catalogue cases of [CF-2](../docs/semantics/control-flow.md#cf-2) to [CF-15](../docs/semantics/control-flow.md#cf-15), one fixture per finding, including the contested CF-12 cases ([Q-CF-14](../docs/semantics/control-flow.md#q-cf-14), recorded as reading (b)). The for-loop runs (cf-2 to cf-8, cf-9 run `a`, cf-11, cf-13 and the loop runs of cf-12) gate N3a; the while and branch runs (cf-10, cf-9 run `b`, cf-14, cf-15 and the branch runs `d` and `e-*` of cf-12) gate N3b. |
| `flows/execution/fx-23` … `fx-29` | Milestone N2 (ADR 0012): the catalogue cases of [FX-23](../docs/semantics/execution.md#fx-23) to [FX-29](../docs/semantics/execution.md#fx-29), recorded on the oracle, with further runs for JE-28's and JE-29's texts. Covered: `sleep` (a javascript sleep of the module's own result, a fraction, after a skip and a tolerated failure, a final module's in the virtual step, string and null values, text that is not JSON, a sleep a cancellation cuts short); the step timeout (static, javascript and zero process timeouts, negative ones at the root's pickup and later, values that are not an i32, a float with an exponent, text that is not JSON, an untimed identity job, a flow's own timeout that nothing enforces); a timeout tolerated and retried; the retry schedule's constant and exponential phases, with `delays` read from the recordings' job times; argument reuse; `retry_if`; the flow-wide retry counter and list. FX-24/d–e (a loop's `timeout`) are NI's, recorded in `flows/control-flow/cf-27`, FX-24/g is `later`, and FX-26/c–e's hours-long delays are checked at unit level ([internal/engine](../internal/engine/README.md)); FX-29/c is recorded by N5's `flows/control-flow/subflow-retry`. The manifest is `internal/oracle/testdata/batches/n2.json`. |
| `flows/control-flow/cf-16` … `cf-18`, `cf-20`, `cf-29`, `cf-v1` … `cf-v5` | Milestone N3c (ADR 0012): the catalogue cases of [CF-16](../docs/semantics/control-flow.md#cf-16) to [CF-18](../docs/semantics/control-flow.md#cf-18), [CF-20](../docs/semantics/control-flow.md#cf-20), [CF-29](../docs/semantics/control-flow.md#cf-29) and [CF-V1](../docs/semantics/control-flow.md#cf-v1) to [CF-V5](../docs/semantics/control-flow.md#cf-v5), one fixture per finding, deployed with `oracle.deploy api`. Covered: the copied failure module in a loop body, once per failed iteration, and no second run of the flow's own; a subflow by path with its own input and results scope; a skipped loop passing `previous_result` through; branches mutating independent copies; an empty container's own error stop; recovery counted as failed by sequential containers and as succeeded by parallel ones; empty containers that skip `stop_after_all_iters_if` but stop the flow with `stop_after_if`; a subflow's pickup error bypassing its failure module, and the same flow run directly; the stored-node handler id; a collapsed loop by path. CF-V4/b and CF-V5/b need an inline or raw-preview submission, which windmill-lite never runs. CF-17/b–i are recorded by N5's `flows/control-flow/subflow-retry`, and CF-16/e–f and CF-17/j, added by the reference's revision SC4, are requested from batch E3f. The manifest, `internal/oracle/testdata/batches/n3c.json`, defers them with the reason. |
| `flows/control-flow/cf-21` … `cf-28`, `waits` | Milestone NI (ADR 0012): the catalogue cases of [CF-21](../docs/semantics/control-flow.md#cf-21) to [CF-28](../docs/semantics/control-flow.md#cf-28), with FX-24/d and /e in `cf-27`, recorded on the oracle, one fixture per finding; `waits` holds N2's waits and retries inside containers. Covered: a sequential loop's `skip_if` evaluated again mid-loop ([Q-CF-6](../docs/semantics/control-flow.md#q-cf-6)); the sleep before a container, with `delays`; a container's timeout per child and its scope; a container's retry, its failed attempt and the `iter_parent` artefact ([Q-CF-7](../docs/semantics/control-flow.md#q-cf-7)); the iterator errors of CF-23 with a failure module, and, added by milestone FU8 (`cf-23`'s `carry_*` runs), route 3 inside a `branchone`: the parent carries the branch's last carried value, to a later step, a failure module and a `retry_if`, also in a loop, while storage holds the `InternalError` (CF-23/d to f); job paths in bodies; `squash` on CE, where six squashed loops complete with their unsquashed results (CF-25/d); the virtual identity of a body; timed collapsed jobs and untimed child flows; a cancellation reaching held iterations, compared as a subset; retries, sleeps and timeouts in child flows. Runs predicted from the reference and corrected after recording carry a `note`. The reference's revision SC4 made cases of these recordings (CF-21/b–e, CF-22/b–e), which the runs cite; CF-22/f–g are recorded by N5's `flows/control-flow/subflow-retry`, and CF-21/f and CF-22/h are requested from batch E3f. The manifest is `internal/oracle/testdata/batches/ni.json`. |
| `flows/control-flow/subflow-retry` | Milestone N5 (ADR 0012): a retry on a flow step that runs a flow by path ([CF-17](../docs/semantics/control-flow.md#cf-17)), recorded on the oracle without an expectation first, so that the recordings decide. The runs: a subflow that fails once; one that runs out of retries, plain, under `continue_on_error` and with a failure module; a 2 s delay, with `delays`; the step in a sequential and in a parallel loop; above a child that retries its own step ([FX-27](../docs/semantics/execution.md#fx-27)); and a step whose input transform throws, whose subflow job still launches with nothing run ([FX-14](../docs/semantics/execution.md#fx-14)), and whose retry runs the subflow with an empty input. Each failed attempt's subflow job is at the step's `r:<k>` with its modules below it, and a failure module, or the step after a tolerated failure, lists the attempts again (FX-29). The fixture also records a retry on a while loop and on a branchone ([CF-22](../docs/semantics/control-flow.md#cf-22)). A script counts its attempts in a file named by the outer run, which every attempt shares. The reference's revision SC4 made catalogue cases of these recordings (CF-17/b–i, CF-22/f–g, FX-14/c and FX-29/c); N5 has no recording-batch manifest, so the manifests of those findings' batches (`n3c.json`, `ni.json`, `e3c.json`, `n2.json`) defer each to N5 with its run. |
| `flows/execution/fx-40`, `fx-40-hash` | Batch E3d: [FX-40](../docs/semantics/execution.md#fx-40), `cache_ttl` on steps, flows and collapsed loops, including the contested FX-40/d ([Q-FX-17](../docs/semantics/execution.md#q-fx-17), recorded as reading (b)); `fx-40-hash` pins the body's script by the hash its deployment assigns. Each cached call takes a per-run nonce (`WM_ROOT_JOB_ID`) and returns its own `WM_JOB_ID`, so a cache hit shows as a result that names another job's site. Gates N4, whose engine passes `fx-40`'s seven runs in `fresh` and `warm`; `fx-40-hash`'s runs stay unsupported, since a folder cannot supply the version a hash pins. |
| `flows/stub/retry_loop` | Stub mode: a retry inside a loop. The body step has two retries; run `ok` fails iteration 1 once and iteration 2 twice before they succeed, run `exhausted` fails iteration 1 on all three attempts, so the loop and the flow fail ([FX-26](../docs/semantics/execution.md#fx-26), [CF-5](../docs/semantics/control-flow.md#cf-5)). |
| `flows/stub/collapsed` | Stub mode: a collapsed body ([CF-8](../docs/semantics/control-flow.md#cf-8)), each iteration at `i:<n>/m:book`, then a path script. The body takes a `Date` through a local alias, which the stub keeps ([WR-V3](../docs/semantics/languages.md#wr-v3)). |
| `flows/stub/nested` | Stub mode: nested loops whose bodies do not collapse, the inner one reading the outer iteration as `iter_parent` ([CF-3](../docs/semantics/control-flow.md#cf-3)). |
| `flows/stub/scopes` | Stub mode: a named subflow and a parallel loop under one outer run (run `alone`), and two concurrent outer runs (`r` and `t`, group `rt`), with every call's `WM_ROOT_JOB_ID`, `WM_ROOT_FLOW_JOB_ID`, `WM_FLOW_JOB_ID` and `WM_JOB_ID` ([OR-V6](../docs/semantics/oracle.md#or-v6)/a, [Q-OR-10](../docs/semantics/oracle.md#q-or-10)). |
| `flows/stub/codebase` | Stub mode on codebase scripts ([SY-8](../docs/semantics/openflow.md#sy-8)): a retry inside a loop on a CommonJS codebase script, then an esm one ([WR-47](../docs/semantics/languages.md#wr-47), [WR-50](../docs/semantics/languages.md#wr-50)); the stubs stay codebase scripts, bundled with esbuild by the CLI and by windmill-lite (milestone E4b). |
| `flows/stub/subflow_retry` | Stub mode on a retried flow step (milestone N5): the subflow's second step fails on the first attempt, so the step's retry runs the subflow again. The table gives each subflow step one outcome per job, counted across the attempts, and each call resolves at the step's site without the attempt ([CF-17](../docs/semantics/control-flow.md#cf-17), [FX-27](../docs/semantics/execution.md#fx-27)). Run `skipped`: a subflow step skipped on the first attempt and run on the second, whose identity job takes no place in the count ([FX-17](../docs/semantics/execution.md#fx-17)). |
| `flows/stub/outside` | Stub mode on a codebase entry that reaches outside the sync tree ([SY-8](../docs/semantics/openflow.md#sy-8)): its `relative_path` is `../shared`, its asset comes from `../shared/lib/assets/child.mjs`, and the original script imports from `../shared/lib`; the stubbed copy carries `shared/` beside the stubbed tree, so the unchanged `wmill.yaml` reaches the same files ([WR-47](../docs/semantics/languages.md#wr-47), [WR-49](../docs/semantics/languages.md#wr-49); milestone E4c). |
| `flows/je/shortcuts` | The shortcut and read-route cases that name a predecessor: JE-8/a–b, JE-9/a–d and g–l, JE-12, JE-16/d–f, JE-17/b, JE-18/d, JE-44/a–b, JE-V6, and the key-order example of [0003](../docs/design/0003-semantics-placement.md) for JE-8 with its oracle discriminator (below). |
| `flows/je/sites` | Sequential expression sites: JE-22/c, JE-23/c, JE-25/a, JE-26/a, and the `later` JE-33, JE-49 and JE-V4 cases. |
| `flows/je/errors` | Failure-module transforms and error routes: JE-17/a, JE-26/b–c, JE-35/a–d. JE-26/b–c configure a `retry` and gate NI, JE-35/d's subflow by path gates N3c, and the others gate N1b. |
| `flows/je/waits` | `sleep`, `timeout` and `retry_if` sites and retried arguments: JE-22/a–b, JE-28, JE-29/a, JE-32, JE-V2 (JE-V2/a and /b are F1's: their steps write a variable through the job API). |
| `flows/je/loops` | For-loop cases: JE-9/e–f, JE-19/a and c, JE-26/d, JE-27 on loops, JE-30, JE-36/a–c and e, JE-37/a, JE-38/c, JE-39/a, JE-40, JE-V1. |
| `flows/je/branches` | `branchone`, `branchall` and while-loop cases: JE-24/a, JE-27 on `branchall`, JE-31, JE-36/d and f. |
| `flows/je/scopes` | Cross-scope cases: JE-19/b and d, JE-38/d. |
| `flows/je/providers` | Variable helpers, `flow_env` resolution and `$var:` resolution: JE-20, JE-38/a–b, JE-41, JE-42, JE-V3/a. |
| `flows/cf/cf-19` | `results` lookups across containers, CF-19/a–c, and the contested CF-V6/a–b of [Q-CF-16](../docs/semantics/control-flow.md#q-cf-16). |
| `flows/languages/bun-results` | Milestone E1: a Bun step's outcome in a sequential flow. Run `serialise`: [WR-13](../docs/semantics/languages.md#wr-13)/a's value as stored, what the next step sees of a key that held `undefined` ([WR-14](../docs/semantics/languages.md#wr-14)), and WR-13/b's `null` as the flow's result. Run `thrown_string`: [FX-47](../docs/semantics/execution.md#fx-47)/a. Run `timeout`: [WR-37](../docs/semantics/languages.md#wr-37)/a and [FX-24](../docs/semantics/execution.md#fx-24)/a. After each failure the next module is `not_run` ([OR-11](../docs/semantics/oracle.md#or-11)). |
| `flows/languages/codebase` | Milestone E1's codebase-script fixture: a flow of two path steps that are codebase scripts ([SY-8](../docs/semantics/openflow.md#sy-8)), one in an `esm` entry and one in an entry with no `format`, CommonJS ([WR-47](../docs/semantics/languages.md#wr-47), [WR-50](../docs/semantics/languages.md#wr-50)). The CLI bundles them with esbuild on the oracle, and `wltest check` with esbuild too, as milestone C2b builds codebase scripts (`bundle.BuildCodebase`); the CommonJS one runs on Bun's CommonJS loader (D13). The scripts are self-contained. |
| `flows/client-api/job-env` | Milestone E1: the contextual variables of inline and path steps ([WA-4](../docs/semantics/client-api.md#wa-4)/a, [FX-45](../docs/semantics/execution.md#fx-45)/a, [WR-22](../docs/semantics/languages.md#wr-22)), with each step's job ids normalised to placeholders ([OR-14](../docs/semantics/oracle.md#or-14)): its own job, and the root flow as the parent, the innermost flow scope and the effective root of a step directly in the flow ([WA-V1](../docs/semantics/client-api.md#wa-v1)). |
| `flows/client-api/identity` | Milestone PF1: the job identity of an inline step and a path script in a flow: the state path ([WA-5](../docs/semantics/client-api.md#wa-5)/a), the base URL variables ([WA-2](../docs/semantics/client-api.md#wa-2)/a), the token, and the root that a lookup with the step's own token answers by fetch at `BASE_URL` (the call `getRootJobId` makes, [WA-14](../docs/semantics/client-api.md#wa-14), WA-V1), which on windmill-lite the platform facade answers. |
| `flows/client-api/providers` | Milestone PF2: the platform services a step reads, one fixture on the oracle and on windmill-lite ([WA-40](../docs/semantics/client-api.md#wa-40)/b), from its `setup.yaml` (a resource type, variables with a secret, resources): arguments the worker resolved, a `$res:` with a nested `$res:` and a secret `$var:`, `$WM_ROOT_JOB_ID` read with the job's id (`""`, [WA-V3](../docs/semantics/client-api.md#wa-v3)/a) and beside a marker (the flow), and a plain argument ([WR-5](../docs/semantics/languages.md#wr-5)/a, /c); the reads the stock client makes, by fetch at `BASE_URL` with the step's token (WA-10/a, WA-11/a, WA-12, WA-42/b, /c, WA-V3/b); and the failures of a missing resource and an invalid path at argument resolution (WR-5/e, /b). A body that carries a Rust location on Windmill is returned as whether it starts as the reference says (D14). |
| `flows/client-api/user-state` | Milestone PF3: flow user state ([WA-14](../docs/semantics/client-api.md#wa-14)/a), by the calls the stock helpers make, by fetch at `BASE_URL` with the step's token: the root lookup, then the state routes at the root; a write's answer, the read, a missing key, the routes at a script job (404 and null); the iterations of a parallel loop sharing the root's map although each starts its own innermost scope ([WA-V1](../docs/semantics/client-api.md#wa-v1)); and a key read through the expression helper `flow_user_state()` ([JE-20](../docs/semantics/expressions.md#je-20)). Run `order` records the text a value comes back as: jsonb's key order, numbers as Windmill's JSON library writes them. The reference's revision SC4 states both runs' observations as [WA-14](../docs/semantics/client-api.md#wa-14)/e. |
| `flows/host/provided` | Milestone F2a's policy case, windmill-lite only: the run-time half of the provided-site policy case of [ADR 0002](../docs/design/0002-ir-and-loader.md). Each flow's step calls a semaphore subflow that suspends ([FX-39](../docs/semantics/execution.md#fx-39)), which the fixture's `provided` sites replace with the host's providers (`providers`): a provider's value that the next step reads through `results` ([FX-10](../docs/semantics/execution.md#fx-10)), a static value the site's own `stop_after_if` stops on ([FX-19](../docs/semantics/execution.md#fx-19)), a provider that declines, which latches the run (`provider_declined`), and a provider's error, which fails the flow as a failed job's would ([FX-12](../docs/semantics/execution.md#fx-12)). The oracle would run the real semaphore, so the runs hold expects written from the ADRs. |
| `flows/client-api/dispatch` | Milestone PF3, on two workers ([OR-5](../docs/semantics/oracle.md#or-5)): the runs a step dispatches, as `runScriptByPath`, `runFlow` and `runFlowAsync` request them, polled as `waitJob` polls. Run `wait`: a script's result, a failed script's error ([WA-15](../docs/semantics/client-api.md#wa-15)/a), a flow's result, and a UUID that names no job ([WA-V2](../docs/semantics/client-api.md#wa-v2)/a, /b). Run `identity`: the job identity of a dispatched script and flow, tracked in their parent or not, and the keys of a dispatched failure, which the reference left out until its revision SC4 stated them from this recording ([WA-V1](../docs/semantics/client-api.md#wa-v1)/b, and WA-15/a's key set). `fixture.yaml`'s `roots` lists the dispatched targets, which `wltest check` compiles as the default dispatcher's program. |
| `flows/client-api/flow-path` | Milestone PF3: the runnable paths a body step sees ([WA-6](../docs/semantics/client-api.md#wa-6)/a–d, with [WA-5](../docs/semantics/client-api.md#wa-5) and [FX-46](../docs/semantics/execution.md#fx-46)): a simple sequential body under the flow, a body made non-simple by a retry (NI, which runs retries inside containers), a parallel body, and a `branchone`'s numbering, on N3b's branches. The catalogue's `WM_FLOW_PATH` is in each run's claims. |
| `flows/execution/fx-36-http` | Batch E3e: [FX-36](../docs/semantics/execution.md#fx-36)'s HTTP part (`later`), every run submitted through run-and-wait (`submit: run_wait_result`), so its observation holds the HTTP response beside the walk: FX-36/a–g, an array result ([Q-FX-12](../docs/semantics/execution.md#q-fx-12), decoded positionally), a content type with and without a parameter, base64 transfer encoding to text and to bytes that are not UTF-8, a redirect status, FX-12/a over HTTP, FX-V3/a's skipped flow, and FX-37/a's early response. Gates F7b. |
| `flows/execution/fx-49` | Batch E3e: [FX-49](../docs/semantics/execution.md#fx-49) ([Q-FX-11](../docs/semantics/execution.md#q-fx-11), `later`): a flow-level `skip_expr` that the loop at module 0 flips from false to true, with a collapsed and a two-step body, and to a throw. `setup.yaml` resets the variable before every run (`oracle.setup: each_run`). |
| `flows/execution/fx-10-c` | Batch E3e: FX-10/c, `results[""]` inside a step's own `stop_after_if`, which the reference added after E3b. |
| `flows/languages/wr-16` | Batch E3e: [WR-16](../docs/semantics/languages.md#wr-16) ([Q-WR-15](../docs/semantics/languages.md#q-wr-15), `later`): result streaming, WR-16/a–g, each one inline step. |
| `flows/languages/wr-28` | Batch E3e: [WR-28](../docs/semantics/languages.md#wr-28) ([Q-WR-16](../docs/semantics/languages.md#q-wr-16)): static and dynamic imports of workspace scripts, WR-28/a–c and e, each a flow running the importing script by path; WR-28/d's script, whose only import is a dynamic extensionless path, cannot be deployed, so the batch defers it, and WR-28/f–g (source-derived) have no run. Created through the API in path order (`oracle: {deploy: api}`), so each imported script exists before the script that imports it. |
| `flows/languages/codebase-cases` | Batch E3e: codebase scripts in the shape of `flows/languages/codebase`, one `wmill.yaml` entry per case: CommonJS on Node and ESM on Bun ([WR-47](../docs/semantics/languages.md#wr-47)/a–b), esbuild's `define`, `inject`, `loader` and `banner` (WR-47/c–f), assets and a multi-file output ([WR-49](../docs/semantics/languages.md#wr-49)/a–b), the runtime `//native` and `//nodejs` select and the Node version ([WR-50](../docs/semantics/languages.md#wr-50)/b–e), and in both formats a relative import and the step's environment. The runs that report Node are deviation D13 on windmill-lite. |
| `flows/languages/codebase-outside` | A codebase entry that reaches outside the sync tree: its `relative_path` is `../shared`, its script imports from `../shared/lib` and its asset comes from `../shared/lib/assets/note.txt` ([WR-47](../docs/semantics/languages.md#wr-47), [WR-49](../docs/semantics/languages.md#wr-49)/a). `wltest record` pushes a copy that carries `shared/` beside the copied tree (milestone E4c); `wltest check` builds the original. |
| `flows/oracle/probes` | Batch E3e: the [probe catalogue](../docs/semantics/oracle.md#probe-catalogue) rebuilt as executable fixtures with every binding written out ([Q-OR-16](../docs/semantics/oracle.md#q-or-16), [OR-V9](../docs/semantics/oracle.md#or-v9)/a–d), and its OR cases (key order, loops, branches, retries, failure modules, stops, `skip_if`, `$var:`, effects, `delete_after_use`, a subflow's `delete_after_use`, `sleep`, response metadata asynchronously and through run-and-wait, the environment). |
| `flows/oracle/big-payload`, `nul`, `seeded`, `key-order` | Batch E3e: the probes `big_payload` ([OR-V1](../docs/semantics/oracle.md#or-v1)) and `nul` with the other NUL positions of [Q-OR-6](../docs/semantics/oracle.md#q-or-6) ([OR-27](../docs/semantics/oracle.md#or-27)); a variable seeded before the push, which removes it ([OR-31](../docs/semantics/oracle.md#or-31)/a, OR-12/d; `oracle.setup: before_push`); the key-order cases of [OR-24](../docs/semantics/oracle.md#or-24) that E3a left (OR-24/b, OR-24/c) and OR-24/f, an object a flow's preprocessor built. |
| `flows/oracle/workers2` | Batch E3e: the two-worker profile ([OR-5](../docs/semantics/oracle.md#or-5)/b): a step that waits for a script it starts, and parallel bodies whose index 0 is slow ([Q-OR-7](../docs/semantics/oracle.md#q-or-7), [Q-OR-15](../docs/semantics/oracle.md#q-or-15)). Recorded by the oracle workflow's two-worker session. |
| `flows/control-flow/pull` | Batch E3e: CF-6/b and SY-6/c, a flow created through the API as written and read back with the CLI's `sync pull` (`oracle: {deploy: api, pull: true}`); the pulled value is in each recording's provenance. |
| `flows/je/shortcut-texts` | Batch E3e: B2a's request 8, the text the JE-7 and JE-8 shortcuts answer for a value read from storage, quoted by a `timeout`'s parse error. Its unit cases (FU5) restate them: a stored `flow_input` member in jsonb's spaced text, and the compact array after a loop. |
| `flows/je/transform-order`, `transform-order-api` | Batch E3e: the evaluation order of a step's input transforms ([JE-23](../docs/semantics/expressions.md#je-23)), with the transforms in the CLI's name order and, created through the API, as written. |

`internal/oracle/testdata/batches/<batch>.json` names each batch's
findings, fixtures and the cases it defers, and `TestRecordingBatches`
holds every run of those fixtures to its recording and its recording to its
`expect`.

The runs of batch E3a say what the catalogue expects of them in `claims`,
`delays` and `unit` (see [Claims and unit cases](#claims-and-unit-cases))
and leave the whole observation to the recording, because most expectations
are partial: an error message's prefix, one argument. The fixtures of
recording batch E3a are `oracle-observed`; where a recording differs from
the catalogue's source-derived expectation, the run's comment says so.

The sync tree holds what `wmill folder add-missing` and `generate-metadata`
write, generated against the oracle and committed as written
([OR-8](../docs/semantics/oracle.md#or-8)); the oracle workflow regenerates
it on every recording and fails on any change. `generate-metadata` writes a
container's defaults into `flow.yaml` (`skip_failures` and `parallel` on a
loop, `parallel` on a `branchall`, and `skip_failure`, `parallel` and `expr`
on each branch), so a field left out of such a tree never reaches the
oracle absent. A fixture with `oracle: {deploy: api}` (`cf-6`, `cf-15`,
`fx-40-hash`, `pull`, `wr-28`, `transform-order-api`) is instead created through the REST API exactly as written
and never regenerated, so its tree holds no generated metadata; each of its
recordings' provenance holds the target flow's submitted value, the value
the API answers after the dependency job, and what `generate-metadata`
writes for the same source ([internal/oracle](../internal/oracle/README.md#api-deployment)).
Either way the recordings show deployed flows, the reference configuration
of [ADR 0001](../docs/design/0001-equivalence-contract.md) §1.

A stub fixture (`stub: true`) commits its original tree; each run's
`replay` table gives the stubs' outcomes. The scripts call hosts under
`.invalid`, which nothing resolves, so a script that kept its body would
fail. The stubbed tree is written and its metadata regenerated in a
temporary copy on every recording ([internal/oracle](../internal/oracle/README.md#stub-mode)).
`wltest check` runs the same stubbed copy on windmill-lite, with the same
replay service (milestone E4b, [cmd/wltest](../cmd/wltest/README.md#flow-fixtures)).

Each E3b run's `expect` was written from the reference before the oracle
first ran it. Where the oracle disagreed, the run's `note` says what was
predicted, what was recorded, and in which oracle runs. The engine
milestones added the `carried` value of a module that an error stop or a
throwing `stop_after_if` fails (N1a, N1b, and N3b in FX-20's error-stop
run), which the oracle exposes no recording of: the recorder compares an
`expect` without it.

### Recording batches

The E3b rows above are recording batch E3b of
[0012](../docs/design/0012-layout-and-milestones.md), the flow-execution
core. Its manifest, with each run's gate, is
`internal/oracle/testdata/batches/e3b.json`, described in
[internal/oracle](../internal/oracle/README.md#recording-batches).

## Expression cases

An expression case ([0003](../docs/design/0003-semantics-placement.md)) is
context-free: an input transform or the `skip_if` predicate of a flow's
first step, whose only bound values are `flow_input` and `flow_env`. At
module 0, `previous_result` is the flow input
([JE-36](../docs/semantics/expressions.md#je-36)) and the previous id is
`""` ([JE-8](../docs/semantics/expressions.md#je-8)), so a case may use them
too. Each `exprs/<finding>.yaml` holds the cases of one finding, and is
named after it in lower case (`je-7.yaml`), as its recordings are:

```yaml
finding: JE-7
scope: core
flow_input: '{"n":12345678901234567890,"a.b":"lit","a":{"b":"nested"}}'
cases:
  - spec: JE-7/a
    provenance: oracle-observed
    title: a top-level key keeps full precision
    expr: flow_input.n
    expect: {value: '12345678901234567890'}
```

File fields: `finding`, `scope`, the defaults `flow_input` and `flow_env`
(JSON object texts), `slow` (the recorder records slow files last), `mask`
(JSON Pointers into every case's observation whose values are replaced by
`<masked>`, for text with times or host names), `note`, `cases`. Case
fields:

| Field | Meaning |
|---|---|
| `spec`, `variant` | the catalogue case, or the finding with a `variant` for a case beyond the catalogue |
| `title`, `note` | what the case shows (required), and why it reads as it does |
| `provenance`, `scope` | as for the other levels; a recorded case is `oracle-observed` |
| `site` | `transform` (the default, a `javascript` input transform `x`), `static` (a `static` transform) or `skip_if` |
| `expr`, `static` | the expression text, or the static value's JSON text |
| `flow_input`, `flow_env` | the run's arguments and the flow's `flow_env`, as JSON object texts |
| `expect.value` | argument `x` as the job API records it (point O2 of the [notation](../docs/semantics/conformance.md#je-notation): jsonb, before `$var:` resolution), as JSON text; `key_order: strict` compares member order |
| `expect.text` | the evaluator's exact output (point O1), which the oracle cannot observe; its stored form must be `expect.value` |
| `expect.error` | the step's error object, when the expression fails the step: `name`, and `message` exactly or `prefix` with `contains` and `not_contains` |
| `expect.flow_error` | the run's error object, when a predicate fails the flow at its first pickup ([JE-35](../docs/semantics/expressions.md#je-35) route (b)) |
| `expect.skipped` | whether `skip_if` skipped the step |
| `expect.status` | the run's status where it is not the one the expectation implies (`success` for a value or a skip, `failure` for an error), for example `canceled` |

A `skip_if` case expects `skipped` or `flow_error`; a transform or static
case expects `value` or `error`, or `flow_error` with `status: canceled`
when the oracle cancels the run before the step exists (JE-2/a).

A case that names a predecessor, a handler or a container (a key such as
`previous_result`, `results` or `failure_module`, or another site) is
refused with `contextual_case_needs_flow_fixture`: it is a flow fixture.

**Lifting.** `wltest record` lifts each file into a generated fixture
([0011](../docs/design/0011-differential-testing.md)): one flow
`f/wl_expr/<case>` per case, whose one module, `b`, runs the sink script
`f/wl_expr/sink` with the expression as its input `x` (or as its `skip_if`,
with `x` static `null`), and one run with the case's flow input. The sink
returns `null`, so the observation shows what the expression produced, not
what Bun made of it. The generated tree is never committed; the recorder
writes its metadata with the CLI before each push. The recordings are
`exprs/recorded/<finding>/<case>.json`, with the finding in lower case.

`exprs/deferred.yaml` lists the JE, CF-19 and CF-V6 catalogue cases that
no expression case here and no flow fixture encodes, and those recorded
only in part, each with the reason and where it is checked.
The cases the semantics reference added on 2026-10-28 are checked at
evaluator level in the meantime, by the pipeline cases of
[internal/expr](../internal/expr/README.md) that run on the engine; those
it added on 2026-10-29 that no recording holds are checked there too
(JE-11/b–e, JE-16/h), or at engine level in
[internal/engine](../internal/engine/README.md) (JE-18/e, and JE-11/e as an
input transform's failure), since milestone FU6; JE-44/f is not encoded
yet, nor are revision SC3's JE-15/k, JE-20/c–d, JE-42/i–j and JE-49/d–e,
whose recordings
[docs/design/README.md](../docs/design/README.md#recording-requests-for-batch-e3f)
requests from batch E3f; JE-19/f, like JE-19/e, needs a restarted run and
is outside the contract.

## Claims and unit cases

Runs of flow fixtures, and the runs lifted from expression cases, carry
three kinds of assertion beyond `expect`, and two comparison hints. `go test ./internal/oracle` checks
them against the committed recordings; the recorder does not, so a
contradicted claim still leaves its recording to inspect. `wltest check`
checks the claims on windmill-lite's own observation too, so a claim
states only what both sides write: a Rust location suffix, which
windmill-lite never writes (D14), stays in the recording (JE-35/b).

- **`claims`**: a JSON Pointer `at` into the observation, in its canonical
  encoding (a step field left to its canonical absence is absent), and one
  of `equals` (a JSON value, with `key_order: strict` if member order
  counts), a string match (`prefix`, `contains`, `not_contains`), `length`
  (of an array) or `absent`.
- **`delays`**: `from` and `to` sites and `at_least_ms`, a lower bound on the
  time from one job's completion to the next one's start, read from the
  recording's provenance, since timings are never compared
  ([JE-28](../docs/semantics/expressions.md#je-28)).
- **`unit`**: the run's expressions restated at unit level for the
  expression milestones: the `site` and `input` (an input transform's name,
  or `skip_if`, `stop_after_if`, `timeout`, `sleep`), the `expr`, the
  `context` the evaluation sees (`previous_id`, `previous_result` as the
  worker holds it, which after an ordinary step is the producer's text and
  elsewhere follows [JE-44](../docs/semantics/expressions.md#je-44)'s
  table, `flow_input` as submitted, and `results`, the stored texts a
  database read returns), and the expected `text` or `error`. A
  test ties each one to its flow and recording: the expression is the one
  at the site, the previous id is the module before it, the stored forms of
  the context's texts are the recorded results, at an input transform
  the stored form of the expected text is the recorded argument, and at a
  `timeout` site the expected text is exactly the value the recorded i32
  parse error quotes ([JE-29](../docs/semantics/expressions.md#je-29)),
  spacing and key order included.
- **`not_found_locs`**: JSON Pointers to the values whose error message
  quotes the lookup helper's not-found text with a parenthesised location
  ` (<file>.rs:<line>)` that Windmill generated: where the case's expected
  text writes `<loc>` for it ([FX-50](../docs/semantics/execution.md#fx-50)
  rule 3; `cf/cf-19` run `cf19-c`, milestone N3c). Only `wltest check`
  reads it, to strip that location from the reference where windmill-lite
  generated the value; the recording keeps it.
- **`copies`**: the values the case's flow copies, through user code,
  from an error windmill-lite generates at another site, each `at` (a
  JSON Pointer into the observation) and `of` (that site's path): a
  failure module or a later step that passes a pickup error on
  (`execution/fx-15` run `root_pickup`, `je/errors` run `je35-d`,
  milestone N3c). Only `wltest check` reads it: the value at `at` counts
  as generated when it equals that site's generated error, its error
  member or its message, so the location rule of
  [0011](../docs/design/0011-differential-testing.md) reaches it.

**The JE-8 discriminator.** The key-order example of
[0003](../docs/design/0003-semantics-placement.md) cannot be told apart in a
recorded argument, because the job API stores arguments as jsonb, which
reorders keys (review item cx R16). `flows/je/shortcuts` keeps it as a unit
case (`je8-order-transform`: the evaluator returns the producer text
`{"b":1,"a":2}`, whose stored form is the recorded `{"a":2,"b":1}`), and
observes it on the oracle at the `timeout` site, whose parse error quotes the
evaluated text (`je8-order-timeout`, with the database route as its control
in `je8-order-timeout-db`).
## OpenFlow decode cases

A decode case gives a flow `value` as JSON and the decision of Windmill's
effective decoder ([OF-2](../docs/semantics/openflow.md#of-2)): a rejection,
or an accepted value and parts of its decoded form. The decoded form is
described in [internal/openflow](../internal/openflow/README.md).

Each `of-<n>.json` file holds the cases of one finding:

```json
{
  "finding": "OF-10",
  "cases": [
    {
      "spec": "OF-10/a",
      "provenance": "source-derived",
      "title": "static without value",
      "given": {"modules": [{"id": "a", "value": {"type": "script", "path": "f/demo/s", "input_transforms": {"x": {"type": "static"}}}}]},
      "expect": {"accept": {"at": {"/modules/0/value/input_transforms/x": {"type": "static", "value": null}}}}
    }
  ]
}
```

Case fields:

| Field | Meaning |
|---|---|
| `spec` | the catalogue case id |
| `provenance` | the catalogue's provenance tag, which the runner checks |
| `variant` | distinguishes several inputs of one catalogue case |
| `scope` | set only where it differs from the finding's (also allowed at file level) |
| `part` | `decode` when the catalogue case has a run-time or deploy half that is checked elsewhere; `note` says which |
| `deviation` | the named deviation of [0001](../docs/design/0001-equivalence-contract.md) whose behaviour the expectation shows |
| `given` | the flow value, as JSON |
| `expect.reject` | `kind` (`flow_value`, `module`, `retry_base`, `step_path`, …), `contains` and `not_contains` (message substrings, compared as the reference compares them), `not_prefix` |
| `expect.accept` | `at` (JSON Pointer to expected JSON in the decoded form), `matches` (JSON Pointer to a regular expression for a string), `absent` (JSON Pointers that must not resolve) |

`deferred.json` lists the catalogue's OF cases that are not decode cases,
each with the reason and where it is expected to be checked. A test fails
unless every OF case of the catalogue is a decode case here or is deferred.

## Stored-form cases

A stored-form case gives a value's *producer text*, as a step, QuickJS-ng or
a static transform writes it, and expects its *stored text*, the text
PostgreSQL `jsonb` gives back without its output spaces, or the rejection
that leaves it with no stored form. Both texts are described in
[internal/jsonval](../internal/jsonval/README.md). The catalogue cases are
flow cases; a stored-form case checks only the texts, and names where the
rest of the case is checked.

Each `<finding>.yaml` file holds the cases of one finding. Texts are YAML
single-quoted strings, in which a backslash is literal and only a quote is
doubled, so the JSON text stays byte for byte as written:

```yaml
finding: JE-44
cases:
  - spec: JE-44/b
    provenance: oracle-observed
    title: an older step is read from the database (jsonb order)
    read: stored
    given: '{"bbb":1,"a":2,"cc":3}'
    expect:
      keys: [a, cc, bbb]
      stored: '{"a":2,"cc":3,"bbb":1}'
    rest_in: N1a, which serves results.a past a step in between from the stored text (JE-18)
```

Case fields:

| Field | Meaning |
|---|---|
| `spec` | the catalogue case id, or the finding id for a case that encodes the finding's rule beyond its catalogue cases |
| `variant` | names a case that has only a finding id |
| `provenance` | for a catalogue case, the catalogue's tag, which the runner checks; for a rule case, the finding's, or `policy` where the case pins bytes the rule leaves open or PostgreSQL behaviour the reference does not state |
| `scope` | the catalogue's scope tag, where it gives one |
| `read` | `producer` or `stored`: the text the catalogue case's consumer reads, by the routing table of [0005](../docs/design/0005-json-step-boundaries.md) |
| `given` | the producer text |
| `expect.stored` | the exact stored text |
| `expect.keys` | the top-level keys of the `read` text, in order, as the catalogue expects them |
| `expect.reject` | `kind` (`nul`, `surrogate`, `numeric`, `syntax`) and optionally `database`, PostgreSQL's message |
| `rest_in` | for a catalogue case, the milestone that checks the rest of it |
| `note` | why the case reads as it does |

`deferred.yaml` lists the catalogue cases of these findings that are not
stored-form cases, each with the reason and where it is expected to be
checked. A test fails unless every catalogue case of a finding with a file
here is a case or is deferred.

## Sync-folder load cases

A load case gives a sync folder as a map of files and what the loader of
[internal/syncfolder](../internal/syncfolder/README.md) makes of it: whether
the folder opens, which items it lists or filters out, and for named items
parts of the record read or the error
([SY-1](../docs/semantics/openflow.md#sy-1) to
[SY-19](../docs/semantics/openflow.md#sy-19),
[SY-V1](../docs/semantics/openflow.md#sy-v1) to
[SY-V4](../docs/semantics/openflow.md#sy-v4), PA-1, PA-2).

Each `sy-<n>.json` or `pa-<n>.json` file holds the cases of one finding:

```json
{
  "finding": "SY-5",
  "cases": [
    {
      "spec": "SY-5/b",
      "provenance": "source-derived",
      "title": "legacy array lock",
      "given": {"files": {"wmill.yaml": "defaultTs: bun\n", "f/a/x.ts": "...", "f/a/x.script.yaml": "lock: [a, b]\n"}},
      "expect": {"items": {"script": {"f/a/x": {"at": {"/lock": "a\nb"}}}}}
    }
  ]
}
```

Case fields beyond those of the decode cases:

| Field | Meaning |
|---|---|
| `spec` | the catalogue case id, or the finding id alone for a case that extends the catalogue |
| `policy` | the windmill-lite policy of [0001](../docs/design/0001-equivalence-contract.md) or [0002](../docs/design/0002-ir-and-loader.md) whose behaviour the expectation shows, where it differs from the CLI's |
| `given.files`, `given.hex_files` | the folder: file paths and their text, or their bytes in hex for text that is not UTF-8 |
| `given.start` | the working directory the folder is opened from, relative to the case; default the case root |
| `given.step` | a flow path and module id whose by-path target `expect.target` describes |
| `expect.load` | the error `kind` and message substrings (`contains`) when the folder does not open |
| `expect.root` | the sync root, relative to the case |
| `expect.paths`, `expect.filtered` | per item kind, the exact sorted paths listed, or filtered out |
| `expect.items` | per item kind and path: `at` (JSON Pointer into the item's JSON form) and `absent`, or `error` (`kind`, `contains`) |
| `expect.target` | the same, for `given.step`'s target |
| `expect.warnings`, `expect.not_warnings` | substrings that some warning holds, or that no warning holds |
| `expect.skipped` | files listed as recognised and not loaded, with their kind |
| `expect.check_ok`, `expect.check_error` | `Check` succeeds, or fails with an error of that kind |

The runner writes each case to a temporary directory, with a `.git`
directory at its root so that the upward search for `wmill.yaml` stays in the
case; a case without `wmill.yaml` thus has its start directory as the root
([SY-13](../docs/semantics/openflow.md#sy-13)). `deferred.json` lists the
catalogue's SY and PA cases that are not load cases here, each with the
reason and where it is expected to be checked; the SY-9 cases are the YAML dialect's, in
[internal/yamlx](../internal/yamlx/README.md).

## Compile cases

A compile case gives a sync folder, as a load case does, and optionally a
host contract and build options, and what the compiler of
[internal/ir](../internal/ir/README.md) makes of it: the errors, or parts of
the compiled program ([0002](../docs/design/0002-ir-and-loader.md)). Most
catalogue cases they encode are flow cases whose expectation depends on
what a deploy applies (container defaults, the native switch, virtual
steps, collapse); a compile case checks that part (`part: compile`), and
its `note` says which milestone checks the run.

```json
{
  "finding": "CF-8",
  "cases": [
    {
      "spec": "CF-8/c",
      "provenance": "oracle-observed",
      "part": "compile",
      "note": "The catalogue records the case as oracle-observed and CF-8 as settled. The run half, every iteration getting the flow input, is N3a's.",
      "title": "a loop at step 0 collapses",
      "given": {"files": {"wmill.yaml": "defaultTs: bun\n", "f/x/main.flow/flow.yaml": "..."}},
      "expect": {"steps": {"f/x/main m:l": {"at": {"/forloop/collapse": true}}}}
    }
  ]
}
```

Case fields beyond those of the load cases:

| Field | Meaning |
|---|---|
| `provenance` | as the catalogue tags the case, `disputed` included: a disputed case's `note` says which reading it encodes and why the design fixes it ([0001](../docs/design/0001-equivalence-contract.md) §6) |
| `part` | `compile` when the catalogue case's run half is checked elsewhere |
| `given.contract` | the host contract, as the YAML text of its file; without one every flow and script is a root |
| `given.options` | build options (`{"strict": true}`) |
| `expect.errors` | errors that must all be among those the compile reports: `kind` and message substrings (`contains`) |
| `expect.at`, `expect.absent` | JSON Pointers into the program's JSON form |
| `expect.flows`, `expect.no_flows` | per flow path, pointers into that flow; flows that must not be in the program (opaque or unreached) |
| `expect.steps` | per `"<flow path> <site>"`, pointers into that module, and `script` for its inventory entry; the site is a site path with iteration segments left out (`m:l/m:x`, `m:o/b:default/m:c`, `s:failure`) |
| `expect.lists` | per `"<flow path>"` or `"<flow path> <site>"` (a loop, or a branch), the site segments of that module list in order, occurrences and virtual steps included |
| `expect.scripts` | per inventory id, pointers into that entry |

`deferred.json` lists the catalogue cases of these findings that are
neither compile cases nor accounted for at the decode or load level (a case
there, or an entry in its `deferred.json`), each with the reason and where
it is expected to be checked. A test fails unless every catalogue case of a
finding with a file here is accounted for, and unless every case that
`openflow/deferred.json` or `syncfolder/deferred.json` defers to
`conformance/ir` is a compile case.

## Protocol cases

The harness protocol ([0004](../docs/design/0004-harness-protocol.md)) is
windmill-lite's own, so these cases carry provenance `policy`. They pin the
Go side ([internal/harness](../internal/harness/README.md)) and each harness
([harnesses/bun](../harnesses/bun/README.md)) to one set of frames. Each
file names the design section it encodes (`design`) and the findings it
relies on (`relies_on`); a case that also encodes a catalogue case says so
in `spec` (the version check, `WR-3/a`).

**`frames.json`** holds the codec's golden frames. `go test ./internal/harness
-run TestFrameCases -args -update` writes `payload` and `frames` from Go's
encoder; both codecs then check every case unless `sides` limits it.

| Kind | Fields | Check |
|---|---|---|
| encode | `message` (JSON) or `message_text` (a payload in any key order), `payload` | the message decodes and encodes to `payload`, which re-encodes to itself; `raw` names values whose bytes must survive exactly |
| split | an encode case plus `split` (`stream`, `chunk_bytes`) and `frames` | the payload splits into exactly those part frames, which reassemble to it |
| sequence | `frames`, `messages` | the frames, read in order, decode and reassemble to the messages |
| reject | `bytes_hex` or `frames`, optional `max_frame` and `max_message`, `error` | reading raises the protocol error code `error` |

**`exchanges/<case>.json`** are transcripts that the Go tests play against
the real harness, on the synthetic bundle in `protocol/bundle/` (a
`manifest.json` whose SHA-256 is the bundle digest, and per script under
`scripts/bun/` a module written by hand, which takes named parameters, and
the `.meta.json` signature the build helper would record for it, by which
the harness binds its arguments). Each step does one thing:

| Step | Meaning |
|---|---|
| `send` | encode a message canonically and send it, as part frames when it is larger than a frame; `fill` stands in for a long string, `split` sends it as small parts with one whole frame between them |
| `send_text` | send one frame with this exact payload |
| `expect` | the next message from the harness matches, keys in order; `parts` is how many part frames carried it; messages whose type `ignore` lists are dropped while it waits (`heartbeat`: a ping may still come before the harness handles the drain) |
| `quiet_ms` | no message arrives for that long |
| `exit` | the process exits with this code and sends nothing more |
| `stdout_contains` | the process's stdout contains the text |
| `release` | create the file of this name in the harness's working directory, which ends a `gate` step waiting for it |

Expected values may use placeholders: `<any>`, `<string>`, `<int>`, `<pid>`
(the harness's pid), `<bundle>` (the fixture bundle's digest) and `<c*N>`
(a string of `N` copies of `c`).

A transcript never relies on a step outlasting the frames that follow it.
A call that must still run when a later frame arrives is a `gate` step
(`scripts/bun/gate.mjs`), which runs until a `release` step creates its
file; its paths are relative to the harness's working directory.
`cancel-late` and `cancel-error-late` cancel such a call, check the pong,
and only then release it; `concurrent` releases its calls in the order in
which it expects their results.

What a step writes through `console`, `process.stdout` or `process.stderr`
arrives as `log` frames ahead of the frame that reports its outcome
(milestone C3b), so a transcript expects them, with `<any>` for the text of
a stack. `left-behind` and `platform-left-behind` expect the `left_behind`
warning ahead of the result; `welcome-fresh`, `fresh-one-invocation` and
`fresh-exit` run a harness in `fresh` mode, which serves one invocation and
exits with it.

**`host/workdir.json`** (milestone F3a) holds host mode's working-directory
cases ([ADR 0004](../docs/design/0004-harness-protocol.md), the
shared-process failure model, item 5, and host mode item 7), which both
sides check: the host SDK before it starts the engine (`checkWorkdir` in
`harnesses/bun/host`, run by `bun test`), and the engine in the handshake
(`harness.CheckWorkdir`, through `AttachHost` too, run by
`go test ./internal/harness`). Each case lays out a fresh directory with
`cwd/` (the host's working directory), `other/`, `link` (to `cwd/`) and
`file`, sets the modes it names (skipped as root), and passes `workdir`;
`expect` is the refusal's code (`workdir_not_writable`,
`workdir_mismatch`) or null.

## Step cases

A step file holds the step cases of one finding, or of a deviation of
[ADR 0001](../docs/design/0001-equivalence-contract.md) §5 that has none
(`d5.yaml`, `d9.yaml`, `d11.yaml`, `d12.yaml`, `d13.yaml`), and is named after it in
lower case. Each case is one script and its runs, in order: in `warm`
mode one harness process serves them all, so a later run sees what an
earlier one left in memory (D1); in `fresh` mode each run has a process of
its own. A native step runs in a realm of its own each time, in either
(D5). A
run's observation is the step's **stored** value,
`{"status": "success" | "failure", "result": …}`: the result, or the
failure value ([WR-21](../docs/semantics/languages.md#wr-21)).

```yaml
finding: WR-12
scope: core
cases:
  - spec: WR-12/b
    provenance: source-derived
    title: an arrow preprocessor receives the non-underscore values, integer-like keys ascending
    step_id: preprocessor
    script: |
      export const preprocessor = (x, y) => ({ x, y });
    runs:
      - args: '{"2":"two","1":"one","_ENTRYPOINT_OVERRIDE":"preprocessor"}'
        expect: {status: success, result: {x: one, y: two}}
```

| Field | Meaning |
|---|---|
| `spec`, `variant`, `provenance`, `scope`, `title`, `note` | as for the other levels; a case of a finding beyond the catalogue, or of a deviation, cites the finding and needs a `variant` |
| `script`, `path` | the script, and its Windmill path (default `f/wl_step/<name in snake case>`), which is its import base |
| `language` | `bun` (the default), `bunnative` or `nativets`; a deploy relabels `bun` and `bunnative` from the script's `//native` header, and `bunnative` and `nativets` run with the native boundary ([WR-40](../docs/semantics/languages.md#wr-40)). The catalogue's "both kinds" are two cases: `language: nativets`, and a script whose first line is `//native` |
| `timeout` | the step's static timeout in seconds: when it passes, the run is abandoned and its observation is the engine's timeout error for the step's boundary ([WR-37](../docs/semantics/languages.md#wr-37), [WR-44](../docs/semantics/languages.md#wr-44)) |
| `step_id` | the module id it runs as, which `WM_FLOW_STEP_ID` carries (default `a`); `""` runs it as a script job outside any flow, whose errors carry no `step_id` |
| `env` | job variables that add to or replace those of the job identity ([internal/platform](../internal/platform/README.md)) |
| `host_modules` | scripts registered as host-provided modules (D12): `path` and `script`, their key the file path `<path>.ts` |
| `codebase` | makes the script a codebase script, bundled by esbuild (C2b): the settings of its `codebases` entry as `wmill.yaml` gives them (`format`, `define`, `inject`, `loader`, `banner`, `external`, `assets`) |
| `files` | other files of the folder, by path from the sync root, that a codebase build reads: modules it imports, inject files, asset sources |
| `setup` | the variables and resources the case's platform facade serves, in `setup.yaml`'s format; a secret's `value` is its plaintext (milestone PF2: `wa-10.yaml`, `wa-11.yaml`, `wa-12.yaml`, `wa-42.yaml`, `wa-v3.yaml`) |
| `compare` | `stack` and `mask` pointers, beyond `/result/error/stack`, which is masked on a side whose status is `failure` (a successful result is data and is compared) unless `keep_stack` is set, for a case that states the stack in claims; `unordered` arrays; `strict_key_order` |
| `deviation` | the deviation whose two readings the case's `by_mode` gives, or `5a` for the restriction of ADR 0001 §5a that a warm process reaches the platform only through `fetch` (`wa-31.yaml`) |
| `runs[].args` | the argument object's JSON text, as the script receives it; `_ENTRYPOINT_OVERRIDE` in it selects the export (WR-12) |
| `runs[].expect`, `claims` | the whole observation, and partial assertions (an error's `name`, a message's prefix) |
| `runs[].late`, `settled` | what the invocation reports after its outcome (`kind`, `final`, `body`), up to the end of the case, so a late report of work it left running after it settled counts too, compared when either is given; and how it settles: `completed`, `not_started`, `process_exited`, or `pending` for a call that must still run at the end of a 500 ms window |
| `runs[].warnings` | the codes of every warning the invocation gets up to its settlement, which the runner waits for (D9): `unattributed_error`; `left_behind`, which reports what it still had when it settled; and `native_untracked`, which reports what the native boundary handed it but does not track (D5); none when absent |
| `runs[].left` | the counts of the `left_behind` warning by kind (`timeout`, `interval`, `immediate`, `fetch`, `child`), compared when given |
| `runs[].untracked` | the counts of the `native_untracked` warning by kind (a class name, or `function <name>`), compared when given |
| `runs[].calls` | the platform calls the facade recorded against the invocation, in order, each `<status> <METHOD> <route template>` (`501 POST /w/{ws}/jobs/run/p/{path}`), or the path as sent where no route matched; `[]` for none; compared when given. A call refused by the kernel, or with a token the facade does not know, is never listed (milestone PF1) |
| `runs[].by_mode` | `warm` and `fresh`, each a check of the fields above, instead of the run's own |
| `runs[].node` | for a CommonJS codebase case: what the same bundle shows on Node (`expect`, `claims`) where it differs from the run's own check (D13); without it, the run's own `expect` and `claims` must hold on Node too |

**D13.** `wltest check --node` (or `WL_NODE`) runs each run of a CommonJS
codebase case on Node as well, as Windmill runs such a bundle
([WR-30](../docs/semantics/languages.md#wr-30),
[WR-50](../docs/semantics/languages.md#wr-50)): in a job directory of its
own laid out in Windmill's order (a placeholder `package.json` of `{}`,
then the bundle as `main.js` and the entry's assets, so that an asset named
`package.json` replaces the placeholder), with the harness's own wrapper
rules, so that the two sides differ only in the runtime and its module
loader. `d13.yaml` holds the differential: where Node and the Bun harness
agree, and where they differ. `wr-50.yaml` holds the one case whose
`package.json` asset declares `"type": "module"`, in a bundle of its own:
the harness extracts every asset of a step file's bundle into its working
directory (D10), and such a file there also makes Bun import the default
export of every CommonJS module beside it as Node does.

**The arguments file.** `wr-5.yaml` (milestone PF2) checks that a script
reads its own arguments from `args.json` in its working directory, which
the harness writes before each invocation
([WR-5](../docs/semantics/languages.md#wr-5)): run after run of one warm
process, whose directory is every invocation's (D10), and in each fresh
process; and that the file holds no `_MODULES` or `_TEMP_SCRIPT_REFS`
while every other member keeps its text and place, as the script's
parameters see it too. WR-5's catalogue cases need the engine's argument resolution and
are checked at the flow level.

**The services of milestone PF3.** `wa-14.yaml` checks flow user state
([WA-14](../docs/semantics/client-api.md#wa-14)/a): a flow step's case
runs in its case's flow run, which the job registry knows while the case
runs, so a later run reads what an earlier one wrote, and a script job's
case (`step_id: ""`) is its own root, with no flow status. `wa-16.yaml`
checks the object store's job helpers for explicit keys
([WA-16](../docs/semantics/client-api.md#wa-16)/a, /c), each case with a
store of its own, and the 501 of an upload that asks the server for a
key. `wa-15.yaml` holds the dispatch-target restriction of
[ADR 0001](../docs/design/0001-equivalence-contract.md) §5a: a step case's
bundle has no root to dispatch, so every dispatch is refused with
`dispatch target not in bundle: <path>`, which fails the step as
`UnsupportedPlatformCall`, also when the step waits on it as
`runScriptByPath` does. `wa-v2.yaml` checks the result poll of a UUID that
names no job ([WA-V2](../docs/semantics/client-api.md#wa-v2)/a, /b).

`steps/bun/deferred.yaml` lists the catalogue cases of these findings that
are not step cases, with the reason and where each is checked. The step
files cover the run halves of the build cases whose `rest_in` names C3a
(binding, hydration, the missing export), and
[ADR 0004](../docs/design/0004-harness-protocol.md)'s alternate-entrypoint
and arrow-export cases in `wr-12.yaml`.

## Build cases

A build case gives a sync folder and one or more Bun scripts, builds them
with [internal/bundle](../internal/bundle/README.md) and the
[Bun build helper](../harnesses/bun/build/README.md), and checks what the
build records for each script (the signature of each export, its exports,
annotations, bundler target, workflow-as-code markers, the workspace
scripts it inlined, the host-provided modules it uses) or the problem it
reports. A case can also run the built modules on the pinned Bun and check
what their exports return, which is how the import transforms
([WR-26](../docs/semantics/languages.md#wr-26),
[WR-28](../docs/semantics/languages.md#wr-28),
[WR-29](../docs/semantics/languages.md#wr-29)), the env rewrite and the
host-provided-module shims are checked against real behaviour. Most cases
check the build half of a catalogue case whose run belongs to the step
suite; `part` says so and `rest_in` names where the rest is checked.

Each `<finding>.json` holds the cases of one finding, or `d12.json` those
of deviation D12, which has no finding:

```json
{
  "finding": "WR-8",
  "cases": [
    {
      "spec": "WR-8/a",
      "provenance": "source-derived",
      "title": "a local alias to Date",
      "part": "build",
      "rest_in": "C3a hydrates the argument",
      "given": {"source": "type Ts = Date; export function main(d: Ts) { return d instanceof Date; }"},
      "expect": {"signatures": {"main": {"params": [{"name": "d", "date": true}]}}}
    }
  ]
}
```

Case fields beyond those of the decode cases:

| Field | Meaning |
|---|---|
| `given.source`, `given.path` | the script, and its Windmill path (default `f/case/main`), written as `<path>.ts` |
| `given.inline`, `given.flow` | the script is inline; `flow` gives `path`, `module` and `explicit_path`, from which the import base is computed (WR-29); `explicit_path` is the module's `path` where a deployed flow keeps it, in the failure and preprocessor slots |
| `given.entries` | more scripts, each with `id` and the fields above |
| `given.files` | other folder files; every `.ts` file is a script at its path without the extension |
| `given.dependencies`, `given.packages` | a script's npm dependencies, and the synthetic packages (`name@version` and their files) that a registry the test serves holds; the runner makes the lock |
| `given.host_modules` | host-provided modules: `id`, `key`, and a folder `file` or a `package` with its `dependencies` |
| `expect.signatures` | per export, `params` exactly, or `error_contains` |
| `expect.exports`, `expect.annotations`, `expect.target`, `expect.workflow_as_code`, `expect.imports`, `expect.host_modules` | what the build recorded; `annotations` lists the flags that are set |
| `expect.problem` | the problem `kind` and message substrings |
| `expect.bundle_contains`, `expect.bundle_not_contains`, `expect.first_line` | text of the bundled module |
| `expect.entries` | the same expectations for other scripts, by id |
| `expect.provided`, `expect.host_module_order`, `expect.order_problem`, `expect.warnings` | host-provided modules: exports, imports or problem by id; the registration order or the cycle; warning codes |
| `expect.run` | `env` (process.env is replaced by it), `register` (host-provided copies registered, in order) and `calls`, each an `entry`, an `export` (default `main`), positional `args` and the `result` or an `error` substring |

A **whole-folder case** gives `given.folder` instead: a whole sync folder,
`wmill.yaml` included, which the runner builds with `bundle.Build`, the
build of `windmill-lite build`, so the rules the build checks on the
compiled program apply: workflow-as-code is refused
([WA-33/c](../docs/semantics/conformance.md#wa-33),
[WR-32](../docs/semantics/languages.md#wr-32)), and so is a script that
imports a package without a lock
([WR-26](../docs/semantics/languages.md#wr-26); ADR 0002, validation 5).

| Field | Meaning |
|---|---|
| `given.folder` | the folder's files by path from the sync root |
| `given.outside` | files outside the sync root, by path from it, each starting with a single `../`: a codebase entry's `relative_path` may lie there ([WR-49](../docs/semantics/languages.md#wr-49)) |
| `given.contract` | the host contract's text; without it every flow and script is a root |
| `given.locks` | by script file, the npm dependencies whose lock the runner makes from `given.packages` and writes as `<path>.script.lock` |
| `expect.build_problems` | every problem the build reports, each an `item` (the start of the reported item: `script <path>`, or `<flow> step '<id>' (script inline-` for an inline step), a `kind` and message substrings in `contains`; empty for a clean build, which must then verify |
| `expect.entries` | for a clean build, by script id: what its metadata in the bundle records (`format`, `target`, `exports`, `signatures`, `host_modules`) and text of its module (`bundle_contains`, `bundle_not_contains`, `first_line`) |
| `expect.assets` | for a clean build, the files under the bundle's `assets/`, by path, with their content |
| `expect.warnings` | for a clean build, when given, the codes of every warning the build reports, in order |
| `expect.host_module_order` | for a clean build, the ids of the manifest's `host_modules`, in registration order |
| `expect.run` | for a clean build, as above: each call's `entry` is a script id, its module the bundle's `.cjs` or `.mjs`; `register` names host-provided modules by id |

The codebase cases are whole-folder cases: `wr-47.json` (how the pinned
CLI bundles a codebase script, WR-47), `wr-48.json` (a falsy
`customBundler` leaves the bundle to esbuild, WR-48/b), `wr-49.json`
(assets, WR-49), and the codebase variants of `wa-31.json` (the env rewrite in CommonJS and ESM
output) and `d12.json` (host-provided modules through their shims, those
outside the folder included).

`deferred.json` lists the catalogue cases of these findings that are not
build cases, each with the reason and where it is checked. A test fails
unless every catalogue case of a finding with a file here is a case or is
deferred.

`annotations.json` holds the leading-comment annotation cases
([WR-31](../docs/semantics/languages.md#wr-31),
[SY-V3](../docs/semantics/openflow.md#sy-v3)): a `source` and the `flags` it
sets. Both readers of the rule run them: the Go loader's
`syncfolder.HasAnnotation` and the Bun build helper, in
`go test ./internal/bundle`, and the helper's parser in `bun test`.
