# Conformance catalogue

**Current state:** a catalogue of the conformance case of every finding in
this reference, grouped by area. The executable fixtures that encode these
cases live under [`conformance/`](../../conformance) at the repository root,
in the format the design chose; [OR-15](oracle.md#or-15) describes the
language-neutral format first proposed for oracle recordings. Recording
batches E3a to E3d (design record 0012) recorded many cases on the oracle,
and the cases they recorded name their recording.

## How to read a case

Each area lists, in ID order, the findings that have at least one case. An
entry names the finding's primary scope and links to the finding; each case
below it has:

- **A case ID**, `<finding>/<letter>` (for example `FX-12/a`). Cite it from
  tests as `// spec: FX-12/a`. Case IDs are stable like finding IDs.
- **A provenance tag:**
  - `source-derived`: the expected output was derived from the pinned source
    and has not been recorded on a running Windmill yet. An open question in
    the area file tracks the recording.
  - `oracle-observed`: recorded on Windmill CE `v1.767.0` (see
    [oracle.md](oracle.md)). A case recorded by a committed fixture ends
    with a **Recorded:** line naming the fixture under
    [`conformance/`](../../conformance), the run, and the CI run of the
    oracle that recorded it. Where the recording differed from the
    expectation derived from the source, **Expect** holds what the oracle
    did, and a dated **Correction** line says what the case expected before
    and which rule explains the difference.
  - `disputed`: the finding is `DISPUTED`, `needs-evidence` or `open`. The case
    states the contested expectation and both claims; do not encode either
    as a passing test until the open question is closed.
  - `policy`: a windmill-lite acceptance or policy check. It tests a choice
    windmill-lite makes (rejecting an unsupported feature, a design option
    such as the platform facade), not Windmill behaviour, and it is not an
    oracle case unless the case says it compares with the oracle.
- **A scope tag**, only when the case's scope differs from the entry's.
- **Given** (the input: flow, scripts, arguments, state) and **Expect** (the
  exact result, or the exact error shape).

Fixtures here are synthetic: written for this reference to exercise one
behaviour each. Paths such as `f/demo/...` are invented.

## Summary

| Area | Cases | `core` | `later` | `out` | `EE` | oracle-observed | disputed | policy |
|---|---|---|---|---|---|---|---|---|
| [OpenFlow model and the sync folder](#openflow) | 145 | 125 | 20 | 0 | 0 | 0 | 4 | 0 |
| [Flow execution](#execution) | 125 | 95 | 29 | 1 | 0 | 64 | 8 | 1 |
| [Loops, branches and subflows](#control-flow) | 87 | 68 | 17 | 0 | 2 | 43 | 0 | 0 |
| [Expressions](#expressions) | 170 | 122 | 48 | 0 | 0 | 164 | 0 | 0 |
| [Languages and script wrappers](#languages) | 184 | 125 | 59 | 0 | 0 | 0 | 12 | 0 |
| [The wmill client API and the platform interface](#client-api) | 93 | 52 | 36 | 5 | 0 | 0 | 0 | 6 |
| [The oracle](#oracle) | 82 | 75 | 5 | 0 | 2 | 40 | 15 | 0 |
| [Licensing and the clean room](#licensing) | 3 | 3 | 0 | 0 | 0 | 0 | 0 | 0 |
| **Total** | 889 | 665 | 214 | 6 | 4 | 311 | 39 | 7 |

<a id="openflow"></a>
## OpenFlow model and the sync folder

Findings: [openflow.md](openflow.md).

<a id="openflow-notation"></a>
### Notation

- Fixtures are written in JSON or in YAML flow style
  (`{type: static, value: 1}`). Key order is not significant unless a case
  says so.
- **Decode cases** state the decoder's accept/reject decision. Error messages
  are compared by "contains": decode messages carry a position suffix
  (`at line L column C`) and are wrapped by the request-body decoder. No HTTP
  status or body is implied ([Q-OF-2](openflow.md#q-of-2)).
- **Deployed** means run by path after the flow version's dependency job
  succeeded; **preview** means the same JSON sent to
  `POST /jobs/run/preview_flow`, which executes raw module values.
  `flows/get` is `GET /api/w/{w}/flows/get/{path}`.
- `ERR(M)` means `{"error": {"name": "Error", "message": M, ...}}`, compared on
  `name` and `message` only; `stack` and an optional `step_id` are further
  fields owned by [languages.md](languages.md).
- **Status ids** are the projected list of step ids in the flow status.
- A case marked **windmill-lite policy** states the proposed windmill-lite
  behaviour next to Windmill's, not a Windmill error string.
- Every case below is source-derived unless tagged `disputed`; none has been
  recorded on the oracle yet. Oracle-pending cases are listed in
  [Q-OF-1](openflow.md#q-of-1) and [Q-OF-11](openflow.md#q-of-11).

<a id="of-2"></a>
### OF-2 · Deploy-time validation: a typed decode plus two checks

**Scope:** core · **Finding:** [OF-2](openflow.md#of-2)

- **OF-2/a** · source-derived · non-workspace step path rejects
  - **Given:** `{"modules":[{"id":"a","value":{"type":"script","path":"/tmp/x","input_transforms":{}}}]}`
  - **Expect:** reject; message contains `not a workspace path` and `'a'`.
- **OF-2/b** · source-derived · empty step path is accepted
  - **Given:** as OF-2/a with `"path":""`.
  - **Expect:** accept.
- **OF-2/c** · source-derived · zero exponential base rejects
  - **Given:** module `a` with `retry: {exponential: {attempts: 2, seconds: 0}}`.
  - **Expect:** reject; message contains
    `Module 'a': Exponential backoff base (seconds) must be greater than 0. A base of 0 would cause immediate retries.`
- **OF-2/d** · source-derived · duplicate ids are not checked
  - **Given:** two top-level modules, both with `id: "a"`.
  - **Expect:** accept.
- **OF-2/e** · source-derived · bad module value uses the `Module` prefix
  - **Given:** `{"modules":[{"id":"a","value":{"type":"foo"}}]}`
  - **Expect:** reject; message contains ``Module 'a': unknown variant `foo` ``
    and does **not** start with `Invalid flow value:`.
- **OF-2/f** · source-derived · bad top-level envelope uses the outer prefix
  - **Given:** `{"modules":[{"id":"a","value":{"type":"identity"},"stop_after_if":{"expr":"true"}}]}`
  - **Expect:** reject; message contains `Invalid flow value:` and
    ``missing field `skip_if_stopped` ``.
- **OF-2/g** · source-derived · bad body-module envelope is attributed to the container
  - **Given:** a for-loop `l` whose body module `b` has
    `stop_after_if: {"expr":"true"}`.
  - **Expect:** reject; message contains `Module 'l':` and
    ``missing field `skip_if_stopped` ``.

<a id="of-3"></a>
### OF-3 · Raw, full and lite forms of a flow value

**Scope:** core · **Finding:** [OF-3](openflow.md#of-3)

- **OF-3/a** · source-derived · full form keeps kept values and materialises container defaults
  - **Given:** deploy a flow with module values `{"type":"identity","extra":7}`,
    `{"type":"script","path":"f/x/s"}` (no `input_transforms`), and a for-loop
    without `skip_failures`; wait for the dependency job to succeed.
  - **Expect:** `flows/get` returns the identity value with `extra: 7`, the
    script value still without `input_transforms`, and the loop with
    `"skip_failures": true, "parallel": false`.
- **OF-3/b** · source-derived · a failed first lock keeps the raw value
  - **Given:** a flow with a for-loop lacking `skip_failures` and an inline Bun
    script that fails to lock, on its first deploy.
  - **Expect:** the flow keeps its raw value; the loop still lacks
    `skip_failures`.
- **OF-3/c** · source-derived · native switch on relock
  - **Given:** a rawscript submitted with `language: bun`, `lock: ""` and first
    line `//native`; successful deploy.
  - **Expect:** the full value has `language: bunnative` and the lite node
    records `bunnative`; the raw form says `bun`.
- **OF-3/d** · disputed · virtual step after deploy
  - **Given:** flow `[a]`, where `a` has `sleep: {type: static, value: 1}`; run
    by path after deploy.
  - **Expect (as drafted):** status ids `[a, a-v]`; the flow result equals
    `a`'s result.
  - **Expect (correction, OF-V5):** `[a, a-v]` only when enqueue builds the
    initial status from lite; with `DISABLE_FLOW_SCRIPT=true` at enqueue the
    status ids are `["a"]` and no final sleep is scheduled. The result equals
    `a`'s in both. Contested: whether "after deploy" alone fixes the status
    list. Tracked by [Q-OF-11](openflow.md#q-of-11); see OF-V5/a–b.
- **OF-3/e** · source-derived · harmless canonicalisation
  - **Given:** a kept module value `{"type":"identity","extra":7}`.
  - **Expect:** behaves exactly like `{"type":"identity"}`.

<a id="of-4"></a>
### OF-4 · Unknown fields are ignored; `notes` and `groups` are editor-only

**Scope:** core · **Finding:** [OF-4](openflow.md#of-4)

- **OF-4/a** · source-derived · notes and an unknown key parse
  - **Given:** `{"modules":[],"notes":[{"id":"n","text":"t","color":"yellow","type":"free"}],"foo":1}`
  - **Expect:** accept; a flow with zero modules.

<a id="of-5"></a>
### OF-5 · Module envelope (`FlowModule`) fields, types and defaults

**Scope:** core · **Finding:** [OF-5](openflow.md#of-5)

- **OF-5/a** · source-derived · `skip_if_stopped` is required
  - **Given:** `stop_after_if: {expr: "true"}` with no `skip_if_stopped`.
  - **Expect:** reject; message contains ``missing field `skip_if_stopped` ``.
- **OF-5/b** · source-derived · bare and object timeout are equivalent
  - **Given:** `timeout: 30`; `timeout: {type: static, value: 30}`.
  - **Expect:** both decode to static 30.
- **OF-5/c** · source-derived · `sleep` needs an object
  - **Given:** `sleep: 5`; `sleep: {type: static, value: 5}`.
  - **Expect:** the first rejects (an object with `type` is expected); the
    second is accepted.
- **OF-5/d** · source-derived · missing id is generated
  - **Given:** a module without `id`.
  - **Expect:** accept; its id is a 6-character `[A-Za-z0-9]` string.
- **OF-5/e** · source-derived · timeout object value must be an integer
  - **Given:** `timeout: {"type":"static","value":"30"}`
  - **Expect:** reject.
- **OF-5/f** · source-derived · timeout expression is accepted
  - **Given:** `timeout: {"type":"javascript","expr":"30"}`
  - **Expect:** accept as an expression (evaluation:
    [FX-24](execution.md#fx-24)).
- **OF-5/g** · source-derived · debounce alias
  - **Given:** `debouncing: {"custom_debounce_key":"k"}`
  - **Expect:** `debounce_key = "k"`.

<a id="of-6"></a>
### OF-6 · `forloopflow`, `whileloopflow`, `branchone`, `branchall` defaults

**Scope:** core · **Finding:** [OF-6](openflow.md#of-6)

- **OF-6/a** · source-derived · deployed `branchall` shape
  - **Given:** a module value `{type: branchall, branches: [{modules: []}]}`,
    deployed.
  - **Expect:** after deploy the value is
    `{"type":"branchall","branches":[{"expr":"","modules":[],"skip_failure":true,"parallel":true}],"parallel":true}`
    (key order not significant). For-loop defaults: see OF-3/a.

<a id="of-7"></a>
### OF-7 · Skip-flag defaults differ between raw and deployed values

**Scope:** core · **Finding:** [OF-7](openflow.md#of-7)

Common body **B2** (two modules, so the single-step fast path of
[CF-8](control-flow.md#cf-8) cannot apply; no `failure_module`):

- `b`: a Bun rawscript with input `x = {type: javascript, expr: "flow_input.iter.value"}`;
  it throws `new Error("boom")` when `x === 1` and returns `x` otherwise.
- `c`: `identity`.

Recording protocol: wait for the version's dependency job to succeed before the
by-path run, record `flows/get` to show the explicit stored flag, and use
preview for the raw case ([Q-OF-1](openflow.md#q-of-1)).

- **OF-7/a** · source-derived · F7-seq: sequential for-loop
  - **Given:** module `l` = `forloopflow`, `iterator: {type: static, value: [1, 2]}`,
    `parallel: false`, body B2, `skip_failures` absent.
  - **Expect:** deployed → status success, result `[ERR("boom"), 2]`; preview →
    status failure, result `[ERR("boom")]` (no second iteration).
- **OF-7/b** · source-derived · F7-par: parallel for-loop
  - **Given:** as OF-7/a with `parallel: true`.
  - **Expect:** deployed → success, `[ERR("boom"), 2]`; preview → failure,
    `[ERR("boom"), 2]`.
- **OF-7/c** · source-derived · F7-ba: `branchall`
  - **Given:** module `ba` = `branchall`, `parallel: false`, branches
    `[[f], [r]]`, where `f` is a Bun rawscript that throws `new Error("b")` and
    `r` returns `2`; no `skip_failure` on either branch.
  - **Expect:** deployed → success, `[ERR("b"), 2]`; preview → failure,
    `[ERR("b")]` (branch 2 never runs).
- **OF-7/d** · source-derived · controls
  - **Given:** each of OF-7/a–c with the flag explicitly `false`, then
    explicitly `true`.
  - **Expect:** with `false`, the deployed run matches the preview result; with
    `true`, the preview run matches the deployed result.

<a id="of-8"></a>
### OF-8 · `whileloopflow.parallel` is lost on deploy

**Scope:** later · **Finding:** [OF-8](openflow.md#of-8)

- **OF-8/a** · source-derived · while-loop parallel fields are dropped
  - **Given:** push `{type: whileloopflow, parallel: true, parallelism: {type: static, value: 2}, skip_failures: false, modules: [...]}`.
  - **Expect:** `flows/get` returns the while-loop without `parallel` and
    `parallelism`.

<a id="of-9"></a>
### OF-9 · Module value kinds (`value.type`)

**Scope:** core · **Finding:** [OF-9](openflow.md#of-9)

- **OF-9/a** · source-derived · unknown kind rejects
  - **Given:** `{type: "foo"}`
  - **Expect:** reject with
    ``unknown variant `foo`, expected one of `script`, `flow`, `forloopflow`, `whileloopflow`, `branchone`, `branchall`, `rawscript`, `identity`, `aiagent` ``.
- **OF-9/b** · source-derived · rawscript without `input_transforms`
  - **Given:** `{type: rawscript, content: "export function main(){}", language: bun}`
  - **Expect:** accept; `input_transforms = {}`.

<a id="of-10"></a>
### OF-10 · InputTransform parsing: ordinary and restricted slots

**Scope:** core · **Finding:** [OF-10](openflow.md#of-10)

- **OF-10/a** · source-derived · static without value
  - **Given:** `{type: static}`
  - **Expect:** static `null`.
- **OF-10/b** · source-derived · javascript without expr
  - **Given:** `{type: javascript}`
  - **Expect:** expression `""`.
- **OF-10/c** · source-derived · unsupported transform type
  - **Given:** `{type: "jq", expr: "."}`
  - **Expect:** reject with
    ``got value: jq for field `type`, expected value: `static` or `javascript` ``.
- **OF-10/d** · source-derived · bare parallelism
  - **Given:** `parallelism: 4`
  - **Expect:** static 4.
- **OF-10/e** · source-derived · parallelism out of range
  - **Given:** `parallelism: 70000`
  - **Expect:** reject (u16).
- **OF-10/f** · source-derived · string sleep value decodes
  - **Given:** `sleep: {"type":"static","value":"30"}`
  - **Expect:** decodes; its run-time meaning is
    [FX-23](execution.md#fx-23).
- **OF-10/g** · source-derived · unused value in a restricted slot is checked
  - **Given:** `parallelism: {"type":"javascript","expr":"2","value":"unused"}`
  - **Expect:** reject.
- **OF-10/h** · source-derived · restricted static without value
  - **Given:** `parallelism: {"type":"static"}`
  - **Expect:** static `null`.

<a id="of-11"></a>
### OF-11 · Retry fields and ranges

**Scope:** core · **Finding:** [OF-11](openflow.md#of-11)

- **OF-11/a** · source-derived · constant retry defaults
  - **Given:** `retry: {constant: {attempts: 2}}`
  - **Expect:** 2 retries after the original run, with 0 s delay.
- **OF-11/b** · source-derived · empty retry
  - **Given:** `retry: {}`
  - **Expect:** no retry.
- **OF-11/c** · source-derived · out-of-range seconds
  - **Given:** `retry: {exponential: {attempts: 1, seconds: 70000}}`
  - **Expect:** reject.

<a id="of-13"></a>
### OF-13 · Languages

**Scope:** core · **Finding:** [OF-13](openflow.md#of-13)

- **OF-13/a** · source-derived · unknown language rejects
  - **Given:** `language: typescript`
  - **Expect:** reject (unknown variant).

<a id="of-14"></a>
### OF-14 · Script hash format

**Scope:** later · **Finding:** [OF-14](openflow.md#of-14)

- **OF-14/a** · source-derived · 16 digits
  - **Given:** `hash: "0000000000000001"`
  - **Expect:** accept.
- **OF-14/b** · source-derived · extra bytes ignored
  - **Given:** `hash: "0000000000000001ff"`
  - **Expect:** accept; re-serialised as `0000000000000001`.
- **OF-14/c** · source-derived · upper case normalised
  - **Given:** `hash: "00000000000000AB"`
  - **Expect:** accept; re-serialised as `00000000000000ab`.
- **OF-14/d** · source-derived · odd length
  - **Given:** `hash: "1"`
  - **Expect:** reject (odd length).
- **OF-14/e** · source-derived · fewer than 8 bytes
  - **Given:** `hash: "00000001"`
  - **Expect:** reject; message contains `did not decode to an u64`.
- **OF-14/f** · source-derived · an unknown pin deploys and fails at run time
  - **Given:** script `f/demo/s` deployed; a flow
    `[a: {type: script, path: f/demo/s, hash: "0123456789abcdef"}]`, where no
    version in the workspace has that hash; deploy, then run by path.
  - **Expect:** the deploy and its dependency job succeed. The run fails
    without a job for `a`; the error message contains
    `script not found at name 81985529216486895`.

<a id="of-15"></a>
### OF-15 · Inline-script locks on deploy

**Scope:** core · **Finding:** [OF-15](openflow.md#of-15)

Common setup for OF-15/a–c: a Bun module `a` with a non-empty lock, a matching
`native` header and language, and supported workers.

- **OF-15/a** · source-derived · no selection list
  - **Given:** dependency job without `nodes_to_relock`.
  - **Expect:** the existing lock is kept.
- **OF-15/b** · source-derived · module not selected
  - **Given:** `nodes_to_relock: []`.
  - **Expect:** `a` is skipped; its value is unchanged.
- **OF-15/c** · source-derived · module selected
  - **Given:** `nodes_to_relock: ["a"]`.
  - **Expect:** `a` is relocked and listed as modified; the lock text need not
    change.
- **OF-15/d** · source-derived · empty bash lock
  - **Given:** a workspace with **no workspace dependencies**; an inline `bash`
    step.
  - **Expect:** final lock `""`.
- **OF-15/e** · source-derived · failure on first deploy
  - **Given:** a flow that fails to lock on its first deploy.
  - **Expect:** `flows/get` returns the raw submitted value.

<a id="of-16"></a>
### OF-16 · Flow-level fields (`FlowValue`) and flow metadata

**Scope:** core · **Finding:** [OF-16](openflow.md#of-16)

- **OF-16/a** · source-derived · integer concurrency limit
  - **Given:** `concurrent_limit: 1.5`
  - **Expect:** reject (i32).
- **OF-16/b** · source-derived · failure-module id is not enforced
  - **Given:** `failure_module: {id: "oops", value: {type: identity}}`
  - **Expect:** accept.
- **OF-16/c** · source-derived · later · `same_worker` with one child
  - **Given:** `same_worker: true`; a parallel for-loop over static `[5]` whose
    body returns `flow_input.iter.value`.
  - **Expect:** success, result `[5]`.
- **OF-16/d** · source-derived · later · `same_worker` with several children
  - **Given:** as OF-16/c over `[5, 6]`.
  - **Expect:** failure; message contains
    `Cannot continue on same worker with multiple jobs`.

<a id="of-17"></a>
### OF-17 · Virtual trailing identity for a final sleep or suspend

**Scope:** core · **Finding:** [OF-17](openflow.md#of-17)

- **OF-17/a** · disputed · final sleep adds `a-v`
  - **Given:** flow `[a]`, where `a` has `sleep: {type: static, value: 1}`.
  - **Expect (as drafted):** the run's per-step list is `[a, a-v]`; the flow
    result equals `a`'s result.
  - **Expect (correction, OF-V5):** this holds for an initial-lite status (or
    the independently augmented preview path); with a full-built status (for
    example `DISABLE_FLOW_SCRIPT=true` at enqueue) the list is `["a"]`.
    Contested: whether the expectation applies regardless of the enqueue form.
    Tracked by [Q-OF-11](openflow.md#q-of-11).

<a id="of-18"></a>
### OF-18 · The flow `schema` has no server-side execution semantics

**Scope:** core · **Finding:** [OF-18](openflow.md#of-18)

Fixture: flow schema `{"type":"object","properties":{"x":{"type":"number","default":5}}}`,
no preprocessor; one module `p`, a Bun rawscript taking `hasX: boolean` and
returning `{hasX}`, with input
`hasX = {type: javascript, expr: "flow_input.x !== undefined"}`. Transform
semantics: [FX-7](execution.md#fx-7) and [expressions.md](expressions.md).

- **OF-18/a** · source-derived · schema default is not applied
  - **Given:** run by path with body `{}`.
  - **Expect:** result `{"hasX": false}`.
- **OF-18/b** · source-derived · control
  - **Given:** run by path with body `{"x": 1}`.
  - **Expect:** result `{"hasX": true}`.

<a id="of-19"></a>
### OF-19 · Client-side validation is optional and schema-based

**Scope:** later · **Finding:** [OF-19](openflow.md#of-19)

- **OF-19/a** · source-derived · lint looser than the server
  - **Given:** `stop_after_if: {expr: "true"}` (no `skip_if_stopped`).
  - **Expect:** `wmill lint` passes; the server rejects.
- **OF-19/b** · source-derived · lint stricter than the server
  - **Given:** a for-loop without `skip_failures`.
  - **Expect:** `wmill lint` fails; the server accepts.

<a id="of-v1"></a>
### OF-V1 · Decode rules include cross-kind checks, null distinctions and aliases

**Scope:** core · **Finding:** [OF-V1](openflow.md#of-v1)

- **OF-V1/a** · source-derived · unused field type-checked (identity)
  - **Given:** `{"type":"identity","language":"not-a-language"}`
  - **Expect:** reject.
- **OF-V1/b** · source-derived · unused field type-checked (while-loop)
  - **Given:** `{"type":"whileloopflow","modules":[],"parallel":"yes"}`
  - **Expect:** reject.
- **OF-V1/c** · source-derived · truly unknown field ignored
  - **Given:** `{"type":"identity","unrecognized":7}`
  - **Expect:** accept.
- **OF-V1/d** · source-derived · null selects the for-loop default
  - **Given:** a for-loop with a valid `iterator` and `modules` and
    `skip_failures: null`.
  - **Expect:** semantic `skip_failures` is `true`.
- **OF-V1/e** · source-derived · null rejects for branch booleans
  - **Given:** a `branchall` branch `{"modules":[],"skip_failure":null}`.
  - **Expect:** reject.
- **OF-V1/f** · source-derived · `input_transform` alias on a flow step
  - **Given:** a `flow` value with `path: f/demo/child` and only
    `input_transform: {}`.
  - **Expect:** accept, with empty transforms.

<a id="of-v2"></a>
### OF-V2 · `timeout` and `parallelism` restrict object payloads as well as bare numbers

**Scope:** core · **Finding:** [OF-V2](openflow.md#of-v2)

- **OF-V2/a** · source-derived · string timeout value
  - **Given:** `timeout: {"type":"static","value":"30"}`
  - **Expect:** reject.
- **OF-V2/b** · source-derived · timeout beyond i32
  - **Given:** `timeout: {"type":"static","value":2147483648}`
  - **Expect:** reject.
- **OF-V2/c** · source-derived · parallelism beyond u16
  - **Given:** `parallelism: {"type":"static","value":70000}`
  - **Expect:** reject.
- **OF-V2/d** · source-derived · unused value checked under javascript
  - **Given:** `parallelism: {"type":"javascript","expr":"2","value":"unused"}`
  - **Expect:** reject.
- **OF-V2/e** · source-derived · static timeout without value
  - **Given:** `timeout: {"type":"static"}`
  - **Expect:** `{"type":"static","value":null}`.
- **OF-V2/f** · source-derived · null timeout
  - **Given:** `timeout: null`
  - **Expect:** no timeout (absent optional).
- **OF-V2/g** · source-derived · ordinary slot keeps arbitrary JSON
  - **Given:** `sleep: {"type":"static","value":"30"}`
  - **Expect:** passes structural decoding (execution validity is separate).

<a id="of-v3"></a>
### OF-V3 · `early_return` selects an API response node

**Scope:** later · **Finding:** [OF-V3](openflow.md#of-v3)

- **OF-V3/a** · source-derived · early response, continued execution
  - **Given:** a flow with a successful node `a` returning `{"stage":"a"}` and a
    delayed node `b` returning `{"stage":"b"}`; `early_return: "a"`; run with
    wait-for-result.
  - **Expect:** the wait-for-result response is `{"stage":"a"}`; completion and
    result inspection eventually yield `{"stage":"b"}`. The recording must wait
    for `b` to prove continued execution.
- **OF-V3/b** · source-derived · never evaluated
  - **Given:** `early_return: "1+1"`.
  - **Expect:** treated as an id lookup; never evaluated to 2.

<a id="of-v4"></a>
### OF-V4 · `pass_flow_input_directly` means different things by location

**Scope:** core · **Finding:** [OF-V4](openflow.md#of-v4)

Fixture: flow args `{"x":1}`; one script step returning its `x` arg; its
transform `x` is static `2`.

- **OF-V4/a** · source-derived · envelope flag
  - **Given:** envelope `pass_flow_input_directly: true`.
  - **Expect:** result `1`.
- **OF-V4/b** · source-derived · value-only flag
  - **Given:** the flag only inside the script value; envelope flag absent.
  - **Expect:** result `2`.

<a id="of-v5"></a>
### OF-V5 · Enqueue status and worker data select flow forms separately

**Scope:** core · **Finding:** [OF-V5](openflow.md#of-v5)

Fixture: a successfully deployed flow with one ordinary module `a` returning
JSON `7`, with `sleep: {type: static, value: 1}`; no cache, preprocessor,
retry, stop condition or failure handler; v1.767.0 workers. Assert scheduling
and status, not a wall-clock upper bound. Oracle corroboration is optional
([Q-OF-11](openflow.md#q-of-11)).

- **OF-V5/a** · disputed · lite at enqueue
  - **Given:** lite available; ordinary enqueue selection.
  - **Expect:** status ids `["a","a-v"]`; success, result `7`; the final sleep
    is scheduled before `a-v`.
- **OF-V5/b** · disputed · full at enqueue
  - **Given:** `DISABLE_FLOW_SCRIPT=true` at enqueue.
  - **Expect:** status ids `["a"]`; success, result `7`; no final-sleep
    scheduling, even though the worker may fetch lite.

<a id="of-v6"></a>
### OF-V6 · Workspace-dependency lock header and the PHP early return

**Scope:** core · **Finding:** [OF-V6](openflow.md#of-v6)

These are lock-assembly boundary cases with supported workers; they do not
assert that arbitrary Bun source yields an empty resolver body.

- **OF-V6/a** · source-derived · later · mode line only
  - **Given:** language Bun; explicit `manual` dependencies with no external
    reference; empty resolver body.
  - **Expect:** final lock text `"// workspace-dependencies-mode: manual\n"`.
- **OF-V6/b** · source-derived · no dependency state
  - **Given:** absent dependency state; empty body.
  - **Expect:** final lock `""`.
- **OF-V6/c** · source-derived · PHP early return
  - **Given:** PHP with neither workspace dependencies nor parsed imports.
  - **Expect:** final lock `""`.

<a id="sy-1"></a>
### SY-1 · Tree root, recognised paths and path mapping

**Scope:** core · **Finding:** [SY-1](openflow.md#sy-1)

- **SY-1/a** · source-derived · flat script
  - **Given:** `f/a/hello.ts` and `f/a/hello.script.yaml`.
  - **Expect:** script `f/a/hello`.
- **SY-1/b** · source-derived · dotted folder truncates
  - **Given:** `f/my.dir/x.ts`.
  - **Expect:** Windmill path `f/my`. windmill-lite policy: reject with a clear
    error instead of reproducing the truncation.
- **SY-1/c** · source-derived · dot directory skipped
  - **Given:** `.git/x.ts`.
  - **Expect:** ignored.
- **SY-1/d** · source-derived · `node_modules` skipped
  - **Given:** `node_modules/a/b.ts`.
  - **Expect:** ignored.
- **SY-1/e** · source-derived · multi-part language suffixes
  - **Given:** `f/a/x.bun.ts`, `f/a/x.deno.ts`, `f/a/x.fetch.ts`, `f/a/x.pg.sql`
    (each on its own).
  - **Expect:** each maps to `f/a/x`.
- **SY-1/f** · source-derived · later · `noCdToRoot: true`
  - **Given:** the working directory is a child; its parent holds `wmill.yaml`;
    `noCdToRoot: true`.
  - **Expect:** the parent's config is found; the working directory stays the
    child.
- **SY-1/g** · source-derived · later · `noCdToRoot: false`
  - **Given:** as SY-1/f with `noCdToRoot: false`.
  - **Expect:** the same config is found; the working directory changes to the
    parent.

<a id="sy-2"></a>
### SY-2 · Script files

**Scope:** core · **Finding:** [SY-2](openflow.md#sy-2)

- **SY-2/a** · source-derived · two sources for one path
  - **Given:** `f/a/x.ts` and `f/a/x.py`, both next to `f/a/x.script.yaml`.
  - **Expect:** load error naming both files.

<a id="sy-3"></a>
### SY-3 · Script language comes from the file extension

**Scope:** core · **Finding:** [SY-3](openflow.md#sy-3)

- **SY-3/a** · source-derived · `defaultTs: bun`
  - **Given:** `a.ts`; `a.deno.ts`; `a.fetch.ts`; `a.ts` whose source carries the
    `native` header annotation; `a.sql`.
  - **Expect:** `bun`; `deno`; `nativets`; `bunnative`; error (cannot infer
    language, with a dialect hint).
- **SY-3/b** · source-derived · `defaultTs: deno`
  - **Given:** `a.ts`; `a.bun.ts`.
  - **Expect:** `deno`; `bun`.

<a id="sy-4"></a>
### SY-4 · `.script.yaml` contents

**Scope:** core · **Finding:** [SY-4](openflow.md#sy-4)

- **SY-4/a** · source-derived · metadata record
  - **Given:** `{summary: s, kind: script, schema: {...}, lock: '!inline f/a/x.script.lock', timeout: 30}`
    for script `f/a/x`.
  - **Expect:** a script record with `timeout = 30` seconds, language from
    SY-3, lock from SY-5.
- **SY-4/b** · source-derived · a script without metadata on push
  - **Given:** `f/a/x.ts` = `export async function main(n: number) { return n }`,
    with no `f/a/x.script.yaml` and no `f/a/x.script.lock`; `wmill sync push`
    into an empty workspace.
  - **Expect:** the push creates `f/a/x.script.yaml` and an empty
    `f/a/x.script.lock` in the tree. The deployed script's schema is inferred
    from `main`: property `n` of type `number`, listed in `required`; it is
    not the skeleton
    `{"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object", "properties": {}, "required": []}`
    and not `{}`.

<a id="sy-5"></a>
### SY-5 · Script locks

**Scope:** core · **Finding:** [SY-5](openflow.md#sy-5)

- **SY-5/a** · source-derived · missing lock file
  - **Given:** `lock: '!inline f/a/x.script.lock'`, file absent.
  - **Expect:** lock `""` (Windmill would then generate one on deploy, OF-15).
- **SY-5/b** · source-derived · legacy array lock
  - **Given:** `lock: [a, b]`
  - **Expect:** lock `"a\nb"`.
- **SY-5/c** · source-derived · a leading space is not a reference
  - **Given:** `f/a/x.script.yaml` with `lock: ' !inline f/a/x.script.lock'`
    and `f/a/x.script.lock` = `L`.
  - **Expect:** the deployed lock is the literal text
    ` !inline f/a/x.script.lock`, not `L` and not `""`.

<a id="sy-6"></a>
### SY-6 · Flow folders and what a pulled `flow.yaml` contains

**Scope:** core · **Finding:** [SY-6](openflow.md#sy-6)

- **SY-6/a** · source-derived · default folder suffix
  - **Given:** `f/a/x.flow/flow.yaml`; `nonDottedPaths` unset.
  - **Expect:** flow `f/a/x`.
- **SY-6/b** · source-derived · wrong suffix style
  - **Given:** the same tree with `nonDottedPaths: true`.
  - **Expect:** load error (wrong format; the CLI says
    `Found N directory(ies) using … format`).
- **SY-6/c** · source-derived · pull returns the full form
  - **Given:** push a flow with `{"type":"identity","extra":7}` and a container
    without `skip_failures`; wait for the deploy; pull.
  - **Expect:** `extra: 7` is still present; `skip_failures: true` is added.

<a id="sy-7"></a>
### SY-7 · Inline scripts inside a flow folder

**Scope:** core · **Finding:** [SY-7](openflow.md#sy-7)

Fixture for SY-7/a–c, in `f/x.flow/flow.yaml`:

```yaml
value:
  modules:
    - id: a
      value:
        type: rawscript
        language: bun
        content: '!inline a.inline_script.ts'
        lock: '!inline a.inline_script.lock'
        input_transforms: {}
```

- **SY-7/a** · source-derived · both files present
  - **Given:** `f/x.flow/a.inline_script.ts` and `f/x.flow/a.inline_script.lock`
    exist.
  - **Expect:** content = text of `f/x.flow/a.inline_script.ts`; lock = text of
    `f/x.flow/a.inline_script.lock`; language `bun`.
- **SY-7/b** · source-derived · lock file missing
  - **Given:** the lock file is absent.
  - **Expect:** lock `""`.
- **SY-7/c** · source-derived · content file missing
  - **Given:** the content file is absent (no `a.inline_script.ts` fallback
    either).
  - **Expect:** load error naming the file (the CLI says
    `Cannot push flow <path>: missing inline script file(s): …`).
- **SY-7/d** · source-derived · subfolder path
  - **Given:** `content: "!inline sub/a.ts"` with `f/x.flow/sub/a.ts` present.
  - **Expect:** resolves to that file's text.
- **SY-7/e** · source-derived · escaping path
  - **Given:** `content: "!inline ../../../etc/x"`.
  - **Expect:** the CLI would read it. windmill-lite policy: reject.
- **SY-7/f** · source-derived · the fallback for a reference without a dot
  - **Given:** module `a` with `content: "!inline helper"`; `f/x.flow/helper`
    is absent and `f/x.flow/a.` (the id and a trailing dot) holds
    `export function main() { return 1 }`.
  - **Expect:** the content is the text of `f/x.flow/a.`; the push does not
    fail.

<a id="sy-8"></a>
### SY-8 · Codebase scripts

**Scope:** core · **Finding:** [SY-8](openflow.md#sy-8)

- **SY-8/a** · source-derived · first match wins
  - **Given:** `codebases: [{relative_path: ../lib, includes: ['f/a/**']}, {relative_path: ../shared}]`;
    `defaultTs: bun`.
  - **Expect:** `f/a/x.ts` uses the first entry, `f/b/y.ts` the second;
    `f/a/z.py` uses none.
- **SY-8/b** · source-derived · Bun-only bundling
  - **Given:** `codebases: [{relative_path: ../lib}]` (no includes),
    `defaultTs: bun`.
  - **Expect:** `f/a/x.ts` is bundled; `f/a/y.deno.ts` and `f/a/z.fetch.ts` are
    not.
- **SY-8/c** · source-derived · `includes: []` never matches
  - **Given:** `codebases: [{relative_path: ../lib, includes: []}, {relative_path: ../shared}]`;
    `defaultTs: bun`.
  - **Expect:** `f/a/x.ts` uses the second entry (`../shared`).

<a id="sy-9"></a>
### SY-9 · YAML dialect

**Scope:** core · **Finding:** [SY-9](openflow.md#sy-9)

These cases were checked against the npm `yaml` 2.8.3 package, not through the
CLI end to end; Go parity is [Q-OF-6](openflow.md#q-of-6).

- **SY-9/a** · source-derived · scalar resolution
  - **Given / Expect (YAML → JSON):**

    | YAML | JSON |
    |---|---|
    | `yes` | `"yes"` |
    | `2024-01-01` | `"2024-01-01"` |
    | `017` | `17` |
    | `1_000` | `"1_000"` |
    | `0x1F` | `31` |
    | `content: !inline a.ts` | `"content": "!inline a.ts"` |
- **SY-9/b** · source-derived · duplicate key
  - **Given:** a map with a repeated key.
  - **Expect:** parse error.
- **SY-9/c** · source-derived · precision
  - **Given:** `9007199254740993`
  - **Expect:** `9007199254740992`.

<a id="sy-10"></a>
### SY-10 · Resources and resource types

**Scope:** core · **Finding:** [SY-10](openflow.md#sy-10)

- **SY-10/a** · source-derived · plain resource
  - **Given:** `f/a/cfg.resource.yaml` with `{resource_type: x, value: {host: h}}`.
  - **Expect:** resource `f/a/cfg` = `{"host": "h"}`.
- **SY-10/b** · source-derived · a resource without `value`
  - **Given:** `f/a/cfg.resource.yaml` with `{resource_type: x, description: d}`
    and no `value`; `wmill sync push` into an empty workspace.
  - **Expect:** the push fails at that item with a `TypeError` whose message
    contains `reading 'content'`; no resource `f/a/cfg` is created.

<a id="sy-11"></a>
### SY-11 · Variables and secrets

**Scope:** core · **Finding:** [SY-11](openflow.md#sy-11)

- **SY-11/a** · source-derived · encrypted secret without a resolver
  - **Given:** `P.variable.yaml` with `{is_secret: true, value: "<base64 ciphertext>"}`;
    no resolver configured.
  - **Expect:** windmill-lite policy: resolving `$var:P` fails with a clear
    "secret not available" error rather than returning the ciphertext.
- **SY-11/b** · source-derived · a variable without `value`
  - **Given:** `f/a/v.variable.yaml` with `{is_secret: false, description: d}`
    and no `value`; `wmill sync push` into an empty workspace.
  - **Expect:** the push fails; the server's refusal contains
    ``missing field `value` ``; no variable `f/a/v` is created (it does not
    read as `""`).

<a id="sy-12"></a>
### SY-12 · Other files a loader must recognise (and may ignore)

**Scope:** later · **Finding:** [SY-12](openflow.md#sy-12)

- **SY-12/a** · source-derived · per-workspace override is not an item
  - **Given:** `f/a/x.ts`, `f/a/x.script.yaml` and `f/a/x.prod.resource.yaml`,
    with `prod` declared as a workspace.
  - **Expect:** one script; no resource named `f/a/x.prod`.

<a id="sy-13"></a>
### SY-13 · `wmill.yaml`

**Scope:** core · **Finding:** [SY-13](openflow.md#sy-13)

- **SY-13/a** · source-derived · `extraIncludes` narrows
  - **Given:** `wmill.yaml` = `{includes: ['f/**'], extraIncludes: ['f/a/**']}`.
  - **Expect:** `f/a/x.ts` in; `f/b/y.ts` out; `u/me/z.ts` out.
- **SY-13/b** · source-derived · no `wmill.yaml`
  - **Given:** a tree with `f/a/x.ts`, `f/b/y.ts` and `f/a/fl.flow/flow.yaml`,
    with no `wmill.yaml` in the working directory or above it;
    `wmill sync push` with no options.
  - **Expect:** the CLI warns
    `No wmill.yaml found. Use 'wmill init' to bootstrap it.` and pushes all
    three items, unfiltered: `f/a/x` and `f/b/y` as `bun` scripts, and the
    dotted `.flow` folder as flow `f/a/fl`.

<a id="sy-14"></a>
### SY-14 · `wmill-lock.yaml` is a CLI cache, not part of the model

**Scope:** later · **Finding:** [SY-14](openflow.md#sy-14)

- **SY-14/a** · source-derived · cache absent or corrupt
  - **Given:** a tree whose `wmill-lock.yaml` is missing or corrupt.
  - **Expect:** windmill-lite policy: loads normally (a warning at most). The
    CLI itself rejects unknown versions and some malformed shapes.

<a id="sy-15"></a>
### SY-15 · What a folder cannot supply

**Scope:** core · **Finding:** [SY-15](openflow.md#sy-15)

- **SY-15/a** · source-derived · hub step
  - **Given:** a flow step `{type: script, path: hub/123/foo}`.
  - **Expect:** windmill-lite policy: load error naming the step id (or
    delegation, if configured).

<a id="sy-16"></a>
### SY-16 · Globs are minimatch 10 with default options, over the root-relative file path

**Scope:** core · **Finding:** [SY-16](openflow.md#sy-16)

- **SY-16/a** · source-derived · default options
  - **Given / Expect** (`includes` pattern, file path, whether the file
    passes):

    | Pattern | Path | Passes |
    |---|---|---|
    | `*.ts` | `f/a/x.ts` | no |
    | `f/**` | `f/a/x.ts` | yes |
    | `f/**` | `f/a/.x.ts` | no |
    | `F/**` | `f/a/x.ts` | no |
    | `f/{a,b}/*.ts` | `f/b/y.ts` | yes |

<a id="sy-17"></a>
### SY-17 · The filter decides file by file; a passing file pushes its whole item

**Scope:** core · **Finding:** [SY-17](openflow.md#sy-17)

- **SY-17/a** · source-derived · which files select a script and a flow
  - **Given:** `f/a/x.ts` with `f/a/x.script.yaml`, and a flow folder
    `f/a/fl.flow/` with `flow.yaml` and `a.inline_script.ts`; `wmill sync push`
    into an empty workspace, once with `includes: ['**/*.ts']` and once with
    `includes: ['**/*.yaml']`.
  - **Expect:** with `**/*.ts`, script `f/a/x` (with its metadata) and flow
    `f/a/fl` (with its `flow.yaml`) are both deployed. With `**/*.yaml`, flow
    `f/a/fl` is deployed and script `f/a/x` is not.
- **SY-17/b** · source-derived · a new lock file selects nothing
  - **Given:** a valid new flow folder `f/a/fl.flow/` with `flow.yaml`,
    `a.inline_script.ts` and its lock `a.inline_script.lock`;
    `includes: ['**/*.lock']`; a plain `wmill sync push` (no
    `--auto-metadata`) into an empty workspace.
  - **Expect:** nothing is deployed: the only passing file is a new lock
    file, which is skipped, so flow `f/a/fl` is not pushed.

<a id="sy-18"></a>
### SY-18 · Which files the walk reads, and as what

**Scope:** core · **Finding:** [SY-18](openflow.md#sy-18)

- **SY-18/a** · source-derived · unclassified files are ignored
  - **Given:** an ordinary script `f/a/x.ts`, plus `f/a/notes.md`, `f/a/x.yml`,
    `f/a/data.json` (YAML mode) and `f/a/fl.flow/README.md` beside a valid
    `f/a/fl.flow/flow.yaml`; no filter.
  - **Expect:** the push succeeds and deploys exactly script `f/a/x` and flow
    `f/a/fl`; none of the other files is read as an item.
- **SY-18/b** · source-derived · a stray `.js` file fails the push
  - **Given:** an ordinary script `f/a/x.ts` and `f/a/helper.js`; no filter.
  - **Expect:** the push fails with
    `The item f/a/helper.js has an unrecognized type ending script`.

<a id="sy-19"></a>
### SY-19 · Text files: a UTF-8 byte order mark is stripped, UTF-16 and UTF-32 are refused

**Scope:** core · **Finding:** [SY-19](openflow.md#sy-19)

- **SY-19/a** · source-derived · UTF-8 byte order mark
  - **Given:** `f/a/x.ts` whose bytes are `EF BB BF` followed by
    `export function main() { return 1 }`.
  - **Expect:** the deployed content starts with `export`; it holds no
    U+FEFF.
- **SY-19/b** · source-derived · UTF-16 byte order mark
  - **Given:** `f/a/x.ts` whose bytes start with `FF FE` (UTF-16 LE text).
  - **Expect:** the push fails; the message contains
    `is encoded as UTF-16 LE, which is not supported. Please convert it to UTF-8.`
- **SY-19/c** · source-derived · a refused lock reached only as a reference
  - **Given:** an ordinary UTF-8 `f/a/x.ts` with existing `f/a/x.script.yaml`
    holding `lock: '!inline f/a/x.script.lock'`, where `f/a/x.script.lock`
    starts with `FF FE`; `includes: ['**/*.ts']`, so the walk selects only
    `f/a/x.ts`; `wmill sync push` into an empty workspace.
  - **Expect:** the push succeeds with a warning that names
    `f/a/x.script.lock`. Script `f/a/x` is pushed with the resolved lock
    `""` (what the server then does with an empty lock is
    [OF-15](openflow.md#of-15)).

<a id="sy-v1"></a>
### SY-V1 · Inline lock resolution depends on inline content

**Scope:** core · **Finding:** [SY-V1](openflow.md#sy-v1)

- **SY-V1/a** · source-derived · embedded content leaves the lock literal
  - **Given:** non-directive (embedded) content plus `lock: "!inline a.lock"`.
  - **Expect:** after replacement the lock is still the literal string
    `"!inline a.lock"`.
- **SY-V1/b** · source-derived · one leading space
  - **Given:** content `"!inline a.ts"` with a valid `a.ts`; lock
    `" !inline a.lock"`.
  - **Expect:** the lock file name is the token `!inline`; that file being
    absent, the lock becomes `""`.
- **SY-V1/c** · source-derived · two leading spaces
  - **Given:** as SY-V1/b with lock `"  !inline a.lock"`.
  - **Expect:** an empty file name is selected; the file being absent, the lock
    becomes `""`.
- **SY-V1/d** · source-derived · well-formed directives
  - **Given:** content `"!inline a.ts"` and lock `"!inline a.lock"`, both files
    present.
  - **Expect:** both resolve normally.

<a id="sy-v2"></a>
### SY-V2 · Filters distinguish absent `includes` from empty `includes`

**Scope:** core · **Finding:** [SY-V2](openflow.md#sy-v2)

- **SY-V2/a** · source-derived · excludes only
  - **Given:** ordinary `f/a/x.ts`; config `{"excludes":["u/**"]}`.
  - **Expect:** included.
- **SY-V2/b** · source-derived · explicitly empty includes
  - **Given:** config `{"includes":[],"excludes":["u/**"]}`.
  - **Expect:** `f/a/x.ts` excluded.
- **SY-V2/c** · source-derived · `extraIncludes` alone
  - **Given:** config `{"extraIncludes":["u/**"]}`.
  - **Expect:** `f/a/x.ts` remains included.
- **SY-V2/d** · source-derived · resource-type bypass
  - **Given:** root `x.resource-type.yaml`; config `{"includes":["f/**"]}`;
    `skipResourceTypes` unset.
  - **Expect:** included.

<a id="sy-v3"></a>
### SY-V3 · Native annotation spelling and position decide the Bun runtime

**Scope:** core · **Finding:** [SY-V3](openflow.md#sy-v3)

- **SY-V3/a** · source-derived · recognised forms
  - **Given:** otherwise ordinary Bun source whose first line is `// native`,
    or `//native=false`.
  - **Expect:** native true, hence `bunnative` on the normal deploy path.
- **SY-V3/b** · source-derived · unrecognised forms
  - **Given:** first line ` //native` (indented), `//Native`, or
    `//native =true`.
  - **Expect:** native false.
- **SY-V3/c** · source-derived · after code
  - **Given:** a `//native` comment after the first executable line.
  - **Expect:** native false.

<a id="sy-v4"></a>
### SY-V4 · Sync item paths stay relative to the working directory under `noCdToRoot`

**Scope:** later · **Finding:** [SY-V4](openflow.md#sy-v4)

- **SY-V4/a** · source-derived · cwd-relative paths
  - **Given:** config at `/tmp/synthetic-project/wmill.yaml`; working directory
    `/tmp/synthetic-project/sub`; `noCdToRoot=true`; ordinary filters; a flow
    file `/tmp/synthetic-project/sub/f/demo/example.flow/flow.yaml`.
  - **Expect:** local sync item `f/demo/example.flow/flow.yaml`, hence flow path
    `f/demo/example`. A sibling item under `/tmp/synthetic-project/f/` is
    outside the walk. A pull of that flow targets
    `/tmp/synthetic-project/sub/f/demo/example.flow/flow.yaml`.

<a id="pa-1"></a>
### PA-1 · What windmill-ts does, and where it diverges from the CLI

**Scope:** core · **Finding:** [PA-1](openflow.md#pa-1)

- **PA-1/a** · source-derived · two folder styles for one flow
  - **Given:** both `f/a/x.flow/flow.yaml` and `f/a/x__flow/flow.yaml`.
  - **Expect:** windmill-ts: load error naming both files.
- **PA-1/b** · source-derived · lone flat `*.flow.json`
  - **Given:** a tree with only `f/a/x.flow.json`.
  - **Expect:** windmill-ts loads it; the CLI would not push it.
    windmill-lite policy: follow the CLI (ignore it, with a warning).

<a id="pa-2"></a>
### PA-2 · What windmill-ts does not do, and windmill-lite must

**Scope:** core · **Finding:** [PA-2](openflow.md#pa-2)

- **PA-2/a** · source-derived · tag and quoted forms are equivalent
  - **Given:** `content: !inline a.inline_script.ts` (unquoted tag) and
    `content: '!inline a.inline_script.ts'` (quoted).
  - **Expect:** both resolve to the file's text.

<a id="pa-3"></a>
### PA-3 · Schema clean-ups learned from prior art

**Scope:** later · **Finding:** [PA-3](openflow.md#pa-3)

- **PA-3/a** · source-derived · tolerant generator
  - **Given:** schema `{properties: {x: {type: string, enum: null, default: null}}}`.
  - **Expect:** the generator emits an optional string, with no error.

<a id="execution"></a>
## Flow execution

Findings: [execution.md](execution.md).

<a id="execution-notation"></a>
### Notation

- Flows are OpenFlow, abbreviated. `[a: …, b: …]` is the `value.modules`
  list with module ids `a`, `b`. Module fields follow a step in parentheses,
  for example `(retry: …)` or `(continue_on_error: true)`.
- `bun: <body>` is a synthetic Bun `rawscript` step whose `main` does what
  the body says; `bun(x, y)` names its parameters. `python(x):` and `deno:`
  are the same for Python and Deno.
- `x = "<expr>"` is the input transform `{type: javascript, expr: "<expr>"}`.
  Static transforms are written in full: `{type: static, value: …}`.
- "Handler" is the flow's `failure_module`.
- `expect.result` is the final flow result as returned by the "run and wait
  for result" API; `expect.success` is the flow job's success flag;
  `expect.status` is its completed status (`success` | `failure` |
  `skipped` | `canceled`).
- `<any>` marks values the harness normalises (stacks, timestamps, ids).
  `<starts "…">` asserts a prefix only. Durations are lower bounds on wall
  time unless a case says "about".
- `<loc>` stands for one Rust location suffix **including its leading
  space**: ` @<file>.rs:<line>:<col>`, for example ` @worker.rs:3822:13`
  ([FX-50](execution.md#fx-50)), or the parenthesised not-found form
  ` (<file>.rs:<line>)`. Nothing else separates it from the text before it,
  and two suffixes are written `<loc><loc>`. The location text is the source position at
  the pin, so cases never assert it ([0001](../design/0001-equivalence-contract.md)
  §7 matches error text except location suffixes and stacks).
- Cases recorded by batches E3b, E3c and E3d (design record 0012) are
  `oracle-observed` and name their recording; the others are still
  source-derived, and the open questions in
  [execution.md](execution.md#open-questions) say which recording settles
  each.

<a id="fx-1"></a>
### FX-1 · Flow run state: a cursor plus one status per module

**Scope:** core · **Finding:** [FX-1](execution.md#fx-1)

- **FX-1/a** · source-derived · Two successful steps.
  - **Given:** `[a: bun: return 1, b: bun: return 2]`, input `{}`.
  - **Expect:** `expect.result = 2`. Flow status: `a: Success{skipped: false}`,
    `b: Success{skipped: false}`.

<a id="fx-3"></a>
### FX-3 · A flow with no modules returns its input

**Scope:** core · **Finding:** [FX-3](execution.md#fx-3)

- **FX-3/a** · oracle-observed · An empty flow returns its input.
  - **Given:** `{"modules": []}`, input `{"b": 1, "a": 2}`.
  - **Expect:** `{success: true, result: {"a": 2, "b": 1}}`; keys come back in
    jsonb order. The flow status holds one `modules` entry, a `Success` with
    id `failure` and a nil job.
  - **Recorded:** [`flows/execution/fx-36`](../../conformance/flows/execution/fx-36)
    run `empty`, oracle run 40685766017.

<a id="fx-4"></a>
### FX-4 · Trailing virtual identity step when the last module sleeps or suspends

**Scope:** core · **Finding:** [FX-4](execution.md#fx-4)

- **FX-4/a** · source-derived · A sleep on the last step delays completion.
  - **Given:** `[a: bun: return "x" (sleep: {type: static, value: 1})]`.
  - **Expect:** `{success: true, result: "x"}`; wall time at least 1 s; flow
    status has modules `a` and `a-v`.

<a id="fx-5"></a>
### FX-5 · Inline bodies versus subflows by path

**Scope:** core · **Finding:** [FX-5](execution.md#fx-5)

- **FX-5/a** · source-derived · later · A subflow by path never runs its
  preprocessor.
  - **Given:** a deployed child flow whose preprocessor would return
    `{"x": 0}`; a parent calls it through a `flow` module that passes `x = 5`.
  - **Expect:** the child sees `flow_input.x === 5`.

<a id="fx-6"></a>
### FX-6 · What `previous_result` is

**Scope:** core · **Finding:** [FX-6](execution.md#fx-6)

- **FX-6/a** · oracle-observed · Step 0 sees the flow input.
  - **Given:** `[a: bun(x): return x]` with `x = "previous_result"`, input
    `{"k": 1}`.
  - **Expect:** `expect.result = {"k": 1}`.
  - **Recorded:** [`flows/execution/fx-6`](../../conformance/flows/execution/fx-6) run `first_step`, oracle run 40685013818.
- **FX-6/b** · oracle-observed · After a tolerated failure, the next step sees
  the wrapped error.
  - **Given:** `[a: bun: throw new Error("e") (continue_on_error: true), b:
    bun(p): return p]` with `p = "previous_result"`.
  - **Expect:** `{success: true, result: {"error": {"message": "e", "name":
    "Error", "stack": <any>, "step_id": "a"}}}`.
  - **Recorded:** [`flows/execution/fx-6`](../../conformance/flows/execution/fx-6) run `after_tolerated`, oracle run 40685013818.

<a id="fx-7"></a>
### FX-7 · Step input transforms

**Scope:** core · **Finding:** [FX-7](execution.md#fx-7)

- **FX-7/a** · oracle-observed · JavaScript and static transforms.
  - **Given:** `[a: bun(x, y): return {x, y}]` with `x = "flow_input.n * 2"`
    and `y = {type: static, value: [1, "s"]}`, input `{"n": 3}`.
  - **Expect:** `expect.result = {"x": 6, "y": [1, "s"]}`.
  - **Recorded:** [`flows/execution/fx-7`](../../conformance/flows/execution/fx-7) run `js_and_static`, oracle run 40685013818.
- **FX-7/b** · oracle-observed · `params` is empty.
  - **Given:** a transform `expr: "params"`.
  - **Expect:** the argument is `{}`.
  - **Recorded:** [`flows/execution/fx-7`](../../conformance/flows/execution/fx-7) run `params_and_undefined`, oracle run 40685013818.
- **FX-7/c** · oracle-observed · `undefined` becomes `null`.
  - **Given:** a transform `expr: "undefined"`.
  - **Expect:** the argument is `null`, and the script receives `null`.
  - **Recorded:** [`flows/execution/fx-7`](../../conformance/flows/execution/fx-7) run `params_and_undefined`, oracle run 40685013818.
- **FX-7/d** · oracle-observed · `result` is not defined in a transform.
  - **Given:** `[a: bun(x): return x]` with `x = "result"`.
  - **Expect:** `a` fails before it runs, with empty args, no `step_id`, and
    ``{"error": {"name": "ExecutionErr", "message": "Error during isolated evaluation of expression `result`:\nQuickJS evaluation error: Error: result is not defined\n    at <anonymous> (eval_script:1:1)\n    at <eval> (eval_script:1:35)\n"}}``;
    the flow fails with the same value.
  - **Recorded:** [`flows/execution/fx-7`](../../conformance/flows/execution/fx-7) run `result_unbound`, oracle run 40686049913.
  - **Correction 2026-10-28:** the case asserted the prefix and an engine
    `ReferenceError`, leaving the engine text open. The message names no
    class: the embedding renders every engine exception as `Error:` plus its
    message and stack ([FX-7](execution.md#fx-7),
    [JE-21](expressions.md#je-21)).

<a id="fx-8"></a>
### FX-8 · Scope of each flow-level expression

**Scope:** core · **Finding:** [FX-8](execution.md#fx-8)

- **FX-8/a** · oracle-observed · In `stop_after_if`, `result` and
  `previous_result` are the step's own result.
  - **Given:** `[a: bun: return 5 (stop_after_if: {expr: "result === 5 &&
    previous_result === 5", skip_if_stopped: false}), b: bun: return 9]`.
  - **Expect:** `{success: true, result: 5}`; `b` never runs.
  - **Recorded:** [`flows/execution/fx-8`](../../conformance/flows/execution/fx-8) run `stop_scope`, oracle run 40685013818.
- **FX-8/b** · oracle-observed · `results` is undefined in `sleep`.
  - **Given:** `[a: bun: return 1 (sleep: {type: javascript, expr:
    "results.a"}), b: bun: return 2]`.
  - **Expect:** the flow fails at the transition after `a` with a chaining
    error ([FX-15](execution.md#fx-15)): `a` succeeded with `1`, `b` never
    runs, and the flow's result is
    ``{"error": {"name": "InternalError", "message": "Expected a number value, had error instead: ExecutionErr(\"Error during evaluation of expression `results.a`:\\nQuickJS evaluation error: Error: results is not defined\\n    at <anonymous> (eval_script:1:1)\\n    at <eval> (eval_script:1:46)\\n\")"}}``,
    with no `Error: ` prefix and no `<loc>`.
  - **Recorded:** [`flows/execution/fx-8`](../../conformance/flows/execution/fx-8) run `sleep_no_results`, oracle run 40686049913.
  - **Correction 2026-10-28:** the case named the chaining error without its
    text. The recorded message wraps the debug form of the typed evaluation
    error ([FX-8](execution.md#fx-8) evidence), so it has neither the
    `Error: ` prefix nor the location an untyped chaining error carries.

<a id="fx-9"></a>
### FX-9 · Boolean predicates use JavaScript truthiness

**Scope:** core · **Finding:** [FX-9](execution.md#fx-9)

- **FX-9/a** · oracle-observed · A non-empty string is true.
  - **Given:** `skip_if: {expr: "'false'"}` on a step; also `[]` and `({})`.
  - **Expect:** the step is skipped in each.
  - **Recorded:** [`flows/execution/fx-9`](../../conformance/flows/execution/fx-9) run `values`, oracle run 40685013818.
- **FX-9/b** · oracle-observed · `0` is false.
  - **Given:** `skip_if: {expr: "0"}` on a step; also `''`, `null`,
    `undefined`, `NaN` and `false`.
  - **Expect:** the step runs in each.
  - **Recorded:** [`flows/execution/fx-9`](../../conformance/flows/execution/fx-9) run `values`, oracle run 40685013818.

<a id="fx-10"></a>
### FX-10 · `results.<id>` resolution routes

**Scope:** core · **Finding:** [FX-10](execution.md#fx-10)

- **FX-10/a** · oracle-observed · A failure module reads an error-stopped step
  three ways.
  - **Given:** `[a: bun: return {"x": 1} (stop_after_if: {expr: "true",
    skip_if_stopped: false, error_message: "E"})]`; handler transforms
    `p = "results.a"`, `x = "results.a.x"`, `k = "Object.keys(results.a)"`.
  - **Expect:** `p = {"error": {"name": "EarlyStopError", "message": "E"}}`;
    `x = 1`; `k = ["error"]`.
  - **Recorded:** [`flows/execution/fx-10`](../../conformance/flows/execution/fx-10)
    run `handler_reads`, oracle run 40684097626; and
    [`flows/execution/fx-v6`](../../conformance/flows/execution/fx-v6) run
    `three_ways`, oracle run 40685766017.
- **FX-10/b** · oracle-observed · An unknown id gives `null`.
  - **Given:** a transform `results.zzz` in a flow with no step `zzz`.
  - **Expect:** `null`.
  - **Recorded:** [`flows/execution/fx-10`](../../conformance/flows/execution/fx-10)
    run `unknown_id`, oracle run 40684097626, which also shows `null` for the
    same id in the engine and for a step with no job yet, by both routes.
- **FX-10/c** · source-derived · The empty-id spelling reads the step's own
  result inside `stop_after_if`.
  - **Given:** `[a: bun: return {"k": 1} (stop_after_if: {expr:
    "results[\"\"].k === 1", skip_if_stopped: false}), b: bun: return 9]`.
  - **Expect:** a clean stop: `{success: true, result: {"k": 1}}`; `b`
    never runs. The predicate's preceding id is empty, so `results[""]`
    binds `previous_result`, here `a`'s own result, and the proxy returns it.

<a id="fx-11"></a>
### FX-11 · `flow_env`

**Scope:** later · **Finding:** [FX-11](execution.md#fx-11)

- **FX-11/a** · source-derived · `flow_env` is visible, also in a loop body.
  - **Given:** a flow with `flow_env: {"K": "v"}` and `[a: bun(x): return x]`
    with `x = "flow_env.K"`; repeat with the step inside a loop body.
  - **Expect:** `x = "v"` in both.
- **FX-11/b** · source-derived · A failed resolution falls back to the
  literal.
  - **Given:** `flow_env: {"K": "$var:u/x/missing"}` and a transform
    `x = "flow_env.K"`.
  - **Expect:** `x = "$var:u/x/missing"`; the flow does not fail.

<a id="fx-12"></a>
### FX-12 · Failure values of script and worker jobs

**Scope:** core · **Finding:** [FX-12](execution.md#fx-12)

- **FX-12/a** · oracle-observed · Bun Error with an extra property.
  - **Given:** `[a: bun: throw Object.assign(new Error("boom"), {code: 7})]`.
  - **Expect:** `{success: false, status: "failure", result: {"error":
    {"message": "boom", "name": "Error", "stack": <any>, "step_id": "a",
    "extra": {"code": 7}}}}`. Over HTTP, run-and-wait returns status 422.
  - **Recorded:** [`flows/execution/fx-12`](../../conformance/flows/execution/fx-12)
    run `error_extra`, oracle run 40685766017, except the HTTP 422 half,
    which batch E3e records with FX-36's HTTP part.
- **FX-12/b** · oracle-observed · Bun plain-object throw omits absent keys.
  - **Given:** `[a: bun: throw {message: "boom", code: 7}]`.
  - **Expect:** `{"error": {"message": "boom", "step_id": "a", "extra":
    {"code": 7}}}`, with no `name` and no `stack`.
  - **Recorded:** [`flows/execution/fx-12`](../../conformance/flows/execution/fx-12)
    run `object_throw`, oracle run 40685766017.
- **FX-12/c** · oracle-observed · later · Deno collects no `extra`.
  - **Given:** a Deno step `throw Object.assign(new Error("boom"), {code: 7})`.
  - **Expect:** the error object has no `extra`.
  - **Recorded:** [`flows/execution/fx-12-deno`](../../conformance/flows/execution/fx-12-deno)
    run `error_extra`, oracle run 40685766017.
- **FX-12/d** · oracle-observed · A failed flow's result is not wrapped.
  - **Given:** a flow with a failing step and a handler `bun: return "H"`.
  - **Expect:** the flow fails with result `"H"`, not `{"error": "H"}`.
  - **Recorded:** [`flows/execution/fx-12`](../../conformance/flows/execution/fx-12)
    run `handled`, oracle run 40685766017.

<a id="fx-13"></a>
### FX-13 · A successful result with `wm_failure` is turned into a failure

**Scope:** later · **Finding:** [FX-13](execution.md#fx-13)

- **FX-13/a** · oracle-observed · `wm_failure` marks a ManualFailure.
  - **Given:** `[a: bun: return {wm_failure: "nope", x: 1}]`.
  - **Expect:** `{success: false, result: {"x": 1, "error": {"name":
    "ManualFailure", "message": "nope"}, "wm_failure": "nope"}}` (jsonb key
    order).
  - **Recorded:** [`flows/execution/fx-13`](../../conformance/flows/execution/fx-13)
    run `manual`, oracle run 40685766017.

<a id="fx-14"></a>
### FX-14 · An input-transform error fails that step; a retry runs with empty args

**Scope:** core · **Finding:** [FX-14](execution.md#fx-14)

- **FX-14/a** · oracle-observed · A throwing transform fails the step before
  it runs.
  - **Given:** `[a: bun(x): return x]` with `x = "flow_input.a.b.c"`, input
    `{}`.
  - **Expect:**
    `` {success: false, result: {"error": {"name": "ExecutionErr", "message": <starts "Error during isolated evaluation of expression `flow_input.a.b.c`:\n">}}} ``.
    With a handler present, the handler runs. The recorded text after the
    prefix is `QuickJS evaluation error: Error: cannot read property 'b' of undefined`
    followed by the engine's frames ([FX-14](execution.md#fx-14)).
  - **Recorded:** [`flows/execution/fx-14`](../../conformance/flows/execution/fx-14)
    runs `bad_transform` and `bad_transform_handled`, oracle run 40685766017.
- **FX-14/b** · oracle-observed · The retry runs the script with empty args.
  - **Given:** `[a: bun(x = 0): return 7 (retry: {constant: {attempts: 1,
    seconds: 0}})]` with `x = "null.x"`.
  - **Expect:** `{success: true, result: 7}`. Attempt 1 fails before running;
    attempt 2 runs with `{}`.
  - **Recorded:** [`flows/execution/fx-14`](../../conformance/flows/execution/fx-14)
    run `retry_empty`, oracle run 40685766017.

<a id="fx-15"></a>
### FX-15 · Chaining errors: three contexts

**Scope:** core · **Finding:** [FX-15](execution.md#fx-15)

- **FX-15/a** · oracle-observed · Later transition: `InternalError`, no
  handler.
  - **Given:** `[a: bun: return 1, b: bun: return 2 (skip_if: {expr:
    "null.x"})]` with handler `bun: return "handled"`.
  - **Expect:** `{success: false, result: {"error": {"name": "InternalError",
    "message": <starts "Error: ">}}}`; the handler does not run. The
    recorded message is `Error: QuickJS evaluation error: Error: cannot read property 'x' of null\n    at <anonymous> (eval_script:1:35)\n    at <eval> (eval_script:1:44)\n<loc>`
    ([FX-50](execution.md#fx-50)).
  - **Recorded:** [`flows/execution/fx-15`](../../conformance/flows/execution/fx-15)
    run `later_transition`, oracle run 40685766017.
- **FX-15/b** · oracle-observed · Root pickup: an `Anyhow` module failure that
  reaches the handler.
  - **Given:** `[a: bun: return 1 (timeout: {type: javascript, expr: "-1"})]`
    with handler `bun(e): return {n: e.name, m: e.message}` and
    `e = "error"`.
  - **Expect:** `{success: false, result: {"n": "Anyhow", "m": <starts
    "Error: Timeout value cannot be negative">}}`. The recorded message is
    `Error: Timeout value cannot be negative<loc>`; `a`'s status entry is a
    `Failure` with no job.
  - **Recorded:** [`flows/execution/fx-15`](../../conformance/flows/execution/fx-15)
    run `root_pickup`, oracle run 40685766017.
- **FX-15/c** · oracle-observed · Child pickup bypasses the child's handler.
  - **Given:** parent `[s: flow(path: f/child)]` with handler `bun: return
    "P"`; deployed child `f/child` = `[c: bun: return 1 (skip_if: {expr:
    "null.x"})]` with handler `bun: return "C"`.
  - **Expect:** the child job fails with `{"error": {"name": "Anyhow", …}}`;
    the child's handler never runs; the parent result is `"P"`, failed. The
    child message has two prefixes and two suffixes ([FX-50](execution.md#fx-50)).
  - **Recorded:** [`flows/execution/fx-15`](../../conformance/flows/execution/fx-15)
    run `child_pickup`, oracle run 40685766017.

<a id="fx-16"></a>
### FX-16 · `identity` module

**Scope:** core · **Finding:** [FX-16](execution.md#fx-16)

- **FX-16/a** · oracle-observed · Identity with direct input.
  - **Given:** `[a: identity (pass_flow_input_directly: true)]`, input
    `{"x": 1}`.
  - **Expect:** `expect.result = {}`; the identity's args are `{"x": 1}`.
  - **Recorded:** [`flows/execution/fx-16`](../../conformance/flows/execution/fx-16) run `direct_input`, oracle run 40684097626.
- **FX-16/b** · oracle-observed · Identity passes the preceding result.
  - **Given:** `[a: bun: return [1], b: identity]`.
  - **Expect:** `expect.result = [1]`; `b`'s args are
    `{"previous_result": [1]}`.
  - **Recorded:** [`flows/execution/fx-16`](../../conformance/flows/execution/fx-16) run `passthrough`, oracle run 40684097626.

<a id="fx-17"></a>
### FX-17 · `skip_if`

**Scope:** core · **Finding:** [FX-17](execution.md#fx-17)

- **FX-17/a** · oracle-observed · A skipped step passes its input through.
  - **Given:** `[a: bun: return 1, b: bun: return 2 (skip_if: {expr:
    "previous_result === 1"}), c: bun(x): return x]` with
    `x = "previous_result"`.
  - **Expect:** `expect.result = 1`; `b: Success{skipped: true}`; flow status
    `success`.
  - **Recorded:** [`flows/execution/fx-17`](../../conformance/flows/execution/fx-17)
    run `skip_passthrough`, oracle run 40685013818.
- **FX-17/b** · oracle-observed · A module skipped only on retry returns `{}`.
  - **Given:** step `a` with `x = {type: static, value: 1}`, `retry:
    {constant: {attempts: 1, seconds: 0}}` and `skip_if: {expr:
    "variable('u/t/flag') === 'skip'"}`; the script throws. The variable reads
    `"run"` at the first evaluation and is set to `"skip"` before the retry
    (requires changing the variable mid-run).
  - **Expect:** `a` is skipped on the retry and returns `{}` (the reused args
    `{"x": 1}` have no `previous_result`).
  - **Recorded:** [`flows/execution/fx-17`](../../conformance/flows/execution/fx-17)
    run `skip_on_retry`, oracle run 40685013818; a preceding step resets the
    variable, and the script sets it to `"skip"` before throwing.

<a id="fx-18"></a>
### FX-18 · `mock`

**Scope:** later · **Finding:** [FX-18](execution.md#fx-18)

- **FX-18/a** · oracle-observed · A mocked step does not run.
  - **Given:** `[a: bun: throw new Error("x") (mock: {enabled: true,
    return_value: {"m": 1}})]`.
  - **Expect:** `{success: true, result: {"m": 1}}`; `a` ran as an identity
    job with args `{"previous_result": {"m": 1}}`.
  - **Recorded:** [`flows/execution/fx-18`](../../conformance/flows/execution/fx-18) run `mocked`, oracle run 40685013818.

<a id="fx-19"></a>
### FX-19 · `stop_after_if` on a step, with its interactions

**Scope:** core · **Finding:** [FX-19](execution.md#fx-19)

In FX-19/d to FX-19/i, `a` is `bun: return 3` with `stop_after_if: {expr:
"true", skip_if_stopped: false, error_message: "E"}`, and the handler, where
present, is `bun: return "H"`.

- **FX-19/a** · oracle-observed · Clean stop with `skip_if_stopped`.
  - **Given:** `[a: bun: return 3 (stop_after_if: {expr: "result > 2",
    skip_if_stopped: true}), b: bun: return 4]`.
  - **Expect:** `{success: true, status: "skipped", result: 3}`; `a` is
    `Success{skipped: true}` and `b` never runs.
  - **Recorded:** [`flows/execution/fx-19`](../../conformance/flows/execution/fx-19) run `clean_skipped`, oracle run 40685013818.
- **FX-19/b** · oracle-observed · Error-stop including the result.
  - **Given:** `[a: bun: return 3 (stop_after_if: {expr: "result > 2",
    skip_if_stopped: false, error_message: "too big", error_include_result:
    true})]`.
  - **Expect:** `{success: false, result: {"error": {"name": "EarlyStopError",
    "message": "too big", "result": 3}}}`; `a`'s module status is `Failure`
    while its stored job result stays `3` ([FX-V2](execution.md#fx-v2)).
  - **Recorded:** [`flows/execution/fx-19`](../../conformance/flows/execution/fx-19) run `error_include`, oracle run 40685013818.
- **FX-19/c** · oracle-observed · Empty `error_message`.
  - **Given:** FX-19/b with `error_message: ""`.
  - **Expect:** the message is `"stop after if: result > 2"`.
  - **Recorded:** [`flows/execution/fx-19`](../../conformance/flows/execution/fx-19) run `empty_message`, oracle run 40685013818.
- **FX-19/d** · oracle-observed · An error-stop is retried before the handler.
  - **Given:** `[a (retry: {constant: {attempts: 1, seconds: 0}})]` plus the
    handler.
  - **Expect:** `a` runs twice, then the handler runs; `{success: false,
    result: "H"}`. `a`'s `Failure` status lists its first attempt in
    `failed_retries`, and so does the handler's `Success` status, beside
    `parent_module: "a"` ([FX-27](execution.md#fx-27)).
  - **Recorded:** [`flows/execution/fx-19`](../../conformance/flows/execution/fx-19) run `retry_then_handler`, oracle run 40686049913.
  - **Correction 2026-10-28:** the case did not mention the handler's
    `failed_retries`. The list is flow-wide and is cleared only after a
    module succeeds, so the handler's status repeats `a`'s attempt.
- **FX-19/e** · oracle-observed · A tolerated error-stop advances to the next
  step.
  - **Given:** `[a (continue_on_error: true), b: bun: return 4]` plus the
    handler.
  - **Expect:** `b` runs and the handler does not; `{success: true, result:
    4}`.
  - **Recorded:** [`flows/execution/fx-19`](../../conformance/flows/execution/fx-19) run `tolerated_next`, oracle run 40685013818.
- **FX-19/f** · oracle-observed · A tolerated error-stop at the last step
  without a handler succeeds.
  - **Given:** `[a (continue_on_error: true)]`, no handler.
  - **Expect:** `{success: true, status: "success", result: {"error":
    {"name": "EarlyStopError", "message": "E"}}}`.
  - **Recorded:** [`flows/execution/fx-19`](../../conformance/flows/execution/fx-19) run `tolerated_last`, oracle run 40685013818.
- **FX-19/g** · oracle-observed · A tolerated error-stop before a later step,
  without a handler, fails.
  - **Given:** `[a (continue_on_error: true), b: bun: return 4]`, no handler.
  - **Expect:** `{success: false, result: {"error": {"name":
    "EarlyStopError", "message": "E"}}}`; `b` never runs.
  - **Recorded:** [`flows/execution/fx-19`](../../conformance/flows/execution/fx-19) run `tolerated_next_no_handler`, oracle run 40685013818.
- **FX-19/h** · oracle-observed · Without a handler, retry is not used.
  - **Given:** `[a (retry: {constant: {attempts: 1, seconds: 0}})]`, no
    handler, no tolerance.
  - **Expect:** `a` runs once; `{success: false, result: {"error": {"name":
    "EarlyStopError", "message": "E"}}}`.
  - **Recorded:** [`flows/execution/fx-19`](../../conformance/flows/execution/fx-19) run `retry_no_handler`, oracle run 40685013818.
- **FX-19/i** · oracle-observed · A tolerated error-stop at the final module
  enters the handler ([FX-V4](execution.md#fx-v4)).
  - **Given:** `[a (continue_on_error: true)]`, no retry, plus the handler.
  - **Expect:** `{success: false, result: "H"}`; the handler runs once.
  - **Recorded:** [`flows/execution/fx-19`](../../conformance/flows/execution/fx-19) run `tolerated_last_handler`, oracle run 40685013818.

<a id="fx-20"></a>
### FX-20 · How a stop-early propagates through containers

**Scope:** core · **Finding:** [FX-20](execution.md#fx-20)

- **FX-20/a** · oracle-observed · A stop in a branch body stops the parent.
  - **Given:** `[br: branchone(default: [x: bun: return 1 (stop_after_if:
    {expr: "true", skip_if_stopped: true})]), y: bun: return 2]`.
  - **Expect:** `{status: "skipped", result: 1}`; `y` never runs. `br`'s own
    status is `Success{skipped: true}` with `branch_chosen` `default`.
  - **Recorded:** [`flows/execution/fx-20`](../../conformance/flows/execution/fx-20) run `branch_stop`, oracle run 40686049913.

<a id="fx-21"></a>
### FX-21 · A `stop_after_if` that throws fails the step unrecoverably

**Scope:** core · **Finding:** [FX-21](execution.md#fx-21)

- **FX-21/a** · oracle-observed · A string error that tolerance cannot catch.
  - **Given:** `[a: bun: return 1 (stop_after_if: {expr: "null.x",
    skip_if_stopped: false}, continue_on_error: true), b: bun: return 2]`.
  - **Expect:** `{success: false, result: {"error": "Error: QuickJS evaluation error: Error: cannot read property 'x' of null\n    at <anonymous> (eval_script:1:35)\n    at <eval> (eval_script:1:44)\n<loc>"}}`;
    `b` never runs; `a`'s stored job result stays `1`.
  - **Recorded:** [`flows/execution/fx-21`](../../conformance/flows/execution/fx-21) run `throwing_stop`, oracle run 40686049913; the raw `<loc>` was ` @worker_flow.rs:2451:11`.
  - **Correction 2026-10-28:** the case asserted only a string. The string is
    the untyped error's display ([FX-21](execution.md#fx-21)): `Error: `, the
    evaluation error with the engine's `TypeError` shown as `Error:`, the
    stack, then a location, written `<loc>` so that a comparison can strip it.

<a id="fx-22"></a>
### FX-22 · Flow-level `skip_expr`

**Scope:** later · **Finding:** [FX-22](execution.md#fx-22)

- **FX-22/a** · oracle-observed · A true `skip_expr` skips the flow.
  - **Given:** a flow with `skip_expr: "flow_input.skip"`, input
    `{"skip": true}`.
  - **Expect:** `{success: true, status: "skipped", result: "stopped early"}`.
  - **Recorded:** [`flows/je/sites`](../../conformance/flows/je/sites) run
    `je33-a` (the same flow, recorded as JE-33/a), oracle run 40687124625.

<a id="fx-23"></a>
### FX-23 · `sleep`

**Scope:** core · **Finding:** [FX-23](execution.md#fx-23)

- **FX-23/a** · source-derived · A JavaScript sleep of the step's own result.
  - **Given:** `[a: bun: return 2 (sleep: {type: javascript, expr:
    "result"}), b: bun: return Date.now()]`.
  - **Expect:** `b` starts at least 2 s after `a` ends.
- **FX-23/b** · source-derived · A fractional static sleep.
  - **Given:** `sleep: {type: static, value: 0.25}` on a step.
  - **Expect:** a delay of at least 250 ms before the next step.
- **FX-23/c** · source-derived · A string sleep value is a chaining error.
  - **Given:** `sleep: {type: static, value: "1"}` on a step.
  - **Expect:** the flow fails with `{"error": {"name": "InternalError",
    "message": "Expected an integer, found: String(\"1\")"}}`.

<a id="fx-24"></a>
### FX-24 · Per-step `timeout`: value, cap and enforcement by execution path

**Scope:** core · **Finding:** [FX-24](execution.md#fx-24)

- **FX-24/a** · source-derived · A process timeout.
  - **Given:** `[a: bun: await sleep(3000) (timeout: 1)]`.
  - **Expect:** the [FX-25](execution.md#fx-25) error, with "job-specific".
- **FX-24/b** · source-derived · A negative timeout at root pickup.
  - **Given:** `[a: bun: return 1 (timeout: {type: javascript, expr: "-1"})]`.
  - **Expect:** an `Anyhow` failure of `a` (as in FX-15/b).
- **FX-24/c** · source-derived · A negative timeout at a later transition.
  - **Given:** `[x: bun: return 1, a: bun: return 1 (timeout: {type:
    javascript, expr: "-1"})]`.
  - **Expect:** the `InternalError` `"Timeout value cannot be negative"`.
- **FX-24/d** · source-derived · A collapsed loop applies the loop's timeout
  per iteration; a preview does not collapse.
  - **Given:** deployed `[l: forloopflow(iterator: [1], parallel: false,
    timeout: 1, modules: [x: bun: await sleep(3000) (timeout: 10)])]`; then
    the same flow as a preview.
  - **Expect:** deployed, the iteration times out after about 1 s. As a
    preview, the outer timeout is not enforced and the iteration runs 3 s.
- **FX-24/e** · source-derived · A parallel loop's timeout is not enforced.
  - **Given:** `[l: forloopflow(iterator: [1], parallel: true, timeout: 1,
    modules: [x: bun: await sleep(3000)])]`.
  - **Expect:** completes after about 3 s without a timeout.
- **FX-24/f** · source-derived · Identity jobs are untimed.
  - **Given:** `[a: identity (timeout: 0)]`, input `{"x": 1}`.
  - **Expect:** `{success: true, result: {"x": 1}}`.
- **FX-24/g** · source-derived · later · An in-process executor timeout.
  - **Given:** an in-process step with `timeout: 1` that runs for 3 s.
  - **Expect:** `{"error": {"name": "ExecutionErr", "message": "execution
    error:\nQuery timeout after (>1s)", "step_id": <id>}}`.

<a id="fx-25"></a>
### FX-25 · Process timeout: enforcement and error

**Scope:** core · **Finding:** [FX-25](execution.md#fx-25)

- **FX-25/a** · source-derived · A timed-out step is an ordinary, tolerable
  failure.
  - **Given:** `[a: bun: await new Promise(r => setTimeout(r, 5000))
    (timeout: 1, continue_on_error: true), b: bun(p): return p]` with
    `p = "previous_result"`.
  - **Expect:** `{success: true, result: {"error": {"name": "ExecutionErr",
    "message": "execution error:\njob process terminated due to timeout
    after exceeding job-specific duration limit", "step_id": "a"}}}`.

<a id="fx-26"></a>
### FX-26 · `retry`: the delay schedule and its exact arithmetic

**Scope:** core · **Finding:** [FX-26](execution.md#fx-26)

- **FX-26/a** · source-derived · Constant then exponential phases.
  - **Given:** `retry: {constant: {attempts: 2, seconds: 1}, exponential:
    {attempts: 2, multiplier: 1, seconds: 2}}` on a step that always throws.
  - **Expect:** 5 runs in total, with delays of 1, 1, 8 (2³) and 16 (2⁴)
    seconds; the final result is the 5th failure's wrapped error.
- **FX-26/b** · source-derived · A multiplier.
  - **Given:** `exponential: {attempts: 1, multiplier: 3, seconds: 2}`.
  - **Expect:** a single delay of 6 s.
- **FX-26/c** · source-derived · 16-bit wrap.
  - **Given:** `exponential: {attempts: 1, multiplier: 250, seconds: 300}`.
  - **Expect:** a first delay of `(250 × 300) mod 65536 = 9464` s, observed
    from the retry job's scheduled time (no need to wait).
- **FX-26/d** · source-derived · The 6-hour cap.
  - **Given:** `exponential: {attempts: 1, multiplier: 1, seconds: 30000}`.
  - **Expect:** a delay of 21600 s.
- **FX-26/e** · source-derived · Saturation.
  - **Given:** `exponential: {attempts: 2, multiplier: 1, seconds: 300}`.
  - **Expect:** the second delay is `min(65535, 21600) = 21600` s.

<a id="fx-27"></a>
### FX-27 · `retry`: what is retried and how state evolves

**Scope:** core · **Finding:** [FX-27](execution.md#fx-27)

- **FX-27/a** · source-derived · A retry reuses the failed attempt's args.
  - **Given:** step `a: bun(x): if (await counter() === 0) throw …; return x`
    with `x = "flow_input.n"` and `retry: {constant: {attempts: 1, seconds:
    0}}`; it fails once, then succeeds.
  - **Expect:** `expect.result` is the value of `flow_input.n`; the step has
    1 failed retry; a `skip_if`, if present, was evaluated twice.

<a id="fx-28"></a>
### FX-28 · `retry_if`

**Scope:** core · **Finding:** [FX-28](execution.md#fx-28)

- **FX-28/a** · source-derived · A false `retry_if` prevents the retry.
  - **Given:** `retry: {constant: {attempts: 3, seconds: 0}, retry_if: {expr:
    "error.message === 'transient'"}}` on a step that throws
    `new Error("fatal")`.
  - **Expect:** exactly 1 run; the flow fails with that error.
- **FX-28/b** · source-derived · A throwing `retry_if` during completion, with
  a handler.
  - **Given:** `retry: {constant: {attempts: 1, seconds: 0}, retry_if: {expr:
    "null.x"}}` on a step that throws, with a handler.
  - **Expect:** the handler runs and sees `{"error": <string>}`.
- **FX-28/c** · source-derived · The same without a handler.
  - **Given:** FX-28/b without a handler.
  - **Expect:** the flow fails with `{"error": <string>}`.

<a id="fx-29"></a>
### FX-29 · Quirk: the retry counter carries over after a tolerated failure

**Scope:** core · **Finding:** [FX-29](execution.md#fx-29)

- **FX-29/a** · source-derived · The counter carries over.
  - **Given:** `[a: bun: throw (retry: {constant: {attempts: 2, seconds: 0}},
    continue_on_error: true), b: bun: throw (retry: {constant: {attempts: 2,
    seconds: 0}})]`.
  - **Expect:** `a` runs 3 times and `b` **once**; the flow fails with `b`'s
    error.
- **FX-29/b** · source-derived · A successful step in between resets it.
  - **Given:** `[a: (as in FX-29/a), s: bun: return 0, b: (as in FX-29/a)]`.
  - **Expect:** `b` runs 3 times; the flow still fails. `s`'s `Success`
    status lists `a`'s two failed attempts in `failed_retries`, carried over
    with the counter ([FX-27](execution.md#fx-27)).

<a id="fx-30"></a>
### FX-30 · `continue_on_error`

**Scope:** core · **Finding:** [FX-30](execution.md#fx-30)

- **FX-30/a** · oracle-observed · A tolerated last-step failure succeeds
  without the handler.
  - **Given:** `[a: bun: throw new Error("x") (continue_on_error: true)]`
    with a handler.
  - **Expect:** `{success: true, status: "success", result: {"error":
    {"message": "x", "name": "Error", "stack": <any>, "step_id": "a"}}}`; the
    handler never runs. Contrast FX-19/i.
  - **Recorded:** [`flows/execution/fx-30`](../../conformance/flows/execution/fx-30)
    run `tolerated_last`, oracle run 40685766017.

<a id="fx-31"></a>
### FX-31 · `failure_module`: when it runs

**Scope:** core · **Finding:** [FX-31](execution.md#fx-31)

- **FX-31/a** · oracle-observed · An untolerated failure runs the handler.
  - **Given:** `[a: bun: throw new Error("x"), b: bun: return 1]` with
    `failure_module: {id: "failure", value: bun(e): return {seen:
    e.message}}` and `e = "error"`.
  - **Expect:** `{success: false, status: "failure", result: {"seen": "x"}}`;
    `b` never runs.
  - **Recorded:** [`flows/execution/fx-31`](../../conformance/flows/execution/fx-31)
    run `untolerated`, oracle run 40685766017.

<a id="fx-32"></a>
### FX-32 · `failure_module`: what it sees

**Scope:** core · **Finding:** [FX-32](execution.md#fx-32)

- **FX-32/a** · oracle-observed · The handler sees the wrapped error.
  - **Given:** the flow of FX-31/a, with handler transforms
    `x = "previous_result"` and `y = "results.a"`.
  - **Expect:** `x = {"error": {"message": "x", "name": "Error", "stack":
    <any>, "step_id": "a"}}`; `y` is the same value.
  - **Recorded:** [`flows/execution/fx-32`](../../conformance/flows/execution/fx-32)
    run `sees`, oracle run 40685766017, which also shows `error` as the
    inner object and the handler's `WM_FLOW_STEP_ID` as `failure`.
- **FX-32/b** · oracle-observed · A handler retry keeps its own failed value
  and reuses its args.
  - **Given:** a flow whose step fails, with handler `bun(n): throw new
    Error("h" + n)`, `n = "1"`, `retry: {constant: {attempts: 1, seconds:
    0}}` and `skip_if: {expr: "previous_result?.error?.message === 'h1'"}`.
  - **Expect:** the handler's first attempt fails; its retry is skipped; the
    flow fails with the skip passthrough, the reused args' `previous_result`
    field, so `{}` (no `recover`). The handler's status is `Success` with
    `skipped: false`: a skipped failure module is not flagged
    ([FX-17](execution.md#fx-17)).
  - **Recorded:** [`flows/execution/fx-32`](../../conformance/flows/execution/fx-32)
    run `handler_retry`, oracle run 40685766017.

<a id="fx-33"></a>
### FX-33 · `failure_module`: the flow's final result and `recover`

**Scope:** core · **Finding:** [FX-33](execution.md#fx-33)

All seven cases are recorded by one flow whose failure module returns
`flow_input.ret` ([`flows/execution/fx-33`](../../conformance/flows/execution/fx-33),
oracle run 40685766017, one run per case). They settled
[Q-FX-19](execution.md#q-fx-19) for the record decoder: a one-element
array recovers.

- **FX-33/a** · oracle-observed · `recover: true` makes the flow succeed.
  - **Given:** a failing flow whose handler returns `{"recover": true, "v":
    1}`.
  - **Expect:** `{success: true, status: "success", result: {"v": 1,
    "recover": true}}` (jsonb key order).
  - **Recorded:** run `object_true`.
- **FX-33/b** · oracle-observed · A string `"true"` does not recover.
  - **Given:** the handler returns `{"recover": "true"}`.
  - **Expect:** `expect.success = false`.
  - **Recorded:** run `object_string`.
- **FX-33/c** · oracle-observed · A one-element array `[true]`.
  - **Given:** a failing top-level flow whose failure module succeeds and
    returns `[true]`.
  - **Expect:** `{success: true, status: "success", result: [true]}` (the
    array decodes positionally as `recover: true`).
  - **Recorded:** run `array_true`. The superseded object-only reading
    expected a failure.
- **FX-33/d** · oracle-observed · `[false]` does not recover.
  - **Given:** as FX-33/c, but the failure module returns `[false]`.
  - **Expect:** `expect.success = false`, `expect.result = [false]`.
  - **Recorded:** run `array_false`.
- **FX-33/e** · oracle-observed · `[null]` does not recover.
  - **Given:** as FX-33/c, but the failure module returns `[null]`.
  - **Expect:** `expect.success = false`, `expect.result = [null]`.
  - **Recorded:** run `array_null`.
- **FX-33/f** · oracle-observed · `[]` does not recover.
  - **Given:** as FX-33/c, but the failure module returns `[]`.
  - **Expect:** `expect.success = false`, `expect.result = []`.
  - **Recorded:** run `array_empty`.
- **FX-33/g** · oracle-observed · `[true,false]` does not recover.
  - **Given:** as FX-33/c, but the failure module returns `[true,false]`.
  - **Expect:** `expect.success = false`, `expect.result = [true,false]`.
  - **Recorded:** run `array_two`.

<a id="fx-34"></a>
### FX-34 · `failure_module` inside inline bodies runs at the innermost level

**Scope:** core · **Finding:** [FX-34](execution.md#fx-34)

- **FX-34/a** · oracle-observed · The handler runs once per failing
  iteration.
  - **Given:** `[l: forloopflow(iterator: [1, 2], skip_failures: true,
    modules: [x: bun(v): if (v == 1) throw new Error("one"); return v])]`
    with `v = "flow_input.iter.value"`, and handler `bun: return "F"`.
  - **Expect:** `{success: true, result: ["F", 2]}`; the handler ran once,
    inside iteration 0's body flow, as `failure`; the flow's own failure
    module did not run.
  - **Recorded:** [`flows/execution/fx-34`](../../conformance/flows/execution/fx-34)
    run `loop`, oracle run 40685766017; the handler's id by run `step_id`
    (loop at cursor 1, both iterations failing: both copies report
    `WM_FLOW_STEP_ID` `failure`), oracle run 40689442792.
  - **Correction 2026-10-28:** the case said the handler ran as
    `failure-0-0`. A deployed body is a stored flow node whose copy keeps its
    own id; only bodies built from raw modules get the suffix
    ([FX-34](execution.md#fx-34), [CF-V4](control-flow.md#cf-v4)).

<a id="fx-35"></a>
### FX-35 · `preprocessor_module`

**Scope:** later · **Finding:** [FX-35](execution.md#fx-35)

- **FX-35/a** · oracle-observed · A non-object preprocessor result is wrapped.
  - **Given:** a deployed flow with preprocessor `bun preprocessor(event):
    return 5` and `[a: bun(x): return x]` with `x = "flow_input"`, started
    with preprocessing applied.
  - **Expect:** `expect.result = {"value": 5}`. The preprocessor job's
    stored args read `{"reason": "PREPROCESSOR_ARGS_ARE_DISCARDED"}`.
  - **Recorded:** [`flows/execution/fx-35`](../../conformance/flows/execution/fx-35)
    run `pre`, oracle run 40685766017, started through the run API.

<a id="fx-36"></a>
### FX-36 · The flow's final result and the HTTP response

**Scope:** core · **Finding:** [FX-36](execution.md#fx-36)

The finding is `needs-evidence` only for non-object (array) results, which no
case below exercises. None of these expectations is contested; they are
carried as disputed with the finding until [Q-FX-12](execution.md#q-fx-12)
is settled. They are FX-36's HTTP part, which batch E3e records with
run-and-wait; the executor part, which no case here states, is recorded by
[`flows/execution/fx-36`](../../conformance/flows/execution/fx-36) (oracle run
40685766017).

- **FX-36/a** · disputed · later · An object result over HTTP.
  - **Given:** `[a: bun: return {"b": 1, "a": 2}]`.
  - **Expect:** HTTP 200 with body `{"a": 2, "b": 1}`.
- **FX-36/b** · disputed · later · A failed flow over HTTP.
  - **Given:** `[a: bun: throw new Error("z")]`.
  - **Expect:** HTTP 422 with body `{"error": {"message": "z", "name":
    "Error", "stack": <any>, "step_id": "a"}}`.
- **FX-36/c** · disputed · later · A status code and nested result.
  - **Given:** `[a: bun: return {windmill_status_code: 201, result: {ok:
    true}}]`.
  - **Expect:** HTTP 201 with body `{"ok": true}`.
- **FX-36/d** · disputed · later · A status code without a result.
  - **Given:** `[a: bun: return {windmill_status_code: 200}]`.
  - **Expect:** HTTP 200 with an empty body.
- **FX-36/e** · disputed · later · A mistyped status code disables the
  metadata.
  - **Given:** a successful result `{"windmill_status_code": "201", "result":
    7}`.
  - **Expect:** HTTP 200 with the whole object as the body.
- **FX-36/f** · disputed · later · A `null` status code is no metadata.
  - **Given:** a successful result `{"windmill_status_code": null, "result":
    7}`.
  - **Expect:** HTTP 200 with the whole object as the body.
- **FX-36/g** · disputed · later · Metadata present, no non-null result.
  - **Given:** a successful result `{"windmill_headers": {}, "result": null}`.
  - **Expect:** HTTP 204 with an empty body.

<a id="fx-37"></a>
### FX-37 · `early_return`

**Scope:** later · **Finding:** [FX-37](execution.md#fx-37)

- **FX-37/a** · oracle-observed · Run-and-wait returns early; the flow keeps
  running.
  - **Given:** a flow with `early_return: "a"` and `[a: bun: return 1, b:
    bun: await sleep(2000); return 2]`.
  - **Expect:** run-and-wait returns `1` in well under 2 s; the flow later
    completes with result `2`.
  - **Recorded:** [`flows/execution/fx-37`](../../conformance/flows/execution/fx-37)
    run `early`, oracle run 40685766017: the flow's result `2` only. The
    run-and-wait half is recorded by batch E3e.

<a id="fx-38"></a>
### FX-38 · Cancellation

**Scope:** core · **Finding:** [FX-38](execution.md#fx-38)

- **FX-38/a** · oracle-observed · A canceled flow skips the handler.
  - **Given:** `[a: bun: await sleep(10000)]` with a handler, canceled after
    1 s with reason `r` by `u`.
  - **Expect:** `{status: "canceled", success: false, result: {"error":
    {"message": "Job canceled: r by u", "name": "Canceled", "reason": "r",
    "canceler": "u"}}}`; the handler never runs.
  - **Recorded:** [`flows/execution/fx-38`](../../conformance/flows/execution/fx-38)
    run `cancel`, oracle run 40685766017, with a 30 s sleep canceled after
    5 s by `admin` with reason `wltest: cancel_after_ms`: the result has
    those values in the same shape, and `a` itself ended `canceled` with
    `execution error:\njob process terminated due to cancelled by admin (reason: wltest: cancel_after_ms)`.

<a id="fx-39"></a>
### FX-39 · `suspend` / approval

**Scope:** out · **Finding:** [FX-39](execution.md#fx-39)

- **FX-39/a** · policy · windmill-lite rejects a flow that suspends.
  - **Given:** a flow with `suspend: {required_events: 1}` on any module,
    loaded by windmill-lite.
  - **Expect:** windmill-lite's own error `unsupported: suspend`, at load
    time. This tests windmill-lite's policy, not Windmill behaviour; it is
    not an oracle case.

<a id="fx-40"></a>
### FX-40 · `cache_ttl` (step and flow)

**Scope:** later · **Finding:** [FX-40](execution.md#fx-40)

- **FX-40/a** · oracle-observed · A cache hit returns the stored result.
  - **Given:** `[a: bun: return Math.random()]` with `cache_ttl: 60`, run
    twice with the same input.
  - **Expect:** both results are equal.
  - **Recorded:** [`flows/execution/fx-40`](../../conformance/flows/execution/fx-40) run `a`, oracle run 40686532046: one run calls the cached flow twice, and the second call returns the first call's stored result.
- **FX-40/b** · oracle-observed · A failed flow's handler result is cached
  and served as a success.
  - **Given:** a flow with `cache_ttl: 60`, `[a: bun: throw new
    Error("x")]` and a handler returning `"H"`.
  - **Expect:** run 1 gives `{success: false, result: "H"}`; run 2 with the
    same input within 60 s gives `{success: true, result: "H"}`, and neither
    `a` nor the handler runs.
  - **Recorded:** [`flows/execution/fx-40`](../../conformance/flows/execution/fx-40) run `b`, oracle run 40685057858 (both calls inside one run).
- **FX-40/c** · oracle-observed · A collapsed loop takes the loop module's
  TTL.
  - **Given:** deployed `[l: forloopflow(iterator: [1], parallel: false,
    cache_ttl: 60, modules: [x: rawscript bun: return Math.random()])]`, with
    no input transforms on `x`, run twice within 60 s; then with `x` a
    by-path script that has no metadata TTL.
  - **Expect:** with the rawscript body, run 2's result equals run 1's (a
    cache hit). With the by-path body, the two runs differ.
  - **Recorded:** [`flows/execution/fx-40`](../../conformance/flows/execution/fx-40) runs `c-raw` and `c-path`, oracle run 40686532046 (both calls inside one run).
- **FX-40/d** · oracle-observed · Collapsed iterations of one run share an
  entry.
  - **Given:** a deployed collapsed loop with two sequential iterations and
    identical transformed args, whose body is, in turn: a script pinned by
    hash under an enabled loop `cache_ttl`; an inline rawscript with no
    explicit path under the loop TTL; the same with an explicit `path`; an
    unhashed by-path script with its own `cache_ttl`. Inspect cache hits, not
    random-number inequality.
  - **Expect:** in all four, iteration 1 is a hit and returns iteration 0's
    result. A hash-pinned script with its own `cache_ttl` and no loop TTL is
    not cached: its own TTL is dropped.
  - **Recorded:** [`flows/execution/fx-40-hash`](../../conformance/flows/execution/fx-40-hash) runs `d-hash` and `d-hash-script-ttl`, oracle run 40692334073; [`flows/execution/fx-40`](../../conformance/flows/execution/fx-40) runs `d-raw`, `d-raw-path` and `d-script`, oracle run 40686532046.
  - **Correction 2026-10-28:** the case was `disputed`. As drafted, no variant
    shared; the correction expected the hash-pinned body to share and the
    rawscript without a path not to. The rawscript shares too: on a deployed
    flow it runs as `flowscript` jobs that carry one flow-node id, and the
    cache key uses that id instead of the path ([FX-40](execution.md#fx-40)).

<a id="fx-41"></a>
### FX-41 · `priority`, `same_worker`, deletion, squash and scheduling settings

**Scope:** later · **Finding:** [FX-41](execution.md#fx-41)

All three cases use `same_worker: true`.

- **FX-41/a** · source-derived · Steps share `./shared`.
  - **Given:** `[a: bun: write ./shared/f "x", b: bun: read ./shared/f]`.
  - **Expect:** `"x"`.
- **FX-41/b** · source-derived · An inline body reaches the root directory
  through the link chain.
  - **Given:** `[a: bun: write ./shared/f "x", br: branchone(default: [c:
    bun: read ./shared/f])]`.
  - **Expect:** `"x"`.
- **FX-41/c** · source-derived · An unpinned step breaks the chain.
  - **Given:** `[a: bun: write ./shared/f "x" (sleep: 0), b: bun: read
    ./shared/f]`.
  - **Expect:** `a` is unpinned and writes into a fresh directory; `b` fails
    with file-not-found.

<a id="fx-42"></a>
### FX-42 · Where JSON serialisation happens between steps

**Scope:** core · **Finding:** [FX-42](execution.md#fx-42)

The finding was `DISPUTED` until batch E3b recorded FX-42/h for the
correction ([Q-FX-16](execution.md#q-fx-16)); FX-42/a to FX-42/g were never
contested and now carry their own provenance.

- **FX-42/a** · source-derived · Nested arg values reach the script in jsonb
  order.
  - **Given:** `[a: bun: return {"bb": 1, "a": 2, "c": 3}, b: bun(o): return
    Object.keys(o)]` with `o = "previous_result"`.
  - **Expect:** `["a", "c", "bb"]`.
- **FX-42/b** · source-derived · An engine-built object is stored in jsonb order.
  - **Given:** `[a: bun(o): return Object.keys(o)]` with
    `o = "({zz: 1, y: 2})"`.
  - **Expect:** `["y", "zz"]`.
- **FX-42/c** · oracle-observed · An in-memory `previous_result` keeps the
  producer's order.
  - **Given:** `[a: bun: return {"bb": 1, "a": 2}, b: bun(k): return k]` with
    `k = "Object.keys(previous_result)"`.
  - **Expect:** `["bb", "a"]`.
  - **Recorded:** [`flows/execution/fx-10`](../../conformance/flows/execution/fx-10) run `memory_or_storage`, oracle run 40685013818, with `a` returning `{zz, b, aaa}`: `Object.keys(previous_result)` gives `["zz", "b", "aaa"]`, where a read from storage gives `["b", "zz", "aaa"]`.
- **FX-42/d** · source-derived · The outer input order is not asserted.
  - **Given:** a transform `Object.keys(flow_input).sort()`, input
    `{"b": 1, "a": 2}`.
  - **Expect:** `["a", "b"]`; the unsorted order is not asserted.
- **FX-42/e** · source-derived · The `flow_input` shortcut reads a whole-suffix key.
  - **Given:** input `{"a.b": 7, "a": {"b": 9}}`; transforms `flow_input.a.b`
    and `flow_input.a.b + 0`.
  - **Expect:** `7` and `9` respectively.
- **FX-42/f** · source-derived · later · Python sees jsonb's rewritten number.
  - **Given:** `[a: bun: return 1e21, b: python(x): return
    type(x).__name__]` with `x = "previous_result"`.
  - **Expect:** `"int"`.
- **FX-42/g** · oracle-observed · A static object arg is jsonb-normalised.
  - **Given:** a Bun step with `o = {type: static, value: {"zz": 1, "a": 2}}`
    returning `Object.keys(o)`.
  - **Expect:** `["a", "zz"]`.
  - **Recorded:** [`flows/execution/fx-42`](../../conformance/flows/execution/fx-42) run `javascript`, oracle run 40690630818, with the keys `{"aaa": 1, "zz": 2}`, which tell jsonb order from alphabetical order: `["zz", "aaa"]`, the shorter key first.
- **FX-42/h** · oracle-observed · `$jsonvar:` output keeps its key order,
  not its spelling.
  - **Given:** a variable with the text
    `{"zz": 1e3, "b": 1e21, "aaa": 12345678901234567891, "big": 123456789012345678901234567890, "n": 1.50e3, "s": "A\/"}`,
    passed to a Bun step as the static argument `v = "$jsonvar:<path>"`; the
    step returns the text of its arguments file, then, in a second flow, the
    keys and values it receives.
  - **Expect:** the arguments file is
    `{"v":{"zz":1000.0,"b":1e+21,"aaa":12345678901234567891,"big":1.2345678901234568e+29,"n":1500.0,"s":"A/"}}`;
    the step's stored args keep `{"v": "$jsonvar:<path>"}`; the script sees
    the keys `["zz", "b", "aaa", "big", "n", "s"]` and the doubles
    `1000`, `1e21`, `12345678901234567000`, `1.2345678901234568e29`, `1500`.
  - **Recorded:** [`flows/execution/fx-42`](../../conformance/flows/execution/fx-42) runs `args_file` (oracle run 40686049913) and `javascript` (oracle run 40690630818).
  - **Correction 2026-10-28:** the case was `disputed` between the drafted
    "spelling survives" and the correction "re-serialised, exact output to be
    recorded". The oracle chose the correction; the exact spelling is the
    backend JSON library's ([FX-42](execution.md#fx-42) rule 2).

<a id="fx-45"></a>
### FX-45 · Step identity in the environment

**Scope:** core · **Finding:** [FX-45](execution.md#fx-45)

- **FX-45/a** · source-derived · `WM_FLOW_STEP_ID` is the module id.
  - **Given:** `[my_step: bun: return process.env.WM_FLOW_STEP_ID]`.
  - **Expect:** `"my_step"`.

<a id="fx-46"></a>
### FX-46 · Collapsed single-step loops change which settings apply

**Scope:** core · **Finding:** [FX-46](execution.md#fx-46)

- **FX-46/a** · source-derived · Body `continue_on_error` is ignored when the
  loop collapses.
  - **Given:** deployed `[l: forloopflow(iterator: [1], parallel: false,
    skip_failures: false, modules: [x: bun: throw new Error("e")
    (continue_on_error: true)])]`; then the same flow as a preview.
  - **Expect:** deployed: `{success: false, result: [{"error": {"message":
    "e", "name": "Error", "stack": <any>, "step_id": "l"}}]}`. Preview:
    `{success: true, result: [{"error": {…, "step_id": "x"}}]}`.

<a id="fx-47"></a>
### FX-47 · Thrown non-Error values in Bun

**Scope:** core · **Finding:** [FX-47](execution.md#fx-47)

- **FX-47/a** · source-derived · A thrown string.
  - **Given:** `[a: bun: throw "ab"]`.
  - **Expect:** `{"error": {"step_id": "a", "extra": {"0": "a", "1": "b",
    "length": 2}}}`.
- **FX-47/b** · source-derived · later · A thrown `null`.
  - **Given:** `[a: bun: throw null]`.
  - **Expect:** `{"error": {"name": "ExecutionErr", "exit_code": 1,
    "message": <starts "exit code for ">, "step_id": "a"}}`.

<a id="fx-48"></a>
### FX-48 · NUL characters at storage boundaries

**Scope:** core · **Finding:** [FX-48](execution.md#fx-48)

- **FX-48/a** · source-derived · A NUL in a result fails the completion
  insert.
  - **Given:** `[a: bun: return "x\u0000y"]`.
  - **Expect:** after the insert retries (up to 10 × 3 s), `{success: false,
    result: {"error": {"name": "InternalErr", "message": <starts "Internal:
    Could not add completed job ">}}}`. Normalise the job UUID, the database
    detail and the location.
- **FX-48/b** · source-derived · A NUL generated in args at root pickup.
  - **Given:** `[a: bun(s): return s]` with `s = "String.fromCharCode(0)"`.
  - **Expect:** an `Anyhow` failure of `a`.
- **FX-48/c** · source-derived · The same at a later transition.
  - **Given:** the transform of FX-48/b on a second step.
  - **Expect:** an `InternalError` chaining failure.

<a id="fx-51"></a>
### FX-51 · Unpaired surrogate escapes and out-of-range numbers at storage boundaries

**Scope:** core · **Finding:** [FX-51](execution.md#fx-51)

- **FX-51/a** · source-derived · An unpaired surrogate in a result fails the
  completion insert.
  - **Given:** `[a: bun: return "a" + String.fromCharCode(0xd83d), b: bun:
    return 2]`; a variant returning `String.fromCharCode(0xde00) + "a"`.
  - **Expect:** after the insert retries (up to 10 × 3 s), `a` and the flow
    fail with `{"error": {"name": "InternalErr", "message": <starts "Internal:
    Could not add completed job ">}}`, whose message contains
    `error returned from database: invalid input syntax for type json` and
    ends in `<loc>`; no `step_id`; `b` never runs. Normalise the job id.
- **FX-51/b** · source-derived · An unpaired surrogate generated in args.
  - **Given:** `[a: bun(s): return s]` with `s = "String.fromCharCode(0xd83d)"`;
    then the same transform on a second step.
  - **Expect:** as FX-48/b and FX-48/c: an `Anyhow` failure of `a` at root
    pickup, an `InternalError` chaining failure at the later transition.
- **FX-51/c** · source-derived · later · `$jsonvar:` validates its text.
  - **Given:** a variable with the text `{"s": "\ud83d"}` passed as
    `v = "$jsonvar:<path>"` to a Bun step `a`; a second variable with the
    text `{"n": 1e400}`.
  - **Expect:** the step fails before its script runs. Its stored result,
    and the flow's, is
    `{"error": {"name": "ExecutionErr", "message": "execution error:\nInternal: Failed to parse $jsonvar value as JSON: unexpected end of hex escape at line 1 column C<loc>", "step_id": "a"}}`
    for the first variable, and the same with
    `number out of range at line 1 column C` for the second, where `C` is
    the parser's column and `<loc>` the one location suffix. The name is
    `ExecutionErr`, not the completion insert's `InternalErr` of FX-51/a.
  - **Correction 2026-10-28:** the case expected the resolver's inner
    `InternalErr`. The executor converts it into the execution envelope
    before storing it ([FX-51](execution.md#fx-51) rule 5), as the E3a
    recordings `je42-h` and `je42-e` of
    [`flows/je/providers`](../../conformance/flows/je/providers) show for
    other resolver failures.
- **FX-51/d** · source-derived · later · A number outside `numeric`'s range in
  the flow input.
  - **Given:** a run request whose input is `{"x": 1e131072}`.
  - **Expect:** the run request fails when the flow job is stored, with
    PostgreSQL's `value overflows numeric format`; no flow job runs (low
    confidence; HTTP status and body to be recorded,
    [Q-FX-20](execution.md#q-fx-20)).

<a id="fx-49"></a>
### FX-49 · Cursor-0 checks run on every start phase at cursor 0

**Scope:** later · **Finding:** [FX-49](execution.md#fx-49)

- **FX-49/a** · disputed · A `skip_expr` that flips during a loop at module 0.
  - **Given:** a flow with `skip_expr: "variable('u/t/skip') === 'yes'"`, the
    variable at `"no"`, and `[l: forloopflow(iterator: [1, 2, 3], parallel:
    false, modules: [x: bun(v): await setVar('u/t/skip', v == 1 ? 'yes' :
    'no'); return v])]` with `v = "flow_input.iter.value"`.
  - **Expect (as drafted):** the expression is evaluated 3 times, once per
    start phase; the flow outcome is open.
  - **Expect (correction):** the expression is evaluated 2 times; the second
    evaluation is true and stops the loop, giving loop result `[1]` and an
    ordinary successful completion.

<a id="fx-v1"></a>
### FX-V1 · A throwing `stop_after_all_iters_if` uses the EarlyStopError path

**Scope:** core · **Finding:** [FX-V1](execution.md#fx-v1)

- **FX-V1/a** · source-derived · Sequential loop.
  - **Given:** a sequential loop over `[1]` whose body returns `1`, with
    `stop_after_all_iters_if.expr = "null.x"`, no retry, no tolerance, no
    handler.
  - **Expect:** failure with result
    `` {"error": {"name": "EarlyStopError", "message": <starts "Error evaluating stop_after_all_iters_if expression `null.x`: ">}} ``.
    Normalise engine stack and location suffixes. The inner
    error is an object, not a string, and has no `result` field.
- **FX-V1/b** · source-derived · Parallel container.
  - **Given:** FX-V1/a with a parallel container.
  - **Expect:** as FX-V1/a.

<a id="fx-v2"></a>
### FX-V2 · Stored child-job result and effective module outcome can differ

**Scope:** core · **Finding:** [FX-V2](execution.md#fx-v2)

- **FX-V2/a** · source-derived · The handler sees the error; storage keeps the
  success.
  - **Given:** `[a: bun: return {"x": 1} (stop_after_if: {expr: "true",
    skip_if_stopped: false, error_message: "E"})]`; a handler with
    `p = "previous_result"`, `r = "results.a"`, `x = "results.a.x"` that
    returns its inputs.
  - **Expect:** final result `{"p": {"error": {"name": "EarlyStopError",
    "message": "E"}}, "r": {"error": {"name": "EarlyStopError", "message":
    "E"}}, "x": 1}`, flow failed (unless the handler also returns `recover:
    true`). Separately, `a`'s stored job is successful with result
    `{"x": 1}`, and its module status is `Failure`.

<a id="fx-v3"></a>
### FX-V3 · Synchronous HTTP completion treats a stored `skipped` status as failure

**Scope:** core · **Finding:** [FX-V3](execution.md#fx-v3)

- **FX-V3/a** · source-derived · A skipped flow returns 422.
  - **Given:** `[a: bun: return 3 (stop_after_if: {expr: "true",
    skip_if_stopped: true})]`, no `early_return`, no response metadata.
  - **Expect:** completed flow status `skipped`; stored result `3`;
    run-and-wait returns HTTP 422 with JSON body `3`.

<a id="fx-v4"></a>
### FX-V4 · A tolerated error-stop at the final module enters the failure module

**Scope:** core · **Finding:** [FX-V4](execution.md#fx-v4)

- **FX-V4/a** · source-derived · What the handler sees after a cursor-`len`
  entry.
  - **Given:** `[a: bun: return 3 (continue_on_error: true, stop_after_if:
    {expr: "true", skip_if_stopped: false, error_message: "E"})]`, no retry;
    a handler with `p = "previous_result"` and `r = "results.a"` that returns
    `{p, r}`.
  - **Expect:** the handler runs once; the flow fails with result `{"p":
    {"error": {"name": "EarlyStopError", "message": "E"}}, "r": 3}`. Without
    the handler, see FX-19/f; with a regular step `b` after `a`, see FX-19/e.

<a id="fx-v5"></a>
### FX-V5 · Argument resolution can introduce objects after the jsonb boundary

**Scope:** core · **Finding:** [FX-V5](execution.md#fx-v5)

- **FX-V5/a** · oracle-observed · `$jsonvar:` keeps the variable's key order.
  - **Given:** a synthetic variable `u/test/ordered_json` with text
    `{"zz":1,"a":2}`; a Bun step with static arg
    `o = "$jsonvar:u/test/ordered_json"` returning `Object.keys(o)`.
  - **Expect:** `["zz", "a"]`. Contrast a static object arg
    `{"zz": 1, "a": 2}`, which gives `["a", "zz"]` (FX-42/g).
  - **Recorded:** [`flows/execution/fx-42`](../../conformance/flows/execution/fx-42) run `javascript`, oracle run 40690630818, with FX-42/h's variable: the keys arrive as `["zz", "b", "aaa", "big", "n", "s"]`, the variable's order.

<a id="fx-v6"></a>
### FX-V6 · Dynamic preceding-result lookup depends on all context-binding triggers

**Scope:** core · **Finding:** [FX-V6](execution.md#fx-v6)

- **FX-V6/a** · oracle-observed · Computed indexing with and without a
  `previous_result` binding.
  - **Given:** input `{"target": "a"}`; `[a: bun: return 3 (stop_after_if:
    {expr: "true", skip_if_stopped: false, error_message: "E"})]`, no
    tolerance and no retry; an ordinarily entered handler with
    `stored = "results[flow_input.target]"` and
    `carried = "previous_result && results[flow_input.target]"` that returns
    both.
  - **Expect:** the flow fails with result `{"stored": 3, "carried":
    {"error": {"name": "EarlyStopError", "message": "E"}}}`. The async
    expression wrapper adopts the returned proxy promise.
  - **Recorded:** [`flows/execution/fx-v6`](../../conformance/flows/execution/fx-v6)
    run `lookup`, oracle run 40685766017. This settled
    [Q-FX-15](execution.md#q-fx-15) for FX-V6; the superseded FX-10
    amendment predicted the stored `3` for `carried` too.

<a id="control-flow"></a>
## Loops, branches and subflows

Findings: [control-flow.md](control-flow.md).

<a id="cf-notation"></a>
### Notation

- **Steps.** `b: ret(E)` is a Bun `rawscript` step with id `b` whose
  `main(x)` returns the JS value `E`. Its single input `x` is bound by the
  input transform written after it, `x = <js expr>`; without an `x =`, the
  script takes no inputs. `fail(M)` is a Bun `rawscript` whose `main()` throws
  `new Error(M)`. `n = ret(…)` and `n: ret(…)` mean the same.
- **Containers.** "forloop `l` over `[1,2]`" is a `forloopflow` with id `l`
  whose `iterator` yields that array: a static value unless a JavaScript
  expression is written. A body is written as a list `[step, step, …]`.
  Module fields such as `stop_after_if: {expr: "…"}` use flow-YAML shorthand.
- **Errors.** `ERR(M)` is reserved for errors thrown by user Bun code: a step
  error result `{"error": {"name": "Error", "message": M, ...}}`. Compare only
  `name` and `message`; `stack` and `step_id` follow
  [languages.md](languages.md). `ERR(..)` leaves the message unasserted.
  Errors Windmill itself generates (timeout, cancellation, `EarlyStopError`,
  `InternalError`, `Anyhow`) are written out exactly.
- **Locations.** `<loc>` stands for one Rust source location suffix that
  Windmill appends to some error texts, **including its leading space**:
  ` @<file>.rs:<line>:<col>` (for example ` @worker.rs:3822:13`), or
  ` (<file>.rs:<line>)` inside a not-found message; two suffixes are written
  `<loc><loc>` ([FX-50](execution.md#fx-50)). It depends on the build;
  compare the text with it masked.
- **Result and status.** "Result" is the flow job's final JSON result and
  "status" its success flag. "Loop result" is the loop step's own result
  (`results.<loop id>`).
- **Run mode.** Core cases run **by path, after the flow's dependency job has
  succeeded** ([CF-6](control-flow.md#cf-6)). Raw flow submissions are
  labelled as comparison runs.
- **Recorded with every oracle run:**
  - the dependency job's state: succeeded, pending, failed, or not applicable
    for raw runs;
  - the stored form that actually ran (lite with stored nodes, or full),
    observed rather than inferred from dependency success
    ([CF-24](control-flow.md#cf-24), [Q-CF-15](control-flow.md#q-cf-15));
  - the job kind of the root and of every child flow involved: `flow`,
    `flownode` or `flowpreview`.
- **Provenance.** Recording batch E3d recorded the cases of CF-2 to CF-15
  and CF-V2/a, and batch E3a those of CF-19 and CF-V6, each by path after a
  successful dependency job on one worker; each such case names its
  recording. Those recordings settled CF-12, CF-19 and CF-V6, and a source
  re-check settled CF-8, so no case in this section is `disputed` any more.
  Raw-submission halves are outside the reference configuration (design
  record 0001) and are not recorded.

<a id="cf-1"></a>
### CF-1 · Every container body runs as a nested child flow scope

**Scope:** core · **Finding:** [CF-1](control-flow.md#cf-1)

- **CF-1/a** · source-derived · A body step sees the outer `flow_input`; inline and stored-node bodies agree.
  - **Given:** flow input `{"k": 7}`; forloop `l` over `[1]` with body
    `[b: ret(x) x = flow_input.k, c: ret(x) x = flow_input.iter.value]`. Run
    once deployed by path (stored-node body) and once as a raw flow (inline
    body).
  - **Expect:** both runs give result `[1]`; step `b` in the iteration
    received `x = 7`.

<a id="cf-2"></a>
### CF-2 · `forloopflow` iterator evaluation

**Scope:** core · **Finding:** [CF-2](control-flow.md#cf-2)

- **CF-2/a** · oracle-observed · An empty iterator gives `[]` and creates no child.
  - **Given:** `a: ret([])`; forloop `l` over `results.a` (JavaScript) with
    body `[ret(x) x = flow_input.iter.value]`; `s: ret(x.length) x = results.l`.
  - **Expect:** status success, result `0`.
  - **Recorded:** [`flows/control-flow/cf-2`](../../conformance/flows/control-flow/cf-2) run `a`, oracle run 40685057858.
- **CF-2/b** · oracle-observed · A non-array iterator fails the loop.
  - **Given:** flow input `{"o": {"p": 1}}`; forloop `l` with iterator
    `flow_input.o`.
  - **Expect:** status failure; `error.message` contains
    `Expected an array value in the iterator expression, found: invalid type: map, expected a sequence`.
    The rest is `later` and depends on the route
    ([CF-23](control-flow.md#cf-23)): with the loop at step 0, name `Anyhow`
    and message
    `Error: Expected an array value in the iterator expression, found: invalid type: map, expected a sequence at line 1 column 0<loc>`;
    with the loop after a first step, name `InternalError` and the same
    message without `Error: ` and `<loc>`.
  - **Recorded:** [`flows/control-flow/cf-2`](../../conformance/flows/control-flow/cf-2) runs `b-step0` and `b-later`, oracle run 40686532046.

<a id="cf-3"></a>
### CF-3 · The `iter` object and nesting (`iter_parent`)

**Scope:** core · **Finding:** [CF-3](control-flow.md#cf-3)

- **CF-3/a** · oracle-observed · The outer `iter` moves to `iter_parent` in a nested loop.
  - **Given:** `outer` = forloop over `["a","b"]` whose body is `inner` =
    forloop over `[1,2]`, with inner body
    `[ret(x) x = [flow_input.iter_parent.value, flow_input.iter.index]]`.
  - **Expect:** result `[[["a",0],["a",1]],[["b",0],["b",1]]]`.
  - **Recorded:** [`flows/control-flow/cf-3`](../../conformance/flows/control-flow/cf-3) run `a`, oracle run 40685057858.

<a id="cf-4"></a>
### CF-4 · Loop result shape and order

**Scope:** core · **Finding:** [CF-4](control-flow.md#cf-4)

- **CF-4/a** · oracle-observed · A parallel loop's result is in index order.
  - **Given:** forloop `l` over `[3,1,2]`, `parallel: true`, body
    `[ret(x*10) x = flow_input.iter.value]`.
  - **Expect:** result `[30,10,20]`.
  - **Recorded:** [`flows/control-flow/cf-4`](../../conformance/flows/control-flow/cf-4) run `a`, oracle run 40685057858.
- **CF-4/b** · oracle-observed · A skipped failed iteration keeps its error object.
  - **Given:** as CF-4/a with `skip_failures: true`, and the body throws
    `new Error("boom")` for value `1` only.
  - **Expect:** status success, result `[30, ERR("boom"), 20]`.
  - **Recorded:** [`flows/control-flow/cf-4`](../../conformance/flows/control-flow/cf-4) run `b`, oracle run 40685057858.

<a id="cf-5"></a>
### CF-5 · Sequential loop failure, `skip_failures`, and what the flow sees

**Scope:** core · **Finding:** [CF-5](control-flow.md#cf-5)

- **CF-5/a** · oracle-observed · A non-skippable failure stops the loop with the array so far.
  - **Given:** forloop over `[0,1,2,3]`, `skip_failures: false`; body: one Bun
    script with `x = flow_input.iter.value` that returns `x` and throws when
    `x == 2`.
  - **Expect:** status failure, result `[0, 1, ERR(..)]` (length 3).
  - **Recorded:** [`flows/control-flow/cf-5`](../../conformance/flows/control-flow/cf-5) run `a`, oracle run 40685057858.
- **CF-5/b** · oracle-observed · With `skip_failures: true` every iteration runs.
  - **Given:** as CF-5/a with `skip_failures: true`.
  - **Expect:** status success, result `[0, 1, ERR(..), 3]`.
  - **Recorded:** [`flows/control-flow/cf-5`](../../conformance/flows/control-flow/cf-5) run `b`, oracle run 40685057858.

<a id="cf-6"></a>
### CF-6 · Absent container fields: defaults depend on the stored state

**Scope:** core · **Finding:** [CF-6](control-flow.md#cf-6)

- **CF-6/a** · oracle-observed · After successful normalization an absent `skip_failures` behaves as `true`.
  - **Given:** push a flow whose forloop omits `skip_failures`, over `[1,2]`,
    with a body that throws for `1` and returns the value otherwise. Wait
    until the flow's dependency job has **completed successfully**, then run
    by path.
  - **Expect:** status success, result `[ERR(..), 2]`. The flow the API
    answers after the dependency job, and the one `generate-metadata`
    writes from the same source, both carry `skip_failures: true` and
    `parallel: false`.
  - **Recorded:** [`flows/control-flow/cf-6`](../../conformance/flows/control-flow/cf-6) run `a`, oracle run 40692334073 (deployed through the API so the field reaches the server absent; the provenance holds the submitted, materialized and `generate-metadata` values).
- **CF-6/b** · source-derived · A pulled folder shows the materialized field.
  - **Given:** after CF-6/a's dependency job, run `wmill sync pull`.
  - **Expect:** the forloop in `flow.yaml` carries `skip_failures: true`.
  - **Note:** not recorded yet: the oracle harness has no pull read-back;
    recording batch E3e records it.
- **CF-6/c** · source-derived · Before or without normalization an absent `skip_failures` behaves as `false`.
  - **Given:** the same flow run as a raw flow payload, or a version whose
    dependency job failed.
  - **Expect:** status failure, result `[ERR(..)]`.
  - **Note:** a raw submission or a failed dependency job is outside the
    reference configuration (design record 0001), so this case is not
    recorded.

<a id="cf-7"></a>
### CF-7 · Parallel `forloopflow`: spawning, `parallelism`, completion

**Scope:** core · **Finding:** [CF-7](control-flow.md#cf-7)

- **CF-7/a** · oracle-observed · No fail-fast; the full array is the failed result.
  - **Given:** parallel forloop over `[1,2,3,4]`, `parallelism: 2`,
    `skip_failures: false`; the body returns `flow_input.iter.value` and
    throws for `2`.
  - **Expect:** all four iterations run; status failure; result
    `[1, ERR(..), 3, 4]`.
  - **Recorded:** [`flows/control-flow/cf-7`](../../conformance/flows/control-flow/cf-7) run `a`, oracle run 40685057858 (the iteration child flows overlap).
- **CF-7/b** · oracle-observed · `parallelism: 1` releases iterations in index order.
  - **Given:** as CF-7/a with `parallelism: 1`.
  - **Expect:** iteration start times are strictly ordered by index; each
    iteration's child flow starts after the previous one completed.
  - **Recorded:** [`flows/control-flow/cf-7`](../../conformance/flows/control-flow/cf-7) run `b`, oracle run 40685057858, from the child flows' times in the provenance. One worker cannot show the concurrency of several eligible iterations ([Q-CF-18](control-flow.md#q-cf-18)).
- **CF-7/c** · oracle-observed · later · One child in a `same_worker` flow does not error.
  - **Given:** a flow with `same_worker: true`; a parallel forloop over `[5]`
    whose body returns the iteration value.
  - **Expect:** status success, result `[5]`.
  - **Recorded:** [`flows/control-flow/cf-7`](../../conformance/flows/control-flow/cf-7) run `c`, oracle run 40685057858.
- **CF-7/d** · oracle-observed · later · Several children in a `same_worker` flow fail to start.
  - **Given:** as CF-7/c over `[5,6]`.
  - **Expect:** status failure with the error
    `Cannot continue on same worker with multiple jobs, parallel cannot be used in conjunction with same_worker`.
    With the loop at step 0 the flow fails at pickup:
    `{"error": {"name": "Anyhow", "message": "Error: Bad request: Cannot continue on same worker with multiple jobs, parallel cannot be used in conjunction with same_worker<loc>"}}`,
    and no iteration is created.
  - **Recorded:** [`flows/control-flow/cf-7`](../../conformance/flows/control-flow/cf-7) run `d`, oracle run 40686532046.

<a id="cf-8"></a>
### CF-8 · The simple-loop-body fast path; eligibility follows the executing job's kind

**Scope:** core · **Finding:** [CF-8](control-flow.md#cf-8)

- **CF-8/a** · oracle-observed · Top-level loop: collapsed by path, not collapsed as a raw submission.
  - **Given:** `a: ret(100)`; a sequential forloop over `[1,2,3]` with body
    `[ret(x) x = (typeof previous_result === "number" ? previous_result : -1000) + flow_input.iter.value]`.
  - **Expect:** by path after a successful dependency job (root `flow`):
    `[101,103,106]`. As a raw submission (root `flowpreview`):
    `[-999,-998,-997]`. Both succeed.
  - **Recorded:** the by-path half, [`flows/control-flow/cf-8`](../../conformance/flows/control-flow/cf-8) run `a`, oracle run 40685057858. The raw-submission half stays source-derived: it is outside the reference configuration (design record 0001) and is not recorded.
- **CF-8/b** · oracle-observed · Nested loop: collapses only inside a stored-node body.
  - **Given:** an outer sequential forloop over `[1]` with body
    `[a2: ret(100), inner: <the loop of CF-8/a>]`.
  - **Expect:**
    - by path after a successful dependency job, with the lite version
      read (outer body `flownode`): `[[101,103,106]]`;
    - by path while the dependency job is pending or failed, or with the
      full version read (`DISABLE_FLOW_SCRIPT`; outer body `flowpreview`):
      `[[-999,-998,-997]]`;
    - as a raw submission: `[[-999,-998,-997]]`.
  - **Recorded:** the first expectation, [`flows/control-flow/cf-8`](../../conformance/flows/control-flow/cf-8) run `b`, oracle run 40685057858, with the outer body a `flownode` job. The other two stay source-derived and are not recorded (outside the reference configuration).
- **CF-8/c** · oracle-observed · A loop at step 0 gives every iteration the flow input.
  - **Given:** by path; flow input `{"z":1}`; a sequential forloop over
    `[1,2,3]` as step 0 with body `[ret(x) x = previous_result]`.
  - **Expect:** `[{"z":1},{"z":1},{"z":1}]`.
  - **Recorded:** [`flows/control-flow/cf-8`](../../conformance/flows/control-flow/cf-8) run `c`, oracle run 40685057858.
- **CF-8/d** · source-derived · Raw parent calling a deployed subflow.
  - **Given:** deploy `f/t/child` containing CF-8/a's flow (`a: ret(100)`
    followed by the eligible loop) and let its dependency job succeed.
    Submit a raw parent flow whose step is `{type: flow, path: f/t/child}`.
    Record the job kinds of the parent and the child.
  - **Expect:** the child is a `flow` job, so its loop collapses:
    `[101,103,106]` as the child's and the parent's result.
  - **Correction 2026-10-28:** the case was `disputed` and also stated the
    draft's expectation, `[-999,-998,-997]` ("in a raw submission, no
    forloop collapses at any depth"). A source re-check settled it for the
    correction: a `flow` step always enqueues a `Flow` job, and the forloop
    gate tests only the executing job's kind ([CF-8](control-flow.md#cf-8)).
    The case is outside the reference configuration and is not recorded.

<a id="cf-9"></a>
### CF-9 · `previous_result` at the first step of a non-collapsed loop body or branch

**Scope:** core · **Finding:** [CF-9](control-flow.md#cf-9)

- **CF-9/a** · oracle-observed · The first body step gets the child's input.
  - **Given:** flow input `{"z": 1}`; `a: ret(100)`; forloop `l` over `[5]`
    with body `[b: ret(x) x = previous_result, c: ret(x) x = previous_result]`.
  - **Expect:** result `[{"z":1,"iter":{"index":0,"value":5}}]` (`c` passes
    on `b`'s result, which is the child's input).
  - **Recorded:** [`flows/control-flow/cf-9`](../../conformance/flows/control-flow/cf-9) run `a`, oracle run 40685057858.
- **CF-9/b** · oracle-observed · Same for a `branchone` branch.
  - **Given:** as CF-9/a, but the container is a `branchone` whose default
    branch `[b: ret(x) x = previous_result]` runs.
  - **Expect:** the branchone result is `{"z": 1}`.
  - **Recorded:** [`flows/control-flow/cf-9`](../../conformance/flows/control-flow/cf-9) run `b`, oracle run 40685057858.

<a id="cf-10"></a>
### CF-10 · `whileloopflow`

**Scope:** core · **Finding:** [CF-10](control-flow.md#cf-10)

- **CF-10/a** · oracle-observed · A while loop breaks on its `stop_after_if` and keeps the breaking iteration.
  - **Given:** whileloop `w` with `stop_after_if: {expr: "result >= 2"}`,
    body `[ret(x) x = flow_input.iter.index]`, followed by a step that reads
    `previous_result`.
  - **Expect:** loop result `[0,1,2]`, status success; the flow continues and
    the next step's `previous_result` is `[0,1,2]`.
  - **Recorded:** [`flows/control-flow/cf-10`](../../conformance/flows/control-flow/cf-10) run `a`, oracle run 40685057858.

<a id="cf-11"></a>
### CF-11 · A loop module's `stop_after_if` means "break"

**Scope:** core · **Finding:** [CF-11](control-flow.md#cf-11)

- **CF-11/a** · oracle-observed · `all_iters` is bound and the error message is ignored.
  - **Given:** forloop over `[1,2,3,4]`, body
    `[ret(x) x = flow_input.iter.value]`,
    `stop_after_if: {expr: "all_iters.length >= 2", error_message: "x"}`; then
    `n: ret(x) x = previous_result`.
  - **Expect:** result `[1,2]`, status success; the error message has no
    effect.
  - **Recorded:** [`flows/control-flow/cf-11`](../../conformance/flows/control-flow/cf-11) run `a`, oracle run 40685057858.

<a id="cf-12"></a>
### CF-12 · Early stop raised inside a container body

**Scope:** core · **Finding:** [CF-12](control-flow.md#cf-12)

- **CF-12/a** · oracle-observed · Clean body stop breaks a sequential loop.
  - **Given:** forloop `l` over `[1,2,3]` with a two-step body: step
    `b: ret(x) x = flow_input.iter.value` carrying
    `stop_after_if: {expr: "result == 2"}`, then step
    `c: ret(x*10) x = results.b`. After the loop, `n: ret("after")`.
  - **Expect:** result `"after"`; loop result `[10, 2]`.
  - **Recorded:** [`flows/control-flow/cf-12`](../../conformance/flows/control-flow/cf-12) run `a`, oracle run 40685057858.
- **CF-12/b** · oracle-observed · An error stop is consumed by the copied handler.
  - **Given:** flow `failure_module` = `ret("handled")`; forloop over `[1,2]`,
    `skip_failures: true`, with a two-step body: step
    `b: ret(x) x = flow_input.iter.value` carrying
    `stop_after_if: {expr: "true", error_message: "halt"}`, then a second
    step so the body is non-simple.
  - **Expect:** status success, loop result `["handled","handled"]`.
  - **Recorded:** [`flows/control-flow/cf-12`](../../conformance/flows/control-flow/cf-12) run `b`, oracle run 40685057858 (each iteration's copy of the handler ran; the flow's own did not).
- **CF-12/c** · oracle-observed · Error stop without a handler breaks the loop.
  - **Given:** as CF-12/b without the `failure_module`, followed by a next
    step.
  - **Expect:** with `skip_failures: true`: status success, loop result
    `[{"error":{"name":"EarlyStopError","message":"halt"}}]`, and the next
    step runs. With `skip_failures: false`: status failure with that array
    as the flow's result, and the next step does not run.
  - **Recorded:** [`flows/control-flow/cf-12`](../../conformance/flows/control-flow/cf-12) runs `c-skip` and `c-fail`, oracle run 40685057858.
  - **Correction 2026-10-28:** the case was `disputed` on whether the next
    step runs with `skip_failures: false` (the draft: the flow continues
    after the loop either way). The oracle recorded the correction: a break
    clears the stop, not the failure ([CF-12](control-flow.md#cf-12)).
- **CF-12/d** · oracle-observed · A clean stop propagates through `branchone`.
  - **Given:** the step `b` (input value 2, `stop_after_if: {expr: "result == 2"}`)
    as the only step of a `branchone` default branch, followed by
    `n: ret("after")`.
  - **Expect:** the flow stops after the branchone with result `2`; `n` does
    not run.
  - **Recorded:** [`flows/control-flow/cf-12`](../../conformance/flows/control-flow/cf-12) run `d`, oracle run 40685057858.
- **CF-12/e** · oracle-observed · A stopped sequential `branchall` keeps its own success status.
  - **Given:** no `failure_module`. Sequential branchall `ba` with two
    branches, then `n: ret("after")`. Branch 0 has explicit
    `skip_failure: true` and a one-step body `ret(7)` carrying
    `stop_after_if: {expr: "true", error_message: "halt"}`; branch 1 is
    `[ret(9)]`.
  - **Expect:** status **success**, result
    `[{"error":{"name":"EarlyStopError","message":"halt"}}]`; branch 1 and `n`
    do not run. With `skip_failure: false` on branch 0: status **failure**,
    same result, same skipped execution.
  - **Recorded:** [`flows/control-flow/cf-12`](../../conformance/flows/control-flow/cf-12) runs `e-skip` and `e-fail`, oracle run 40686532046.
- **CF-12/f** · oracle-observed · The flow does not continue after a failed, broken loop.
  - **Given:** no `failure_module`, no loop `retry` or `continue_on_error`.
    Sequential forloop over `[1,2]` with explicit `skip_failures: false` and
    a one-step body `ret(7)` carrying
    `stop_after_if: {expr: "true", error_message: "halt"}`; then
    `n: ret("after")`.
  - **Expect:** status failure, result
    `[{"error":{"name":"EarlyStopError","message":"halt"}}]`, and `n` does
    not run. With `skip_failures: true`: status success and `n` runs.
  - **Recorded:** [`flows/control-flow/cf-12`](../../conformance/flows/control-flow/cf-12) runs `f` and `f-skip`, oracle run 40685057858.
  - **Correction 2026-10-28:** the case was `disputed`
    ([Q-CF-14](control-flow.md#q-cf-14)) and also stated the draft's
    expectation: the flow continues after the loop and `n` runs. The oracle
    recorded the correction ([CF-12](control-flow.md#cf-12)).

<a id="cf-13"></a>
### CF-13 · `stop_after_all_iters_if` (loops and branchall)

**Scope:** core · **Finding:** [CF-13](control-flow.md#cf-13)

- **CF-13/a** · oracle-observed · A truthy predicate stops the enclosing flow, not just the loop.
  - **Given:** forloop over `[1,2]`, body `[ret(x) x = flow_input.iter.value]`,
    `stop_after_all_iters_if: {expr: "result.length == 2", skip_if_stopped: true}`;
    then `n: ret("after")`.
  - **Expect:** result `[1,2]`, the flow is marked skipped, `n` does not run.
  - **Recorded:** [`flows/control-flow/cf-13`](../../conformance/flows/control-flow/cf-13) run `a`, oracle run 40685057858 (the flow's status is `skipped`).
- **CF-13/b** · oracle-observed · An evaluation error becomes `EarlyStopError`.
  - **Given:** as CF-13/a with `expr: "bad!!!syntax"`.
  - **Expect:** status failure, `error.name == "EarlyStopError"`, message
    contains `stop_after_all_iters_if`. Exactly, as `later` scope: the
    flow's result is
    ``{"error": {"name": "EarlyStopError", "message": "Error evaluating stop_after_all_iters_if expression `bad!!!syntax`: Error: QuickJS evaluation error: Error: Unexpected token '!'\n    at eval_script:1:7\n<loc>"}}``;
    the loop step's own result is `[1,2]` and it carries that error; `n`
    does not run.
  - **Recorded:** [`flows/control-flow/cf-13`](../../conformance/flows/control-flow/cf-13) run `b`, oracle run 40685057858.
- **CF-13/c** · oracle-observed · Not evaluated for an empty iterator.
  - **Given / Expect:** as [CF-V2/a](#cf-v2).
  - **Recorded:** [`flows/control-flow/cf-13`](../../conformance/flows/control-flow/cf-13) run `c`, oracle run 40685057858.

<a id="cf-14"></a>
### CF-14 · `branchone`

**Scope:** core · **Finding:** [CF-14](control-flow.md#cf-14)

- **CF-14/a** · oracle-observed · Predicates are evaluated in order and short-circuit.
  - **Given:** `a: ret({"n": 5})`; `br` = branchone with branches
    `[{expr: "results.a.n > 10", modules: [ret("big")]}, {expr: "results.a.n > 3", modules: [ret("mid")]}, {expr: "undefined.x", modules: [ret("never")]}]`
    and default `[ret("small")]`.
  - **Expect:** result `"mid"`; the third predicate is never evaluated, so
    there is no error.
  - **Recorded:** [`flows/control-flow/cf-14`](../../conformance/flows/control-flow/cf-14) run `a`, oracle run 40685057858.
- **CF-14/b** · oracle-observed · An empty chosen branch passes `previous_result` through.
  - **Given:** `a: ret({"n": 5})`; `br` = branchone with no matching branch
    and `default: []`; then a step with `x = previous_result`.
  - **Expect:** that step gets `{"n": 5}`.
  - **Recorded:** [`flows/control-flow/cf-14`](../../conformance/flows/control-flow/cf-14) run `b`, oracle run 40685057858.
- **CF-14/c** · oracle-observed · later · Empty branch at step 0 ([Q-CF-4](control-flow.md#q-cf-4)).
  - **Given:** a `branchone` as step 0 of a flow, no matching branch,
    `default: []`.
  - **Expect:** the branchone result, and so the flow's result, is the JSON
    string `"{}"`.
  - **Recorded:** [`flows/control-flow/cf-14`](../../conformance/flows/control-flow/cf-14) run `c`, oracle run 40686532046.

<a id="cf-15"></a>
### CF-15 · `branchall`

**Scope:** core · **Finding:** [CF-15](control-flow.md#cf-15)

- **CF-15/a** · oracle-observed · Parallel by default; a skippable failed branch keeps its error entry.
  - **Given:** `a: ret([1])`; `ba` = branchall with no `parallel` field and
    branches `[[ret(x.concat([2])) x = results.a], [fail("b")], [ret(x.concat([3])) x = results.a]]`,
    branch 1 with `skip_failure: true`.
  - **Expect:** status success, result `[[1,2], ERR("b"), [1,3]]`.
  - **Recorded:** [`flows/control-flow/cf-15`](../../conformance/flows/control-flow/cf-15) run `a`, oracle run 40692334073 (deployed through the API, so `parallel` and branches 0 and 2's `skip_failure` reached the server absent).
- **CF-15/b** · oracle-observed · A non-skippable parallel failure fails the step with the full array.
  - **Given:** as CF-15/a with `skip_failure: false` on branch 1.
  - **Expect:** status failure, result `[[1,2], ERR("b"), [1,3]]`.
  - **Recorded:** [`flows/control-flow/cf-15`](../../conformance/flows/control-flow/cf-15) run `b`, oracle run 40692334073.
- **CF-15/c** · oracle-observed · Sequential branchall stops at the first non-skippable failure.
  - **Given:** as CF-15/b with `parallel: false`.
  - **Expect:** status failure, result `[[1,2], ERR("b")]`; branch 2 never
    runs.
  - **Recorded:** [`flows/control-flow/cf-15`](../../conformance/flows/control-flow/cf-15) run `c`, oracle run 40692334073.
- **CF-15/d** · oracle-observed · A zero-branch branchall evaluates its own `stop_after_if`.
  - **Given:** a branchall with `branches: []` and
    `stop_after_if: {expr: "true"}`, followed by `n: ret("after")`.
  - **Expect:** status success, result `[]`, `n` does not run.
  - **Recorded:** [`flows/control-flow/cf-15`](../../conformance/flows/control-flow/cf-15) run `d`, oracle run 40692334073.
- **CF-15/e** · oracle-observed · Sequential branchall error stop.
  - **Given / Expect:** as [CF-12/e](#cf-12).
  - **Recorded:** as CF-12/e, [`flows/control-flow/cf-12`](../../conformance/flows/control-flow/cf-12) runs `e-skip` and `e-fail`, oracle run 40686532046.
- **CF-15/f** · oracle-observed · Empty branch returns its child input ([Q-CF-4](control-flow.md#q-cf-4)).
  - **Given:** a branchall with a branch whose `modules` is `[]`.
  - **Expect:** that branch's entry is the enclosing flow input object.
  - **Recorded:** [`flows/control-flow/cf-15`](../../conformance/flows/control-flow/cf-15) run `f`, oracle run 40692334073 (flow input `{"k":1}`, second branch returning `1`: result `[{"k":1},1]`).

<a id="cf-16"></a>
### CF-16 · `failure_module` inside containers

**Scope:** core · **Finding:** [CF-16](control-flow.md#cf-16)

- **CF-16/a** · source-derived · The copied handler runs once per failed iteration.
  - **Given:** flow `failure_module` = `ret({"handled": x}) x = flow_input.iter.value`;
    forloop over `[1,2]`, `skip_failures: true`, body `[fail("e")]`.
  - **Expect:** status success, result `[{"handled":1},{"handled":2}]`; the
    handler ran twice (two child handler jobs).
- **CF-16/b** · source-derived · Without `skip_failures` the loop stops after the first handled failure.
  - **Given:** as CF-16/a with `skip_failures: false`.
  - **Expect:** status failure, result `[{"handled":1}]`; the handler ran
    once.
- **CF-16/c** · source-derived · Recovery counting.
  - **Given / Expect:** as [CF-V1/a to CF-V1/c](#cf-v1).
- **CF-16/d** · source-derived · Handler id per representation.
  - **Given / Expect:** as [CF-V4/a and CF-V4/b](#cf-v4).

<a id="cf-17"></a>
### CF-17 · Subflows (`type: flow`, by path)

**Scope:** core · **Finding:** [CF-17](control-flow.md#cf-17)

- **CF-17/a** · source-derived · A subflow has its own inputs and results scope.
  - **Given:** deploy `f/t/child` = `[s: ret(x*2) x = flow_input.v]`. Parent:
    `c` = `{type: flow, path: f/t/child, input_transforms: {v: {type: javascript, expr: "flow_input.n"}}}`,
    then `d: ret(x) x = [previous_result, results.s ?? "hidden"]`. Parent
    input `{"n": 4}`.
  - **Expect:** result `[8, "hidden"]`.

<a id="cf-18"></a>
### CF-18 · `identity`, skipped containers, mocked containers

**Scope:** core · **Finding:** [CF-18](control-flow.md#cf-18)

- **CF-18/a** · source-derived · A skipped container passes `previous_result` through.
  - **Given:** `a: ret(42)`; forloop `l` over `[1,2]` with
    `skip_if: {expr: "true"}`; then `n: ret(x) x = previous_result`.
  - **Expect:** result `42`.

<a id="cf-19"></a>
### CF-19 · How `results.<id>` resolves across container boundaries

**Scope:** core · **Finding:** [CF-19](control-flow.md#cf-19)

- **CF-19/a** · oracle-observed · Visibility of inner and outer steps after containers.
  - **Given:** `a: ret(1)`; `ba` = branchone with default
    `[x1: ret(x+1) x = results.a]`; `l` = sequential forloop over `[10,20]`
    with body `[y1: ret(x) x = flow_input.iter.value, y2: ret(x) x = results.y1]`;
    `p` = parallel forloop over `[1]` with body `[z1: ret(5), z2: ret(0)]`;
    `f: ret(x) x = [results.x1, results.y1, results.z1, results.l, results.nope]`.
  - **Expect:** result `[2, 20, null, [10,20], null]`.
  - **Recorded:** [`flows/cf/cf-19`](../../conformance/flows/cf/cf-19) run `cf19-a`, oracle run 40687124625.
- **CF-19/b** · oracle-observed · Direct lookup gives `null`; the JS engine throws.
  - **Given:** no step named `missing`. `a: ret(1)`;
    `b: ret(x) x = results.missing[2].z`; `c: ret(x) x = results.missing.y[2].z`.
  - **Expect:** `b` gets `null`; `c` fails with a transform-evaluation error
    (shape per [expressions.md](expressions.md)): name `ExecutionErr`,
    message ``Error during isolated evaluation of expression `results.missing.y[2].z`:`` followed by
    `\nQuickJS evaluation error: Error: cannot read property 'y' of null\n` and
    the engine's frame line; the flow fails.
  - **Recorded:** [`flows/cf/cf-19`](../../conformance/flows/cf/cf-19) run `cf19-b`, oracle run 40687124625.
- **CF-19/c** · oracle-observed · Empty-branch `branchone` lookups depend on the route.
  - **Given:** `a: ret(42)`; `br` = branchone with no matching branch and
    `default: []`; `c: ret(x) x = [results.br]`; `d: ret(x) x = results.br`;
    `e: ret(x) x = [results.br]`.
  - **Expect:** `c` → `[42]`; `d` → `null`; `e` fails with a message
    containing `Failed to fetch result for step 'br'`. Exactly, as `later`
    scope: name `ExecutionErr`, and the message is the transform-error
    envelope around
    `Failed to fetch result for step 'br': Not found: Completed Job not found at name 00000000-0000-0000-0000-000000000000<loc>`
    followed by the engine's frame lines.
  - **Recorded:** [`flows/cf/cf-19`](../../conformance/flows/cf/cf-19) run `cf19-c`, oracle run 40687124625 (`<loc>` was ` (jobs.rs:9074)`).

<a id="cf-20"></a>
### CF-20 · Values crossing container boundaries are JSON copies

**Scope:** core · **Finding:** [CF-20](control-flow.md#cf-20)

- **CF-20/a** · source-derived · Sibling branches mutate independent copies.
  - **Given:** `a: ret({"v": [1]})`; a branchall with two branches, each
    `[ret((x.v.push(9), x)) x = results.a]`.
  - **Expect:** `[{"v":[1,9]},{"v":[1,9]}]`; neither entry is `[1,9,9]`.

<a id="cf-21"></a>
### CF-21 · Evaluation timing of container-level fields

**Scope:** later · **Finding:** [CF-21](control-flow.md#cf-21)

- **CF-21/a** · source-derived · A loop's `skip_if` is re-evaluated mid-loop ([Q-CF-6](control-flow.md#q-cf-6)).
  - **Given:** by path, top level (root `flow`, so the loop collapses).
    `a: ret(0)`; forloop over `[1,2,3]` with
    `skip_if: {expr: "previous_result == 2"}` and body
    `[ret(x) x = flow_input.iter.value]`.
  - **Expect (predicted):** iteration 0 runs (previous 0), iteration 1 runs
    (previous 1), then the skip check sees previous 2 and skips: the loop
    step's result is `2`.

<a id="cf-22"></a>
### CF-22 · `retry` on a container module

**Scope:** later · **Finding:** [CF-22](control-flow.md#cf-22)

- **CF-22/a** · source-derived · A retried non-collapsed iteration sees `iter_parent` ([Q-CF-7](control-flow.md#q-cf-7)).
  - **Given:** forloop over `[1]` with explicit `skip_failures: false` and
    `retry: {constant: {attempts: 1, seconds: 0}}`; two-module body `[b, c]`:
    `b` uses an independent attempt counter (for example a variable read and
    written through the client), fails on the first attempt and on the
    second returns `flow_input.iter_parent ?? null`; `c: ret(x) x = results.b`.
  - **Expect:** status success, result `[{"index":0,"value":1}]`.

<a id="cf-23"></a>
### CF-23 · Errors raised while starting a container

**Scope:** core · **Finding:** [CF-23](control-flow.md#cf-23)

- **CF-23/a** · source-derived · Root flow at pickup: a step failure, handled normally.
  - **Given:** step 0 = forloop over `flow_input` (an object); flow handler
    `ret("handled")`.
  - **Expect:** status failure, result `"handled"`.
- **CF-23/b** · source-derived · During chaining: the flow aborts with `InternalError`.
  - **Given:** the same loop after `a: ret(1)`.
  - **Expect:** status failure, `error.name == "InternalError"`, message
    containing `Expected an array value in the iterator expression`; the
    handler did not run.
- **CF-23/c** · source-derived · Child flow at pickup.
  - **Given / Expect:** as [CF-V3/a](#cf-v3).

<a id="cf-24"></a>
### CF-24 · Child job paths and step ids

**Scope:** later · **Finding:** [CF-24](control-flow.md#cf-24)

- **CF-24/a** · source-derived · Synthesized path of an inline body step.
  - **Given:** deployed flow `f/t/x`, run by path. Forloop `l` over `[1]`
    with body `[b: rawscript returning its job-path environment value, c: ret(x) x = results.b]`
    (environment values per [languages.md](languages.md)).
  - **Expect:** `results.l = ["f/t/x/loop-0/b"]`.
- **CF-24/b** · source-derived · The lite form drops an explicit inline path.
  - **Given:** as CF-24/a with `path: f/t/custom` on `b`; deployed run with
    the lite form recorded as selected.
  - **Expect:** `["f/t/x/loop-0/b"]`.
- **CF-24/c** · source-derived · A raw run keeps the explicit path.
  - **Given:** CF-24/b's flow as a raw flow payload.
  - **Expect:** `["f/t/custom"]`.
- **CF-24/d** · source-derived · A by-path `script` body keeps its own path.
  - **Given:** deployed flow `f/t/x`, run by path; a sequential forloop over
    `[1]` whose single body module is `type: script, path: f/t/body`; script
    `f/t/body` returns its job-path environment value.
  - **Expect:** `["f/t/body"]`.
- **CF-24/e** · source-derived · A collapsed inline body gets the iteration path.
  - **Given:** as CF-24/d with an inline single-module body returning its job
    path.
  - **Expect:** `["f/t/x/loop-0"]`.

<a id="cf-25"></a>
### CF-25 · `squash`

**Scope:** EE · **Finding:** [CF-25](control-flow.md#cf-25)

- **CF-25/a** · source-derived · CE rejects a squashed loop.
  - **Given:** CE; a sequential forloop over `[1]` with `squash: true`.
  - **Expect:** the run fails with the message
    `Squashing flow loops is not available in OSS`.
- **CF-25/b** · source-derived · EE data semantics ([Q-CF-11](control-flow.md#q-cf-11)).
  - **Given:** EE; the same loop.
  - **Expect:** unverified hypothesis only: the same results as without
    squash. No expectation is established.

<a id="cf-26"></a>
### CF-26 · Representation and restart details

**Scope:** core · **Finding:** [CF-26](control-flow.md#cf-26)

- **CF-26/a** · source-derived · The virtual identity step keeps the body result.
  - **Given:** a loop body whose last step has
    `sleep: {type: static, value: 0}`.
  - **Expect:** the iteration result equals that step's result.
  - Stored-node effects: see [CF-V4](#cf-v4), [CF-24](#cf-24) and
    [CF-8/b](#cf-8).

<a id="cf-27"></a>
### CF-27 · Container `timeout` enforcement

**Scope:** later · **Finding:** [CF-27](control-flow.md#cf-27)

Common setup: a deployed flow, run by path after a successful dependency
job, no handler. A top-level sequential forloop `l` over `[1,2]` with
`timeout: {type: static, value: 1}` and a single Bun body module that waits
3 s (collapsed; root `flow`). `T` is
`{"error":{"name":"ExecutionErr","message":"execution error:\njob process terminated due to timeout after exceeding job-specific duration limit","step_id":"l"}}`
(the one-second timeout must be below the instance maximum).

- **CF-27/a** · source-derived · Collapsed body jobs are timed; no skip.
  - **Given:** the common setup with `skip_failures: false`.
  - **Expect:** status failure, result `[T]`; the second iteration never
    starts.
- **CF-27/b** · source-derived · Collapsed body jobs are timed; skip.
  - **Given:** the common setup with `skip_failures: true`.
  - **Expect:** status success, result `[T, T]`.
- **CF-27/c** · source-derived · Child flows are not timed.
  - **Given:** the common setup with a two-step body (not collapsed); the
    inner steps have no timeouts and step 1 waits 3 s. The inner steps'
    effective default or instance deadlines must exceed 3 s.
  - **Expect:** status success with normal results.
- **CF-27/d** · source-derived · A raw submission does not collapse, so it is not timed.
  - **Given:** the single-step flow of the common setup as a raw submission
    ([CF-V5](control-flow.md#cf-v5)).
  - **Expect:** status success with normal results.

<a id="cf-28"></a>
### CF-28 · Cancellation inside containers

**Scope:** later · **Finding:** [CF-28](control-flow.md#cf-28)

- **CF-28/a** · source-derived · Cancelling the root reaches held iterations.
  - **Given:** a deployed flow, run by path. Parallel forloop over `[1,2,3]`
    with `parallelism: 1`; body = one module that sleeps 30 s plus a second
    step, so iterations are child flows. Wait until iteration 0's body job is
    observed **running** and iterations 1 and 2 are observed **held**; then
    cancel the root with user `tester` and reason `test`.
  - **Expect:** the root eventually completes with
    `{"error":{"name":"Canceled","message":"Job canceled: test by tester","reason":"test","canceler":"tester"}}`.
    Iterations 1 and 2 complete as cancelled and run no body code (no body
    logs). Assert neither iteration 0's final status nor any kill latency.

<a id="cf-29"></a>
### CF-29 · An empty container's own `stop_after_if` stops the flow

**Scope:** core · **Finding:** [CF-29](control-flow.md#cf-29)

- **CF-29/a** · source-derived · An error stop on an empty loop fails the flow.
  - **Given:** forloop over `[]` with
    `stop_after_if: {expr: "true", error_message: "empty"}`, then
    `n: ret("after")`.
  - **Expect:** status failure,
    `{"error": {"name": "EarlyStopError", "message": "empty"}}`; `n` does not
    run.

<a id="cf-30"></a>
### CF-30 · The failure and preprocessor modules are slots, not list members

**Scope:** core · **Finding:** [CF-30](control-flow.md#cf-30)

- **CF-30/a** · source-derived · The failure module's `sleep` is never evaluated.
  - **Given:** `[a: fail("x")]`; flow `failure_module` = `h: ret("handled")`
    carrying `sleep: {type: javascript, expr: "nope"}`. Run by path.
  - **Expect:** status failure, result `"handled"`; no error from the
    `sleep` expression. (The same `sleep` on a list step that has a next
    step fails the flow when that step starts;
    [JE-28](expressions.md#je-28).)
- **CF-30/b** · source-derived · later · A forloop in a container-valued preprocessor's body never collapses.
  - **Given:** a deployed flow with no `failure_module`, whose
    `preprocessor_module` is a branchone `p` with no branches and default
    `[a2: ret(100), inner: <the loop of CF-8/a>]`, and one module
    `s: ret(x) x = flow_input`. Run by path with preprocessing applied
    ([FX-35](execution.md#fx-35)).
  - **Expect:** the inner loop runs as child flows (`flowpreview`), so the
    preprocessor's result is `[-999,-998,-997]` and the flow's input
    becomes `{"value": [-999,-998,-997]}`; result
    `{"value": [-999,-998,-997]}`. (In the module list the same branchone's
    body would be a `flownode` child, and the loop would give
    `[101,103,106]`; [CF-8/b](#cf-8).)
- **CF-30/c** · source-derived · later · A preprocessor that is itself a collapsible forloop collapses.
  - **Given:** a deployed flow with no `failure_module`, whose
    `preprocessor_module` is a sequential forloop `p` over `[1,2]` with the
    one-module body `[b: ret(x) x = flow_input.iter.value]` (an inline Bun
    script with no disqualifying field), and one module
    `s: ret(x) x = flow_input`. Run by path with preprocessing applied
    ([FX-35](execution.md#fx-35)).
  - **Expect:** the first child job dispatched for `p` is a script job of
    kind `preview` (a collapsed iteration running the unreduced inline
    script) directly under the root `flow` job, not a `flowpreview` child
    flow and not a `flowscript` job. Only the dispatch is asserted, not the
    preprocessor's result or the flow's.

<a id="cf-v1"></a>
### CF-V1 · Recovery success differs between sequential propagation and parallel aggregation

**Scope:** core · **Finding:** [CF-V1](control-flow.md#cf-v1)

- **CF-V1/a** · source-derived · A sequential loop counts a recovered child as failed.
  - **Given:** the flow handler returns exactly `{"recover":true}`; a
    sequential loop over `[1,2]` whose body always fails, explicit
    `skip_failures: false`.
  - **Expect:** status failure, result `[{"recover":true}]`.
- **CF-V1/b** · source-derived · A parallel loop counts recovered children as succeeded.
  - **Given:** as CF-V1/a with `parallel: true`.
  - **Expect:** status success, result `[{"recover":true},{"recover":true}]`.
- **CF-V1/c** · source-derived · The same split for `branchall`.
  - **Given:** repeat CF-V1/a and CF-V1/b with a sequential and a parallel
    branchall whose branches always fail and explicitly set
    `skip_failure: false`.
  - **Expect:** as CF-V1/a and CF-V1/b respectively.

<a id="cf-v2"></a>
### CF-V2 · Optimized empty containers bypass the all-iterations stop predicate

**Scope:** core · **Finding:** [CF-V2](control-flow.md#cf-v2)

- **CF-V2/a** · oracle-observed · `stop_after_all_iters_if` is not evaluated on an empty loop.
  - **Given:** loop `l` over `[]` with a non-empty body and
    `stop_after_all_iters_if: {expr: "true"}`, followed by a step returning
    `"after"`.
  - **Expect:** status success, result `"after"`.
  - **Recorded:** [`flows/control-flow/cf-13`](../../conformance/flows/control-flow/cf-13) run `c`, oracle run 40685057858 (also CF-13/c).
- **CF-V2/b** · source-derived · The container's own `stop_after_if` stops the flow.
  - **Given:** as CF-V2/a with `stop_after_if: {expr: "true"}` instead.
  - **Expect:** status success, result `[]`; the later step does not run.
- **CF-V2/c** · source-derived · Same for a zero-branch `branchall`.
  - **Given:** repeat CF-V2/a and CF-V2/b with a branchall with
    `branches: []`.
  - **Expect:** as CF-V2/a and CF-V2/b respectively.

<a id="cf-v3"></a>
### CF-V3 · Child-flow startup errors bypass that child's failure module

**Scope:** core · **Finding:** [CF-V3](control-flow.md#cf-v3)

- **CF-V3/a** · source-derived · The child's handler is bypassed; the parent's runs.
  - **Given:** deploy child `f/t/start_error`, whose first module is a
    forloop with static iterator `{}` and whose own handler returns
    `"child-handled"`. A parent calls it and has a handler returning
    `"parent-handled"`.
  - **Expect:** parent status failure, result `"parent-handled"`. The child's
    result is an error object with `error.name = "Anyhow"` and a message
    containing `Expected an array value in the iterator expression`, not
    `"child-handled"`.
- **CF-V3/b** · source-derived · Without the call boundary the root handler applies.
  - **Given:** run `f/t/start_error` directly as the root flow.
  - **Expect:** its own handler may run (route 1 of
    [CF-23](control-flow.md#cf-23)).

<a id="cf-v4"></a>
### CF-V4 · Stored child nodes do not receive runtime handler-id suffixes

**Scope:** core · **Finding:** [CF-V4](control-flow.md#cf-v4)

Common setup: parent handler id `h`; a sequential loop at position zero over
`[1]` whose body fails, explicit `skip_failures: true`; the handler returns
its flow-step-id environment value. Record the selected representation as
part of the setup.

- **CF-V4/a** · source-derived · Stored-node body: the original handler id.
  - **Given:** the common setup using the successfully built lite version
    (body in a flow node).
  - **Expect:** status success, result `["h"]`.
  - **Note:** not recorded with this setup, but the same rule is recorded
    by [`flows/execution/fx-34`](../../conformance/flows/execution/fx-34) run
    `step_id` (handler id `failure`, loop at cursor 1, two failing
    iterations, result `["failure", "failure"]`), oracle run 40689442792.
- **CF-V4/b** · source-derived · Inline body: the suffixed handler id.
  - **Given:** the common setup as an otherwise equivalent inline child
    payload.
  - **Expect:** status success, result `["h-0-0"]`.

<a id="cf-v5"></a>
### CF-V5 · For-loop fast-path eligibility follows runtime job kind

**Scope:** core · **Finding:** [CF-V5](control-flow.md#cf-v5)

Common setup: CF-8/a's flow: `a: ret(100)`, then a sequential forloop over
`[1,2,3]` whose one-module body returns the iteration value plus the numeric
`previous_result`, or plus `-1000` when `previous_result` is not a number.
Record the actual job kinds rather than labelling a run "raw" or "editor".

- **CF-V5/a** · source-derived · Deployed by path: the loop collapses.
  - **Given:** run by path after a successful dependency job.
  - **Expect:** status success, `[101,103,106]`.
- **CF-V5/b** · source-derived · Raw-flow preview request: the loop does not collapse.
  - **Given:** submit the same flow value as a raw-flow preview request.
  - **Expect:** status success, `[-999,-998,-997]` (each iteration is a
    child flow).

<a id="cf-v6"></a>
### CF-V6 · Direct result lookup can ignore surrounding multiline expression text

**Scope:** core · **Finding:** [CF-V6](control-flow.md#cf-v6)

- **CF-V6/a** · oracle-observed · A multiline transform whose first line is a direct lookup.
  - **Given:** `a` returns `7`; an intervening step returns `0`; the final
    step returns its transformed input `x`, whose transform text is
    `results.a`, a newline, then `+ 100`. Use an actual newline character,
    not a two-character backslash-n in the expression.
  - **Expect:** status success, final result `7`.
  - **Recorded:** [`flows/cf/cf-19`](../../conformance/flows/cf/cf-19) run `cf-v6-a`, oracle run 40687124625.
  - **Correction 2026-10-28:** the case was `disputed`
    ([Q-CF-16](control-flow.md#q-cf-16)) and also stated the competing
    expectation `107` (direct lookup needing the whole expression). The
    oracle recorded `7`, the line-anchored reading of
    [CF-V6](control-flow.md#cf-v6).
- **CF-V6/b** · oracle-observed · Single-line control.
  - **Given:** as CF-V6/a with the single-line transform `results.a + 100`.
  - **Expect:** `107`.
  - **Recorded:** [`flows/cf/cf-19`](../../conformance/flows/cf/cf-19) run `cf-v6-b`, oracle run 40687124625.

<a id="expressions"></a>
## Expressions

Findings: [expressions.md](expressions.md).

<a id="je-notation"></a>
### Notation

**Default fixture.** Unless a case says otherwise, a case is a two-step flow: a step `a` whose script returns the value written `a=…`, then a step `b` whose input transform `x` (`type: javascript`) holds the expression, written `x = <expr>`. "Static" marks a `type: static` transform. The expected value is `b`'s argument `x`, or, when the case says `b` fails, `b`'s error.

**Conventions inside cases.**

- Where a case needs `a` *not* to be the previous step, a step `m` is inserted between `a` and `b`, so the previous-step shortcut ([JE-8](expressions.md#je-8)) does not fire.
- A newline inside an expression is written `\n`.
- "Module 0" means the step under test is the flow's first module, so its `previous_result` is the flow input.
- `F` is a failure module that returns `"ran"`; `FS` is a subflow's own failure module.
- When a case needs a step output that Bun cannot produce exactly (an integer beyond 2^53, a negative-zero token), the producer is a Python step.
- Cases tagged `slow` need a generous time allowance; the timeout case of JE-2 needs at least 75 s (3 attempts of 20 s plus 2 pauses of 5 s, plus overhead).

**Observation points.** Each expectation is observed at one of these points; the default is O2.

| Point | What is observed |
|---|---|
| **O1** evaluator output | The JSON text the evaluator returns for a context and an expression. Only an implementation that exposes its evaluator can use it. |
| **O2** recorded args | Job `b`'s argument JSON as the job API returns it: `jsonb`-normalised and before `$var:`/`$res:` resolution. |
| **O3** resolved args | The argument JSON after leaf resolution, before any language runtime touches it. |
| **O4** script observation | What `b`'s script sees or returns after its language runtime parsed the arguments. Bun rounds integers beyond 2^53. |
| **O5** flow and job outcome | The flow's final result, each module's status, and each child job's own row (result and success flag). |

O1 is not the only point where pre-database values matter: a step can compute something from an in-memory value and record it, which then shows at O2 and O5. A case at O2 to O5 that depends on whether a read came from memory or from the database says which read it uses.

**Comparing.**

- **Structure and values:** compare exactly, as JSON values. Large integers are compared numerically, without parsing them through JavaScript.
- **Key order:** top-level key order is never significant. Nested key order counts only where a case says so. No case may assert the order of `Object.keys(flow_input)` or of a script's top-level argument keys, because that order comes from a hash map.
- **Engine messages** (text after `QuickJS evaluation error: `): the quickjs-ng messages `nope is not defined`, `cannot read property 'x' of null`, `cannot read property 'x' of undefined` and `BigInt are forbidden in JSON.stringify` may be matched exactly up to the first newline. Stack text and any other engine wording are compared by prefix or substring ([Q-JE-1](expressions.md#q-je-1)).
- **Rust location suffix:** strip ` @<file>.rs:<line>:<col>` before comparing. `<loc>` in an expected message stands for one such suffix **including its leading space** (or the parenthesised not-found form ` (<file>.rs:<line>)`), with nothing else between it and the text before it; a message that ends in `<loc><loc>` carries two ([FX-50](execution.md#fx-50)).
- **`…`** in an expected message marks elided text: a stack, a location suffix, or the rest of a library message.

**Provenance.** Recording batch E3a recorded every case of this area on the v1.767.0 CE oracle except three, each of which names its recording. Cases of the expression files `conformance/exprs/je-*.yaml` were recorded as one-module flows: step `b` is module 0, so its `previous_result` is the flow input, and the observation is `b`'s recorded argument (O2). The rest are runs of the flow fixtures under `conformance/flows/je` and `conformance/flows/cf`. Not recorded, and still `source-derived`: JE-16/c (an evaluator-level case, O1, whose raw `-0` no flow input can carry; checked in milestone B2b), JE-19/e (needs a restarted run, [Q-JE-8](expressions.md#q-je-8); recording batch E3e) and JE-V5/a (which transform fails first changes between runs, so it is a host- or evaluator-level case; B2b and PF2). New cases added on 2026-10-28 (JE-50/a, JE-51/a, JE-52/a) are `source-derived` until recorded ([Q-JE-12](expressions.md#q-je-12)).

<a id="je-1"></a>
### JE-1 · QuickJS-ng evaluates every expression that reaches the engine

**Scope:** core · **Finding:** [JE-1](expressions.md#je-1)

*Also recorded under this finding, without a case ID:* the census of host APIs (`absent-host-apis`, `present-built-ins`) and the libm digits of transcendental `Math` functions (`math-trig`, `math-exp-log`, `math-pow-roots`, `math-hyperbolic`) in [`exprs/je-1.yaml`](../../conformance/exprs/je-1.yaml), oracle run 40687124625; [JE-1](expressions.md#je-1) states the rules.

- **JE-1/a** · oracle-observed · `Intl` is absent ([Q-JE-2](expressions.md#q-je-2))
  - **Given:** `x = typeof Intl`
  - **Expect:** `"undefined"`
  - **Recorded:** [`exprs/je-1.yaml`](../../conformance/exprs/je-1.yaml) case `JE-1/a`, oracle run 40687124625.
- **JE-1/b** · oracle-observed · `atob` is absent ([Q-JE-2](expressions.md#q-je-2))
  - **Given:** `x = typeof atob`
  - **Expect:** `"undefined"`
  - **Recorded:** [`exprs/je-1.yaml`](../../conformance/exprs/je-1.yaml) case `JE-1/b`, oracle run 40687124625.
- **JE-1/c** · oracle-observed · ES2023 array methods exist
  - **Given:** `x = [1,2,3].at(-1)`
  - **Expect:** `3`
  - **Recorded:** [`exprs/je-1.yaml`](../../conformance/exprs/je-1.yaml) case `JE-1/c`, oracle run 40687124625.
- **JE-1/d** · oracle-observed · `Object.groupBy` exists
  - **Given:** `x = Object.groupBy([1,2,3], n => n % 2 ? "odd" : "even")`
  - **Expect:** `{"odd":[1,3],"even":[2]}`
  - **Recorded:** [`exprs/je-1.yaml`](../../conformance/exprs/je-1.yaml) case `JE-1/d`, oracle run 40687124625.
- **JE-1/e** · oracle-observed · `console` is absent ([Q-JE-2](expressions.md#q-je-2))
  - **Given:** `x = typeof console`
  - **Expect:** `"undefined"`
  - **Recorded:** [`exprs/je-1.yaml`](../../conformance/exprs/je-1.yaml) case `JE-1/e`, oracle run 40687124625.

<a id="je-2"></a>
### JE-2 · An evaluation has a 20 s wall-clock limit, tried up to 3 times

**Scope:** later · **Finding:** [JE-2](expressions.md#je-2)

- **JE-2/a** · oracle-observed · a CPU-bound loop outlasts the zombie-flow monitor (slow)
  - **Given:** `x = (() => { while (true) {} })()`; allow at least 75 s.
  - **Expect:** on the oracle the flow is **canceled** after about 60 s, before `b` runs: status `canceled`, error `{"name":"Canceled","canceler":"monitor","reason":…,"message":…}`, whose reason and message (times, worker names) are not compared. The evaluator itself would report `took too long to execute (>20000ms)` after its third attempt, about 70 s in, but the zombie-flow monitor cancels the flow first ([JE-2](expressions.md#je-2)). The outcome depends on that timing.
  - **Correction 2026-10-28:** the case expected `b` to fail with the timeout message; the monitor's 60 s threshold is shorter than the three attempts.
  - **Recorded:** [`exprs/je-2.yaml`](../../conformance/exprs/je-2.yaml) case `JE-2/a`, oracle run 40688787344.

<a id="je-3"></a>
### JE-3 · An evaluation is capped at 128 MiB of heap

**Scope:** later · **Finding:** [JE-3](expressions.md#je-3)

- **JE-3/a** · oracle-observed · a `null` throw is reported as out of memory
  - **Given:** `x = (() => { throw null })()`
  - **Expect:** `b` fails; its message contains `exceeded the memory limit of 128 MB`.
  - **Recorded:** [`exprs/je-3.yaml`](../../conformance/exprs/je-3.yaml) case `JE-3/a`, oracle run 40687124625.

<a id="je-5"></a>
### JE-5 · Trimming, and what error messages quote

**Scope:** core · **Finding:** [JE-5](expressions.md#je-5)

- **JE-5/a** · oracle-observed · surrounding whitespace is trimmed before evaluation
  - **Given:** flow input `{"n":5}`; `x = "\n  flow_input.n \n"` (the expression text itself starts and ends with whitespace)
  - **Expect:** `5`
  - **Recorded:** [`exprs/je-5.yaml`](../../conformance/exprs/je-5.yaml) case `JE-5/a`, oracle run 40687124625.
- **JE-5/b** · oracle-observed · later · the site message quotes the untrimmed text
  - **Given:** `x = " nope "` (one leading and one trailing space in the expression text)
  - **Expect:** `b` fails; its message starts with ``Error during isolated evaluation of expression ` nope `:`` followed by a newline (spaces kept), and contains `nope is not defined`.
  - **Recorded:** [`exprs/je-5.yaml`](../../conformance/exprs/je-5.yaml) case `JE-5/b`, oracle run 40687124625.

<a id="je-6"></a>
### JE-6 · An exact context name returns the stored value verbatim

**Scope:** core · **Finding:** [JE-6](expressions.md#je-6)

*Setup for both cases:* `b` is module 0; flow input `{"n":12345678901234567890}`, so `previous_result` is that input.

- **JE-6/a** · oracle-observed · the shortcut keeps full precision
  - **Given:** `x = previous_result`
  - **Expect:** `{"n":12345678901234567890}`, exact.
  - **Recorded:** [`exprs/je-6.yaml`](../../conformance/exprs/je-6.yaml) case `JE-6/a`, oracle run 40687124625.
- **JE-6/b** · oracle-observed · JavaScript rounds the same value
  - **Given:** `x = previous_result.n`
  - **Expect:** `12345678901234567000`
  - **Recorded:** [`exprs/je-6.yaml`](../../conformance/exprs/je-6.yaml) case `JE-6/b`, oracle run 40687124625.

<a id="je-7"></a>
### JE-7 · A top-level `flow_input` or `flow_env` key returns the stored value verbatim

**Scope:** core · **Finding:** [JE-7](expressions.md#je-7)

*Setup for JE-7/a–e:* flow input `{"n":12345678901234567890,"a.b":"lit","a":{"b":"nested"}}`. *Setup for JE-7/f–g:* flow input `{}`.

- **JE-7/a** · oracle-observed · a top-level key keeps full precision
  - **Given:** `x = flow_input.n`
  - **Expect:** `12345678901234567890`
  - **Recorded:** [`exprs/je-7.yaml`](../../conformance/exprs/je-7.yaml) case `JE-7/a`, oracle run 40687124625.
- **JE-7/b** · oracle-observed · arithmetic goes through JavaScript and rounds
  - **Given:** `x = flow_input.n + 0`
  - **Expect:** `12345678901234567000`
  - **Recorded:** [`exprs/je-7.yaml`](../../conformance/exprs/je-7.yaml) case `JE-7/b`, oracle run 40687124625.
- **JE-7/c** · oracle-observed · later · a dotted text is looked up as one literal key first
  - **Given:** `x = flow_input.a.b`
  - **Expect:** `"lit"`
  - **Recorded:** [`exprs/je-7.yaml`](../../conformance/exprs/je-7.yaml) case `JE-7/c`, oracle run 40687124625.
- **JE-7/d** · oracle-observed · bracket form
  - **Given:** `x = flow_input["a"]`
  - **Expect:** `{"b":"nested"}`
  - **Recorded:** [`exprs/je-7.yaml`](../../conformance/exprs/je-7.yaml) case `JE-7/d`, oracle run 40687124625.
- **JE-7/e** · oracle-observed · a missing direct key gives `null`
  - **Given:** `x = flow_input.missing`
  - **Expect:** `null`, and `x` is present in `b`'s arguments.
  - **Recorded:** [`exprs/je-7.yaml`](../../conformance/exprs/je-7.yaml) case `JE-7/e`, oracle run 40687124625.
- **JE-7/f** · oracle-observed · a missing direct key of an empty input gives `null`
  - **Given:** `x = flow_input.absent`
  - **Expect:** `null`
  - **Recorded:** [`exprs/je-7.yaml`](../../conformance/exprs/je-7.yaml) case `JE-7/f`, oracle run 40687124625.
- **JE-7/g** · oracle-observed · a missing intermediate throws on the JavaScript path
  - **Given:** `x = flow_input.absent.child`
  - **Expect:** `b` fails; its message contains `cannot read property 'child' of undefined`.
  - **Recorded:** [`exprs/je-7.yaml`](../../conformance/exprs/je-7.yaml) case `JE-7/g`, oracle run 40687124625.

<a id="je-8"></a>
### JE-8 · The previous step's id returns `previous_result` verbatim

**Scope:** core · **Finding:** [JE-8](expressions.md#je-8)

*Also recorded under this finding, without a case ID:* the key-order discriminator of runs `je8-order-transform`, `je8-order-timeout` and `je8-order-timeout-db` of [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts), oracle run 40687124625 ([JE-8](expressions.md#je-8), [JE-29](expressions.md#je-29)).

- **JE-8/a** · oracle-observed · the whole-result spelling returns the stored text
  - **Given:** `a` is a Python step returning `{"n": 12345678901234567890}`; `b` directly after it; `x = results.a`
  - **Expect:** `{"n":12345678901234567890}`
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je8-a`, oracle run 40687124625.
- **JE-8/b** · oracle-observed · parentheses defeat the shortcut, and JavaScript rounds
  - **Given:** as JE-8/a, with `x = (results.a)`
  - **Expect:** `{"n":12345678901234567000}`
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je8-b`, oracle run 40687124625.
- **JE-8/c** · oracle-observed · later · the empty previous id at module 0
  - **Given:** `b` is module 0; flow input `{"seed":4}`; `x = results[""]`
  - **Expect:** `{"seed":4}`
  - **Recorded:** [`exprs/je-8.yaml`](../../conformance/exprs/je-8.yaml) case `JE-8/c`, oracle run 40687124625.
- **JE-8/d** · oracle-observed · later · an invalid-JavaScript spelling still matches
  - **Given:** as JE-8/c, with `x = results.`
  - **Expect:** `{"seed":4}`
  - **Recorded:** [`exprs/je-8.yaml`](../../conformance/exprs/je-8.yaml) case `JE-8/d`, oracle run 40687124625.

<a id="je-9"></a>
### JE-9 · Simple `results` paths are served by a lenient database lookup

**Scope:** core · **Finding:** [JE-9](expressions.md#je-9)

*Setup unless stated:* a step `m` sits between `a` and `b`.

- **JE-9/a** · oracle-observed · a `null` intermediate gives `null`
  - **Given:** `a={}`; `x = results.a.b.c`
  - **Expect:** `null`
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je9-a`, oracle run 40687124625.
- **JE-9/b** · oracle-observed · the same path in parentheses throws in JavaScript
  - **Given:** `a={}`; `x = (results.a.b.c)`
  - **Expect:** `b` fails; its message starts with ``Error during isolated evaluation of expression `(results.a.b.c)`:`` and contains `QuickJS evaluation error: Error:`.
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je9-b`, oracle run 40687124625.
- **JE-9/c** · oracle-observed · a final `.length` declines the shortcut
  - **Given:** `a=[10,20,30]`; `x = results.a.length`
  - **Expect:** `3`
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je9-c`, oracle run 40687124625.
- **JE-9/d** · oracle-observed · a missing step gives `null`
  - **Given:** `x = results.nope.x`
  - **Expect:** `null`
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je9-d`, oracle run 40687124625.
- **JE-9/e** · oracle-observed · an index selects a loop child's result
  - **Given:** a loop `l` over `[1,2]` whose body returns ten times the item, then `b` with `x = results.l[1]`
  - **Expect:** `20`
  - **Recorded:** [`flows/je/loops`](../../conformance/flows/je/loops) run `je9-ef`, oracle run 40687124625.
- **JE-9/f** · oracle-observed · a non-numeric first element on a loop gives `null`
  - **Given:** as JE-9/e, with `x = results.l.foo`
  - **Expect:** `null`
  - **Recorded:** [`flows/je/loops`](../../conformance/flows/je/loops) run `je9-ef`, oracle run 40687124625.
- **JE-9/g** · oracle-observed · a matching first line discards the rest
  - **Given:** `a=7`; `x = "results.a\n+ 100"`
  - **Expect:** `7`, not `107`.
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je9-g` and [`flows/cf/cf-19`](../../conformance/flows/cf/cf-19) run `cf-v6-a`, oracle run 40687124625.
- **JE-9/h** · oracle-observed · a later matching line also fires the shortcut
  - **Given:** `a=7`; `x = "100 +\nresults.a"`
  - **Expect:** `7`
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je9-h`, oracle run 40687124625.
- **JE-9/i** · oracle-observed · an indented line never qualifies
  - **Given:** `a=7`; `x = "100 +\n  results.a"`
  - **Expect:** `107`
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je9-i`, oracle run 40687124625.
- **JE-9/j** · oracle-observed · a predicate can be handed a non-boolean
  - **Given:** `a=7`; a later step `c` with `skip_if: {expr: "true ||\nresults.a\n"}`
  - **Expect:** the error `Expected a boolean value, found: "7"`, routed per [JE-35](expressions.md#je-35) route (a).
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je9-j`, oracle run 40687124625.
- **JE-9/k** · oracle-observed · a predicate served by the shortcut
  - **Given:** `a={"flag":true}`; a later step `c` with `skip_if: {expr: "false ||\nresults.a.flag\n"}`
  - **Expect:** `c` is skipped.
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je9-k`, oracle run 40687124625.
- **JE-9/l** · oracle-observed · later · only the first matching line is inspected
  - **Given:** `a=[1,2,3]`; `x = "results.a.length\nresults.a"`
  - **Expect:** the shortcut declines and QuickJS runs; without an inserted semicolon the two lines parse as calling the number `3`, so `b` fails with a message containing `QuickJS evaluation error: Error: not a function`. `b` does **not** receive `[1,2,3]`.
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je9-l`, oracle run 40687124625.

<a id="je-10"></a>
### JE-10 · Names are bound only if they appear in the expression text

**Scope:** core · **Finding:** [JE-10](expressions.md#je-10)

- **JE-10/a** · oracle-observed · later · computed access cannot reach an unmentioned name
  - **Given:** `x = globalThis["flow_" + "input"]`
  - **Expect:** `null` (`undefined` becomes `null`).
  - **Recorded:** [`exprs/je-10.yaml`](../../conformance/exprs/je-10.yaml) case `JE-10/a`, oracle run 40687124625.
- **JE-10/b** · oracle-observed · `flow_env` defaults to an empty object
  - **Given:** a flow with no `flow_env`; `x = flow_env`
  - **Expect:** `{}`
  - **Recorded:** [`exprs/je-10.yaml`](../../conformance/exprs/je-10.yaml) case `JE-10/b`, oracle run 40687124625.
- **JE-10/c** · oracle-observed · `results` is a lexical binding
  - **Given:** `x = typeof results`
  - **Expect:** `"object"`
  - **Recorded:** [`exprs/je-10.yaml`](../../conformance/exprs/je-10.yaml) case `JE-10/c`, oracle run 40687124625.
- **JE-10/d** · oracle-observed · `results` is not a `globalThis` property
  - **Given:** `x = typeof globalThis.results` (the trailing `results` is not followed by `.` or `["`, so it is not rewritten)
  - **Expect:** `"undefined"`
  - **Recorded:** [`exprs/je-10.yaml`](../../conformance/exprs/je-10.yaml) case `JE-10/d`, oracle run 40687124625.

<a id="je-11"></a>
### JE-11 · Calls to `variable(`, `resource(` and `flow_user_state(` are rewritten textually

**Scope:** later · **Finding:** [JE-11](expressions.md#je-11)

- **JE-11/a** · oracle-observed · the rewrite applies inside string literals
  - **Given:** `x = "call variable(1)"` (the expression is a string literal)
  - **Expect:** `"call (await variable(1))"`
  - **Recorded:** [`exprs/je-11.yaml`](../../conformance/exprs/je-11.yaml) case `JE-11/a`, oracle run 40687124625.

<a id="je-12"></a>
### JE-12 · `results.<id>` is rewritten textually into an awaited lookup

**Scope:** core · **Finding:** [JE-12](expressions.md#je-12)

*Setup for all cases:* `a={"v":3}`, with a step `m` between `a` and `b`.

- **JE-12/a** · oracle-observed · a single-quoted key stays a Promise
  - **Given:** `x = results['a'].v`
  - **Expect:** `null`
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je12`, oracle run 40687124625.
- **JE-12/b** · oracle-observed · a lone Promise is adopted
  - **Given:** `x = results['a']`
  - **Expect:** `{"v":3}`
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je12`, oracle run 40687124625.
- **JE-12/c** · oracle-observed · later · string literals are rewritten
  - **Given:** `x = "see results.a"`
  - **Expect:** `"see (await results.a)"`
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je12`, oracle run 40687124625.
- **JE-12/d** · oracle-observed · later · `results` inside a non-async callback is a syntax error
  - **Given:** `x = [1].map(i => results.a.v)`
  - **Expect:** `b` fails; its message starts with ``Error during isolated evaluation of expression `[1].map(i => results.a.v)`:`` and contains `QuickJS evaluation error: Error: Unexpected identifier 'results'`.
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je12-d`, oracle run 40687124625.
- **JE-12/e** · oracle-observed · bare `results` serialises as an empty object
  - **Given:** `x = results`
  - **Expect:** `{}`
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je12`, oracle run 40687124625.

<a id="je-13"></a>
### JE-13 · Expression mode or statement mode

**Scope:** core · **Finding:** [JE-13](expressions.md#je-13)

*Setup for all cases:* flow input `{"n":5,"s":"a;b"}`.

- **JE-13/a** · oracle-observed · a trailing semicolon selects statement mode
  - **Given:** `x = flow_input.n;`
  - **Expect:** `null`
  - **Recorded:** [`exprs/je-13.yaml`](../../conformance/exprs/je-13.yaml) case `JE-13/a`, oracle run 40687124625.
- **JE-13/b** · oracle-observed · a leading `await ` selects statement mode
  - **Given:** `x = await Promise.resolve(1)`
  - **Expect:** `null`
  - **Recorded:** [`exprs/je-13.yaml`](../../conformance/exprs/je-13.yaml) case `JE-13/b`, oracle run 40687124625.
- **JE-13/c** · oracle-observed · a semicolon in a regex literal selects statement mode
  - **Given:** `x = /;/.test(flow_input.s)`
  - **Expect:** `null`
  - **Recorded:** [`exprs/je-13.yaml`](../../conformance/exprs/je-13.yaml) case `JE-13/c`, oracle run 40687124625.
- **JE-13/d** · oracle-observed · a semicolon inside quotes does not
  - **Given:** `x = flow_input.s.split(";")`
  - **Expect:** `["a","b"]`
  - **Recorded:** [`exprs/je-13.yaml`](../../conformance/exprs/je-13.yaml) case `JE-13/d`, oracle run 40687124625.
- **JE-13/e** · oracle-observed · a leading brace is an object literal
  - **Given:** `x = {a: 1}`
  - **Expect:** `{"a":1}`
  - **Recorded:** [`exprs/je-13.yaml`](../../conformance/exprs/je-13.yaml) case `JE-13/e`, oracle run 40687124625.
- **JE-13/f** · oracle-observed · statement mode returns the `return` value
  - **Given:** `x = let y = flow_input.n * 2; return y + 1`
  - **Expect:** `11`
  - **Recorded:** [`exprs/je-13.yaml`](../../conformance/exprs/je-13.yaml) case `JE-13/f`, oracle run 40687124625.

<a id="je-14"></a>
### JE-14 · A trailing `//` comment breaks a single-line direct expression

**Scope:** later · **Finding:** [JE-14](expressions.md#je-14)

*Setup for both cases:* flow input `{"n":5}`.

- **JE-14/a** · oracle-observed · a trailing line comment fails
  - **Given:** `x = flow_input.n // the count`
  - **Expect:** `b` fails; its message contains `QuickJS evaluation error:` (on the oracle: `Error: unexpected token in expression: ''`).
  - **Recorded:** [`exprs/je-14.yaml`](../../conformance/exprs/je-14.yaml) case `JE-14/a`, oracle run 40687124625.
- **JE-14/b** · oracle-observed · a block comment is fine
  - **Given:** `x = flow_input.n /* the count */`
  - **Expect:** `5`
  - **Recorded:** [`exprs/je-14.yaml`](../../conformance/exprs/je-14.yaml) case `JE-14/b`, oracle run 40687124625.

<a id="je-15"></a>
### JE-15 · The returned value becomes JSON

**Scope:** core · **Finding:** [JE-15](expressions.md#je-15)

- **JE-15/a** · oracle-observed · top-level `undefined`
  - **Given:** `x = undefined`
  - **Expect:** `null`
  - **Recorded:** [`exprs/je-15.yaml`](../../conformance/exprs/je-15.yaml) case `JE-15/a`, oracle run 40687124625.
- **JE-15/b** · oracle-observed · `undefined` and function array items
  - **Given:** `x = [undefined, () => 1]`
  - **Expect:** `[null,null]`
  - **Recorded:** [`exprs/je-15.yaml`](../../conformance/exprs/je-15.yaml) case `JE-15/b`, oracle run 40687124625.
- **JE-15/c** · oracle-observed · `undefined` properties are dropped
  - **Given:** `x = ({a: undefined, b: 1})`
  - **Expect:** `{"b":1}`
  - **Recorded:** [`exprs/je-15.yaml`](../../conformance/exprs/je-15.yaml) case `JE-15/c`, oracle run 40687124625.
- **JE-15/d** · oracle-observed · a `Date` becomes its ISO string
  - **Given:** `x = new Date("2024-01-15T12:30:00.000Z")`
  - **Expect:** `"2024-01-15T12:30:00.000Z"`
  - **Recorded:** [`exprs/je-15.yaml`](../../conformance/exprs/je-15.yaml) case `JE-15/d`, oracle run 40687124625.
- **JE-15/e** · oracle-observed · a `Map` becomes `{}`
  - **Given:** `x = new Map([["k",1]])`
  - **Expect:** `{}`
  - **Recorded:** [`exprs/je-15.yaml`](../../conformance/exprs/je-15.yaml) case `JE-15/e`, oracle run 40687124625.
- **JE-15/f** · oracle-observed · `NaN` becomes `null`
  - **Given:** `x = 0/0`
  - **Expect:** `null`
  - **Recorded:** [`exprs/je-15.yaml`](../../conformance/exprs/je-15.yaml) case `JE-15/f`, oracle run 40687124625.
- **JE-15/g** · oracle-observed · number text comes from `Number#toString`
  - **Given:** `x = 1.0`
  - **Expect:** `1`
  - **Recorded:** [`exprs/je-15.yaml`](../../conformance/exprs/je-15.yaml) case `JE-15/g`, oracle run 40687124625.
- **JE-15/h** · oracle-observed · a `BigInt` fails serialisation
  - **Given:** `x = 10n`
  - **Expect:** `b` fails; its message contains `QuickJS evaluation error:` (the quickjs-ng text is `BigInt are forbidden in JSON.stringify`).
  - **Recorded:** [`exprs/je-15.yaml`](../../conformance/exprs/je-15.yaml) case `JE-15/h`, oracle run 40687124625.
- **JE-15/i** · oracle-observed · a returned Promise is awaited
  - **Given:** `x = Promise.resolve({k: 1})`
  - **Expect:** `{"k":1}`
  - **Recorded:** [`exprs/je-15.yaml`](../../conformance/exprs/je-15.yaml) case `JE-15/i`, oracle run 40687124625.

<a id="je-16"></a>
### JE-16 · Context values arrive as converted JSON

**Scope:** core · **Finding:** [JE-16](expressions.md#je-16)

- **JE-16/a** · oracle-observed · JavaScript arithmetic loses precision beyond 2^53
  - **Given:** flow input `{"n":9007199254740993}`; `x = flow_input.n * 1`
  - **Expect:** `9007199254740992`
  - **Recorded:** [`exprs/je-16.yaml`](../../conformance/exprs/je-16.yaml) case `JE-16/a`, oracle run 40687124625.
- **JE-16/b** · oracle-observed · the shortcut keeps it (compare [JE-7](expressions.md#je-7))
  - **Given:** as JE-16/a, with `x = flow_input.n`
  - **Expect:** `9007199254740993`
  - **Recorded:** [`exprs/je-16.yaml`](../../conformance/exprs/je-16.yaml) case `JE-16/b`, oracle run 40687124625.
- **JE-16/c** · source-derived · later · negative zero survives conversion (O1)
  - **Given:** an evaluator-level fixture with `previous_result` raw JSON `{"n":-0}`; `x = Object.is(previous_result.n, -0)`
  - **Expect:** O1 `true`
- **JE-16/d** · oracle-observed · raw key order, with integer-like keys first (in-memory read)
  - **Given:** `a` returns the raw text `{"b":1,"a":2,"10":3}`; `b` directly after `a`; `x = Object.keys(previous_result)`
  - **Expect:** `["10","b","a"]`
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je16-d`, oracle run 40687124625.
- **JE-16/e** · oracle-observed · later · negative zero from memory is observable downstream
  - **Given:** `a` is a Python step returning `{"n": -0.0}`; `b` directly after `a`; `x = Object.is(previous_result.n, -0)` (in-memory read)
  - **Expect:** O2 `true`
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je16-ef`, oracle run 40687124625.
- **JE-16/f** · oracle-observed · later · the database read sees plain zero
  - **Given:** as JE-16/e, plus a step `c` after `b` with `y = Object.is((results.a).n, -0)` (`a` is no longer the previous step, so this is a database read)
  - **Expect:** O2 `y` = `false`
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je16-ef`, oracle run 40687124625.

<a id="je-17"></a>
### JE-17 · The `error` helper variable

**Scope:** core · **Finding:** [JE-17](expressions.md#je-17)

- **JE-17/a** · oracle-observed · a failure module reads the step error
  - **Given:** `a` throws `Error("boom")`; the failure module has `x = error.message`
  - **Expect:** `x` equals the failed step's `error.message`.
  - **Recorded:** [`flows/je/errors`](../../conformance/flows/je/errors) run `je17-a`, oracle run 40687124625.
- **JE-17/b** · oracle-observed · a non-error previous result
  - **Given:** `a={"ok":true}`; `x = error`
  - **Expect:** `{"name":"UnknownError","message":"Could not parse the error","error":{"ok":true}}`
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je17-b`, oracle run 40687124625.
- **JE-17/c** · oracle-observed · an empty object is truthy
  - **Given:** `b` is module 0; flow input `{}`; `x = error.name`
  - **Expect:** `"UnknownError"`
  - **Recorded:** [`exprs/je-17.yaml`](../../conformance/exprs/je-17.yaml) case `JE-17/c`, oracle run 40687124625.

<a id="je-18"></a>
### JE-18 · How a `results.<id>` read resolves on the QuickJS path

**Scope:** core · **Finding:** [JE-18](expressions.md#je-18)

- **JE-18/a** · oracle-observed · an absent step
  - **Given:** `x = results.nonexistent`
  - **Expect:** `null`
  - **Recorded:** [`exprs/je-18.yaml`](../../conformance/exprs/je-18.yaml) case `JE-18/a`, oracle run 40687124625.
- **JE-18/b** · oracle-observed · nullish coalescing on an absent step
  - **Given:** `x = results.nonexistent ?? "d"`
  - **Expect:** `"d"`
  - **Recorded:** [`exprs/je-18.yaml`](../../conformance/exprs/je-18.yaml) case `JE-18/b`, oracle run 40687124625.
- **JE-18/c** · oracle-observed · optional chaining on an absent step
  - **Given:** `x = results.nonexistent?.v ?? "n"`
  - **Expect:** `"n"`
  - **Recorded:** [`exprs/je-18.yaml`](../../conformance/exprs/je-18.yaml) case `JE-18/c`, oracle run 40687124625.
- **JE-18/d** · oracle-observed · a forward reference
  - **Given:** a flow `a`, `b`, `c`; in `b`, `x = results.c`
  - **Expect:** `null`
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je18-d`, oracle run 40687124625.

<a id="je-19"></a>
### JE-19 · Which `results.<id>` a scope can see

**Scope:** core · **Finding:** [JE-19](expressions.md#je-19)

- **JE-19/a** · oracle-observed · a sequential loop exposes its last iteration
  - **Given:** a flow `a`, then a sequential loop `l` over `[1,2,3]` with a **two-step** body whose last step `i` returns ten times the iteration value, then `b` with `x = results.i`
  - **Expect:** `30`
  - **Recorded:** [`flows/je/loops`](../../conformance/flows/je/loops) run `je19-a`, oracle run 40687124625.
- **JE-19/b** · oracle-observed · a parallel loop does not
  - **Given:** as JE-19/a, with a parallel loop
  - **Expect:** `null`
  - **Recorded:** [`flows/je/scopes`](../../conformance/flows/je/scopes) run `je19-b`, oracle run 40687124625.
- **JE-19/c** · oracle-observed · a collapsed body records only the loop id
  - **Given:** as JE-19/a, with a one-step body, in a deployed run
  - **Expect:** `null`
  - **Recorded:** [`flows/je/loops`](../../conformance/flows/je/loops) run `je19-c`, oracle run 40687124625.
- **JE-19/d** · oracle-observed · later · a subflow reads its caller's step ([Q-JE-10](expressions.md#q-je-10))
  - **Given:** a subflow called by path whose step reads `x = results.a`, called from a flow that has a step `a` returning `7`; the subflow has no step `a`
  - **Expect:** `7`
  - **Recorded:** [`flows/je/scopes`](../../conformance/flows/je/scopes) run `je19-d`, oracle run 40687124625.
- **JE-19/e** · source-derived · later · a restarted run reads its original run ([Q-JE-8](expressions.md#q-je-8))
  - **Given:** a flow `a` (returns `17`), `b` (fails), `c` with `x = results.a`; restart the run from `b`, so `a` is not re-run, with `b` succeeding on restart so `c` is reached
  - **Expect:** `c`'s O2 `x` = `17`

<a id="je-20"></a>
### JE-20 · The `variable()`, `resource()` and `flow_user_state()` helpers

**Scope:** core · **Finding:** [JE-20](expressions.md#je-20)

- **JE-20/a** · oracle-observed · `variable()` returns the value
  - **Given:** a variable `u/test/v` = `"s3cr3t"`; `x = variable("u/test/v")`
  - **Expect:** `"s3cr3t"`
  - **Recorded:** [`flows/je/providers`](../../conformance/flows/je/providers) run `je20-a`, oracle run 40687124625.
- **JE-20/b** · oracle-observed · a missing variable throws
  - **Given:** `x = variable("<a path with no variable>")`
  - **Expect:** `b` fails; its message contains `QuickJS evaluation error: Error:`.
  - **Recorded:** [`flows/je/providers`](../../conformance/flows/je/providers) run `je20-b`, oracle run 40687124625.

<a id="je-21"></a>
### JE-21 · The evaluator's own error text

**Scope:** core · **Finding:** [JE-21](expressions.md#je-21)

- **JE-21/a** · oracle-observed · the full message structure for an unknown global
  - **Given:** `x = nope`
  - **Expect:** `b` fails with the message ``Error during isolated evaluation of expression `nope`:\nQuickJS evaluation error: Error: nope is not defined``, followed by `\n<stack>`; on the oracle the stack is `    at <anonymous> (eval_script:1:1)\n    at <eval> (eval_script:1:33)\n` ([JE-21](expressions.md#je-21)). Compare the prefix through `Error: nope is not defined`.
  - **Recorded:** [`exprs/je-21.yaml`](../../conformance/exprs/je-21.yaml) case `JE-21/a`, oracle run 40687124625.

<a id="je-22"></a>
### JE-22 · Scope table

**Scope:** core · **Finding:** [JE-22](expressions.md#je-22)

The `parallelism` row's cases are under [JE-V1](#je-v1).

- **JE-22/a** · oracle-observed · `sleep` has no `results`
  - **Given:** step `a`, with `sleep: {type: javascript, expr: "results.a"}`, then `b`
  - **Expect:** the flow fails; the error text contains `results is not defined`. Routing per [JE-28](expressions.md#je-28) and [JE-35](expressions.md#je-35).
  - **Recorded:** [`flows/je/waits`](../../conformance/flows/je/waits) run `je22-a`, oracle run 40687124625.
- **JE-22/b** · oracle-observed · `sleep` sees its own module's result
  - **Given:** `a={"s":0}`, with `sleep: {type: javascript, expr: "result.s"}`, then `b`
  - **Expect:** the flow continues.
  - **Recorded:** [`flows/je/waits`](../../conformance/flows/je/waits) run `je22-b`, oracle run 40687124625.
- **JE-22/c** · oracle-observed · `stop_after_if` sees `results` when it names it
  - **Given:** `a={"stop":true}`; `b` with `stop_after_if: {expr: "results.a.stop", skip_if_stopped: true}`, then later steps
  - **Expect:** `results` is visible, and the flow stops after `b`.
  - **Recorded:** [`flows/je/sites`](../../conformance/flows/je/sites) run `je22-c`, oracle run 40687124625.
- **JE-22/d** · oracle-observed · input transforms have no `result`
  - **Given:** `x = result`
  - **Expect:** `b` fails with `result is not defined`.
  - **Recorded:** [`exprs/je-22.yaml`](../../conformance/exprs/je-22.yaml) case `JE-22/d`, oracle run 40687124625.

<a id="je-23"></a>
### JE-23 · Step input transforms

**Scope:** core · **Finding:** [JE-23](expressions.md#je-23)

Retry, missing-payload and dispatch cases are under [JE-V2](#je-v2), [JE-V4](#je-v4) and [JE-V5](#je-v5).

- **JE-23/a** · oracle-observed · `params` is always empty
  - **Given:** `x = params`
  - **Expect:** `{}`
  - **Recorded:** [`exprs/je-23.yaml`](../../conformance/exprs/je-23.yaml) case `JE-23/a`, oracle run 40687124625.
- **JE-23/b** · oracle-observed · static strings are not templates
  - **Given:** static `x = "${flow_input.n}"`
  - **Expect:** the literal string `"${flow_input.n}"`.
  - **Recorded:** [`exprs/je-23.yaml`](../../conformance/exprs/je-23.yaml) case `JE-23/b`, oracle run 40687124625.
- **JE-23/c** · oracle-observed · a skipped step does not evaluate its transforms
  - **Given:** `b` with `skip_if: {expr: "true"}` and `x = nope`
  - **Expect:** no error is raised; `b`'s result is the identity passthrough defined in [execution](execution.md).
  - **Recorded:** [`flows/je/sites`](../../conformance/flows/je/sites) run `je23-c`, oracle run 40687124625.
- **JE-23/d** · oracle-observed · a failing transform fails the step before it runs
  - **Given:** `x = nope` on a non-skipped `b`
  - **Expect:** `b`'s result is ``{"error":{"name":"ExecutionErr","message":"Error during isolated evaluation of expression `nope`:\nQuickJS evaluation error: Error: nope is not defined…"}}``.
  - **Recorded:** [`exprs/je-23.yaml`](../../conformance/exprs/je-23.yaml) case `JE-23/d`, oracle run 40687124625.

<a id="je-24"></a>
### JE-24 · Predicates are wrapped as `Boolean(<expr>)`

**Scope:** core · **Finding:** [JE-24](expressions.md#je-24)

The predicate shortcut cases are [JE-9/j and JE-9/k](#je-9).

- **JE-24/a** · oracle-observed · a predicate selects a branch
  - **Given:** flow input `{"n":5}`; a `branchone` with branches `[{expr: "flow_input.n > 3"}]`
  - **Expect:** branch 0 runs.
  - **Recorded:** [`flows/je/branches`](../../conformance/flows/je/branches) run `je24-a`, oracle run 40687124625.
- **JE-24/b** · oracle-observed · later · statement mode yields a non-boolean
  - **Given:** a step with `skip_if: {expr: "/;/.test(flow_input.s)"}`
  - **Expect:** the error `Expected a boolean value, found: "null"`. On the oracle the step is module 0, so the error takes route (b) of [JE-35](expressions.md#je-35): name `Anyhow`, message `Error: Expected a boolean value, found: "null"<loc>`.
  - **Recorded:** [`exprs/je-24.yaml`](../../conformance/exprs/je-24.yaml) case `JE-24/b`, oracle run 40687124625.
- **JE-24/c** · oracle-observed · a trailing newline rescues a line comment
  - **Given:** flow input `{"n":5}`; a step with `skip_if: {expr: "flow_input.n > 3 // big\n"}`
  - **Expect:** the step is skipped.
  - **Recorded:** [`exprs/je-24.yaml`](../../conformance/exprs/je-24.yaml) case `JE-24/c`, oracle run 40687124625.
- **JE-24/d** · oracle-observed · without it the predicate is a syntax error
  - **Given:** flow input `{"n":5}`; a step with `skip_if: {expr: "flow_input.n > 3 // big"}`
  - **Expect:** the flow fails per [JE-35](expressions.md#je-35); the message contains `QuickJS evaluation error:`. On the oracle (module 0, route (b)) it is `Error: Error: QuickJS evaluation error: Error: Unexpected end of input\n    at eval_script:1:7\n<loc><loc>`, named `Anyhow`.
  - **Recorded:** [`exprs/je-24.yaml`](../../conformance/exprs/je-24.yaml) case `JE-24/d`, oracle run 40687124625.

<a id="je-25"></a>
### JE-25 · `skip_if`

**Scope:** core · **Finding:** [JE-25](expressions.md#je-25)

- **JE-25/a** · oracle-observed · `skip_if` reads `results`; a skipped step leaves earlier results visible
  - **Given:** `a={"skip":true}`; `b` with `skip_if: {expr: "results.a.skip === true"}`; `c` with `x = results.a.skip`
  - **Expect:** `b` is skipped, and `c`'s `x` is `true`.
  - **Recorded:** [`flows/je/sites`](../../conformance/flows/je/sites) run `je25-a`, oracle run 40687124625.

<a id="je-26"></a>
### JE-26 · `stop_after_if`, and a throwing predicate fails the parent, not the child

**Scope:** core · **Finding:** [JE-26](expressions.md#je-26)

- **JE-26/a** · oracle-observed · a true predicate stops the flow
  - **Given:** `a` returns `{"done":true}` and has `stop_after_if: {expr: "result.done", skip_if_stopped: true}`; later steps follow
  - **Expect:** the later steps are skipped.
  - **Recorded:** [`flows/je/sites`](../../conformance/flows/je/sites) run `je26-a`, oracle run 40687124625.
- **JE-26/b** · oracle-observed · a throwing predicate fails the flow, not the child (O5)
  - **Given:** `b` returns `11` and has `stop_after_if: {expr: "nope"}`, `retry: {constant: {attempts: 2, seconds: 0}}` and `continue_on_error: true`; no failure module
  - **Expect:** `b`'s job is successful with result `11`, and `b` ran once. The flow fails with the final result `{"error":"Error: QuickJS evaluation error: Error: nope is not defined…"}`, where the error is a **string**; normalise the location suffix.
  - **Recorded:** [`flows/je/errors`](../../conformance/flows/je/errors) run `je26-b`, oracle run 40687124625.
- **JE-26/c** · oracle-observed · the failure module receives a string error
  - **Given:** as JE-26/b, plus a failure module with `x = typeof error`
  - **Expect:** `"string"`
  - **Recorded:** [`flows/je/errors`](../../conformance/flows/je/errors) run `je26-c`, oracle run 40687124625.
- **JE-26/d** · oracle-observed · `all_iters` includes the iteration just completed
  - **Given:** a sequential loop over `[10,20,30]` with a two-step body returning the iteration value, and loop `stop_after_if: {expr: "all_iters.length >= 2"}`
  - **Expect:** exactly 2 iterations run.
  - **Recorded:** [`flows/je/loops`](../../conformance/flows/je/loops) run `je26-d`, oracle run 40687124625.

<a id="je-27"></a>
### JE-27 · `stop_after_all_iters_if`

**Scope:** core · **Finding:** [JE-27](expressions.md#je-27)

- **JE-27/a** · oracle-observed · the predicate sees the array of child results
  - **Given:** a loop over `[1,2]` whose body returns the iteration value, with `stop_after_all_iters_if: {expr: "result.length === 2", skip_if_stopped: true}`
  - **Expect:** the stop triggers.
  - **Recorded:** [`flows/je/loops`](../../conformance/flows/je/loops) run `je27-a-loop`, and the same predicate on a `branchall` in [`flows/je/branches`](../../conformance/flows/je/branches) run `je27-a-branchall`, oracle run 40687124625.
- **JE-27/b** · oracle-observed · an error fails the module
  - **Given:** as JE-27/a, with `expr: "nope"`
  - **Expect:** the flow fails; the error message contains ``Error evaluating stop_after_all_iters_if expression `nope`:``.
  - **Recorded:** [`flows/je/loops`](../../conformance/flows/je/loops) run `je27-b-loop`, and on a `branchall` in [`flows/je/branches`](../../conformance/flows/je/branches) run `je27-b-branchall`, oracle run 40687124625.

<a id="je-28"></a>
### JE-28 · `sleep`

**Scope:** core · **Finding:** [JE-28](expressions.md#je-28)

- **JE-28/a** · oracle-observed · a fractional value delays the next job in milliseconds (O5)
  - **Given:** `a` returns `{"s":1.5}` and has `sleep: {type: javascript, expr: "result.s"}`; then `b`
  - **Expect:** `b` starts at least 1.5 s after `a` finishes.
  - **Recorded:** [`flows/je/waits`](../../conformance/flows/je/waits) run `je28-a`, oracle run 40687124625; recorded in part: the delay is checked against the recording's job timings, which are never compared between recordings ([OR-17](oracle.md#or-17)); windmill-lite's side is checked in milestone N2.
- **JE-28/b** · oracle-observed · a negative static value fails
  - **Given:** `a` with `sleep: {type: static, value: -1}`; then `b`
  - **Expect:** the flow fails with `Expected an integer, found: -1`.
  - **Recorded:** [`flows/je/waits`](../../conformance/flows/je/waits) run `je28-b`, oracle run 40687124625.
- **JE-28/c** · oracle-observed · the last module's `sleep` is evaluated, before the virtual step
  - **Given:** a flow `[a, b]` whose last module `b` has `sleep: {type: javascript, expr: "nope"}`
  - **Expect:** `b` runs and succeeds; then the flow fails with `{"error":{"name":"InternalError","message":"Expected a number value, had error instead: ExecutionErr(\"Error during evaluation of expression `nope`:\\nQuickJS evaluation error: Error: nope is not defined…\")"}}` (the inner newline is escaped, as in a debug form).
  - **Correction 2026-10-28:** the case expected no delay and no error. The deployed flow ends in the virtual `b-v` identity step ([OF-17](openflow.md#of-17)), and `b`'s `sleep` is evaluated when that step starts ([JE-28](expressions.md#je-28)).
  - **Recorded:** [`flows/je/waits`](../../conformance/flows/je/waits) run `je28-c`, oracle run 40687124625.

<a id="je-29"></a>
### JE-29 · `timeout`

**Scope:** core · **Finding:** [JE-29](expressions.md#je-29)

- **JE-29/a** · oracle-observed · a fractional result fails the i32 parse
  - **Given:** `b` with `timeout: {type: javascript, expr: "1.5"}`
  - **Expect:** the flow fails with ``Error parsing result as i32: invalid type: floating point `1.5`, expected i32…``.
  - **Recorded:** [`flows/je/waits`](../../conformance/flows/je/waits) run `je29-a`, oracle run 40687124625.

<a id="je-30"></a>
### JE-30 · The `forloopflow` iterator

**Scope:** core · **Finding:** [JE-30](expressions.md#je-30)

- **JE-30/a** · oracle-observed · the iterator can read `results`
  - **Given:** `a={"items":[1,2]}`; a loop with `iterator: {type: javascript, expr: "results.a.items"}`
  - **Expect:** 2 iterations.
  - **Recorded:** [`flows/je/loops`](../../conformance/flows/je/loops) run `je30-a`, oracle run 40687124625.
- **JE-30/b** · oracle-observed · a non-array fails
  - **Given:** a loop with `iterator: {type: javascript, expr: "({})"}`
  - **Expect:** the error message contains `Expected an array value in the iterator expression, found: invalid type: map, expected a sequence`.
  - **Recorded:** [`flows/je/loops`](../../conformance/flows/je/loops) run `je30-b`, oracle run 40687124625.

<a id="je-31"></a>
### JE-31 · `branchone` predicates

**Scope:** core · **Finding:** [JE-31](expressions.md#je-31)

- **JE-31/a** · oracle-observed · an error stops the scan
  - **Given:** a `branchone` with branches `[{expr: "nope"}, {expr: "true"}]`
  - **Expect:** the module fails with error name `Anyhow`; branch 1 is never considered.
  - **Recorded:** [`flows/je/branches`](../../conformance/flows/je/branches) run `je31-a`, oracle run 40687124625.
- **JE-31/b** · oracle-observed · later predicates are not evaluated
  - **Given:** branches `[{expr: "false"}, {expr: "true"}, {expr: "nope"}]`
  - **Expect:** branch 1 runs, and no error occurs.
  - **Recorded:** [`flows/je/branches`](../../conformance/flows/je/branches) run `je31-b`, oracle run 40687124625.

<a id="je-32"></a>
### JE-32 · `retry_if` on a flow step

**Scope:** core · **Finding:** [JE-32](expressions.md#je-32)

- **JE-32/a** · oracle-observed · a true predicate allows the retries
  - **Given:** a step that throws `Error("retryable")`, with `retry: {constant: {attempts: 2, seconds: 0}, retry_if: {expr: "error.message.includes('retryable')"}}`
  - **Expect:** 3 attempts.
  - **Recorded:** [`flows/je/waits`](../../conformance/flows/je/waits) run `je32-a`, oracle run 40687124625.
- **JE-32/b** · oracle-observed · a false predicate prevents them
  - **Given:** as JE-32/a, with `retry_if: {expr: "false"}`
  - **Expect:** 1 attempt.
  - **Recorded:** [`flows/je/waits`](../../conformance/flows/je/waits) run `je32-b`, oracle run 40687124625.

<a id="je-33"></a>
### JE-33 · The flow-level `skip_expr`

**Scope:** later · **Finding:** [JE-33](expressions.md#je-33)

- **JE-33/a** · oracle-observed · a true `skip_expr` stops the flow early
  - **Given:** a flow with `skip_expr: "flow_input.skip"`; flow input `{"skip":true}`
  - **Expect:** the flow ends with status `skipped` and result `"stopped early"`; module 0 does not run.
  - **Correction 2026-10-28:** the case asserted only the result, and the finding said the flow completes successfully. A `skip_expr` stop is an early stop that skips ([JE-33](expressions.md#je-33), [FX-22](execution.md#fx-22)).
  - **Recorded:** [`flows/je/sites`](../../conformance/flows/je/sites) run `je33-a`, oracle run 40687124625.
- **JE-33/b** · oracle-observed · `result` is the string `"{}"`
  - **Given:** a flow with `skip_expr: "typeof result"` (wrapped as `Boolean`, so true)
  - **Expect:** the flow ends with status `skipped` and result `"stopped early"`.
  - **Correction 2026-10-28:** as JE-33/a.
  - **Recorded:** [`flows/je/sites`](../../conformance/flows/je/sites) run `je33-b`, oracle run 40687124625.

<a id="je-35"></a>
### JE-35 · Failure-module transforms, and where expression errors land

**Scope:** core · **Finding:** [JE-35](expressions.md#je-35)

In each case, `F` is a failure module returning `"ran"`. Batch E3a recorded all four ([Q-JE-3](expressions.md#q-je-3)).

- **JE-35/a** · oracle-observed · route (a): chaining after a child completed (O5)
  - **Given:** `a` returns `1`; `b` with `skip_if: {expr: "nope"}`; failure module `F`
  - **Expect:** the final result is `{"error":{"name":"InternalError","message":"Error: QuickJS evaluation error: Error: nope is not defined…<loc>"}}` (one location suffix), and `F` does not run.
  - **Recorded:** [`flows/je/errors`](../../conformance/flows/je/errors) run `je35-a`, oracle run 40687124625.
- **JE-35/b** · oracle-observed · route (b): first pickup of a root flow, without a failure module (O5)
  - **Given:** a root flow whose module 0 has `skip_if: {expr: "nope"}`; no failure module
  - **Expect:** the final result is `{"error":{"name":"Anyhow","message":"Error: Error: QuickJS evaluation error: Error: nope is not defined…<loc><loc>"}}`: two `Error: ` prefixes and two location suffixes (on the oracle ` @worker_flow.rs:2451:11 @worker.rs:3822:13`).
  - **Correction 2026-10-28:** the case expected one `Error: ` prefix and one suffix. The worker's job dispatch wraps the flow code's error a second time ([JE-35](expressions.md#je-35)).
  - **Recorded:** [`flows/je/errors`](../../conformance/flows/je/errors) run `je35-b`, oracle run 40687124625.
- **JE-35/c** · oracle-observed · route (b), with a failure module (O5)
  - **Given:** as JE-35/b, plus failure module `F`
  - **Expect:** `F` runs.
  - **Recorded:** [`flows/je/errors`](../../conformance/flows/je/errors) run `je35-c`, oracle run 40687124625.
- **JE-35/d** · oracle-observed · route (c): first pickup of a nested flow (O5)
  - **Given:** a parent with a `continue_on_error: true` module `S` that calls a subflow by path. The subflow's module 0 has `skip_if: {expr: "nope"}`, and the subflow has its own failure module `FS`.
  - **Expect:** `FS` does not run. The parent continues, and `results.S` is the subflow's error object, named `Anyhow`, with the two prefixes and two suffixes of JE-35/b.
  - **Recorded:** [`flows/je/errors`](../../conformance/flows/je/errors) run `je35-d`, oracle run 40687124625.

<a id="je-36"></a>
### JE-36 · What `previous_result` is at each position

**Scope:** core · **Finding:** [JE-36](expressions.md#je-36)

In each case the body's transform is `p = previous_result`, compared as JSON. Batch E3a recorded JE-36/a–f on deployed flows; the preview half of JE-36/d is still open ([Q-JE-5](expressions.md#q-je-5)).

- **JE-36/a** · oracle-observed · a parallel one-step body is never collapsed
  - **Given:** parent input `{"seed":4}`; a parallel loop over `["x"]` with a one-step body
  - **Expect:** `p` = `{"seed":4,"iter":{"index":0,"value":"x"}}`
  - **Recorded:** [`flows/je/loops`](../../conformance/flows/je/loops) run `je36-a`, oracle run 40687124625.
- **JE-36/b** · oracle-observed · a collapsed sequential loop at module 0 sees the flow input every time
  - **Given:** deployed run; flow input `{"seed":4}`; a sequential loop at module 0 over `["x","y"]` with a one-step body
  - **Expect:** `p` = `{"seed":4}` on both iterations.
  - **Recorded:** [`flows/je/loops`](../../conformance/flows/je/loops) run `je36-b`, oracle run 40687124625.
- **JE-36/c** · oracle-observed · a collapsed sequential loop at module 1
  - **Given:** as JE-36/b, but the loop follows a step returning `9`
  - **Expect:** `p` = `9` on iteration 0, then iteration 0's result on iteration 1.
  - **Recorded:** [`flows/je/loops`](../../conformance/flows/je/loops) run `je36-c`, oracle run 40687124625.
- **JE-36/d** · oracle-observed · a simple while loop is collapsed in previews too
  - **Given:** a simple while loop after a step returning `9`, with loop `stop_after_if: {expr: "true"}`; run once deployed and once as a preview
  - **Expect:** `p` = `9` on its single iteration, in both runs.
  - **Recorded:** [`flows/je/branches`](../../conformance/flows/je/branches) run `je36-d`, oracle run 40687124625; recorded in part: the deployed run only; the preview run needs the flow-preview API in the harness ([Q-JE-5](expressions.md#q-je-5), recording batch E3e).
- **JE-36/e** · oracle-observed · a two-step sequential body is not collapsed
  - **Given:** a sequential loop with a two-step body, after a step returning `9`
  - **Expect:** the first body step's `p` is the parent's `flow_input` plus `iter`.
  - **Recorded:** [`flows/je/loops`](../../conformance/flows/je/loops) run `je36-e`, oracle run 40687124625.
- **JE-36/f** · oracle-observed · a branch body starts from the parent's input
  - **Given:** a branch body whose first step has `p = previous_result`
  - **Expect:** `p` equals the parent's `flow_input`.
  - **Recorded:** [`flows/je/branches`](../../conformance/flows/je/branches) run `je36-f`, oracle run 40687124625.

<a id="je-37"></a>
### JE-37 · What `flow_input` contains

**Scope:** core · **Finding:** [JE-37](expressions.md#je-37)

- **JE-37/a** · oracle-observed · nested loops displace `iter` to `iter_parent`
  - **Given:** an outer loop over `[1]` containing an inner loop over `["a"]`; the inner body has `x = [flow_input.iter.value, flow_input.iter_parent.value]`
  - **Expect:** `["a",1]`
  - **Recorded:** [`flows/je/loops`](../../conformance/flows/je/loops) run `je37-a`, oracle run 40687124625.

<a id="je-38"></a>
### JE-38 · What `flow_env` contains

**Scope:** later · **Finding:** [JE-38](expressions.md#je-38)

*Setup for JE-38/a–c:* a flow with `flow_env: {"K":"v","S":"$var:u/test/v"}`.

- **JE-38/a** · oracle-observed · a plain value
  - **Given:** `x = flow_env.K`
  - **Expect:** `"v"`
  - **Recorded:** [`flows/je/providers`](../../conformance/flows/je/providers) run `je38-ab`, oracle run 40687124625.
- **JE-38/b** · oracle-observed · env values are resolved
  - **Given:** `x = flow_env.S`
  - **Expect:** the value of variable `u/test/v`.
  - **Recorded:** [`flows/je/providers`](../../conformance/flows/je/providers) run `je38-ab`, oracle run 40687124625.
- **JE-38/c** · oracle-observed · loop bodies inherit the env
  - **Given:** inside a loop body, `x = flow_env.K`
  - **Expect:** `"v"`
  - **Recorded:** [`flows/je/loops`](../../conformance/flows/je/loops) run `je38-c`, oracle run 40687124625.
- **JE-38/d** · oracle-observed · a subflow called by path inherits its caller's env
  - **Given:** a subflow called by path, with no `flow_env`, called from a flow with `flow_env: {"K":"parent"}`; a transform in the subflow `x = flow_env.K`
  - **Expect:** `"parent"`
  - **Recorded:** [`flows/je/scopes`](../../conformance/flows/je/scopes) run `je38-d`, oracle run 40687124625.

<a id="je-39"></a>
### JE-39 · Names that do not exist

**Scope:** core · **Finding:** [JE-39](expressions.md#je-39)

- **JE-39/a** · oracle-observed · there is no bare `iter`
  - **Given:** inside a loop body, `x = iter.value`
  - **Expect:** `b` fails with `iter is not defined`.
  - **Recorded:** [`flows/je/loops`](../../conformance/flows/je/loops) run `je39-a`, oracle run 40687124625.

<a id="je-40"></a>
### JE-40 · Static values are copied verbatim, with no templating

**Scope:** core · **Finding:** [JE-40](expressions.md#je-40)

- **JE-40/a** · oracle-observed · a template literal is ordinary JavaScript
  - **Given:** inside a loop, `x` is `type: javascript` with the expression `` `Item ${flow_input.iter.index}` ``
  - **Expect:** `"Item 0"`, `"Item 1"`, … per iteration.
  - **Recorded:** [`flows/je/loops`](../../conformance/flows/je/loops) run `je40-ab`, oracle run 40687124625.
- **JE-40/b** · oracle-observed · the same text as a static value is literal
  - **Given:** the same text as `type: static`
  - **Expect:** the literal string, including the `${…}`.
  - **Recorded:** [`flows/je/loops`](../../conformance/flows/je/loops) run `je40-ab`, oracle run 40687124625.

<a id="je-41"></a>
### JE-41 · `$var:`, `$res:` and related strings are resolved per leaf step, at argument-build time

**Scope:** core · **Finding:** [JE-41](expressions.md#je-41)

*Setup for all cases:* a variable `u/test/v` = `"s3cr3t"`.

- **JE-41/a** · oracle-observed · a static marker is recorded as written and resolved for the script (O2, O3, O4)
  - **Given:** a Bun `b` with static `x = "$var:u/test/v"`, returning `x`
  - **Expect:** O2 `{"x":"$var:u/test/v"}`; O3 `{"x":"s3cr3t"}`; O4 `"s3cr3t"`.
  - **Recorded:** [`flows/je/providers`](../../conformance/flows/je/providers) run `je41-a`, oracle run 40687124625.
- **JE-41/b** · oracle-observed · an expression result is resolved too (O2, O3)
  - **Given:** `x = "$var:" + "u/test/v"`
  - **Expect:** O2 `x` = `"$var:u/test/v"`; O3 `x` = `"s3cr3t"`.
  - **Recorded:** [`flows/je/providers`](../../conformance/flows/je/providers) run `je41-b`, oracle run 40687124625.
- **JE-41/c** · oracle-observed · expressions see the unresolved string
  - **Given:** flow input `{"v":"$var:u/test/v"}`; `x = flow_input.v.length`
  - **Expect:** O2 `13`
  - **Recorded:** [`flows/je/providers`](../../conformance/flows/je/providers) run `je41-c`, oracle run 40687124625.

<a id="je-42"></a>
### JE-42 · The resolution rules

**Scope:** core · **Finding:** [JE-42](expressions.md#je-42)

*Setup for all cases:* a variable `u/test/v` = `"s"`; every expectation is at O3; all values are static.

- **JE-42/a** · oracle-observed · an array at depth 2 is walked
  - **Given:** `x = {"a":{"b":["$var:u/test/v"]}}`
  - **Expect:** `{"a":{"b":["s"]}}`
  - **Recorded:** [`flows/je/providers`](../../conformance/flows/je/providers) run `je42-a`, oracle run 40687124625.
- **JE-42/b** · oracle-observed · later · an array at depth 3 is not walked
  - **Given:** `x = {"a":{"b":{"c":["$var:u/test/v"]}}}`
  - **Expect:** unchanged.
  - **Recorded:** [`flows/je/providers`](../../conformance/flows/je/providers) run `je42-b`, oracle run 40687124625.
- **JE-42/c** · oracle-observed · later · no marker, no reserved-name lookup
  - **Given:** `x = ["$WM_WORKSPACE"]`
  - **Expect:** unchanged.
  - **Recorded:** [`flows/je/providers`](../../conformance/flows/je/providers) run `je42-c`, oracle run 40687124625.
- **JE-42/d** · oracle-observed · later · with a marker, reserved names are looked up
  - **Given:** `x = ["$WM_WORKSPACE","$var:u/test/v"]`
  - **Expect:** `["<workspace id>","s"]`
  - **Recorded:** [`flows/je/providers`](../../conformance/flows/je/providers) run `je42-d`, oracle run 40687124625 (the workspace id is masked).
- **JE-42/e** · oracle-observed · a missing variable fails the step
  - **Given:** `x = "$var:u/test/missing"`
  - **Expect:** `b` fails with ``Variable u/test/missing not found for `x`:``.
  - **Recorded:** [`flows/je/providers`](../../conformance/flows/je/providers) run `je42-e`, oracle run 40687124625.
- **JE-42/f** · oracle-observed · later · depth 49 is resolved
  - **Given:** an argument holding `"$var:u/test/v"` inside 49 nested objects
  - **Expect:** resolved.
  - **Recorded:** [`flows/je/providers`](../../conformance/flows/je/providers) run `je42-f`, oracle run 40687124625.
- **JE-42/g** · oracle-observed · later · depth 50 fails
  - **Given:** the same inside 50 nested objects
  - **Expect:** `b` fails with the message ``Maximum resource/variable interpolation depth (50) exceeded for `<name>`; this usually indicates a circular `$res:` or `$var:` reference``.
  - **Recorded:** [`flows/je/providers`](../../conformance/flows/je/providers) run `je42-g`, oracle run 40687124625.
- **JE-42/h** · oracle-observed · later · the depth guard is not limited to strings
  - **Given:** the argument `{"m":"$var:u/test/v","deep":<50 nested objects ending in the number 1>}`
  - **Expect:** `b` fails with the depth message.
  - **Recorded:** [`flows/je/providers`](../../conformance/flows/je/providers) run `je42-h`, oracle run 40687124625.

<a id="je-44"></a>
### JE-44 · Which reads go through `jsonb`

**Scope:** core · **Finding:** [JE-44](expressions.md#je-44)

*Setup for JE-44/a–b:* `a` returns `{"bbb":1,"a":2,"cc":3}`. The `jsonb` order is tracked by [Q-JE-9](expressions.md#q-je-9).

- **JE-44/a** · oracle-observed · the previous step is read from memory (producer order)
  - **Given:** `b` directly after `a`; `x = Object.keys(results.a)`
  - **Expect:** `["bbb","a","cc"]`
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je44-a`, oracle run 40687124625.
- **JE-44/b** · oracle-observed · an older step is read from the database (`jsonb` order)
  - **Given:** a step `m` between `a` and `b`; the same transform
  - **Expect:** `["a","cc","bbb"]`
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je44-b`, oracle run 40687124625.
- **JE-44/c** · oracle-observed · exponents are expanded
  - **Given:** flow input `{"n":1e3}`; `x = flow_input.n`
  - **Expect:** O2 `1000`
  - **Recorded:** [`exprs/je-44.yaml`](../../conformance/exprs/je-44.yaml) case `JE-44/c`, oracle run 40687124625.

<a id="je-48"></a>
### JE-48 · The stack limit is quickjs-ng's built-in 1 MiB, measured from where the embedding enters the engine

**Scope:** later · **Finding:** [JE-48](expressions.md#je-48)

The cases bracket the limit; none locates it. Batch E3a recorded them ([Q-JE-11](expressions.md#q-je-11)); frame sizes depend on the engine's build ([JE-48](expressions.md#je-48)).

- **JE-48/a** · oracle-observed · recursions of depth 600 and less fit; depth 1 000 overflows on Windmill
  - **Given:** (1) `x = [10, 100, 300, 600].map(k => ((f) => { try { return f(f, k) } catch (e) { return e instanceof RangeError ? "RangeError" : typeof e } })((g, n) => n === 0 ? 0 : 1 + g(g, n - 1)))`, which has no `;`, so it runs in expression mode ([JE-13](expressions.md#je-13)); (2) `x = (() => { const f = n => n === 0 ? 0 : 1 + f(n - 1); return f(1000); })()`.
  - **Expect:** (1) `[10,100,300,600]`: every depth fits. (2) `b` fails with a message containing `QuickJS evaluation error: Error: Maximum call stack size exceeded`. Both are Windmill's outcomes on the pinned oracle; whether another engine build must reproduce (2) is a compatibility decision for the equivalence contract, not settled here ([JE-48](expressions.md#je-48)'s design note).
  - **Correction 2026-10-28:** the case expected depth 1 000 to fit, with the value `1000`. On Windmill it overflows; and as written the expression holds `;`, so even a run that fits would give `null` in statement mode. Part (1) is the depth bracket batch E3a recorded instead.
  - **Revision 2026-10-28 (validation round 1):** the corrected case had called only (1) portable and said a conforming engine need not overflow at depth 1 000; that allowance is removed, and the case keeps both recorded outcomes.
  - **Recorded:** [`exprs/je-48.yaml`](../../conformance/exprs/je-48.yaml) case `JE-48/a` for (2), oracle run 40687124625, and its `depth-bracket` variant for (1), oracle run 40691336592.
- **JE-48/b** · oracle-observed · a recursion of depth 1 000 000 overflows
  - **Given:** the same expression with `f(1000000)`
  - **Expect:** `b` fails; its message contains `QuickJS evaluation error: Error: Maximum call stack size exceeded`.
  - **Recorded:** [`exprs/je-48.yaml`](../../conformance/exprs/je-48.yaml) case `JE-48/b`, oracle run 40687124625.
- **JE-48/c** · oracle-observed · the overflow is a catchable `RangeError`
  - **Given:** `x = (() => { const f = n => 1 + f(n + 1); try { return f(0); } catch (e) { return [e.name, e.message]; } })()`
  - **Expect:** `null`. The expression holds `;` outside quotes, so it runs in statement mode ([JE-13](expressions.md#je-13)) and the caught value is discarded. Written without `;`, as `x = ((f) => { try { return f(f, 0) } catch (e) { return [e.name, e.message] } })((g, n) => 1 + g(g, n + 1))`, the same overflow returns `["RangeError","Maximum call stack size exceeded"]`: it is a catchable `RangeError`.
  - **Correction 2026-10-28:** the case expected `["RangeError","Maximum call stack size exceeded"]` from the expression with `;`.
  - **Recorded:** [`exprs/je-48.yaml`](../../conformance/exprs/je-48.yaml) case `JE-48/c`, oracle run 40687124625, and its `catchable` variant for the semicolon-free form, oracle run 40691336592.
- **JE-48/d** · oracle-observed · stringifying deeply nested JSON overflows
  - **Given:** `x = (() => { let v = []; for (let i = 0; i < 100000; i++) v = [v]; return JSON.stringify(v).length; })()`
  - **Expect:** `b` fails; its message contains `Maximum call stack size exceeded`.
  - **Recorded:** [`exprs/je-48.yaml`](../../conformance/exprs/je-48.yaml) case `JE-48/d`, oracle run 40687124625.

<a id="je-49"></a>
### JE-49 · Context values nested deeper than 127 levels fail before evaluation

**Scope:** later · **Finding:** [JE-49](expressions.md#je-49)

*Notation:* `nest(1)` is `[]` and `nest(k)` is `[nest(k-1)]`, so `nest(k)` is `k` levels deep. Step `a` is a Bun script that builds and returns `nest(k)`; its result text is compact (`[[…]]`). Batch E3a recorded all three as stated ([Q-JE-11](expressions.md#q-je-11)).

- **JE-49/a** · oracle-observed · depth 127 binds
  - **Given:** `a` returns `nest(127)`; `x = previous_result.length`
  - **Expect:** `1`
  - **Recorded:** [`flows/je/sites`](../../conformance/flows/je/sites) run `je49-a`, oracle run 40687124625.
- **JE-49/b** · oracle-observed · depth 128 fails before any JavaScript runs
  - **Given:** `a` returns `nest(128)`; `x = previous_result.length`
  - **Expect:** `b` fails; its message contains `recursion limit exceeded at line 1 column 128` and does not contain `QuickJS evaluation error`.
  - **Recorded:** [`flows/je/sites`](../../conformance/flows/je/sites) run `je49-b`, oracle run 40687124625.
- **JE-49/c** · oracle-observed · a shortcut passes any depth through
  - **Given:** `a` returns `nest(128)`; `x = previous_result` (the [JE-6](expressions.md#je-6) shortcut)
  - **Expect:** `nest(128)`, unchanged.
  - **Recorded:** [`flows/je/sites`](../../conformance/flows/je/sites) run `je49-c`, oracle run 40687124625.

<a id="je-50"></a>
### JE-50 · Expressions run as strict-mode global scripts

**Scope:** core · **Finding:** [JE-50](expressions.md#je-50)

- **JE-50/a** · source-derived · `this` in a plain function call is `undefined`
  - **Given:** `x = typeof (function () { return this })()` (no `;`, so expression mode)
  - **Expect:** `"undefined"`; a sloppy-mode script would give `"object"`.

<a id="je-51"></a>
### JE-51 · The engine's job queue is drained before the result is read, and a throwing job is ignored

**Scope:** later · **Finding:** [JE-51](expressions.md#je-51)

- **JE-51/a** · source-derived · a queued job that throws does not fail the evaluation
  - **Given:** `x = ((q) => (q(() => { throw new Error("job") }), 1))(queueMicrotask)` (no `;`, so expression mode)
  - **Expect:** `1`; `b` succeeds, and no message mentions `job`.

<a id="je-52"></a>
### JE-52 · Unpaired surrogates: results carry them escaped; an error message holding one is dropped

**Scope:** later · **Finding:** [JE-52](expressions.md#je-52)

- **JE-52/a** · source-derived · an error message with an unpaired surrogate is dropped (O5)
  - **Given:** `x = (() => { throw new Error("bad \ud83d") })()`, with the backslash written in the expression text, so the engine decodes the escape (no `;`, so expression mode)
  - **Expect:** `b` fails with a message that starts ``Error during isolated evaluation of expression `(() => { throw new Error("bad \ud83d") })()`:\nQuickJS evaluation error: Error:\n    at `` (the stack follows at once); nothing after `QuickJS evaluation error: Error:` contains `bad`.

<a id="je-v1"></a>
### JE-V1 · Parallel-loop `parallelism` is an expression site

**Scope:** later · **Finding:** [JE-V1](expressions.md#je-v1)

- **JE-V1/a** · oracle-observed · the expression sets the limit (O5)
  - **Given:** `a` returns `{"cap":2}`; then a parallel loop over three items, each body sleeping briefly, with `parallelism: {type: javascript, expr: "result.cap"}`
  - **Expect:** at most 2 children run at once (from child start and end times).
  - **Recorded:** [`flows/je/loops`](../../conformance/flows/je/loops) run `je-v1-a`, oracle run 40687124625; recorded in part: the expression is accepted and every child runs; how many run at once needs three or more oracle workers (recording batch E3e).
- **JE-V1/b** · oracle-observed · a fractional value fails the u16 parse (O5)
  - **Given:** as JE-V1/a, with `parallelism: {type: javascript, expr: "1.5"}`
  - **Expect:** the flow fails with `{"error":{"name":"InternalError","message":"Error parsing result as u16: …"}}`.
  - **Recorded:** [`flows/je/loops`](../../conformance/flows/je/loops) run `je-v1-b`, oracle run 40687124625.
- **JE-V1/c** · oracle-observed · a sequential loop ignores the field
  - **Given:** a sequential loop with `parallelism: {type: javascript, expr: "nope"}`
  - **Expect:** the loop runs normally.
  - **Recorded:** [`flows/je/loops`](../../conformance/flows/je/loops) run `je-v1-c`, oracle run 40687124625.
- **JE-V1/d** · oracle-observed · an outer `iter` stays visible (O5)
  - **Given:** an outer sequential loop over `[2]` with a two-step body containing an inner parallel loop over `["a","b"]` with `parallelism: {type: javascript, expr: "flow_input.iter.value"}`
  - **Expect:** the expression evaluates to `2`, so both inner children may run at once.
  - **Recorded:** [`flows/je/loops`](../../conformance/flows/je/loops) run `je-v1-d`, oracle run 40687124625.
- **JE-V1/e** · oracle-observed · the loop adds no `iter` of its own
  - **Given:** a top-level parallel loop with `parallelism: {type: javascript, expr: "flow_input.iter.value"}`, where `flow_input.iter` is absent
  - **Expect:** the flow fails with a message containing `cannot read property 'value' of undefined`, routed per [JE-35](expressions.md#je-35).
  - **Recorded:** [`flows/je/loops`](../../conformance/flows/je/loops) run `je-v1-e`, oracle run 40687124625.

<a id="je-v2"></a>
### JE-V2 · Ordinary retries reuse the recorded transformed arguments

**Scope:** core · **Finding:** [JE-V2](expressions.md#je-v2)

*Setup for JE-V2/a–b:* a variable `u/test/retry_value` = `"first"`; `b` has `retry: {constant: {attempts: 1, seconds: 0}}`, and on its first attempt `b`'s script sets the variable to `"second"` and throws.

- **JE-V2/a** · oracle-observed · transforms are not re-evaluated on retry
  - **Given:** `x = variable("u/test/retry_value")`
  - **Expect:** the second attempt's O2 still has `x` = `"first"`.
  - **Recorded:** [`flows/je/waits`](../../conformance/flows/je/waits) run `je-v2-a`, oracle run 40688787344.
- **JE-V2/b** · oracle-observed · markers are resolved again on retry (O2, O3)
  - **Given:** static `x = "$var:u/test/retry_value"`
  - **Expect:** both attempts have O2 `x` = `"$var:u/test/retry_value"`; the second attempt has O3 `x` = `"second"`.
  - **Recorded:** [`flows/je/waits`](../../conformance/flows/je/waits) run `je-v2-b`, oracle run 40688787344.
- **JE-V2/c** · oracle-observed · a retry after a failed transform runs with an empty map (O2, O5; [Q-JE-4](expressions.md#q-je-4))
  - **Given:** `x = nope`, with one retry
  - **Expect:** the first attempt fails with the pre-run error; the second attempt runs with no `x`.
  - **Recorded:** [`flows/je/waits`](../../conformance/flows/je/waits) run `je-v2-c`, oracle run 40687124625.

<a id="je-v3"></a>
### JE-V3 · Recorded arguments and resolved arguments are distinct observation points

**Scope:** core · **Finding:** [JE-V3](expressions.md#je-v3)

- **JE-V3/a** · oracle-observed · three observation points of one argument (O2, O3, O4)
  - **Given:** a variable `u/test/v` = `"s"`; an ordinary Bun step `b` with static `x = "$var:u/test/v"`, returning `x`
  - **Expect:** job API args (O2) `{"x":"$var:u/test/v"}`; resolved argument JSON (O3) `{"x":"s"}`; script result (O4) `"s"`. None of the three is interchangeable with the others.
  - **Recorded:** [`flows/je/providers`](../../conformance/flows/je/providers) run `je-v3-a`, oracle run 40687124625.

<a id="je-v4"></a>
### JE-V4 · Missing transform payloads have defined defaults

**Scope:** later · **Finding:** [JE-V4](expressions.md#je-v4)

- **JE-V4/a** · oracle-observed · a JavaScript transform without `expr`
  - **Given:** input transform `x: {"type":"javascript"}`
  - **Expect:** O2 `{"x":null}`
  - **Recorded:** [`flows/je/sites`](../../conformance/flows/je/sites) run `je-v4`, oracle run 40687124625.
- **JE-V4/b** · oracle-observed · a static transform without `value`
  - **Given:** input transform `x: {"type":"static"}`
  - **Expect:** O2 `{"x":null}`
  - **Recorded:** [`flows/je/sites`](../../conformance/flows/je/sites) run `je-v4`, oracle run 40687124625.
- **JE-V4/c** · oracle-observed · no transform entry
  - **Given:** `input_transforms` has no `x` entry
  - **Expect:** `x` is absent from O2.
  - **Recorded:** [`flows/je/sites`](../../conformance/flows/je/sites) run `je-v4`, oracle run 40687124625.

<a id="je-v5"></a>
### JE-V5 · Transform dispatch is serial and stops at the first error

**Scope:** core · **Finding:** [JE-V5](expressions.md#je-v5)

- **JE-V5/a** · source-derived · only the first transform's read happens (host or evaluator level)
  - **Given:** `x = variable("u/test/a")` and `y = variable("u/test/b")` on one step; the variable service fails every request immediately with the error text `sentinel` (for example a non-200 response whose body is `sentinel`) and records the calls.
  - **Expect:** exactly **one** request is made, for whichever transform comes first. `b`'s error is ``{"error":{"name":"ExecutionErr","message":"Error during isolated evaluation of expression `<that expression>`:\nQuickJS evaluation error: Error: sentinel…"}}``. The fixture must not assume that `x` is evaluated before `y`.

<a id="je-v6"></a>
### JE-V6 · The exact spelling, not the module id, decides memory or database

**Scope:** core · **Finding:** [JE-V6](expressions.md#je-v6)

*Setup for both cases:* `a` is a producer that emits exact integers, for example a Python step returning `{"n": 9007199254740993}` (not a Bun step); `b` follows directly, with two transforms. Compare the recorded JSON numerically, without parsing it through JavaScript.

- **JE-V6/a** · oracle-observed · a property path of the previous step uses the database lookup
  - **Given:** `x = results.a.n`
  - **Expect:** O2 `x` = `9007199254740993`
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je-v6`, oracle run 40687124625.
- **JE-V6/b** · oracle-observed · the same path in parentheses uses JavaScript conversion of the in-memory value
  - **Given:** `y = (results.a.n)`
  - **Expect:** O2 `y` = `9007199254740992`
  - **Recorded:** [`flows/je/shortcuts`](../../conformance/flows/je/shortcuts) run `je-v6`, oracle run 40687124625.

<a id="languages"></a>
## Languages and script wrappers

Findings: [languages.md](languages.md).

<a id="languages-notation"></a>
### Notation

- Scripts are synthetic Bun (TypeScript) fixtures unless a case names another language. An inline script is the whole file.
- "Step `a`" means the script runs as flow step `a`, so error objects carry `"step_id":"a"`. Run outside a flow, the same script produces no `step_id` key.
- `<stack>` stands for a normalised `stack` value. Exact stack text is [Q-WR-10](languages.md#q-wr-10).
- `…` marks free text that a test must not match exactly. "Match on the prefix" means comparing only the leading text shown.
- Error expectations name fields of the inner error object. The stored step value is `{"error": <that object>}` ([WR-21](languages.md#wr-21)), unless stated otherwise.
- In messages, `\n` is the JSON escape for a newline.
- The oracle harness normalises job ids ([oracle](oracle.md)).
- No case below has been recorded on the v1.767.0 CE oracle yet ([Q-WR-20](languages.md#q-wr-20)).
- **Native cases** ([WR-40](#wr-40) to [WR-46](#wr-46)). A `nativets` step is an inline rawscript with `language: nativets`. A `bunnative` step is an inline rawscript with `language: bun` whose first line is `//native`. "Both kinds" means the case runs once as each, with the same expectation. Their recording is [Q-WR-21](languages.md#q-wr-21). In a native error, `<stack>` stands for the two-part `stack` value of [WR-43](languages.md#wr-43). Unlike the Bun errors above, native exception objects carry no `step_id`.
- **Codebase cases** ([WR-47](#wr-47) to [WR-50](#wr-50)). The sync root holds a `wmill.yaml` with `defaultTs: bun` and one `codebases` entry, `relative_path: f/cb` and `includes: ["f/cb/**"]`, plus the fields each case adds. Scripts live under `f/cb/`, with the `.script.yaml` the CLI generates. The fixture is pushed with the pinned `wmill` CLI from the sync root, to the CE oracle in standalone mode ([OR-4](oracle.md#or-4)), and the script is then run by path.

<a id="wr-1"></a>
### WR-1 · One fresh OS process per script job, in a fresh job directory

**Scope:** core · **Finding:** [WR-1](languages.md#wr-1)

- **WR-1/a** · source-derived · module-level state does not survive between jobs
  - **Given:** `let n = 0; export function main() { return ++n; }`, run twice in a row.
  - **Expect:** both runs return `1`.
- **WR-1/b** · source-derived · globals do not survive between jobs
  - **Given:** `export function main() { globalThis.x = (globalThis.x ?? 0) + 1; return globalThis.x; }`, run twice in a row.
  - **Expect:** both runs return `1`.

<a id="wr-2"></a>
### WR-2 · The module is evaluated before args are read, and the entrypoint runs exactly once

**Scope:** core · **Finding:** [WR-2](languages.md#wr-2)

- **WR-2/a** · source-derived · pending timers are cut off at exit
  - **Given:** `export async function main() { setTimeout(() => console.log("late"), 2000); return 1; }`. Precondition: the call and the result write finish within 2 seconds.
  - **Expect:** result `1`; `late` never appears in the logs.
- **WR-2/b** · source-derived · top-level code runs before the entrypoint
  - **Given:** `console.log("top"); export function main() { console.log("main"); return 0; }`
  - **Expect:** the logs show `top` before `main`.

<a id="wr-3"></a>
### WR-3 · Runtime binary and version pin

**Scope:** core · **Finding:** [WR-3](languages.md#wr-3)

- **WR-3/a** · source-derived · runtime version (taken from the image recipe; the running binary has not been recorded)
  - **Given:** `export function main() { return Bun.version; }`
  - **Expect:** `"1.3.10"`. The oracle harness records the actual `Bun.version` in its fixture metadata.

<a id="wr-5"></a>
### WR-5 · `args.json` is the job's args map after `$var:`/`$res:` resolution, with internal keys removed

**Scope:** core · **Finding:** [WR-5](languages.md#wr-5)

- **WR-5/a** · source-derived · a `$res:` reference resolves to the resource value
  - **Given:** resource `u/a/pg` = `{"host":"h"}`; args `{"db":"$res:u/a/pg","n":1}`.
  - **Expect:** the entrypoint receives `db = {"host":"h"}` and `n = 1`.
- **WR-5/b** · source-derived · a one-segment resource path is rejected
  - **Given:** args `{"x":"$res:pg"}`.
  - **Expect:** the job fails. `error.message` begins `` execution error:\nInternal: Argument `x` is an invalid resource path: pg ``, followed by an internal-location suffix.
- **WR-5/c** · source-derived · an arg with no trigger pattern is not resolved
  - **Given:** args `{"a":"$WM_WORKSPACE","b":"$var:u/a/v"}`.
  - **Expect:** `a` stays the literal `"$WM_WORKSPACE"`, because arg `a` itself contains no trigger pattern.
- **WR-5/d** · source-derived · later · reserved-variable substitution inside a qualifying arg
  - **Given:** args `{"a":["$WM_WORKSPACE","$var:u/a/v"]}`.
  - **Expect:** `a = ["<workspace id>", "<value of v>"]`.
- **WR-5/e** · source-derived · a missing resource
  - **Given:** args `{"a":"$res:u/a/missing"}`, no such resource.
  - **Expect:** ``{"error":{"message":"execution error:\nNot found: Resource u/a/missing not found for `a`: …","name":"ExecutionErr","step_id":…}}``. Match on the message prefix.

<a id="wr-6"></a>
### WR-6 · How args bind to the entrypoint

**Scope:** core · **Finding:** [WR-6](languages.md#wr-6)

Cases WR-6/a to WR-6/c use `export function main(a: number, b = "dflt", c?: string) { return [a, b, c ?? "none"]; }`.

- **WR-6/a** · source-derived · a missing key gives `undefined`, so the defaults apply
  - **Given:** args `{"a":1}`.
  - **Expect:** `[1,"dflt","none"]`.
- **WR-6/b** · source-derived · `null` is kept, and unknown keys are dropped
  - **Given:** args `{"a":1,"b":null,"zzz":9}`.
  - **Expect:** `[1,null,"none"]`.
- **WR-6/c** · source-derived · no coercion
  - **Given:** args `{"a":"5"}`.
  - **Expect:** `["5","dflt","none"]`.
- **WR-6/d** · source-derived · an arrow `main` without an override gets no arguments
  - **Given:** `export const main = (...xs) => xs.length;`, args `{"a":1}`.
  - **Expect:** `0`.
- **WR-6/e** · source-derived · override fallback with integer-like keys (deterministic order)
  - **Given:** `export function other() { return [...arguments]; }`, args `{"_ENTRYPOINT_OVERRIDE":"other","9":9,"2":2,"_hidden":7}`.
  - **Expect:** `[2,9]`.
- **WR-6/f** · source-derived · override fallback with non-integer keys (order unspecified)
  - **Given:** the same `other`, args `{"_ENTRYPOINT_OVERRIDE":"other","a":1,"bb":2,"ccc":3}`.
  - **Expect:** some permutation of `[1,2,3]`. The test must accept any of the six orders.

<a id="wr-7"></a>
### WR-7 · Which declarations count as the entrypoint, and the parser's edge cases

**Scope:** core · **Finding:** [WR-7](languages.md#wr-7)

- **WR-7/a** · source-derived · an arrow `main` is called with no arguments
  - **Given:** `export const main = (a: number) => a ?? "undef";`, args `{"a":3}`.
  - **Expect:** `"undef"`.
- **WR-7/b** · source-derived · a missing `main` export
  - **Given:** `export function notMain() {}`, run as step `a`.
  - **Expect:** `{"error":{"message":"main function is missing","name":"Error","stack":"…","step_id":…}}`.
- **WR-7/c** · source-derived · a destructured parameter reads key `anon1`
  - **Given:** `export function main({ a }: { a: number }) { return a; }`, args `{"a":1}`.
  - **Expect:** key `anon1` is absent, so destructuring `undefined` throws, and the job fails with `error.name` = `TypeError`.
- **WR-7/d** · source-derived · a destructured parameter bound through `anon1`
  - **Given:** the same script, args `{"anon1":{"a":1}}`.
  - **Expect:** `1`.

<a id="wr-8"></a>
### WR-8 · Which parameter annotations are hydrated as `Date`

**Scope:** core · **Finding:** [WR-8](languages.md#wr-8)

- **WR-8/a** · source-derived · a local alias to `Date` is hydrated
  - **Given:** `type Ts = Date; export function main(d: Ts) { return d instanceof Date; }`, args `{"d":"2024-01-02T03:04:05Z"}`.
  - **Expect:** `true`.
- **WR-8/b** · source-derived · a three-member union is not hydrated
  - **Given:** `export function main(d: Date | null | undefined) { return typeof d; }`, args `{"d":"2024-01-02T03:04:05Z"}`.
  - **Expect:** `"string"`.
- **WR-8/c** · source-derived · array elements are not hydrated
  - **Given:** `export function main(ds: Date[]) { return typeof ds[0]; }`, args `{"ds":["2024-01-02"]}`.
  - **Expect:** `"string"`.

Cases WR-8/d to WR-8/f use `export function main(d: Date, e: Date | undefined, s: Base64, u: Uint8Array) { return [d instanceof Date ? d.toISOString() : String(d), e === undefined, typeof s, Array.isArray(u)]; }`.

- **WR-8/d** · source-derived · the truthiness rule; `Base64` and `Uint8Array` are not hydrated
  - **Given:** args `{"d":"2024-01-02T03:04:05Z","e":"","s":"aGk=","u":[1,2]}`.
  - **Expect:** `["2024-01-02T03:04:05.000Z",true,"string",true]`.
- **WR-8/e** · source-derived · a number is read as epoch milliseconds
  - **Given:** args `{"d":1700000000000}`.
  - **Expect:** `["2023-11-14T22:13:20.000Z",true,"undefined",false]`.
- **WR-8/f** · source-derived · an unparsable string becomes an Invalid Date
  - **Given:** args `{"d":"nope"}`.
  - **Expect:** `d.toISOString()` throws, so the job fails with `error.name` = `RangeError`.

<a id="wr-9"></a>
### WR-9 · The JSON boundary into a step

**Scope:** core · **Finding:** [WR-9](languages.md#wr-9)

- **WR-9/a** · source-derived · a value that crosses a step boundary arrives as plain JSON
  - **Given:** a two-step flow. Step `a` returns `{ d: new Date(0), m: new Map([[1,2]]), u: undefined, big: 9007199254740993n }`. Step `b` takes an untyped `x` bound to `results.a` and returns `x`.
  - **Expect:** `{"d":"1970-01-01T00:00:00.000Z","m":{},"u":null,"big":"9007199254740993"}`.

<a id="wr-10"></a>
### WR-10 · Key order at the step-argument boundary, by layer

**Scope:** core · **Finding:** [WR-10](languages.md#wr-10)

Each step returns `Object.keys` of the named input.

- **WR-10/a** · source-derived · layer 2: an unresolved nested value arrives in `jsonb` order (oracle integration pending, [Q-WR-4](languages.md#q-wr-4))
  - **Given:** a static input with no `$` references, value `{"bb":1,"a":2,"ccc":3,"ab":4}`.
  - **Expect:** `["a","ab","bb","ccc"]`.
- **WR-10/b** · source-derived · layer 3: a `$jsonvar:` value keeps the variable's own order
  - **Given:** a variable whose string contents are `{"bb":2,"a":1}`, passed as `$jsonvar:`.
  - **Expect:** `["bb","a"]`.
- **WR-10/c** · source-derived · layer 4: the preprocessor's output is handed over in memory
  - **Given:** a script preprocessor returning `{ x: { bb: 1, a: 2 } }`; `main` returns `Object.keys(x)`.
  - **Expect:** `["bb","a"]`.
- **WR-10/d** · source-derived · layer 5, then layer 2: integer-like keys come first
  - **Given:** a value `{"b":1,"2":0,"a":3}` arriving through layer 2.
  - **Expect:** `["2","a","b"]`.
- **WR-10/e** · source-derived · layer 3: an unchanged stored resource keeps `jsonb` order
  - **Given:** a resource with value `{"bb":1,"a":2}`, passed as `$res:`.
  - **Expect:** `["a","bb"]`.

<a id="wr-11"></a>
### WR-11 · Script-level `preprocessor` (trigger preprocessing)

**Scope:** later · **Finding:** [WR-11](languages.md#wr-11)

- **WR-11/a** · source-derived · the preprocessor's output reaches `main` unhydrated, and the stored args are rewritten
  - **Given:** `export function preprocessor(event: any) { return { when: event.ts }; } export function main(when: Date) { return typeof when; }`, run with preprocessing and args `{"event":{"ts":"2024-01-01"}}`.
  - **Expect:** `"string"`. The job's stored args become `{"when":"2024-01-01"}`.

<a id="wr-12"></a>
### WR-12 · Entrypoint override, and the flow `preprocessor_module`

**Scope:** core · **Finding:** [WR-12](languages.md#wr-12)

- **WR-12/a** · source-derived · a declared preprocessor binds by name
  - **Given:** a flow whose `preprocessor_module` is `export function preprocessor(a: number) { return { a: a * 2 }; }`, flow input `{"a":2,"b":1}`.
  - **Expect:** preprocessor result `{"a":4}`; `WM_FLOW_STEP_ID` = `preprocessor`.
- **WR-12/b** · source-derived · an arrow preprocessor with integer-like keys (deterministic)
  - **Given:** `export const preprocessor = (x, y) => ({ x, y });`, flow input `{"2":"two","1":"one"}`.
  - **Expect:** `{"x":"one","y":"two"}`.
- **WR-12/c** · source-derived · an arrow preprocessor with non-integer keys (order unspecified)
  - **Given:** the same preprocessor, flow input `{"b":1,"a":2}`.
  - **Expect:** either `{"x":2,"y":1}` or `{"x":1,"y":2}`. The test must accept both.

<a id="wr-13"></a>
### WR-13 · The result is `JSON.stringify(value ?? null, replacer)`, written to `result.json`

**Scope:** core · **Finding:** [WR-13](languages.md#wr-13)

- **WR-13/a** · source-derived · standard serialisation plus the `undefined` replacer and the BigInt patch
  - **Given:** `export function main() { return { a: undefined, f() {}, n: 10n, d: new Date(Date.UTC(2024,0,2,3,4,5,6)), m: new Map([[1,2]]), u8: new Uint8Array([1,2]), x: [undefined, NaN, -0] }; }`
  - **Expect:** `{"a":null,"n":"10","d":"2024-01-02T03:04:05.006Z","m":{},"u8":{"0":1,"1":2},"x":[null,null,0]}`.
- **WR-13/b** · source-derived · a top-level `undefined` becomes `null`
  - **Given:** `export function main() { return undefined; }`
  - **Expect:** `null`.
- **WR-13/c** · source-derived · a cyclic result fails the job
  - **Given:** the entrypoint returns a cyclic object.
  - **Expect:** the job fails with `error.name` = `TypeError`. The message is engine-specific (on Bun, `JSON.stringify cannot serialize cyclic structures.`); match on `name` only.
- **WR-13/d** · source-derived · a top-level function result (runtime outcome pending, [Q-WR-12](languages.md#q-wr-12))
  - **Given:** `export function main() { return () => 1; }`
  - **Expect:** the job fails with `error.name` = `TypeError`. The message, and whether Bun `1.3.10` behaves this way at all, are to be recorded.

<a id="wr-14"></a>
### WR-14 · Object fields holding `undefined` become `null`, not omitted

**Scope:** core · **Finding:** [WR-14](languages.md#wr-14)

- **WR-14/a** · source-derived · the key survives with `null`
  - **Given:** step `a` is `export function main() { return { a: undefined, b: 1 }; }`. A next-step transform evaluates `results.a.hasOwnProperty("a")`.
  - **Expect:** step `a`'s result is `{"a":null,"b":1}`, and the transform gives `true`.

<a id="wr-15"></a>
### WR-15 · `result.json` is taken as raw text; size limits

**Scope:** core · **Finding:** [WR-15](languages.md#wr-15)

- **WR-15/a** · source-derived · later · below the warning threshold (self-hosted, default maximum)
  - **Given:** a 2.5 MiB string result.
  - **Expect:** success, with no large-result warning.
- **WR-15/b** · source-derived · later · at the warning threshold (self-hosted, default maximum)
  - **Given:** a 3 MiB result.
  - **Expect:** success, with the large-result warning in the logs.
- **WR-15/c** · source-derived · later · over a lowered limit
  - **Given:** a worker started with `MAX_RESULT_SIZE_MB=1`. Two scripts run as jobs of their own: `big` returns a string of 3 145 728 `x` characters (3 MiB of result text), `mid` one of 2 621 440 (2.5 MiB).
  - **Expect:** `big` fails with `{"message":"Result of job <job> is too large: 3MB > MAX_RESULT_SIZE_MB=1MB.\nUse external storages such as the Windmill Object Storage to store large results: https://www.windmill.dev/docs/core_concepts/object_storage_in_windmill","name":"ResultTooLarge"}` and no other key; `mid` succeeds with no large-result warning, since the limit is tested only above 2 MiB.

<a id="wr-16"></a>
### WR-16 · Result streaming: async-iterable results and `WM_STREAM: ` log lines

**Scope:** later · **Finding:** [WR-16](languages.md#wr-16)

The finding is DISPUTED in two producing-side clauses ([Q-WR-15](languages.md#q-wr-15)). Every case is tagged `disputed`; each one says whether its own expectation is contested.

- **WR-16/a** · disputed · string chunks are joined into the result
  - **Given:** `export async function* main() { yield "a\nb"; yield "c"; }`
  - **Expect:** result `"a\nbc"`, a JSON string containing a real newline.
  - **Contested:** no; both sides expect this result.
- **WR-16/b** · disputed · a numeric chunk fails
  - **Given:** `export async function* main() { yield 7; }`
  - **Expect:** the job fails with `error.name` = `TypeError`.
  - **Contested:** no.
- **WR-16/c** · disputed · an inherited custom `replace` returning a string
  - **Given:** `class C { replace() { return "x"; } }`, and the entrypoint is an async generator that yields `new C()`.
  - **Expect:** result `"x"`.
  - **Contested:** no.
- **WR-16/d** · disputed · a printed stream line is merged into an object result
  - **Given:** `export function main() { console.log("WM_STREAM: hi"); return { k: 1 }; }`
  - **Expect:** `{"k":1,"wm_stream":"hi"}`.
  - **Contested:** no.
- **WR-16/e** · disputed · a non-object, non-null result is left unchanged
  - **Given:** `export function main() { console.log("WM_STREAM: hi"); return 5; }`
  - **Expect:** `5`.
  - **Contested:** no.
- **WR-16/f** · disputed · how a custom `replace` return value is converted
  - **Given:** a chunk whose custom `replace` returns an object whose `valueOf` gives `7` and whose `toString` gives `x`.
  - **Expect (as drafted):** the object becomes its `toString()`, so the stream text is `x`.
  - **Expect (validation):** string `+` uses primitive conversion (`valueOf`), so the stream text is `7`.
- **WR-16/g** · disputed · a raw newline inside a custom `replace` return value
  - **Given:** a chunk whose custom `replace` returns `a`, a raw LF, then `WM_STREAM: b`. Normal log budget, no masking.
  - **Expect (as drafted):** only the first printed line carries the prefix, and the rest goes to the logs.
  - **Expect (validation):** every line is classified on its own, so the accumulated stream is `ab`.

<a id="wr-17"></a>
### WR-17 · Result markers `wm_failure` and `wm_labels`

**Scope:** later · **Finding:** [WR-17](languages.md#wr-17)

- **WR-17/a** · source-derived · a string `wm_failure` retags the run as failed
  - **Given:** `export function main() { return { wm_failure: "bad input", x: 1 }; }`
  - **Expect:** the step fails, and its stored value is `{"wm_failure":"bad input","x":1,"error":{"name":"ManualFailure","message":"bad input"}}`.
- **WR-17/b** · source-derived · a mistyped marker disables the markers
  - **Given:** `export function main() { return { wm_failure: true }; }`
  - **Expect:** the step succeeds with `{"wm_failure":true}`.

<a id="wr-18"></a>
### WR-18 · The error object the wrapper writes when the entrypoint, the preprocessor or serialisation throws

**Scope:** core · **Finding:** [WR-18](languages.md#wr-18)

Each case runs as step `a`. The expectation shows the inner error object; WR-21 then wraps it.

- **WR-18/a** · source-derived · a plain `Error`
  - **Given:** `throw new Error("boom")` inside `main`.
  - **Expect:** `{"message":"boom","name":"Error","stack":"<stack>","step_id":"a"}`.
- **WR-18/b** · source-derived · a subclass with a custom property
  - **Given:** `class E extends Error { constructor(){ super("x"); this.code = 5; } }`; `main` throws `new E()`.
  - **Expect:** `{"message":"x","name":"Error","stack":"<stack>","step_id":"a","extra":{"code":5}}`.
- **WR-18/c** · source-derived · a nested Error with no enumerable own properties
  - **Given:** `throw new Error("outer", { cause: new Error("inner") })`.
  - **Expect:** `{…,"extra":{"cause":{}}}`.
- **WR-18/d** · source-derived · a thrown string
  - **Given:** `throw "boom"`.
  - **Expect:** `{"step_id":"a","extra":{"0":"b","1":"o","2":"o","3":"m","length":4}}`.
- **WR-18/e** · source-derived · a thrown plain object
  - **Given:** `throw { code: 42, message: "m" }`.
  - **Expect:** `{"message":"m","step_id":"a","extra":{"code":42}}`.
- **WR-18/f** · source-derived · a thrown number
  - **Given:** `throw 7`.
  - **Expect:** `{"step_id":"a"}`.
- **WR-18/g** · source-derived · outside a flow
  - **Given:** WR-18/a to WR-18/f, each run as a plain script job.
  - **Expect:** the same objects, with no `step_id` key.
- **WR-18/h** · source-derived · a nested Error's enumerable own properties survive
  - **Given:** `const c = new Error("in"); c.code = 1; throw new Error("out", { cause: c })`.
  - **Expect:** `extra` = `{"cause":{"code":1}}`.

<a id="wr-19"></a>
### WR-19 · Failures that escape the wrapper's error shaping

**Scope:** core · **Finding:** [WR-19](languages.md#wr-19)

- **WR-19/a** · source-derived · a top-level module throw (exit code pending, [Q-WR-2](languages.md#q-wr-2))
  - **Given:** `throw new Error("top"); export function main() {}`, run as step `a`.
  - **Expect:** `error.name` = `ExecutionErr`; `error.message` starts with `exit code for "bun run": `; `exit_code` = the observed value. The predicted value `1` is unverified.

<a id="wr-20"></a>
### WR-20 · Log-tail error synthesised on a non-zero exit with an empty `result.json`

**Scope:** core · **Finding:** [WR-20](languages.md#wr-20)

- **WR-20/a** · source-derived · a syntax error fails at the build step
  - **Given:** `export async function main() { if (true) {` (an unclosed bracket).
  - **Expect:** `error.name` = `ExecutionErr`; `error.message` starts with `exit code for "bun build": 1` and contains `syntax error`. Match only `name`, the prefix up to the program name, and `exit_code`; the tail is free text.

<a id="wr-21"></a>
### WR-21 · How a failed script's error value is stored and passed on

**Scope:** core · **Finding:** [WR-21](languages.md#wr-21)

- **WR-21/a** · source-derived · the usual `{"error": …}` wrapping
  - **Given:** step `a` throws `new TypeError("bad")`.
  - **Expect:** the step's stored value is `{"error":{"message":"bad","name":"TypeError","stack":"<stack>","step_id":"a"}}`.
- **WR-21/b** · source-derived · later · a pre-shaped `ManualFailure` bypasses the wrapping
  - **Given:** `fs.writeFileSync("result.json", '{"error":{"name":"ManualFailure"},"k":1}'); process.exit(1)` inside `main`.
  - **Expect:** the stored failure is exactly `{"error":{"name":"ManualFailure"},"k":1}`.

<a id="wr-22"></a>
### WR-22 · Environment variables a Bun job sees

**Scope:** core · **Finding:** [WR-22](languages.md#wr-22)

- **WR-22/a** · source-derived · step and path variables; `BASE_INTERNAL_URL` is not injected
  - **Given:** flow `f/x/fl`, run manually. Step `s` returns `[process.env.WM_FLOW_STEP_ID, process.env.WM_JOB_PATH, process.env.WM_STATE_PATH, process.env.BASE_INTERNAL_URL ?? "unset"]`. No script, workspace or worker-group env configuration supplies `BASE_INTERNAL_URL`.
  - **Expect:** `["s","f/x/fl/s","f/x/fl/s_user","unset"]`.

<a id="wr-23"></a>
### WR-23 · Filesystem view

**Scope:** later · **Finding:** [WR-23](languages.md#wr-23)

- **WR-23/a** · source-derived · the working directory is private and writable
  - **Given:** `export function main() { require("node:fs").writeFileSync("t.txt", "x"); return require("node:fs").readFileSync("t.txt", "utf8"); }`
  - **Expect:** `"x"`.

<a id="wr-24"></a>
### WR-24 · `same_worker` flows share `./shared`

**Scope:** later · **Finding:** [WR-24](languages.md#wr-24)

- **WR-24/a** · source-derived · steps share files only under `same_worker`
  - **Given:** step `a` writes `./shared/x.txt` = `"hi"`; step `b` reads it.
  - **Expect:** `"hi"` with `same_worker: true`. Without it, the file is not shared.

<a id="wr-25"></a>
### WR-25 · The lockfile format stored with a deployed Bun script

**Scope:** core · **Finding:** [WR-25](languages.md#wr-25)

- **WR-25/a** · source-derived · an `<empty>` lock skips installation
  - **Given:** the lock is `{"dependencies":{}}` + `\n//bun.lock\n` + `<empty>`.
  - **Expect:** no install step runs, and the script runs.

<a id="wr-26"></a>
### WR-26 · Dependency inference and pinned-import removal

**Scope:** core · **Finding:** [WR-26](languages.md#wr-26)

- **WR-26/a** · source-derived · pin removal is a whole-text replace
  - **Given:** a script that imports a used binding from `fixture-pkg@1.2.3` and returns the separate literal `"fixture-pkg@1.2.3"`. `fixture-pkg` is a synthetic local package.
  - **Expect:** `"fixture-pkg"`.
- **WR-26/b** · source-derived · later · a version-pinned import is inferred into `package.json`
  - **Given:** `import _ from "lodash@4.17.21"`, with no lock.
  - **Expect:** the generated `package.json` dependencies are `{"lodash":"4.17.21"}`, and at run time the import resolves to `lodash`.

<a id="wr-27"></a>
### WR-27 · Bundled and unbundled modes share the wrapper rules, not every observable

**Scope:** core · **Finding:** [WR-27](languages.md#wr-27)

- **WR-27/a** · source-derived · a path-independent script gives the same result in both modes
  - **Given:** `export function main(a: number, b: string[]) { return { s: a * 2, j: b.join("-") }; }`, args `{"a":2,"b":["x","y"]}`, run bundled and with `//nobundling`.
  - **Expect:** `{"s":4,"j":"x-y"}` in both modes. Scripts that return paths or stacks are excluded from cross-mode comparison.

<a id="wr-28"></a>
### WR-28 · Which imports are rewritten and fetched from the workspace

**Scope:** core · **Finding:** [WR-28](languages.md#wr-28)

The finding is DISPUTED only in its later dynamic-import consequence ([Q-WR-16](languages.md#q-wr-16)). Every case is tagged `disputed`; each one says whether its own expectation is contested.

- **WR-28/a** · disputed · relative and absolute static imports, plus a transitive import
  - **Given:** script `f/a/main` with `import { h } from "./lib"` and `import { g } from "/f/b/util"`; `f/a/lib` imports `../c/z`.
  - **Expect:** loads `f/a/lib` and `f/b/util`, and the nested `../c/z` resolves to `f/c/z`.
  - **Contested:** no; static-import core.
- **WR-28/b** · disputed · an explicit `.ts` absolute import
  - **Given:** `import { k } from "/f/b/util.ts"`.
  - **Expect:** loads `f/b/util`.
  - **Contested:** no; static-import core.
- **WR-28/c** · disputed · later · a dynamic import with the identical literal of a static import
  - **Given:** a static `import { h } from "./helper"` and a dynamic `await import("./helper")` in the same file.
  - **Expect:** both load workspace script `<dir>/helper`.
  - **Contested:** no.
- **WR-28/d** · disputed · later · a dynamic import with no static rewrite trigger
  - **Given:** only a dynamic `await import("./helper")`.
  - **Expect:** not fetched from the workspace; resolution fails unless such a file exists locally.
  - **Contested:** no.
- **WR-28/e** · disputed · later · a static specifier that matches a substring of a different dynamic specifier
  - **Given:** a static import of `./helper` and a dynamic `import("./nested/./helper")` in the same file. Workspace helpers exist for both targets, and no local-file shortcut is available.
  - **Expect (as drafted):** the dynamic literal has no identical static import, so it is not rewritten or intercepted.
  - **Expect (validation):** the static rewrite turns the dynamic literal into `./nested/./helper.ts`, which then becomes eligible for interception. To be observed under the pinned Bun image.

<a id="wr-29"></a>
### WR-29 · The import base for an inline flow script is the flow's path

**Scope:** core · **Finding:** [WR-29](languages.md#wr-29)

- **WR-29/a** · source-derived · the default import base, at top level and inside a loop
  - **Given:** flow `f/a/fl` with an inline step `s` at top level, and the same step inside a for-loop. The step imports `../shared_lib` and `./x`.
  - **Expect:** `../shared_lib` loads `f/a/shared_lib`; `./x` loads `f/a/fl/x`.
- **WR-29/b** · source-derived · an explicit `path` sets the import base only where raw values run
  - **Given:** flow `f/a/fl` with the top-level inline step `s` of WR-29/a, now with `path: f/elsewhere/entry`, whose source is `import { b } from "./x"; export function main() { return b; }`. Workspace scripts `f/a/fl/x`, `f/elsewhere/entry/x` and `f/elsewhere/x` each export `b` as `"flow"`, `"entry"` and `"elsewhere"`. The flow is deployed, its dependency job succeeds, and it is run by path.
  - **Expect:** `"flow"`: the deployed flow keeps no explicit path, so the step imports `f/a/fl/x`. The dependency job resolved `./x` to `f/elsewhere/entry/x`. A preview of the same flow value returns `"elsewhere"`.
  - **Correction 2026-10-28:** the case expected `f/elsewhere/x` for the deployed flow. That is the preview's outcome only ([WR-29](languages.md#wr-29)).
- **WR-29/c** · source-derived · a deployed failure module keeps its explicit `path`
  - **Given:** flow `f/a/fl` whose only step `boom` throws, and whose inline Bun `failure_module` has `path: f/elsewhere/entry` and the source `import { b } from "./x"; export function main() { return b; }`. Workspace scripts `f/a/fl/x`, `f/elsewhere/entry/x` and `f/elsewhere/x` export `b` as `"flow"`, `"entry"` and `"elsewhere"`. The flow is deployed, its dependency job succeeds, and it is run by path.
  - **Expect:** the failure module returns `"elsewhere"`: the slot is not reduced, so its explicit path is its run-time base. The dependency job resolved its `./x` to `f/elsewhere/entry/x`, as for WR-29/b. The same holds for a `preprocessor_module` with that `path`.

<a id="wr-31"></a>
### WR-31 · Leading-comment annotations for TypeScript scripts

**Scope:** core · **Finding:** [WR-31](languages.md#wr-31)

- **WR-31/a** · source-derived · an annotation after a blank first line
  - **Given:**
    ```ts

    // nobundling
    export function main(){return 1}
    ```
  - **Expect:** unbundled mode; result `1`.
- **WR-31/b** · source-derived · a comment after code is not an annotation
  - **Given:**
    ```ts
    const x = 1;
    //nobundling
    export function main(){return 1}
    ```
  - **Expect:** not annotated: the `//nobundling` line is not part of the leading comment block.

<a id="wr-32"></a>
### WR-32 · How the workflow-as-code wrapper is selected

**Scope:** core · **Finding:** [WR-32](languages.md#wr-32)

- **WR-32/a** · source-derived · misselection by a bare `workflow` import
  - **Given:** `import { workflow } from "windmill-client"; export async function main() { return 1; }`
  - **Expect:** the job fails with `error.name` = `ExecutionErr` and an `error.message` containing `No workflow() entrypoint found`.

<a id="wr-33"></a>
### WR-33 · Deno

**Scope:** later · **Finding:** [WR-33](languages.md#wr-33)

- **WR-33/a** · source-derived · the Deno error object has no `extra`
  - **Given:** a Deno script whose `main` runs `class E extends Error { code = 5 }; throw new E("x")`, as step `a`.
  - **Expect:** `{"error":{"message":"x","name":"Error","stack":"<stack>","step_id":"a"}}`.
- See also [WR-V9/a](#wr-v9).

<a id="wr-34"></a>
### WR-34 · Python

**Scope:** later · **Finding:** [WR-34](languages.md#wr-34)

- **WR-34/a** · source-derived · a `null` for a defaulted parameter applies the default; extra keys are dropped
  - **Given:** `def main(a: int, b: str = "d"): return [a, b]`, args `{"a":1,"b":null,"x":2}`.
  - **Expect:** `[1,"d"]`.
- **WR-34/b** · source-derived · `datetime` is serialised with a space
  - **Given:** `def main(t: datetime): return t`, args `{"t":"2024-01-02T03:04:05"}`.
  - **Expect:** `"2024-01-02 03:04:05"`.
- **WR-34/c** · source-derived · a hydration failure escapes the handler
  - **Given:** `def main(t: datetime): return 1`, args `{"t":"nope"}`.
  - **Expect:** `error.name` = `ExecutionErr` and `exit_code` = `1`.
- **WR-34/d** · source-derived · the preprocessor's output is not re-hydrated
  - **Given:** a preprocessor returning `{"when":"2024-01-02"}`; `main(when: datetime)` returns `type(when).__name__`; run with preprocessing.
  - **Expect:** `"str"`.

<a id="wr-35"></a>
### WR-35 · Go and Bash

**Scope:** later · **Finding:** [WR-35](languages.md#wr-35)

- **WR-35/a** · source-derived · a silent Bash success gives `""`
  - **Given:** Bash `echo -n ""`.
  - **Expect:** `""`.
- **WR-35/b** · source-derived · a failure inside a pipeline does not fail the job
  - **Given:** Bash `false | true; echo ok`.
  - **Expect:** `"ok"`.
- **WR-35/c** · source-derived · a Go concrete return type is widened
  - **Given:** Go `func main() (string, error) { return "x", nil }`.
  - **Expect:** `"x"`.
- **WR-35/d** · source-derived · a Bash positional parameter (string)
  - **Given:**
    ```bash
    x="$1"
    echo "hello $x"
    ```
    with args `{"x":"w"}`.
  - **Expect:** `"hello w"`.
- **WR-35/e** · source-derived · a Bash positional parameter (object, passed as compact JSON)
  - **Given:** the same script, with args `{"x":{"k":1}}`.
  - **Expect:** `"hello {\"k\":1}"`.

<a id="wr-37"></a>
### WR-37 · Execution-handler failures (`execution error:`) and timeouts

**Scope:** core · **Finding:** [WR-37](languages.md#wr-37)

- **WR-37/a** · source-derived · a per-step timeout below the instance cap
  - **Given:** flow step `a` with `timeout: {"type":"static","value":1}`, running `await new Promise(r => setTimeout(r, 5000))`.
  - **Expect:** `{"error":{"message":"execution error:\njob process terminated due to timeout after exceeding job-specific duration limit","name":"ExecutionErr","step_id":"a"}}`.

<a id="wr-38"></a>
### WR-38 · Possible `.ts`-rewrite collisions, depending on the scanner's output

**Scope:** later · **Finding:** [WR-38](languages.md#wr-38)

Both cases are pending [Q-WR-13](languages.md#q-wr-13). Setup: workspace scripts `f/t/util` and `f/t/util_db` exist. No script exists at `f/t/util.ts_db`, at any `f/t/util_db.ts`-suffixed variant, or at `f/t/x.ts.ts`.

- **WR-38/a** · source-derived · overlapping extensionless specifiers (expected failure)
  - **Given:** `f/t/main` imports `{ a }` from `"./util"` and `{ b }` from `"./util_db"`.
  - **Expect:** the job fails with an `error.message` starting `exit code for "bun build"`.
- **WR-38/b** · source-derived · explicit `.ts` avoids the rewrite
  - **Given:** the same imports, written as `"./util.ts"` and `"./util_db.ts"`.
  - **Expect:** success, with the combined value.

<a id="wr-39"></a>
### WR-39 · Annotation, WAC-detection and import-rewrite scans are textual; signature parsing uses an AST

**Scope:** core · **Finding:** [WR-39](languages.md#wr-39)

- **WR-39/a** · source-derived · a three-line block comment triggers WAC detection
  - **Given:**
    ```ts
    /*
    import { workflow } from "windmill-client"
    */
    export function main() { return 1; }
    ```
  - **Expect:** the middle line starts with `import`, so the WAC scan fires. The job fails with a message containing `No workflow() entrypoint found`.
- **WR-39/b** · source-derived · a single-line block comment does not
  - **Given:** the same text as one line, `/* import { workflow } from "windmill-client" */`, followed by `export function main() { return 1; }`.
  - **Expect:** that line starts with `/*`, so the scan does not fire. Result `1`.
- **WR-39/c** · source-derived · a `//` comment line does not
  - **Given:** the same import text as a `//` comment line, followed by `export function main() { return 1; }`.
  - **Expect:** `//` lines are skipped. Result `1`.
- **WR-39/d** · source-derived · comments do not affect the parsed signature
  - **Given:** `/* export function main(a: Date) {} */ export function main(b: number) { return b; }`, args `{"b":2,"a":"x"}`.
  - **Expect:** `2`.

<a id="wr-40"></a>
### WR-40 · Which steps run in the native runtime, and how each kind is prepared

**Scope:** core · **Finding:** [WR-40](languages.md#wr-40)

- **WR-40/a** · source-derived · the header, not the stored language, selects the runtime
  - **Given:** two inline steps with the same body, `export function main() { return typeof (globalThis as any).Bun; }`: step `a` with `language: bun` and a first line `//native`; step `b` with `language: bunnative` and no header.
  - **Expect:** `a` returns `"undefined"` (native runtime); `b` returns `"object"` (Bun).
- **WR-40/b** · source-derived · `nativets` imports the embedded client as `./windmill.ts`
  - **Given:** a `nativets` step:
    ```ts
    import * as wmill from "./windmill.ts";
    export function main() { return typeof wmill.getVariable; }
    ```
  - **Expect:** `"function"`.
- **WR-40/c** · source-derived · a static import of a missing host file fails the load (`nativets`)
  - **Given:** a `nativets` step `a`: `import { x } from "./helper.ts"; export function main() { return x; }`. No file `/helper.ts` exists on the worker host. Whether a workspace script exists at a matching path makes no difference.
  - **Expect:** `{"error":{"message":"execution error:\nError: failed to load module…","name":"ExecutionErr","step_id":"a"}}`; match on the prefix.
- **WR-40/d** · source-derived · every spelling that resolves to the client's URL reaches it (`nativets`)
  - **Given:** the WR-40/b script, with the import specifier spelled `/windmill.ts` in one variant and `file:///windmill.ts` in another.
  - **Expect:** `"function"` both times.
- **WR-40/e** · source-derived · a failed dynamic import is an ordinary rejection that `main` can catch (`nativets`; medium)
  - **Given:** no file `/missing.ts` on the worker host; step `a`:
    ```ts
    export async function main() {
      try { await import("./missing.ts"); return "loaded"; }
      catch (e) { return [e instanceof Error, String((e as Error).message).includes("file:///missing.ts")]; }
    }
    ```
  - **Expect:** `[true,true]`.
- **WR-40/f** · source-derived · an uncaught failed dynamic import gives the exception shape, not a load failure (`nativets`; medium)
  - **Given:** no file `/missing.ts` on the worker host; step `a`: `export async function main() { await import("./missing.ts"); return 1; }`
  - **Expect:** `{"error":{"message":"Failed to load file:///missing.ts…","stack":<stack>,"name":…}}`: exactly the three keys, `message` matched on the prefix, and `name` not `ExecutionErr` (its value is [Q-WR-21](languages.md#q-wr-21)). No `failed to load module` text.

<a id="wr-41"></a>
### WR-41 · A fresh isolate per job; how arguments bind

**Scope:** core · **Finding:** [WR-41](languages.md#wr-41)

- **WR-41/a** · source-derived · a `Date` parameter is not hydrated (both kinds)
  - **Given:** `export function main(d: Date) { return typeof d; }`, args `{"d":"2024-01-02T03:04:05.000Z"}`.
  - **Expect:** `"string"`. The same script on Bun returns `"object"` ([WR-8](#wr-8)).
- **WR-41/b** · source-derived · a missing argument takes its default, `null` stays `null`, extra keys are ignored (both kinds)
  - **Given:** `export function main(a = 3, b: number | null = 5) { return [a, b]; }`, args `{"b":null,"z":1}`.
  - **Expect:** `[3,null]`.
- **WR-41/c** · source-derived · every job gets a fresh isolate (both kinds)
  - **Given:** `let n = 0; export function main() { n += 1; return n; }`, run as two separate jobs.
  - **Expect:** `1`, then `1`.
- **WR-41/d** · source-derived · later · `nativets` ignores the preprocessor entrypoint (medium)
  - **Given:** a flow whose `preprocessor_module` is a `nativets` rawscript:
    ```ts
    export function preprocessor() { return { v: "pre" }; }
    export function main() { return { v: "main" }; }
    ```
    then one step `a` with the transform `x = flow_input.v` that returns `x`.
  - **Expect:** `a` returns `"main"`.

<a id="wr-42"></a>
### WR-42 · The result is `JSON.stringify(value ?? null)` with no replacer

**Scope:** core · **Finding:** [WR-42](languages.md#wr-42)

All cases run on both kinds.

- **WR-42/a** · source-derived · an `undefined` field is omitted
  - **Given:** step `a` is `export function main() { return { a: undefined, b: 1, c: [undefined] }; }`. A next-step transform evaluates `results.a.hasOwnProperty("a")`.
  - **Expect:** `a`'s result is `{"b":1,"c":[null]}`, and the transform gives `false`. Compare [WR-14/a](#wr-14).
- **WR-42/b** · source-derived · a top-level function becomes `null`
  - **Given:** `export function main() { return () => 1; }`
  - **Expect:** `null`. Compare [WR-13/d](#wr-13).
- **WR-42/c** · source-derived · a `BigInt` fails the job
  - **Given:** step `a` is `export function main() { return { n: 10n }; }`
  - **Expect:** `{"error":{"message":"Do not know how to serialize a BigInt","stack":<stack>,"name":"TypeError"}}`. Match `name` exactly; the message is V8's ([Q-WR-21](languages.md#q-wr-21)).
- **WR-42/d** · source-derived · `toJSON` and non-finite numbers
  - **Given:** `export function main() { return { d: new Date(Date.UTC(2024, 0, 2)), x: NaN, m: new Map([[1, 2]]) }; }`
  - **Expect:** `{"d":"2024-01-02T00:00:00.000Z","x":null,"m":{}}`.

<a id="wr-43"></a>
### WR-43 · The native error shapes

**Scope:** core · **Finding:** [WR-43](languages.md#wr-43)

- **WR-43/a** · source-derived · an `Error` gives exactly three fields (both kinds)
  - **Given:** step `a`: `export function main() { const e = new TypeError("boom"); (e as any).code = 7; throw e; }`
  - **Expect:** `{"error":{"message":"boom","stack":<stack>,"name":"TypeError"}}`: exactly these keys, so no `step_id` and no `extra`.
- **WR-43/b** · source-derived · any other thrown value gives three `null`s (both kinds)
  - **Given:** step `a` throwing, in three variants, `"s"`, `{ message: "m", name: "N" }` and `null`.
  - **Expect:** each time `{"error":{"message":null,"stack":null,"name":null}}`.
- **WR-43/c** · source-derived · the stack starts with the failing source line (both kinds)
  - **Given:**
    ```ts
    export function main() {
      throw new Error("x");
    }
    ```
  - **Expect:** `error.stack` has a first line containing `throw new Error("x")` (the line as loaded, so its indentation may differ from the source), then a line `Error: x`.
- **WR-43/d** · source-derived · an unhandled rejection fails the job (medium; both kinds)
  - **Given:** step `a`: `export async function main() { Promise.reject(new Error("late")); await new Promise(r => setTimeout(r, 100)); return 1; }`
  - **Expect:** `{"error":{"message":"late","stack":<stack>,"name":"Error"}}`, not `1`.
- **WR-43/e** · source-derived · later · a promise that never settles, with nothing pending (both kinds)
  - **Given:** step `a`: `export function main() { return new Promise(() => {}); }`
  - **Expect:** `{"error":{"message":"execution error:\nPromise resolution is still pending but the event loop has already resolved","name":"ExecutionErr","step_id":"a"}}`, at once, not after a timeout.
- **WR-43/f** · source-derived · a syntax error fails at the transpile step (`nativets`)
  - **Given:** a `nativets` step `a` with the text `export function main( {`.
  - **Expect:** `{"error":{"message":"execution error:\nError: …","name":"ExecutionErr","step_id":"a"}}`; match on the prefix. The message does not contain `failed to load module`.
- **WR-43/g** · source-derived · a non-string `message` nulls both `message` and `name` (both kinds)
  - **Given:** step `a`: `export function main() { const e = new Error("x"); e.name = "Tagged"; (e as any).message = 7; throw e; }`
  - **Expect:** `{"error":{"message":null,"stack":<stack>,"name":null}}`: `name` is `null` even though it was a valid string, and `stack` is a non-null string.
- **WR-43/h** · source-derived · a non-string `name` nulls both as well (both kinds)
  - **Given:** step `a`: `export function main() { const e = new Error("m"); (e as any).name = 7; throw e; }`
  - **Expect:** `{"error":{"message":null,"stack":<stack>,"name":null}}`, with `stack` a non-null string.
- **WR-43/i** · source-derived · later · a non-string `stack` is dropped (both kinds; medium)
  - **Given:** step `a`: `export function main() { const e = new Error("m"); (e as any).stack = 5; throw e; }`
  - **Expect:** `message` is `"m"` and `name` is `"Error"`. `stack` is not `5` and contains no V8 stack text (no `Error: m`, no `at` frame lines). Source predicts the text of one source line of `main` followed by a newline.

<a id="wr-44"></a>
### WR-44 · Timeout, heap and stack limits

**Scope:** core · **Finding:** [WR-44](languages.md#wr-44)

All cases run on both kinds.

- **WR-44/a** · source-derived · the timeout message
  - **Given:** step `a` with `timeout: {"type":"static","value":1}`, running `export async function main() { await new Promise(r => setTimeout(r, 5000)); }`.
  - **Expect:** `{"error":{"message":"execution error:\nQuery timeout after (>1s)","name":"ExecutionErr","step_id":"a"}}`. Compare [WR-37/a](#wr-37).
- **WR-44/b** · source-derived · later · stack overflow is an ordinary `RangeError`
  - **Given:** step `a`: `export function main() { const f = (n: number): number => n === 0 ? 0 : 1 + f(n - 1); return f(1e6); }`
  - **Expect:** `{"error":{"message":"Maximum call stack size exceeded","stack":<stack>,"name":"RangeError"}}`.
- **WR-44/c** · source-derived · later · the heap cap, with the script waiting between allocations (medium)
  - **Given:** step `a`:
    ```ts
    export async function main() {
      const keep: number[][] = [];
      for (let i = 0; i < 64; i++) {
        keep.push(new Array(1_000_000).fill(i + 0.5));
        await new Promise(r => setTimeout(r, 0));
      }
      return keep.length;
    }
    ```
  - **Expect:** `{"error":{"message":"execution error:\nMemory limit reached, killing isolate","name":"ExecutionErr","step_id":"a"}}`.
- **WR-44/d** · source-derived · without a step timeout, the instance's default job timeout applies
  - **Given:** the instance setting `job_default_timeout` = `1` (seconds), and the worker's `TIMEOUT` left at its default of 7 days. Step `a` has no `timeout` and runs the WR-44/a script. Run this case alone and reset the setting afterwards, since it applies to every job.
  - **Expect:** `{"error":{"message":"execution error:\nQuery timeout after (>1s)","name":"ExecutionErr","step_id":"a"}}`.

<a id="wr-45"></a>
### WR-45 · What the native runtime offers: environment, globals and `fetch`

**Scope:** core · **Finding:** [WR-45](languages.md#wr-45)

- **WR-45/a** · source-derived · what `process.env` holds (both kinds)
  - **Given:** step `a`: `export function main() { return [typeof process.env.WM_TOKEN, process.env.WM_FLOW_STEP_ID, process.env.BASE_URL === process.env.BASE_INTERNAL_URL, typeof process.env.PATH, typeof (process as any).exit]; }`
  - **Expect:** `["string","a",true,"undefined","undefined"]`.
- **WR-45/b** · source-derived · reserved names are bare identifiers only in `nativets`
  - **Given:** `declare const WM_WORKSPACE: string; export function main() { return typeof WM_WORKSPACE; }`
  - **Expect:** `"string"` as `nativets`; `"undefined"` as `bunnative`.
- **WR-45/c** · source-derived · redeclaring a preamble name fails the load (`nativets`)
  - **Given:** a `nativets` step `a`: `const BASE_URL = "x"; export function main() { return BASE_URL; }`
  - **Expect:** `{"error":{"message":"execution error:\nError: failed to load module…","name":"ExecutionErr","step_id":"a"}}`; match on the prefix.
- **WR-45/d** · source-derived · installed globals (both kinds)
  - **Given:** `export function main() { return [typeof fetch, typeof URLPattern, typeof atob, typeof setInterval, typeof crypto.subtle, typeof structuredClone, typeof queueMicrotask, typeof CompressionStream, typeof Intl]; }`
  - **Expect:** `["function","function","function","function","object","function","function","function","object"]`.
- **WR-45/e** · source-derived · absent globals (`nativets`)
  - **Given:** a `nativets` step: `export function main() { const g = globalThis as any; return [typeof g.Bun, typeof g.Buffer, typeof g.require, typeof g.WebSocket, typeof g.EventSource, typeof g.setImmediate, typeof g.navigator, typeof g.Deno?.env]; }`
  - **Expect:** eight times `"undefined"`.
- **WR-45/f** · source-derived · later · `//proxy` is refused on CE (`nativets`)
  - **Given:** a `nativets` step `a` whose first line is `//proxy http://127.0.0.1:3128`, followed by `export function main() { return 1; }`.
  - **Expect:** `{"error":{"message":"execution error:\nProxy is an EE feature","name":"ExecutionErr","step_id":"a"}}`.
- **WR-45/g** · source-derived · later · the `fetch` user agent (both kinds)
  - **Given:** a local HTTP endpoint that answers with the request's `User-Agent` header; a step that fetches it and returns the response text. A second variant adds the first line `//useragent probe/1` (for `bunnative`, the line after `//native`).
  - **Expect:** `"windmill/beta"`; with the annotation, `"probe/1"`.

<a id="wr-46"></a>
### WR-46 · Native result streaming

**Scope:** later · **Finding:** [WR-46](languages.md#wr-46)

All cases run on both kinds.

- **WR-46/a** · source-derived · the stream text becomes the raw result
  - **Given:** `export async function* main() { yield "1"; yield "2"; }`
  - **Expect:** the result is the JSON number `12`. Bun gives the JSON string `"12"` ([WR-16](#wr-16)).
- **WR-46/b** · source-derived · a non-string chunk ends the stream without failing
  - **Given:** `export async function* main() { yield "1"; yield 5 as any; yield "2"; }`
  - **Expect:** the job succeeds with the result `1`.

<a id="wr-47"></a>
### WR-47 · How the pinned CLI bundles a codebase script

**Scope:** core · **Finding:** [WR-47](languages.md#wr-47)

The bundles themselves are compared in [Q-WR-24](languages.md#q-wr-24).

- **WR-47/a** · source-derived · an absent `format` runs as CommonJS on Node
  - **Given:** `f/cb/runtime.ts`: `export function main() { return typeof (globalThis as any).Bun; }`; no `format` in the entry.
  - **Expect:** `"undefined"`.
- **WR-47/b** · source-derived · `format: esm` runs on Bun
  - **Given:** the same script; the entry adds `format: esm`.
  - **Expect:** `"object"`.
- **WR-47/c** · source-derived · `define` is applied
  - **Given:** the entry adds `define: {"__MODE__": "\"lite\""}`; `f/cb/mode.ts`: `declare const __MODE__: string; export function main() { return __MODE__; }`.
  - **Expect:** `"lite"`.
- **WR-47/d** · source-derived · `inject` supplies a free identifier
  - **Given:** the entry adds `inject: ["f/cb/shim.ts"]`; `f/cb/shim.ts`: `export const answer = 42;`; `f/cb/inject.ts`: `declare const answer: number; export function main() { return answer; }`.
  - **Expect:** `42`.
- **WR-47/e** · source-derived · a `loader` map is applied
  - **Given:** the entry adds `loader: {".txt": "text"}`; `f/cb/note.txt` contains `hi`; `f/cb/text.ts`: `import note from "./note.txt"; export function main() { return note; }`.
  - **Expect:** `"hi"`.
- **WR-47/f** · source-derived · `banner` is prepended
  - **Given:** the entry adds `banner: {js: "globalThis.__banner = 1;"}`; `f/cb/banner.ts`: `export function main() { return (globalThis as any).__banner; }`.
  - **Expect:** `1`.

<a id="wr-48"></a>
### WR-48 · `customBundler`

**Scope:** later · **Finding:** [WR-48](languages.md#wr-48)

- **WR-48/a** · source-derived · the command's output is the bundle, and esbuild settings are not applied
  - **Given:** the entry adds `customBundler: "sh f/cb/emit.sh"` and `define: {"__X__": "1"}`; `f/cb/emit.sh` ignores its argument and prints `exports.main = () => typeof __X__;`; the script `f/cb/custom.ts` is `export function main() { return "source"; }`.
  - **Expect:** `"undefined"`: the printed text, not the source, ran as CommonJS on Node, and `define` was not applied.
- **WR-48/b** · source-derived · a falsy `customBundler` counts as unset
  - **Given:** the WR-48/a fixture with `customBundler: false`.
  - **Expect:** `"source"`: esbuild built the script itself, as with no `customBundler` key.

<a id="wr-49"></a>
### WR-49 · What the CLI uploads, the codebase id, and where the server keeps the bundle

**Scope:** core · **Finding:** [WR-49](languages.md#wr-49)

- **WR-49/a** · source-derived · an asset is unpacked next to `main.js`
  - **Given:** the entry adds `assets: [{from: "f/cb/data/a.txt", to: "data/a.txt"}]`; `f/cb/data/a.txt` contains `asset`; `f/cb/asset.ts`:
    ```ts
    import { readFileSync } from "node:fs";
    export function main() { return readFileSync("data/a.txt", "utf8"); }
    ```
  - **Expect:** `"asset"`.
- **WR-49/b** · source-derived · later · several output files drop the assets (medium)
  - **Given:** the entry adds `loader: {".bin": "file"}` and `assets: [{from: "f/cb/data/a.txt", to: "a.txt"}]`; `f/cb/blob.bin` holds any bytes; `f/cb/multi.ts`:
    ```ts
    import p from "./blob.bin";
    import { existsSync } from "node:fs";
    export function main() { return [existsSync(p), existsSync("a.txt")]; }
    ```
  - **Expect:** `[true,false]`.

*Setup for WR-49/c–f:* the WR-47/a fixture, plus a file `f/cb/notes/a.txt` (content `one`) that no script uses. All pushes are `wmill sync push` from the sync root unless a case says otherwise, on a fresh CE standalone oracle. The steps are:

1. Push the fixture.
2. Push again and check that this push selects nothing. If it selects `f/cb/runtime`, the local metadata differs from the deployed representation: run `wmill sync pull` and repeat until a push selects nothing.
3. Record the `hash` and `codebase` fields of `f/cb/runtime` from `GET /api/w/<ws>/scripts/get/p/f/cb/runtime`.
4. Make the case's change, push, and compare.

These cases test when a push deploys and which bytes then run, not how the bundle is built.

- **WR-49/c** · source-derived · later · any content change under `relative_path` makes a new version
  - **Given:** the setup; the content of `f/cb/notes/a.txt` changes to `two`.
  - **Expect:** the push selects `f/cb/runtime` and deploys a new version: both `hash` and `codebase` change, although the script's source, metadata and generated bundle are unchanged.
- **WR-49/d** · source-derived · later · a rename the digest cannot see is not selected
  - **Given:** the setup; `f/cb/notes/a.txt` is renamed to `f/cb/notes/b.txt`, with the same bytes. It is the only entry in `f/cb/notes/`.
  - **Expect:** the push selects nothing for `f/cb/runtime`, and its `hash` and `codebase` are unchanged.
- **WR-49/e** · source-derived · later · a change outside `relative_path` is not selected, so the old asset keeps running
  - **Given:** the setup, with two changes made before step 1. The entry adds `assets: [{from: "shared/a.txt", to: "a.txt"}]`, where `shared/a.txt` lies outside `f/cb` and contains `one`. `f/cb/runtime.ts` returns `readFileSync("a.txt", "utf8")` (from `node:fs`). After step 3, run the script, then change `shared/a.txt` to `two` and push.
  - **Expect:** the first run returns `"one"`. The push selects nothing for `f/cb/runtime`, its `hash` is unchanged, and a run after it still returns `"one"`.
- **WR-49/f** · source-derived · later · a direct script push uploads anyway
  - **Given:** the state at the end of WR-49/e. `f/cb/runtime.script.yaml` has no `priority` key, as the CLI generates it. Run `wmill script push f/cb/runtime.ts`.
  - **Expect:** the CLI does not report the script up to date; it uploads the bundle, and a run after it returns `"two"`. Whether `hash` changes depends on the server's no-op check, so this case does not assert it.
- **WR-49/g** · source-derived · later · a no-op snapshot keeps the hash but replaces the bytes (medium: building a body the server accepts as a no-op)
  - **Given:** a fresh CE standalone oracle. `f/cb/v.ts` is `export function main() { return "v1"; }`, pushed with `wmill sync push`; record its `hash` `H`. Then a direct `POST /api/w/<ws>/scripts/create_snapshot?skip_if_noop=true` multipart request:
    - field `script` first: a script body whose every field the server's no-op check compares equals the deployed script, copied from `GET /api/w/<ws>/scripts/get/h/<H>`. That covers the content, the lock, the metadata, `codebase` and the optional settings, and the body sets `parent_hash` to `H`;
    - field `file` second: a different valid CommonJS bundle, `exports.main = () => "v2";`.
  - **Expect:** status 201 with body `H`, so no new version is created; a response with any other hash means the body did not match and the case is invalid. `GET …/get/p/f/cb/v` still reports `H`, and the next run returns `"v2"`. The same holds when the body omits `is_template`, which the check reads as `false`.

<a id="wr-50"></a>
### WR-50 · How a codebase script runs: lookup, extraction and runtime

**Scope:** core · **Finding:** [WR-50](languages.md#wr-50)

- **WR-50/a** · source-derived · CE standalone deploys and runs a codebase script
  - **Given:** the WR-47/a fixture, pushed once to the CE oracle in standalone mode and once to a CE deployment with separate `MODE=server` and `MODE=worker` containers.
  - **Expect:** standalone: the push succeeds and the run returns `"undefined"`. Split: the push of that script fails with HTTP 500 and a body containing `codebase is an EE feature`, and no script exists at the path afterwards.
- **WR-50/b** · source-derived · later · the Node version is recorded
  - **Given:** `f/cb/node.ts`: `export function main() { return process.version; }`; no `format`.
  - **Expect:** a string starting `v20.`. Record the exact value ([Q-WR-24](languages.md#q-wr-24)).

*Probe for WR-50/c–d:* the script body `export function main() { return [typeof (globalThis as any).Bun, typeof (process as any).version]; }` tells the three runtimes apart: `["object","string"]` on Bun, `["undefined","string"]` on Node, `["undefined","undefined"]` in the native runtime, whose `process` has only `env` ([WR-45](languages.md#wr-45)).

- **WR-50/c** · source-derived · `//native` takes an ESM codebase script into the native runtime
  - **Given:** the entry adds `format: esm`; `f/cb/native.ts` has the first line `//native`, then the probe body. The same entry without the header is the control.
  - **Expect:** `["undefined","undefined"]`; the control gives `["object","string"]` (compare [WR-47/b](#wr-47)).
- **WR-50/d** · source-derived · `//nodejs` takes an ESM codebase script to Node
  - **Given:** the entry adds `format: esm` and `assets: [{from: "f/cb/pkg.json", to: "package.json"}]`, where `f/cb/pkg.json` is `{"type":"module"}`, which makes the ESM interpretation explicit on every Node version; `f/cb/node-esm.ts` has the first line `//nodejs`, then the probe body.
  - **Expect:** `["undefined","string"]`.
- **WR-50/e** · source-derived · later · without a declared type, the outcome depends on the Node patch
  - **Given:** the WR-50/d entry without the `package.json` asset, so only the `{}` placeholder applies; `f/cb/node-esm.ts` has the first line `//nodejs`, then `export function main() { return [typeof (globalThis as any).Bun, process.version]; }`.
  - **Expect:** on Node 20.19.0 or later, `["undefined","v20.<minor>.<patch>"]`: module syntax detection loads the bundle as ESM. On an earlier 20.x the job fails while Node loads `main.js`, with a [WR-19](#wr-19) or [WR-20](#wr-20) error. Record which outcome occurs and the version ([Q-WR-24](languages.md#q-wr-24)).

<a id="wr-51"></a>
### WR-51 · Signature-parser details: which declaration counts, `this`, qualified types, aliases and the accepted syntax

**Scope:** core · **Finding:** [WR-51](languages.md#wr-51)

- **WR-51/a** · source-derived · the last declaration of `main` gives the parameters
  - **Given:**
    ```ts
    export function main(a: string): unknown;
    export function main(a: string, d?: Date): unknown { return [a, d instanceof Date]; }
    ```
    with args `{"a":"x","d":"2024-01-02T03:04:05Z"}`.
  - **Expect:** `["x",true]`: the implementation's two parameters bind, and `d` is hydrated. Taking the first signature would pass only `a` and give `["x",false]`.
- **WR-51/b** · source-derived · a `this` parameter breaks the generated wrapper (medium)
  - **Given:** step `a`: `export function main(this: unknown, n: number) { return n; }`, args `{"n":2}`.
  - **Expect:** `{"message":"exit code for \"bun run\": 1, last log lines:\n…","name":"ExecutionErr","step_id":"a","exit_code":1}`, the message containing `Cannot use abbreviated destructuring syntax for keyword 'this'`.
- **WR-51/c** · source-derived · a qualified `Date` is hydrated
  - **Given:** `export function main(d: globalThis.Date) { return d instanceof Date; }`, args `{"d":"2024-01-02T03:04:05Z"}`.
  - **Expect:** `true`.
- **WR-51/d** · source-derived · a decorator fails the signature parse (medium)
  - **Given:** a preview run of:
    ```ts
    function tag(c: any) { return c; }
    @tag class Box {}
    export function main() { return 1; }
    ```
  - **Expect:** `{"message":"execution error:\nError: Error while parsing code, it is invalid TypeScript: …","name":"ExecutionErr"}`; match on the prefix.

<a id="wr-52"></a>
### WR-52 · What the pin-removal parse and the `.ts`-rewrite scanner see, and where a relative import is fetched

**Scope:** core · **Finding:** [WR-52](languages.md#wr-52)

- **WR-52/a** · source-derived · an angle-bracket type assertion fails the pin-removal parse (medium)
  - **Given:** a preview run of `export function main(x: unknown) { return (<number>x) + 1; }`, args `{"x":1}`.
  - **Expect:** `{"message":"execution error:\nError: Error while parsing code, it is invalid TypeScript: …","name":"ExecutionErr"}`; match on the prefix. The same script written with `(x as number)` returns `2`.
- **WR-52/b** · source-derived · a hashbang line fails the `.ts`-rewrite scan (medium)
  - **Given:** a preview run of a script whose first line is `#!/usr/bin/env bun`, followed by `export function main() { return 1; }`.
  - **Expect:** `error.name` = `ExecutionErr` and an `error.message` starting `exit code for "bun build"`. Without the first line the script returns `1`.
- **WR-52/c** · source-derived · `..` segments are resolved over the whole request URL
  - **Given:** a deployed workspace script `f/a/x`: `export const x = "x";`. A preview run, at path `f/a/s`, of `import { x } from "../../../../f/a/x"; export function main() { return x; }`.
  - **Expect:** `error.name` = `ExecutionErr`, with an `error.message` that starts `exit code for "bun build"` and contains `Failed to find relative import at`. The same script importing `./x` returns `"x"`.
- **WR-52/d** · source-derived · pin removal reads a type-only import but not `import(…)` in a type
  - **Given:**
    ```ts
    import type { T } from "types-pkg@1.0.0";
    type Lazy = typeof import("other-pkg@2.0.0");
    export function main() { return ["types-pkg@1.0.0", "other-pkg@2.0.0"]; }
    ```
  - **Expect:** `["types-pkg","other-pkg@2.0.0"]`: the type-only import's specifier is collected, so its text is replaced everywhere; the `import(…)` in a type is not collected.

<a id="wr-53"></a>
### WR-53 · Bundled mode fixes `process.env.NODE_ENV` as `"development"`

**Scope:** core · **Finding:** [WR-53](languages.md#wr-53)

- **WR-53/a** · source-derived · the build fixes the value; an unbundled run reads the job environment
  - **Given:** `export function main() { return process.env.NODE_ENV ?? null; }`, deployed with `envs: ["NODE_ENV=production"]` in its metadata; then the same script with the first line `//nobundling`.
  - **Expect:** `"development"` bundled, whatever `envs` says; `"production"` with `//nobundling`. Without the `envs` entry the unbundled run returns `null`.

<a id="wr-54"></a>
### WR-54 · The `schema_validation` line, and what it switches on

**Scope:** later · **Finding:** [WR-54](languages.md#wr-54)

- **WR-54/a** · source-derived · only the exact line switches validation on
  - **Given:** a flow whose inline step `a` has no input transforms and the source `// schema_validation`, then `export function main(x: string) { return x ?? "none"; }`. A second flow is the same with the first line `//schema_validation`.
  - **Expect:** the first fails with `{"message":"execution error:\nProblem with arguments: Argument x is required","name":"ExecutionErr","step_id":"a"}`; the second returns `"none"`.

<a id="wr-v1"></a>
### WR-V1 · An override with zero parsed parameters passes the non-internal argument values

**Scope:** core · **Finding:** [WR-V1](languages.md#wr-v1)

- **WR-V1/a** · source-derived · fallback with integer-like keys (deterministic)
  - **Given:** a zero-parameter exported function `other` that returns its received arguments as an array; args `{"_ENTRYPOINT_OVERRIDE":"other","9":9,"2":2,"_hidden":7}`.
  - **Expect:** `[2,9]`.
- **WR-V1/b** · source-derived · no override, so no arguments
  - **Given:** the same zero-parameter function as `main`, with no override.
  - **Expect:** `[]`.

<a id="wr-v2"></a>
### WR-V2 · An empty successful result fails completion with `InternalErr`

**Scope:** core · **Finding:** [WR-V2](languages.md#wr-v2)

- **WR-V2/a** · source-derived · exit 0 without writing a result
  - **Given:** `export function main() { process.exit(0); }`, as a plain job and as a flow step.
  - **Expect:** `{"error":{"message":"Internal: Result of job is invalid json (empty) @<source-location>","name":"InternalErr"}}`. Normalise only the build-dependent location. There is no `step_id`, even in the flow step.

<a id="wr-v3"></a>
### WR-V3 · Local type aliases and parentheses take part in `Date` hydration

**Scope:** core · **Finding:** [WR-V3](languages.md#wr-v3)

- **WR-V3/a** · source-derived · a local alias is hydrated
  - **Given:** a local alias `type Timestamp = Date` used for parameter `d`; args `{"d":"2024-01-02T03:04:05Z"}`.
  - **Expect:** `d` is a `Date` whose ISO text is `"2024-01-02T03:04:05.000Z"`.
- **WR-V3/b** · source-derived · array elements are not hydrated
  - **Given:** parameter `items: Date[]`, input `["2024-01-02"]`.
  - **Expect:** the first element stays the string `"2024-01-02"`.
- **WR-V3/c** · source-derived · no Date for a missing input
  - **Given:** a `Date`-typed parameter whose key is absent.
  - **Expect:** no truthy `Date` is manufactured; the parameter is `undefined`.

<a id="wr-v4"></a>
### WR-V4 · Import normalisation can change ordinary source string values

**Scope:** core · **Finding:** [WR-V4](languages.md#wr-v4)

Use a controlled synthetic package and module, so the cases test rewriting rather than registry availability.

- **WR-V4/a** · source-derived · pin removal reaches a string literal
  - **Given:** a script that imports a used binding from `fixture-pkg@1.2.3` and returns the separate literal `"fixture-pkg@1.2.3"`.
  - **Expect:** `"fixture-pkg"`, once package resolution succeeds.
- **WR-V4/b** · source-derived · the `.ts` rewrite reaches a string literal
  - **Given:** a script that imports a used binding from `./helper` and returns the separate literal `"./helper"`.
  - **Expect:** `"./helper.ts"`.

<a id="wr-v5"></a>
### WR-V5 · Pre-shaped failure detection is a structural marker check

**Scope:** later · **Finding:** [WR-V5](languages.md#wr-v5)

- **WR-V5/a** · source-derived · no second wrapper, and no `wm_failure` needed
  - **Given:** a script writes `{"error":{"name":"ManualFailure"},"k":1}` to `result.json` and exits 1.
  - **Expect:** the stored failure stays `{"error":{"name":"ManualFailure"},"k":1}`.

<a id="wr-v6"></a>
### WR-V6 · Python preprocessing uses first-signature hydration and an in-memory handoff

**Scope:** later · **Finding:** [WR-V6](languages.md#wr-v6)

- **WR-V6/a** · source-derived · `main`'s annotation does not re-hydrate
  - **Given:** a synchronous Python preprocessor returning `{"when":"2024-01-02"}`; `main` has a `datetime`-annotated `when` and returns its Python type name; run with preprocessing enabled.
  - **Expect:** `"str"`. The stored preprocessed args stay `{"when":"2024-01-02"}`.

<a id="wr-v7"></a>
### WR-V7 · Go locks contain module metadata and checksums

**Scope:** later · **Finding:** [WR-V7](languages.md#wr-v7)

All three cases use a synthetic, dependency-free Go script that returns a string and a `nil` error.

- **WR-V7/a** · source-derived · both lock parts present
  - **Given:** a lock with a valid `mymod` `go.mod`, then the separator, then empty checksum text.
  - **Expect:** dependency setup selects `download`, and the result is that JSON string.
- **WR-V7/b** · source-derived · separator absent
  - **Given:** a lock with only the `go.mod` text.
  - **Expect:** dependency setup selects `tidy`.
- **WR-V7/c** · source-derived · executable cache hit
  - **Given:** a valid executable cache hit for the script.
  - **Expect:** neither `download` nor `tidy` runs.

<a id="wr-v8"></a>
### WR-V8 · Argument ordering depends on the layer and on post-pull transformations

**Scope:** core · **Finding:** [WR-V8](languages.md#wr-v8)

- **WR-V8/a** · source-derived · fallback with integer-like keys (deterministic)
  - **Given:** a zero-parameter overridden export receives args `{"_ENTRYPOINT_OVERRIDE":"other","9":9,"2":2,"_hidden":7}` and returns its arguments.
  - **Expect:** `[2,9]`.
- **WR-V8/b** · source-derived · fallback with non-integer keys
  - **Given:** non-integer keys `a`, `bb`, `ccc` holding 1, 2 and 3, under the same fallback.
  - **Expect:** each value is passed once, in any of the six orders. Do not encode one order as an exact invariant.
- **WR-V8/c** · source-derived · `$jsonvar:` keeps the variable's order
  - **Given:** variable `u/test/order` holds the JSON string contents `{"bb":2,"a":1}`; args `{"x":"$jsonvar:u/test/order"}`; `main` returns `Object.keys(x)`.
  - **Expect:** `["bb","a"]`, not `["a","bb"]`.
- **WR-V8/d** · source-derived · the preprocessor's output keeps insertion order
  - **Given:** a script preprocessor returns an object whose `x` member is built with keys `bb`, then `a`; `main` returns `Object.keys(x)`.
  - **Expect:** `["bb","a"]`. Persisting the preprocessed args later does not reorder this call.

<a id="wr-v9"></a>
### WR-V9 · Bun lock generation uses the loader's literal `raw_unpinned` endpoint

**Scope:** later · **Finding:** [WR-V9](languages.md#wr-v9)

- **WR-V9/a** · source-derived · an HTTP-observation fixture, not a registry-install outcome
  - **Given:** dependency inference for a script at `f/test/main` that statically imports `./helper`, against a controlled API.
  - **Expect:** the API records a request whose normalised path is `/api/w/<workspace>/scripts/raw_unpinned/p/f/test/helper.ts`, not the `raw` endpoint. That endpoint removes the served helper's package pins before the builder receives it.

<a id="client-api"></a>
## The wmill client API and the platform interface

Findings: [client-api.md](client-api.md).

<a id="client-api-notation"></a>
### Notation

- Paths are synthetic (`u/x/…`, `u/t/…`, `u/test/…`, `f/t/…`). Job ids and
  timestamps are normalised; `<flow>`, `F` and `S` stand for normalised job ids.
- `…/` in a request path abbreviates `/api/w/{ws}/`.
- **TS** is the stock `windmill-client@1.767.0`; **Python** is the stock `wmill`
  client of the same version.
- A *recording facade* is a test HTTP server that returns the stated responses
  and records the client's requests. Cases that use one test client behaviour
  only and need no Windmill server.
- Cases that run on the oracle assume its default configuration unless they say
  otherwise: no sandboxing (nsjail), no workspace custom env vars or worker
  `env_vars` overriding the names under test, and an empty `WHITELIST_ENVS`.
- Every case below is source-derived; none has been recorded on the v1.767.0 CE
  oracle yet ([Q-WA-13](client-api.md#q-wa-13); the S3 server cases are
  [Q-WA-3](client-api.md#q-wa-3)).

<a id="wa-1"></a>
### WA-1 · The TS client is a thin HTTP layer configured from env vars; token and base URL are captured at import

**Scope:** core · **Finding:** [WA-1](client-api.md#wa-1)

- **WA-1/a** · source-derived · the token is captured when the module is first evaluated
  - **Given:** a harness process evaluates `windmill-client` with
    `BASE_URL=http://a:1` and `WM_TOKEN=t1` (no `BASE_INTERNAL_URL`), then sets
    `process.env.WM_TOKEN=t2` and calls `getVariable("u/x/v")`; then calls
    `setClient()` and repeats the call.
  - **Expect:** the first request goes to
    `http://a:1/api/w/<WM_WORKSPACE>/variables/get_value/u/x/v` with
    `Authorization: Bearer t1`; after `setClient()` it carries `Bearer t2`.
- **WA-1/b** · source-derived · later · an unset mock file is retried until a mock loads
  - **Given:** `getVariable("u/x/v")` with `WM_MOCKED_API_FILE` unset; then
    `WM_MOCKED_API_FILE` pointed at a file containing
    `{"variables":{"u/x/v":"m"}}` and the same call again.
  - **Expect:** the first call makes an HTTP request; the second returns `"m"`
    with no HTTP request.

<a id="wa-2"></a>
### WA-2 · Which base-URL variables a job sees

**Scope:** core · **Finding:** [WA-2](client-api.md#wa-2)

- **WA-2/a** · source-derived · a Bun job sees `BASE_URL` but not `BASE_INTERNAL_URL`
  - **Given:** oracle in the default configuration; a Bun step returning
    `[process.env.BASE_INTERNAL_URL ?? null, typeof process.env.BASE_URL]`.
  - **Expect:** `[null,"string"]`.
- **WA-2/b** · source-derived · later · an ordinary Python job sees `BASE_INTERNAL_URL` but not `BASE_URL`
  - **Given:** oracle in the default configuration; a Python step returning
    `[os.environ.get("BASE_INTERNAL_URL") is not None, os.environ.get("BASE_URL")]`.
  - **Expect:** `[true,null]`.

<a id="wa-3"></a>
### WA-3 · Python client: one global client, created lazily from env

**Scope:** later · **Finding:** [WA-3](client-api.md#wa-3)

- **WA-3/a** · source-derived · HTTP error wrapping of `get`
  - **Given:** no variable at `u/x/missing`; Python
    `wmill.get_variable("u/x/missing")`.
  - **Expect:** raises `Exception` whose message starts with
    `<BASE>/api/w/<ws>/variables/get_value/u/x/missing: 404, Not found: Variable u/x/missing not found`.

<a id="wa-4"></a>
### WA-4 · Reserved ("contextual") variables injected into every job

**Scope:** core · **Finding:** [WA-4](client-api.md#wa-4)

- **WA-4/a** · source-derived · flow-step context variables
  - **Given:** a two-step flow `f/t/envs` with Bun rawscripts `a` and `b`. Step
    `b` returns
    `{flow_step: WM_FLOW_STEP_ID, flow_path: WM_FLOW_PATH, job_path: WM_JOB_PATH, flow_job_is_parent: WM_FLOW_JOB_ID !== ""}`
    (each read from `process.env`).
  - **Expect:** `{"flow_step":"b","flow_path":"f/t/envs","job_path":"f/t/envs/b","flow_job_is_parent":true}`
    after normalising UUIDs and timestamps.

<a id="wa-5"></a>
### WA-5 · State path formula

**Scope:** core · **Finding:** [WA-5](client-api.md#wa-5)

- **WA-5/a** · source-derived · flow step, manual trigger
  - **Given:** step `a` of flow `f/t/state`, run manually.
  - **Expect:** `WM_STATE_PATH == "f/t/state/a_user"`.
- **WA-5/b** · source-derived · flow step, schedule trigger
  - **Given:** the same flow run by schedule `f/t/sched`, created by `alice`.
  - **Expect:** `WM_STATE_PATH == "f/t/state/a_alice"`.
- **WA-5/c** · source-derived · standalone script
  - **Given:** script `u/bob/s`, run manually.
  - **Expect:** `WM_STATE_PATH == "u/bob/s/user"`.

<a id="wa-6"></a>
### WA-6 · When loop and branch bodies run as sub-flows, and the runnable paths that drive `WM_FLOW_PATH`, `WM_JOB_PATH` and `WM_STATE_PATH`

**Scope:** core · **Finding:** [WA-6](client-api.md#wa-6)

All four cases use flow `f/t/loop`, all Bun, where each body step returns
`process.env.WM_FLOW_PATH`.

- **WA-6/a** · source-derived · later · a simple sequential body runs directly under the flow
  - **Given:** a sequential loop over `[1,2]` whose single body step has no
    options.
  - **Expect:** `["f/t/loop","f/t/loop"]`.
- **WA-6/b** · source-derived · later · a body step with `retry` makes the body non-simple
  - **Given:** the same body step with `retry: {constant: {attempts: 1, seconds: 1}}`.
  - **Expect:** `["f/t/loop/loop-0","f/t/loop/loop-1"]`.
- **WA-6/c** · source-derived · later · a parallel loop always creates body sub-flows
  - **Given:** a parallel loop over `[1,2]` with a single, option-free body step.
  - **Expect:** `["f/t/loop/forloop-0","f/t/loop/forloop-1"]`.
- **WA-6/d** · source-derived · later · `branchone` sub-flow numbering
  - **Given:** a `branchone` that takes `branches[1]`; separately, one that takes
    its default branch.
  - **Expect:** `"f/t/loop/branchone-2"`; for the default branch,
    `"f/t/loop/branchone-0"`.

<a id="wa-8"></a>
### WA-8 · Catalogue of client functions → endpoints → platform service

**Scope:** core · **Finding:** [WA-8](client-api.md#wa-8)

- **WA-8/a** · source-derived · route inventory
  - **Given:** for each row of the WA-8 table, a one-line Bun script calling the
    function against a recording facade.
  - **Expect:** the recorded method, path and query keys match the table.
- **WA-8/b** · source-derived · TS runtime export surfaces
  - **Given:** the built pinned package; `const ns = await import("windmill-client")`.
  - **Expect:** every non-type name of surface (1) is `in ns`;
    `Object.keys(ns.default).sort()` equals surface (2) sorted;
    `ns.default.OpenAPI === undefined`; for a fetch-generated artifact,
    `typeof ns.OpenAPI === "object"` and `typeof ns.ApiError === "function"`.
- **WA-8/c** · source-derived · TS type-only exports
  - **Given:** a `.ts` file that `import type`s every type-only name of surface
    (1) and `OpenAPIConfig`.
  - **Expect:** it passes `tsc --noEmit`.
- **WA-8/d** · source-derived · later · Python module surface
  - **Given:** `import wmill`.
  - **Expect:** `callable(getattr(wmill, n))` holds for every function name of
    surface (4); `inspect.iscoroutinefunction` is true for `step`, `sleep`,
    `wait_for_approval` and `parallel`.

<a id="wa-9"></a>
### WA-9 · Scripts use the generated services and `OpenAPI` directly

**Scope:** core · **Finding:** [WA-9](client-api.md#wa-9)

- **WA-9/a** · source-derived · a generated service called from a flow step
  - **Given:** on the oracle, a Bun flow step running
    `return await wmill.JobService.getJob({workspace: process.env.WM_WORKSPACE, id: process.env.WM_FLOW_JOB_ID, noLogs: true}).then(j => j.type)`.
  - **Expect:** `"QueuedJob"`.
- **WA-9/b** · source-derived · the default object has no `OpenAPI`
  - **Given:** `import wmill from "windmill-client"; return typeof wmill.OpenAPI`.
  - **Expect:** `"undefined"`.

<a id="wa-10"></a>
### WA-10 · `getResource` / `get_resource`: path syntax, mocked API, error wrapping

**Scope:** core · **Finding:** [WA-10](client-api.md#wa-10)

- **WA-10/a** · source-derived · missing resource through each client
  - **Given:** no resource at `u/x/none`.
  - **Expect:**
    - TS `getResource("$res:u/x/none")` throws an `Error` with message
      `Resource not found at u/x/none or not visible to you: Not found: Resource u/x/none not found`;
    - TS `getResource("u/x/none", true)` returns `undefined`;
    - Python `get_resource("res://u/x/none", none_if_undefined=True)` returns
      `None`.

<a id="wa-11"></a>
### WA-11 · Variables: `getVariable` / `setVariable`

**Scope:** core · **Finding:** [WA-11](client-api.md#wa-11)

- **WA-11/a** · source-derived · `var://` is stripped, `$var:` is not
  - **Given:** variable `u/x/v = "hello"`.
  - **Expect:** `getVariable("var://u/x/v")` returns `"hello"`;
    `getVariable("$var:u/x/v")` throws a message starting
    `Variable not found at $var:u/x/v or not visible to you: Not found:`.
- **WA-11/b** · source-derived · later · TS `setVariable` request bodies
  - **Given:** no variable at `u/x/new`; `setVariable("u/x/new", "v", true, "d")`,
    then `setVariable("u/x/new", "w")`.
  - **Expect:** the first call sends a create with body
    `{"path":"u/x/new","value":"v","is_secret":true,"description":"d"}`; the
    second sends `POST …/variables/update/u/x/new` with body `{"value":"w"}`.

<a id="wa-12"></a>
### WA-12 · Resource values are interpolated on the server

**Scope:** core · **Finding:** [WA-12](client-api.md#wa-12)

In WA-12/b and WA-12/c, `nest(0) = 1` and `nest(n) = {"a": nest(n-1)}`.

- **WA-12/a** · source-derived · `$var:`, nested `$res:`, literal `$WM_*`, and the array depth limit
  - **Given:** variable `u/x/pw = "s3cret"`; resource
    `u/x/inner = {"k":"$var:u/x/pw"}`; resource
    `u/x/db = {"password":"$var:u/x/pw","nested":{"r":"$res:u/x/inner"},"tok":"$WM_TOKEN","deep":{"a":{"b":["$var:u/x/pw"]}},"shallow":["$var:u/x/pw"]}`;
    `getResource("u/x/db")`.
  - **Expect:** `{"password":"s3cret","nested":{"r":{"k":"s3cret"}},"tok":"$WM_TOKEN","deep":{"a":{"b":["$var:u/x/pw"]}},"shallow":["s3cret"]}`
    (`deep.a.b` is an array at depth 3, so it is untouched).
- **WA-12/b** · source-derived · depth 49 is allowed
  - **Given:** resource `u/t/d49 = nest(49)`; `getResource("u/t/d49")`.
  - **Expect:** `nest(49)` unchanged.
- **WA-12/c** · source-derived · depth 50 fails
  - **Given:** resource `u/t/d50 = nest(50)`.
  - **Expect:** the endpoint returns HTTP 500; TS `getResource("u/t/d50")` throws
    a message starting
    `Resource not found at u/t/d50 or not visible to you: Internal: Maximum resource/variable interpolation depth (50) exceeded; this usually indicates a circular`.
- **WA-12/d** · source-derived · `$jsonvar:` output is terminal
  - **Given:** variable `u/t/j` = the text `{"x":"$var:u/t/v"}`; a resource
    `{"f":"$jsonvar:u/t/j"}`.
  - **Expect:** it resolves to `{"f":{"x":"$var:u/t/v"}}`.
- **WA-12/e** · source-derived · invalid `$res:` path
  - **Given:** a resource `{"x":"$res:single"}`.
  - **Expect:** HTTP 500 whose body starts `Internal: Invalid resource path: single`.
- **WA-12/f** · source-derived · arrays longer than 1000 items are not walked
  - **Given:** a resource `{"a":[…1001 copies of "$var:u/x/pw"…]}`.
  - **Expect:** returned unchanged.

<a id="wa-13"></a>
### WA-13 · State (`getState`/`setState`) is a resource of type `state` at the state path

**Scope:** later · **Finding:** [WA-13](client-api.md#wa-13)

- **WA-13/a** · source-derived · a state counter
  - **Given:** a fresh script `u/x/counter` that does
    `const n = (await getState()) ?? 0; await setState(n + 1); return n;`, run
    twice manually.
  - **Expect:** `0`, then `1`; state path `u/x/counter/user`.
- **WA-13/b** · source-derived · Python with no state path
  - **Given:** both `WM_STATE_PATH_NEW` and `WM_STATE_PATH` unset; Python
    `wmill.get_state()`.
  - **Expect:** raises `Exception("State path not found")` before any request.

<a id="wa-14"></a>
### WA-14 · Flow user state: a key/value map on the root flow job, live while it runs

**Scope:** core · **Finding:** [WA-14](client-api.md#wa-14)

- **WA-14/a** · source-derived · set, get, missing key, and outside a flow
  - **Given:** a flow with Bun steps `a`: `await setFlowUserState("k", {n:1}); return 0`
    and `b`: `return await getFlowUserState("k")`; separately
    `getFlowUserState("missing")` in a step; separately
    `setFlowUserState("k", 1)` in a standalone script.
  - **Expect:** `b` returns `{"n":1}`; the missing key returns `null`; the
    standalone call resolves `undefined` and logs
    `Error setting flow user state at k: Not found: Flow job not found`.
- **WA-14/b** · source-derived · later · the Python setter ignores non-404 failures
  - **Given:** a recording facade answering the root lookup normally and HTTP
    500 to the state POST; Python `set_flow_user_state("k", 1)`.
  - **Expect:** returns `None` and prints nothing.
- **WA-14/c** · source-derived · later · reads during and after the run
  - **Given:** a flow whose steps store values with `setFlowUserState`; a GET of
    the same key by the root's id after the run has completed.
  - **Expect:** during the run `getFlowUserState` returns the stored values;
    after the run the GET by id returns `null`. A POST by id after the run is
    excluded until [Q-WA-10](client-api.md#q-wa-10) is settled.

<a id="wa-15"></a>
### WA-15 · Dispatch: `run*Async`, `run*`, `waitJob`

**Scope:** core · **Finding:** [WA-15](client-api.md#wa-15)

- **WA-15/a** · source-derived · a failed child surfaces through `waitJob`
  - **Given:** child script `u/x/fail` that throws `new Error("boom")`; a Bun
    step doing
    `try { await runScriptByPath("u/x/fail", {}) } catch (e) { return e.message }`.
  - **Expect:** a string starting `Job ` and continuing
    `was not successful: {"name":"Error","message":"boom",` … (the rest of the
    error shape is per [languages.md](languages.md)).
- **WA-15/b** · source-derived · Python module vs instance null-result defaults
  - **Given:** script `u/t/null` that returns `null`.
  - **Expect:** Python `wmill.run_script_by_path("u/t/null")` raises
    `Exception("Result was none")`; `wmill.Windmill().run_script_by_path("u/t/null")`
    returns `None`.
- **WA-15/c** · source-derived · tracked synchronous vs untracked asynchronous flow dispatch
  - **Given:** from a flow step, against a recording facade, TS
    `runFlow("f/t/child", {})` and `runFlowAsync("f/t/child", {})`.
  - **Expect:** `runFlow` sends query `parent_job=<WM_JOB_ID>`, plus `root_job`
    when `WM_ROOT_FLOW_JOB_ID` is non-empty; `runFlowAsync` sends neither.
- **WA-15/d** · source-derived · later · `get_result` of a null result
  - **Given:** a completed job whose result is `null`; Python
    `wmill.get_result(id)`.
  - **Expect:** returns `None`.
- **WA-15/e** · source-derived · later · `get_result` of an unknown id
  - **Given:** an unknown id answered with 404; Python `wmill.get_result(id)`.
  - **Expect:** raises `Exception` whose message contains `: 404, `.

<a id="wa-16"></a>
### WA-16 · Object store (S3) client contract

**Scope:** core · **Finding:** [WA-16](client-api.md#wa-16)

- **WA-16/a** · source-derived · explicit-key upload request
  - **Given:** a recording facade; `writeS3File({s3:"t/a.txt"}, "hi")`.
  - **Expect:** `POST …/job_helpers/upload_s3_file?file_key=t%2Fa.txt` with
    body `hi`; resolves to `{"s3":<file_key from the response>}`.
- **WA-16/b** · source-derived · a bare string object is rejected
  - **Given:** `writeS3File("t/a.txt", "x")`.
  - **Expect:** throws `Invalid s3 object "t/a.txt": …` before any request.
- **WA-16/c** · source-derived · CE round trip (oracle acceptance, pending [Q-WA-3](client-api.md#q-wa-3))
  - **Given:** the CE oracle with workspace storage configured; a Bun step
    `const o = await writeS3File({s3:"t/a.txt"}, "hi"); return [o, new TextDecoder().decode(await loadS3File(o))]`.
  - **Expect:** `[{"s3":"t/a.txt"},"hi"]`.
- **WA-16/d** · source-derived · CE with no storage configured (observation, pending [Q-WA-3](client-api.md#q-wa-3))
  - **Given:** the same upload on the CE oracle with no workspace storage
    configured.
  - **Expect:** not specified by source or docs; record the status and body
    that come back.

<a id="wa-17"></a>
### WA-17 · `WM_MOCKED_API_FILE`: an upstream local-mode hook for resources and variables

**Scope:** later · **Finding:** [WA-17](client-api.md#wa-17)

- **WA-17/a** · source-derived · truthiness vs key presence
  - **Given:** `WM_MOCKED_API_FILE` pointing at
    `{"resources":{"u/x/r":{"a":1},"u/x/zero":0}}`.
  - **Expect:** `getResource("u/x/r")` returns `{"a":1}` with no HTTP; TS
    `getResource("u/x/zero")` falls back to HTTP (0 is falsy); Python returns
    `0`.
- **WA-17/b** · source-derived · mock miss on state
  - **Given:** `WM_MOCKED_API_FILE` pointing at `{}`, a state path configured,
    and the state never set; a recording facade answering 404.
  - **Expect:** Python `wmill.get_state()` returns `None` with zero HTTP
    requests; TS `getState()` makes one
    `GET …/resources/get_value_interpolated/<state path>` and returns
    `undefined`.

<a id="wa-18"></a>
### WA-18 · Resume URLs (the step asks for them; resuming is a separate suspend feature)

**Scope:** core · **Finding:** [WA-18](client-api.md#wa-18)

- **WA-18/a** · source-derived · response shape
  - **Given:** on the oracle, `const r = await getResumeUrls()` in a step.
  - **Expect:** `Object.keys(r).sort()` equals
    `["approvalPage","cancel","resume"]`, and `r.resume` matches
    `/\/api\/w\/[^/]+\/jobs_u\/resume\/[0-9a-f-]{36}\/\d+\/[0-9a-f]{64}$/`.
- **WA-18/b** · source-derived · `flow_level` targets the flow
  - **Given:** on the oracle, from a flow step, `getResumeUrls(undefined, true)`.
  - **Expect:** the `resume` URL contains `/jobs_u/resume/<flow job id>/`.

<a id="wa-19"></a>
### WA-19 · Job introspection from scripts (`getJob`, `getRootJobId`, `getResult*`, `listQueue`, cancel)

**Scope:** core · **Finding:** [WA-19](client-api.md#wa-19)

- **WA-19/a** · source-derived · root id of a top-level flow step
  - **Given:** `getRootJobId()` in step `b` of a top-level flow.
  - **Expect:** the flow's job id (`<flow>` after normalisation).

<a id="wa-20"></a>
### WA-20 · Progress, and other small helpers with exact client-side behaviour

**Scope:** later · **Finding:** [WA-20](client-api.md#wa-20)

- **WA-20/a** · source-derived · result-stream line encoding
  - **Given:** `appendToResultStream("a\nb")`, where the argument contains a real
    newline.
  - **Expect:** the stdout line `WM_STREAM: a\nb`, where `\n` is the two literal
    characters backslash and `n`.
- **WA-20/b** · source-derived · progress is last-write-wins
  - **Given:** `setProgress(80)`, then `setProgress(20)`, then `getProgress()`.
  - **Expect:** `20`.
- **WA-20/c** · source-derived · progress is clamped above
  - **Given:** `setProgress(150)`, then `getProgress()`.
  - **Expect:** `99`.
- **WA-20/d** · source-derived · progress is clamped below
  - **Given:** `setProgress(-5)`, then `getProgress()`.
  - **Expect:** `0`.

<a id="wa-30"></a>
### WA-30 · How the client reaches a job, and the options for serving it without Windmill

**Scope:** core · **Finding:** [WA-30](client-api.md#wa-30)

- **WA-30/a** · policy · the facade and the in-process interceptor agree.
  - **Given:** the same fixture flow, run once through the HTTP facade with
    the stock `windmill-client@1.767.0` and once through the in-process
    `fetch` interceptor.
  - **Expect:** byte-identical normalised step results and identical
    recorded platform calls (method, path, query, body). This is a
    windmill-lite acceptance check for a design option, not Windmill
    behaviour.

<a id="wa-31"></a>
### WA-31 · Per-invocation identity: process-global env and token require per-step isolation

**Scope:** core · **Finding:** [WA-31](client-api.md#wa-31)

- **WA-31/a** · source-derived · concurrent steps keep their own identity
  - **Given:** a parallel forloop over `[1..8]` whose single Bun step returns
    `[process.env.WM_JOB_ID, await getRootJobId()]`; under a warm in-process
    harness, the flow is run 50 times.
  - **Expect:** in every run, the 8 `WM_JOB_ID`s are distinct and every
    `getRootJobId` equals the flow id; no cross-talk between invocations.
- **WA-31/b** · source-derived · `Bun.env` reads in a bundled step
  - **Given:** a bundled step reading `Bun.env.WM_JOB_ID`, run with 8-way
    parallelism.
  - **Expect:** 8 distinct ids.

<a id="wa-32"></a>
### WA-32 · TS `task()` switches to HTTP dispatch when `WM_FLOW_JOB_ID` is empty

**Scope:** core · **Finding:** [WA-32](client-api.md#wa-32)

- **WA-32/a** · source-derived · `task()` in a flow step runs inline
  - **Given:** a Bun flow step (non-empty `WM_FLOW_JOB_ID`) calling a
    `task`-wrapped function, against a recording facade.
  - **Expect:** the step returns the function's value directly, and no dispatch
    request is recorded.
- **WA-32/b** · source-derived · out · Python `@task` outside a workflow dispatches over HTTP
  - **Given:** a plain (non-workflow) Python flow step calling a `@task`
    function, with `WM_JOB_ID` set and no `MAIN_OVERRIDE`.
  - **Expect:** a `POST /w/{ws}/jobs/run/workflow_as_code/{WM_JOB_ID}/{name}`.
- **WA-32/c** · source-derived · out · Python `@task` inside a workflow body does not
  - **Given:** the same `@task` function called inside a body run by
    `_run_workflow_async`.
  - **Expect:** no such POST.

<a id="wa-33"></a>
### WA-33 · Workflow-as-code is a separate execution model

**Scope:** out · **Finding:** [WA-33](client-api.md#wa-33)

- **WA-33/a** · source-derived · the parsed path needs no `task(`
  - **Given:** a TS script `export const main = workflow(async (x: number) => x)`
    with no `task(` anywhere.
  - **Expect:** classified as workflow-as-code.
- **WA-33/b** · source-derived · the substring fallback does not run when parameters were found
  - **Given:** a TS script with `export async function main()` that mentions
    `workflow(` and `task(` only in a comment.
  - **Expect:** not workflow-as-code.

- **WA-33/c** · policy · windmill-lite rejects workflow-as-code.
  - **Given:** a script that upstream classifies as workflow-as-code (by
    either detection path), loaded by windmill-lite.
  - **Expect:** the loader rejects it with error kind
    `unsupported_feature`/`workflow_as_code`. windmill-lite policy, not
    Windmill behaviour.

<a id="wa-40"></a>
### WA-40 · Platform services, and the three resource-read contracts they must keep apart

**Scope:** core · **Finding:** [WA-40](client-api.md#wa-40)

Both cases are windmill-lite acceptance checks for the platform-service
design, not Windmill behaviour.

- **WA-40/a** · policy · the WA-V3/a contexts hold under windmill-lite.
  - **Given:** the fixture of [WA-V3/a](#wa-v3), run on windmill-lite.
  - **Expect:** the argument resolves to `{"root":""}`, `getResource` returns
    `{"root":"$WM_ROOT_JOB_ID"}`, and the process `WM_ROOT_JOB_ID` is F.
- **WA-40/b** · policy · each platform service matches the oracle.
  - **Given:** for each platform service, one shared fixture run on both the
    oracle and windmill-lite.
  - **Expect:** equal results after normalisation.

<a id="wa-41"></a>
### WA-41 · Unsupported platform calls: what the stock clients do with an error response

**Scope:** core · **Finding:** [WA-41](client-api.md#wa-41)

The stock-client reactions to a 501 for dispatch and flow state are the cases of
[WA-V5](#wa-v5).

- **WA-41/a** · source-derived · a generated service rejects with `ApiError`
  - **Given:** a fetch-generated client; a recording facade answering HTTP 501
    with status text `Not Implemented`, `Content-Type: text/plain`, body
    `Not implemented in windmill-lite: GET /w/{ws}/jobs_u/get/{id}`;
    `JobService.getJob({workspace, id})`.
  - **Expect:** rejects with
    `{name:"ApiError", status:501, statusText:"Not Implemented", message:"Not Implemented", body:"Not implemented in windmill-lite: GET /w/{ws}/jobs_u/get/{id}"}`.
    `statusText` is the status text the facade sent; `message` comes from the
    generator's status table.
- **WA-41/b** · policy · `fail` mode surfaces unsupported calls.
  - **Given:** windmill-lite in `fail` mode (the default); the steps of
    [WA-V5/a–c](#wa-v5); a call to `getIdToken("aud")`; a call to
    `getState()`.
  - **Expect:** the WA-V5 steps end with the `UnsupportedPlatformCall` error
    even though each script returned normally; `getIdToken("aud")` throws
    with `status === 501`; `getState()` returns `undefined` only for a
    genuinely missing state. windmill-lite policy, not Windmill behaviour.

<a id="wa-42"></a>
### WA-42 · Error status codes and bodies

**Scope:** core · **Finding:** [WA-42](client-api.md#wa-42)

- **WA-42/a** · source-derived · status and body per error variant
  - **Given:** for each row of the WA-42 table, a representative error rendered
    as an API response.
  - **Expect:** that row's status and body prefix; in particular `AIError("x")`
    gives 400 with body `x`, and `MetricNotFound("m")` gives 500 with body
    `Metric not found: m`.
- **WA-42/b** · source-derived · direct missing resource
  - **Given:** no resource at `u/x/none`;
    `GET …/resources/get_value_interpolated/u/x/none`.
  - **Expect:** 404, `Content-Type: text/plain`, body
    `Not found: Resource u/x/none not found`.
- **WA-42/c** · source-derived · nested missing `$res:` gives the located form
  - **Given:** resource `u/t/outer = {"x":"$res:u/t/missing"}` and no
    `u/t/missing`; `GET …/resources/get_value_interpolated/u/t/outer`; TS
    `getResource("u/t/outer")`.
  - **Expect:** 404 with a body starting
    `Not found: Resource not found at name u/t/missing (`; TS throws
    `Resource not found at u/t/outer or not visible to you: Not found: Resource not found at name u/t/missing (<location>)`.
    Compare up to and including the `(`.

<a id="wa-50"></a>
### WA-50 · Delegation endpoints and their semantics

**Scope:** later · **Finding:** [WA-50](client-api.md#wa-50)

- **WA-50/a** · source-derived · a failed preview job answers 422
  - **Given:** a Bun script that throws `Error("x")`, delegated through
    `POST /api/w/{ws}/jobs/run_wait_result/preview`.
  - **Expect:** HTTP 422 with a JSON body whose `error.message == "x"`.
- **WA-50/b** · source-derived · inlining a by-path step keeps the state path
  - **Given:** parent flow `f/t/flow`, step `a`, run manually, no custom env
    overrides; first as a script-by-path step referencing `u/t/script`, then
    with the same code as an inline step without an explicit `path`.
  - **Expect:** by-path: `WM_STATE_PATH == "f/t/flow/a_user"` and
    `WM_JOB_PATH == "u/t/script"`; inline: `WM_STATE_PATH == "f/t/flow/a_user"`
    and `WM_JOB_PATH == "f/t/flow/a"`.

<a id="wa-51"></a>
### WA-51 · Platform calls made by delegated steps, and token scopes

**Scope:** later · **Finding:** [WA-51](client-api.md#wa-51)

- **WA-51/a** · source-derived · a path-scoped token cannot run previews
  - **Given:** on the oracle, a token scoped to `jobs:run:scripts:u/t/a`.
  - **Expect:** it can run `u/t/a` by path but is refused by
    `/api/w/{ws}/jobs/run/preview`.

- **WA-51/b** · policy · remote-backed variables read the same.
  - **Given:** hybrid mode (a), remote-backed platform: a local step and a
    delegated step each call `getVariable("u/x/v")`.
  - **Expect:** both return the same value, and the facade records one
    proxied `GET …/variables/get_value/u/x/v`. windmill-lite acceptance
    check, not Windmill behaviour.

<a id="wa-v1"></a>
### WA-V1 · Stored root fields, effective root identity and innermost flow scope are distinct

**Scope:** core · **Finding:** [WA-V1](client-api.md#wa-v1)

- **WA-V1/a** · source-derived · root notions inside a called sub-flow
  - **Given:** outer flow F invokes sub-flow S through a flow module. A Bun step
    T inside S returns `{root, inner, parent}` from `getRootJobId()`,
    `WM_ROOT_FLOW_JOB_ID` and `WM_FLOW_JOB_ID`. A normal script step directly
    inside F returns the same object. T stores a value through flow user state,
    and a later step of F reads it.
  - **Expect:** after id normalisation, T returns
    `{"root":"F","inner":"S","parent":"S"}`; the direct step returns
    `{"root":"F","inner":"F","parent":"F"}`; the later step of F sees the value
    stored by T.

<a id="wa-v2"></a>
### WA-V2 · Unfinished and unknown-job polling responses carry explicit nulls

**Scope:** core · **Finding:** [WA-V2](client-api.md#wa-v2)

- **WA-V2/a** · source-derived · unknown job, default query
  - **Given:** `GET …/jobs_u/completed/get_result_maybe/<valid UUID with no job>`.
  - **Expect:** HTTP 200, JSON
    `{"started":null,"success":null,"completed":false,"result":null}`.
- **WA-V2/b** · source-derived · unknown job, `get_started=true`
  - **Given:** the same request with `get_started=true`.
  - **Expect:** HTTP 200, JSON
    `{"started":false,"success":null,"completed":false,"result":null}`.

<a id="wa-v3"></a>
### WA-V3 · Shared interpolation needs separate resource and argument contexts

**Scope:** core · **Finding:** [WA-V3](client-api.md#wa-v3)

- **WA-V3/a** · source-derived · `$WM_ROOT_JOB_ID` differs between contexts
  - **Given:** resource `u/test/context = {"root":"$WM_ROOT_JOB_ID"}`; in a
    direct Bun step of top-level flow F, with no env overrides and the ordinary
    null stored root, an argument loaded via `$res:u/test/context`, and a call
    to `getResource("u/test/context")`.
  - **Expect:** the hydrated argument is `{"root":""}`; the process
    `WM_ROOT_JOB_ID` is F; `getResource` returns `{"root":"$WM_ROOT_JOB_ID"}`.
- **WA-V3/b** · source-derived · `$jsonvar:` output is not substituted again
  - **Given:** variable `u/test/j` whose value is the JSON text
    `{"x":"$var:u/test/v"}`; a resource field `"$jsonvar:u/test/j"`.
  - **Expect:** the field becomes exactly `{"x":"$var:u/test/v"}`.

<a id="wa-v4"></a>
### WA-V4 · TS default and named export surfaces are not interchangeable

**Scope:** core · **Finding:** [WA-V4](client-api.md#wa-v4)

- **WA-V4/a** · source-derived · default-object members
  - **Given:** the built pinned package's default export.
  - **Expect:** its `getResultMaybe` member is a function and its `OpenAPI`
    member is `undefined`; a default-object call to `getResultMaybe(id)` uses the
    `…/jobs_u/completed/get_result_maybe/{id}` route.

<a id="wa-v5"></a>
### WA-V5 · An unsupported HTTP response does not imply a rejected helper call

**Scope:** core · **Finding:** [WA-V5](client-api.md#wa-v5)

All three cases observe the stock clients (the `observe` mode of
[WA-41](client-api.md#wa-41)), against a recording facade answering HTTP 501,
`text/plain`.

- **WA-V5/a** · source-derived · async dispatch returns the error body as a job id
  - **Given:** the facade answers `POST /w/{ws}/jobs/run/p/{path}` with body
    `Not implemented in windmill-lite: POST /w/{ws}/jobs/run/p/{path}`;
    `await runScriptByPathAsync("u/t/x")`.
  - **Expect:** resolves to exactly
    `Not implemented in windmill-lite: POST /w/{ws}/jobs/run/p/{path}`, without
    throwing.
- **WA-V5/b** · source-derived · default `setFlowUserState` swallows the error
  - **Given:** a successful root lookup, then a 501 on the state POST with body
    `Not implemented in windmill-lite: POST /w/{ws}/jobs/flow/user_states/{id}/{key}`;
    `setFlowUserState("k", 1)`.
  - **Expect:** resolves `undefined` and logs
    `Error setting flow user state at k: Not implemented in windmill-lite: POST /w/{ws}/jobs/flow/user_states/{id}/{key}`.
- **WA-V5/c** · source-derived · strict `setFlowUserState` rejects
  - **Given:** the same responses; `setFlowUserState("k", 1, true)`.
  - **Expect:** rejects with that message.

<a id="wa-v6"></a>
### WA-V6 · Preview delegation needs a transitive code snapshot

**Scope:** later · **Finding:** [WA-V6](client-api.md#wa-v6)

- **WA-V6/a** · source-derived · an unsupplied relative import runs the deployed code
  - **Given:** a synthetic preview entrypoint that imports `./helper`; the local
    helper returns `"local"` and the deployed helper returns `"remote"`. Run once
    with no supplied module or temp-script reference, once with the local helper
    supplied.
  - **Expect:** `"remote"` without the supplied dependency; `"local"` with it.

<a id="wa-v7"></a>
### WA-V7 · Flow wait-result can finish before the remote flow

**Scope:** later · **Finding:** [WA-V7](client-api.md#wa-v7)

- **WA-V7/a** · source-derived · early return answers before completion
  - **Given:** a flow with `early_return` pointing at step `a`, which returns
    `"early"`, and a later delayed step `b` that returns `"final"`; called via
    flow `run_wait_result`, then polled after completion.
  - **Expect:** the wait-result route returns HTTP 200 JSON `"early"` while the
    flow continues; polling after completion yields result `"final"`.
- **WA-V7/b** · source-derived · a failed early node with a failure module is not returned early
  - **Given:** the same flow where `a` fails and a failure module is configured.
  - **Expect:** the early-result branch does not return `a`'s failure; the
    handler waits for the completed flow.

<a id="wa-v8"></a>
### WA-V8 · Generated core exports, error objects and transport selection

**Scope:** core · **Finding:** [WA-V8](client-api.md#wa-v8)

- **WA-V8/a** · source-derived · `ApiError` fields for a 501
  - **Given:** a fetch-generated client with no operation-specific 501
    override; a recording server answers HTTP 501 with status text
    `Not Implemented`, `Content-Type: text/plain`, body `unsupported`.
  - **Expect:** the rejected error has `name: "ApiError"`, `status: 501`,
    `statusText: "Not Implemented"`, `body: "unsupported"`,
    `message: "Not Implemented"`, and `url` equal to the requested URL.
- **WA-V8/b** · source-derived · generated root exports
  - **Given:** the generated root module.
  - **Expect:** runtime export tests find `OpenAPI` and `ApiError`; compile-time
    type tests cover `OpenAPIConfig` and the type-only helper exports.

<a id="wa-v9"></a>
### WA-V9 · Python S3 upload distinguishes transport/JSON errors from response-shape errors

**Scope:** later · **Finding:** [WA-V9](client-api.md#wa-v9)

All three cases call Python `write_s3_file(None, b"x", None)` against a
recording facade; they say nothing about real server error bodies.

- **WA-V9/a** · source-derived · non-JSON body is wrapped
  - **Given:** HTTP 500 with non-JSON body `failure`.
  - **Expect:** raises `Exception("Could not write file to S3")`.
- **WA-V9/b** · source-derived · JSON without `file_key` is not wrapped
  - **Given:** HTTP 500 with JSON `{}`.
  - **Expect:** raises `KeyError('file_key')`.
- **WA-V9/c** · source-derived · no status check
  - **Given:** HTTP 500 with JSON `{"file_key":"x"}`.
  - **Expect:** returns `{"s3":"x","storage":null}`.

<a id="wa-v10"></a>
### WA-V10 · Runnable identity depends on the flow-step representation

**Scope:** later · **Finding:** [WA-V10](client-api.md#wa-v10)

- **WA-V10/a** · source-derived · `WM_RUNNABLE_ID` per representation
  - **Given:** synthetic flow `f/t/flow`, step `a`, manual trigger, no custom env
    overrides; the step body returns `process.env.WM_RUNNABLE_ID`, once as a
    deployed `u/t/script` step, once as an unreduced `rawscript`, once as the
    corresponding reduced `flowscript`.
  - **Expect:** the selected script hash string; `""`; the flow-node id string.
    Assert identity against the selected representation, not source-text
    equality. All three keep `WM_STATE_PATH = "f/t/flow/a_user"`.
- **WA-V10/b** · source-derived · an explicit `rawscript` path overrides the job path
  - **Given:** the unreduced `rawscript` with explicit `path = "f/t/custom"`.
  - **Expect:** `WM_JOB_PATH == "f/t/custom"`; `WM_STATE_PATH` unchanged
    (`"f/t/flow/a_user"`).

<a id="wa-v11"></a>
### WA-V11 · Python S3 delete error boundaries, including an unwrapped preflight

**Scope:** later · **Finding:** [WA-V11](client-api.md#wa-v11)

- **WA-V11/a** · source-derived · invalid URI fails before any request
  - **Given:** Python `delete_s3_object("s3:///")`.
  - **Expect:** raises `ValueError`; zero HTTP requests.
- **WA-V11/b** · source-derived · mapping without `s3` fails before any request
  - **Given:** Python `delete_s3_object({})`.
  - **Expect:** raises `KeyError` for `s3`; zero HTTP requests.
- **WA-V11/c** · source-derived · HTTP error is wrapped
  - **Given:** Python `delete_s3_object("s3:///k")` against a recording facade
    answering 404.
  - **Expect:** raises `Exception` whose message starts with the request URL and
    contains `: 404, `.

<a id="oracle"></a>
## The oracle

Findings: [oracle.md](oracle.md).

<a id="oracle-notation"></a>
### Notation

- **Observations.** Unless a case says otherwise, an expectation is a
  normalised observation ([OR-14](oracle.md#or-14), [OR-15](oracle.md#or-15)):
  `{"status", "result", "steps": {<path>: {status, ran?, args?, result?, branch_chosen?, skipped?}}, "effects"}`.
  Job status is `success`, `failure`, `canceled` or `skipped`; a module status
  may also be `not_run`. Comparison is exact by default; `subset`,
  `key_order: strict` and `unordered` (multiset equality) apply only where a
  case names them.
- **Paths and placeholders.** Step keys use the drafted path encoding (`a`,
  `l[2]`, `b#0`, `failure`), which is disputed ([OR-V8](oracle.md#or-v8)).
  `<job:PATH>` is a walked job id, `<stack>` a masked runtime stack, `<loc>` the
  masked location of the NUL `InternalErr`, `<ws>` the workspace. In raw
  values, `<worker>`, `<job>`, `<uuid>` and `<64-hex>` elide volatile parts.
  In an expected error message, `<loc>` also stands for one Rust location
  suffix that Windmill appended, **including its leading space**
  (` @<file>.rs:<line>:<col>`, or the parenthesised not-found form
  ` (<file>.rs:<line>)`), which a recording keeps verbatim and a comparison
  with windmill-lite strips ([OR-14](oracle.md#or-14)); two suffixes are
  written `<loc><loc>`.
  Recorded fixtures name their sites with the typed paths of design record
  0011 (`m:<id>`, `i:<n>`, `b:<n>`, `s:failure`, `r:<k>`).
- **Probes** are named after the rows of the
  [Probe catalogue](oracle.md#probe-catalogue), which gives their recipes;
  `x→v` means input transform `x = <javascript v>`. Rows marked *disputed
  recipe* there need reconstruction ([OR-V9](oracle.md#or-v9)).
- **Provenance.** `oracle-observed` means recorded in oracle run 1 on CE
  v1.767.0 (arm64, one standalone worker); the raw captures were not retained.
  A case that also ends with a **Recorded:** line was recorded again, or
  first, by a fixture committed under [`conformance/flows/`](../../conformance/flows)
  in the oracle's public CI (amd64, one standalone worker) since milestone
  E2; that recording, with its provenance, is the durable evidence
  ([oracle.md, Conventions](oracle.md#conventions)).
  `source-derived` means not yet recorded. A case whose description starts
  with *Harness self-test* checks the harness's own logic and needs no oracle
  recording.

<a id="or-1"></a>
### OR-1 · Pin the oracle image by full version and digest

**Scope:** core · **Finding:** [OR-1](oracle.md#or-1)

- **OR-1/a** · oracle-observed · Preflight version check.
  - **Given:** a freshly booted oracle from `ghcr.io/windmill-labs/windmill:1.767.0@sha256:9239264a7a849deedda87868147e65eb910456039fe9d961901e2e5b143ebd43`.
  - **Expect:** `GET /api/version` returns exactly `CE v1.767.0`; image identity
    matches through `RepoDigests`.
- **OR-1/b** · source-derived · Harness self-test: version mismatch aborts.
  - **Given:** `/api/version` returns any other string (for example `EE v1.767.0`).
  - **Expect:** the run aborts with `{"error":"oracle_version_mismatch","got":"…"}`,
    where `got` is the served string.

<a id="or-2"></a>
### OR-2 · Image variants: Bun is 1.3.10, slim is amd64-only, the in-image CLI is unpinned

**Scope:** core · **Finding:** [OR-2](oracle.md#or-2)

- **OR-2/a** · oracle-observed · Bun version inside the oracle.
  - **Given:** a Bun rawscript returning `Bun.version` (probe `env/bun_version`;
    recorded as the `bun` field of the `env` probe).
  - **Expect:** `"1.3.10"`.
- **OR-2/b** · source-derived · Harness self-test: a Bun version mismatch is a
  provenance failure.
  - **Given:** a windmill-lite run whose Bun harness reports a version other than
    `1.3.10`.
  - **Expect:** the provenance check fails and reports the mismatch; no other
    semantic difference is allowed to pass because of it.

<a id="or-4"></a>
### OR-4 · Minimal topology: Postgres plus one `windmill` container in standalone mode

**Scope:** core · **Finding:** [OR-4](oracle.md#or-4)

- **OR-4/a** · oracle-observed · The minimal stack boots.
  - **Given:** the compose of the [Runbook](oracle.md#runbook), `docker compose up -d`.
  - **Expect:** within 180 s, `GET /api/health/status?force=true` returns
    `{"status":"healthy","database_healthy":true,"workers_alive":1,…}`
    (observed at 6 s on arm64, and at 6.4 s on amd64 in E2's first CI
    session, oracle run 40649910412). The harness records boot time,
    container memory peaks and image sizes in provenance; every CI session
    checks this case live.

<a id="or-5"></a>
### OR-5 · Consequences of a single worker

**Scope:** core · **Finding:** [OR-5](oracle.md#or-5)

- **OR-5/a** · source-derived · Harness self-test: refuse a blocking fixture on one
  worker.
  - **Given:** fixture `oracle/blocking_child` declaring `oracle: {workers: 2}`;
    step `a` runs a script that calls the client to run `f/x/child` and waits
    for it; the oracle has one worker.
  - **Expect:** the harness refuses without running or hanging:
    `{"error":"fixture_requires_workers","need":2,"have":1}`.
- **OR-5/b** · source-derived · later · The same fixture on a two-worker profile.
  - **Given:** `oracle/blocking_child` on the two-worker profile.
  - **Expect:** the run completes, and `a`'s result equals the child's result.
    Pending ([Q-OR-18](oracle.md#q-or-18)).

<a id="or-6"></a>
### OR-6 · Bootstrap: identity, token, workspace

**Scope:** core · **Finding:** [OR-6](oracle.md#or-6)

- **OR-6/a** · oracle-observed · Normal-auth bootstrap.
  - **Given:** a fresh oracle.
  - **Expect:** `POST /api/auth/login` with
    `{"email":"admin@windmill.dev","password":"changeme"}` returns 200 and a
    non-empty plain-text body that is not JSON; with it as bearer token,
    `POST /api/workspaces/create` with `{"id":"wl-smoke","name":"wl-smoke"}`
    returns 2xx, and `GET /api/w/wl-smoke/jobs/queue/count` returns
    `{"database_length":0,…}`. Every CI session since E2 checks this live,
    creating one workspace per fixture and deleting it after the fixture's
    runs, because CE refuses a third workspace besides `admins`
    ([OR-6](oracle.md#or-6)).

<a id="or-7"></a>
### OR-7 · Environment knobs (env only, image unmodified)

**Scope:** later · **Finding:** [OR-7](oracle.md#or-7)

- **OR-7/a** · source-derived · Scheduling knobs do not change observations.
  - **Given:** the [OR-20](#or-20) smoke runs, once with
    `LISTEN_NEW_EVENTS_INTERVAL_SEC=1` and once with `10`.
  - **Expect:** byte-identical normalised observations; only timings differ.
    Pending ([Q-OR-18](oracle.md#q-or-18)).
- **OR-7/b** · source-derived · The result-size limit is execution-observable.
  - **Given:** a profile with `MAX_RESULT_SIZE_MB=3` and a step returning a 4 MB
    result.
  - **Expect:** the step's result is rejected; the error shape is to be
    recorded. Optional ([Q-OR-14](oracle.md#q-or-14)).

<a id="or-8"></a>
### OR-8 · Pushing a synthetic folder: the pinned CLI is the reference loader

**Scope:** core · **Finding:** [OR-8](oracle.md#or-8)

- **OR-8/a** · oracle-observed · Generate and push the smoke tree.
  - **Given:** the [OR-20](#or-20) tree; from its `windmill/` directory,
    `windmill-cli@1.767.0 --base-url http://localhost:8000 --token <t> --workspace <ws> --config-dir <tmp>`
    with `folder add-missing --yes`, `generate-metadata --yes`, then
    `sync push --yes`.
  - **Expect:** each command exits 0. For the dependency-free Bun script the
    generator writes `double.script.yaml` (`lock: '!inline f/wl_smoke/double.script.lock'`),
    a 43-byte `double.script.lock` (dependency document `{"dependencies": {}}`,
    then `//bun.lock` and `<empty>`), a lock next to the flow's `!inline` file
    (`b.lock`), and a root `wmill-lock.yaml` with `version: v2`.
  - **Recorded:** [`flows/oracle/smoke`](../../conformance/flows/oracle/smoke),
    whose committed metadata is what `generate-metadata` wrote in oracle run
    40649910412; the drift gate of every later session found it current.
- **OR-8/b** · oracle-observed · The deployed flow has the pushed modules.
  - **Given:** the push of OR-8/a.
  - **Expect:** `GET /api/w/<ws>/flows/get/f/wl_smoke/two_step` returns 200 with
    `value.modules[*].id == ["a","b"]`.
  - **Recorded:** [`flows/oracle/smoke`](../../conformance/flows/oracle/smoke),
    oracle run 40649910412 and every session since: the harness checks
    each deployed flow's module ids against its tree after the deployment
    gate.
  - **Correction 2026-10-28:** this case was `source-derived` and pending
    [Q-OR-18](oracle.md#q-or-18); the expectation is unchanged.
- **OR-8/c** · source-derived · Harness self-test: metadata drift gate.
  - **Given:** a committed fixture tree; re-run `generate-metadata --yes` against
    the oracle.
  - **Expect:** `git diff --exit-code` on the fixture tree exits 0 when the
    committed metadata is current and non-zero otherwise.
    `generate-metadata --dry-run` is not used as the gate.

<a id="or-9"></a>
### OR-9 · Wait for deployment before running

**Scope:** core · **Finding:** [OR-9](oracle.md#or-9)

- **OR-9/a** · oracle-observed · Deployment gate after pushing the smoke tree.
  - **Given:** the push of [OR-8/a](#or-8).
  - **Expect:** `GET /api/w/<ws>/flows/deployment_status/p/f/wl_smoke/two_step`
    returns `{"lock_error_logs": null, "job_id": "<uuid>"}`, and that `job_id`
    polled with `jobs_u/completed/get_result_maybe` is `completed` with
    `success: true`. The script `f/wl_smoke/double` (committed lock) returns
    `{"lock":"{\n  \"dependencies\": {}\n}\n//bun.lock\n<empty>","lock_error_logs":null,"job_id":null}`.
    The gate passes (0.07 s in run 1). A harness that waits for the flow's
    `job_id` to become `null` is wrong.
  - **Recorded:** [`flows/oracle/smoke`](../../conformance/flows/oracle/smoke),
    oracle run 40649910412; every fixture of every later session passes the
    same gate.

<a id="or-10"></a>
### OR-10 · Running a flow: asynchronous submit plus poll, never `run_wait_result`

**Scope:** core · **Finding:** [OR-10](oracle.md#or-10)

- **OR-10/a** · oracle-observed · Composite keys survive the asynchronous path.
  - **Given:** probe `composite`: a rawscript returning
    `{"windmill_status_code":201,"result":{"a":1}}`; submit with `jobs/run/f`,
    poll `get_result_maybe`.
  - **Expect:** `result == {"result":{"a":1},"windmill_status_code":201}`.
    windmill-lite's flow result must equal this form.
- **OR-10/b** · oracle-observed · later · `run_wait_result` rewrites the response.
  - **Given:** the same flow via `run_wait_result/f/f/wl_probe/composite`.
  - **Expect:** HTTP 201, `content-type: application/json`, body `{"a": 1}`
    (a host-adapter concern).
- **OR-10/c** · oracle-observed · Smoke run through submit and poll.
  - **Given:** `POST /api/w/<ws>/jobs/run/f/f/wl_smoke/two_step` with `{"x":3}`.
  - **Expect:** 201 with a text/plain UUID; polling ends with
    `{"started":true,"success":true,"completed":true,"result":"hi:6"}`.
  - **Recorded:** [`flows/oracle/smoke`](../../conformance/flows/oracle/smoke)
    run `ok`, oracle run 40649910412 (the harness pre-assigns the root id
    with `?job_id=`).

<a id="or-11"></a>
### OR-11 · Capturing per-step results: walk `flow_status`

**Scope:** core · **Finding:** [OR-11](oracle.md#or-11)

- **OR-11/a** · disputed · Smoke observation after the walk.
  - **Given:** the [OR-20](#or-20) `ok` run (`{"x":3}`).
  - **Expect:**
    ```json
    {"status":"success","result":"hi:6","steps":{
      "a":{"status":"success","args":{"x":3},"result":{"doubled":6,"seen":3}},
      "b":{"status":"success","args":{"n":6,"tag":"hi"},"result":"hi:6"}}}
    ```
  - **Contested:** the values were observed on the oracle; what is disputed is
    the step-key identity. As drafted, keys are plain module ids with
    `failure` / `preprocessor` reserved for special slots; the correction
    requires an encoding that keeps an ordinary module named `failure` apart
    from the failure slot ([Q-OR-17](oracle.md#q-or-17)).
  - **Recorded:** [`flows/oracle/smoke`](../../conformance/flows/oracle/smoke)
    run `ok`, oracle run 40649910412, with the same values under the typed
    keys `m:a` and `m:b`.
- **OR-11/b** · disputed · Empty for-loop.
  - **Given:** probe `loop_empty` (a `forloopflow` `l` with iterator `[]`).
  - **Expect:** step `l` is `{"status":"success","ran":false,"result":[]}`; its
    module is `Success` with a nil `job` and `flow_jobs: []` (observed).
  - **Contested:** as for OR-11/a, only the identity of the key `l`.
  - **Recorded:** the same shape for an iterator that evaluates to `[]`,
    [`flows/control-flow/cf-2`](../../conformance/flows/control-flow/cf-2)
    run `a`, oracle run 40685057858 (key `m:l`).

<a id="or-12"></a>
### OR-12 · Failure capture: the stored error envelope

**Scope:** core · **Finding:** [OR-12](oracle.md#or-12)

- **OR-12/a** · oracle-observed · Plain Bun `Error` in step `b`.
  - **Given:** the [OR-20](#or-20) `fail` run (`{"x":60}`).
  - **Expect:** root `status: failure`; the root result and `steps.b.result`
    are both
    ```json
    {"error":{"name":"Error","message":"too big: 120","stack":"<stack>","step_id":"b"}}
    ```
    and `steps.a.result` is `{"doubled":120,"seen":60}`. The raw stack was
    `Error: too big: 120\n    at main (/tmp/windmill/cache/bun/<64-hex>:5:16)\n    at run (/tmp/windmill/<worker>/<job>/wrapper.mjs:30:21)\n    at /tmp/windmill/<worker>/<job>/wrapper.mjs:42:11\n    at asyncModuleEvaluation (native:2)`.
  - **Recorded:** [`flows/oracle/smoke`](../../conformance/flows/oracle/smoke)
    run `fail`, oracle run 40649910412.
- **OR-12/b** · oracle-observed · A failure handler's plain result becomes the
  failed root's result.
  - **Given:** probe `fail_handler`.
  - **Expect:** root `status: failure`, result `{"saw":"boom","handled":true}`;
    the handler job has kind `preview`, args `{"error": <a's error object>}`
    and `parent_module: "a"`; step `b` is `not_run`.
- **OR-12/c** · oracle-observed · Error subclass with an own property.
  - **Given:** probe `fail_mid`.
  - **Expect:** root `status: failure`, result
    `{"error":{"name":"MyErr","message":"boom","stack":"<stack>","extra":{"code":42},"step_id":"a"}}`.
- **OR-12/d** · oracle-observed · Missing variable reference.
  - **Given:** probe `args_vs_received` with the variable `f/wl_probe/v` absent.
  - **Expect:** step `a` and the root fail with
    ```json
    {"error":{"name":"ExecutionErr","message":"execution error:\nNot found: Variable f/wl_probe/v not found for `s`: Not found: Variable f/wl_probe/v not found","step_id":"a"}}
    ```

<a id="or-13"></a>
### OR-13 · The oracle stores JSON as Postgres `jsonb`, and that is observable

**Scope:** core · **Finding:** [OR-13](oracle.md#or-13)

- **OR-13/a** · oracle-observed · Stored root args are in jsonb key order.
  - **Given:** probe `key_order` with args `{"zz":1,"a":2,"mmm":3}`.
  - **Expect:** the stored root args, read back with `get_args`, have keys in
    the order `a`, `zz`, `mmm` (`{"a":2,"zz":1,"mmm":3}`).

<a id="or-14"></a>
### OR-14 · Normalisation rules

**Scope:** core · **Finding:** [OR-14](oracle.md#or-14)

- **OR-14/a** · disputed · Self-consistency of normalisation.
  - **Given:** the smoke fixture run twice on the same oracle (observed: `ok` ×3,
    `fail` ×2, `effects` ×2, `loop_seq_simple` ×2).
  - **Expect:** byte-identical normalised observations.
  - **Contested:** the equality was observed; the placeholder and path scheme
    it relies on is disputed (as drafted: `<job:PATH>` over the drafted paths;
    correction: the encoding must be injective, [Q-OR-17](oracle.md#q-or-17)).
  - **Recorded:** every recorded fixture under
    [`conformance/flows/`](../../conformance/flows) is repeated in its
    session (`--repeat 2`, the smoke runs first in oracle run 40649910412)
    and its normalised observations were equal, under the typed site paths
    of design record 0011.
- **OR-14/b** · disputed · Harness self-test: a returned job id is rewritten.
  - **Given:** step `a` returns `process.env.WM_JOB_ID`.
  - **Expect (as drafted):** `"<job:a>"`.
  - **Contested:** the exact placeholder spelling depends on the identity
    encoding still to be chosen ([Q-OR-17](oracle.md#q-or-17)).

<a id="or-15"></a>
### OR-15 · Fixture format (language-neutral, shared by oracle record and windmill-lite replay)

**Scope:** core · **Finding:** [OR-15](oracle.md#or-15)

- **OR-15/a** · source-derived · Harness self-test: `check` passes on a correct
  expectation.
  - **Given:** the [OR-20](#or-20) directory with a hand-written `runs/ok.yaml`
    whose `expect` equals the observation of [OR-11/a](#or-11).
  - **Expect:** `check` passes.
- **OR-15/b** · source-derived · Harness self-test: `check` reports a JSON-pointer
  diff.
  - **Given:** the same, with `tag` changed to `"ho"` in `expect`.
  - **Expect:** `check` fails with `/steps/b/args/tag: "hi" != "ho"`.

<a id="or-16"></a>
### OR-16 · Observing side effects

**Scope:** core · **Finding:** [OR-16](oracle.md#or-16)

- **OR-16/a** · oracle-observed · Effect order through the sink.
  - **Given:** probe `effects`: steps `a` then `b`, each awaiting a POST of
    `{step}` to `WL_EFFECT_SINK`; run twice.
  - **Expect:** each run records `[{"step":"a"},{"step":"b"}]`, separately per
    run, on the oracle and on windmill-lite. (Run 1 keyed runs by
    `WM_ROOT_FLOW_JOB_ID`; the current body posts `root`, `job` and `step`.)
- **OR-16/b** · oracle-observed · EE · `delete_after_use` blanks nothing on CE.
  - **Given:** probe `delete_after_use` with args `{"x":7}`.
  - **Expect:** root result `{"p":{"secret":7}}`; after completion, step `a`
    still has args `{"x":7}`, result `{"secret":7}` and its log line. This CE
    observation is the default expectation.
- **OR-16/c** · source-derived · Harness self-test: foreign effects are rejected.
  - **Given:** an effect whose `root` differs from the root job id the harness
    submitted.
  - **Expect:** `{"error":"foreign_effect"}`; the effect is not attributed to
    the run.

<a id="or-17"></a>
### OR-17 · Timing capture (sanity only; the oracle is not the latency baseline)

**Scope:** later · **Finding:** [OR-17](oracle.md#or-17)

- **OR-17/a** · oracle-observed · A sleep on `a` delays `b`.
  - **Given:** probe `sleep`: step `a` with `sleep: {type: static, value: 2}`,
    then step `b`.
  - **Expect:** `timings.b.started_at − timings.a.completed_at ≥ 2000 ms` on
    both systems (observed 2021.971 ms). A bound assertion; never measured
    from `created_at`.

<a id="or-18"></a>
### OR-18 · Instance lifecycle and hygiene

**Scope:** core · **Finding:** [OR-18](oracle.md#or-18)

- **OR-18/a** · oracle-observed · Teardown leaves nothing.
  - **Given:** a session ended with `docker compose -p wl-oracle down -v`.
  - **Expect:** `docker volume ls --filter label=com.docker.compose.project=wl-oracle`
    and `docker ps -a --filter …` (for the same project) are both empty.

<a id="or-19"></a>
### OR-19 · CI shape

**Scope:** core · **Finding:** [OR-19](oracle.md#or-19)

- **OR-19/a** · oracle-observed · Oracle CI on a clean checkout.
  - **Given:** a clean checkout; the oracle CI job runs the preflight, the
    minimal compose and `record` for all fixtures.
  - **Expect:** every normalised observation projection equals the committed
    one; differences in dates, timings or other provenance do not fail the job.
  - **Recorded:** the repository's oracle workflow since E2: for example
    oracle run 40706584607 reported all 131 committed runs `unchanged` with
    the metadata current, while timings and dates differed.
  - **Correction 2026-10-28:** this case was `source-derived`, pending the
    first CI recording ([Q-OR-2](oracle.md#q-or-2)); the expectation is
    unchanged.

<a id="or-20"></a>
### OR-20 · Smoke fixture: a two-step Bun flow with one input transform

**Scope:** core · **Finding:** [OR-20](oracle.md#or-20)

The synthetic fixture (authored files; commit the CLI's generated metadata and
locks alongside them, [OR-8](oracle.md#or-8)):

`windmill/wmill.yaml`:

```yaml
defaultTs: bun
includes: ["f/**"]
```

`windmill/f/wl_smoke/folder.meta.yaml` is generated by
`wmill folder add-missing --yes`.

`windmill/f/wl_smoke/double.ts`:

```ts
export async function main(x: number) {
  return { doubled: x * 2, seen: x };
}
```

`windmill/f/wl_smoke/two_step.flow/b.ts`:

```ts
export async function main(n: number, tag: string) {
  if (n > 100) throw new Error(`too big: ${n}`);
  return `${tag}:${n}`;
}
```

`windmill/f/wl_smoke/two_step.flow/flow.yaml`:

```yaml
summary: wl smoke two-step
value:
  modules:
    - id: a
      value:
        type: script
        path: f/wl_smoke/double
        input_transforms:
          x: { type: javascript, expr: flow_input.x }
    - id: b
      value:
        type: rawscript
        language: bun
        content: '!inline b.ts'
        input_transforms:
          n: { type: javascript, expr: results.a.doubled }
          tag: { type: static, value: hi }
schema:
  $schema: https://json-schema.org/draft/2020-12/schema
  type: object
  properties: { x: { type: number } }
  required: [x]
```

- **OR-20/a** · oracle-observed · Run `ok`.
  - **Given:** `record` on the oracle, then `check` on windmill-lite, args
    `{"x":3}`.
  - **Expect:** the observation of [OR-11/a](#or-11): root `success`, result
    `"hi:6"`, `a` args `{"x":3}` result `{"doubled":6,"seen":3}`, `b` args
    `{"n":6,"tag":"hi"}` result `"hi:6"`. Stable over 3 oracle runs.
  - **Recorded:** [`flows/oracle/smoke`](../../conformance/flows/oracle/smoke)
    run `ok`, oracle run 40649910412; `unchanged` in every later session.
- **OR-20/b** · oracle-observed · Run `fail`.
  - **Given:** args `{"x":60}`.
  - **Expect:** the observation of [OR-12/a](#or-12): root `failure` with the
    plain-`Error` object (`step_id:"b"`), `b` holding the same object, `a`
    result `{"doubled":120,"seen":60}`. Stable over 2 oracle runs.
  - **Recorded:** [`flows/oracle/smoke`](../../conformance/flows/oracle/smoke)
    run `fail`, oracle run 40649910412; `unchanged` in every later session.

<a id="or-21"></a>
### OR-21 · Edition differences that matter to a CE oracle

**Scope:** EE · **Finding:** [OR-21](oracle.md#or-21)

- **OR-21/a** · source-derived · `delete_after_use` inside a subflow.
  - **Given:** probe `ee/subflow_delete_after_use`: a flow calling a subflow
    whose step has `delete_after_use: true`.
  - **Expect:** on CE, record whether the nested job's args and result are
    blanked after the parent completes; windmill-lite matches CE by default.
    Pending ([Q-OR-18](oracle.md#q-or-18)).

<a id="or-22"></a>
### OR-22 · Arguments as stored versus as received

**Scope:** core · **Finding:** [OR-22](oracle.md#or-22)

- **OR-22/a** · oracle-observed · A static `$var:` reference is stored unresolved.
  - **Given:** probe `args_vs_received`: variable `f/wl_probe/v` = `"secret-ish"`
    created **after** the push; step `a` with input
    `s: {type: static, value: "$var:f/wl_probe/v"}` returns `s`.
  - **Expect:** `steps.a.args` is `{"s":"$var:f/wl_probe/v"}`; `steps.a.result`
    is `"secret-ish"`.

<a id="or-23"></a>
### OR-23 · Top-level `flow_input` key order inside transforms is random per run

**Scope:** core · **Finding:** [OR-23](oracle.md#or-23)

- **OR-23/a** · oracle-observed · Random transform order, stable received order.
  - **Given:** probe `key_order` with args `{"zz":1,"a":2,"mmm":3}`; fixture
    option `unordered: ["/steps/a0/args/fi_keys"]`.
  - **Expect:** `steps.a0.args.fi_keys` is a permutation of `["a","mmm","zz"]`
    (8 runs gave 5 distinct orders); `steps.a0.result.fi_whole_keys` is exactly
    `["a","zz","mmm"]`.
- **OR-23/b** · source-derived · Harness self-test: `unordered` is multiset
  equality.
  - **Given:** expected `["a","mmm","zz"]` at an `unordered` path; actual
    `["a","a","mmm","zz"]`.
  - **Expect:** the comparison fails.

<a id="or-24"></a>
### OR-24 · Key order at step boundaries: a provenance model

**Scope:** core · **Finding:** [OR-24](oracle.md#or-24)

- **OR-24/a** · oracle-observed · An older result is in jsonb order, the
  immediate predecessor in insertion order.
  - **Given:** probe `key_order2`: `a` returns `{"bbb":1,"a":2,"cc":3}`, then `m`
    returns `{"zzz":1,"y":2}`, then `b` with transforms
    `r_keys = Object.keys(results.a)` and `p_keys = Object.keys(previous_result)`.
  - **Expect:** `b.result == {"r_keys":["a","cc","bbb"],"p_keys":["zzz","y"]}`,
    with `key_order: strict` on both arrays.
- **OR-24/b** · source-derived · Integer-index keys enumerate first.
  - **Given:** a step returning `{"2":1,"a":2,"10":3}`; the next step reports
    `Object.keys(results.<id>)` from its transform and the keys of the object
    it receives.
  - **Expect:** the received object's keys are `["2","10","a"]` (local
    JavaScript check); the transform-side array is recorded.
    [Q-OR-12](oracle.md#q-or-12).
- **OR-24/c** · source-derived · Retry restores the predecessor from storage.
  - **Given:** `a` returns `{"bbb":1,"a":2,"cc":3}`; `b` has one retry and its
    body throws; `b`'s `skip_if` returns false while
    `Object.keys(previous_result)[0]` is `bbb`, and otherwise throws a
    diagnostic containing the whole key array.
  - **Expect:** per source, on the retry the diagnostic contains
    `["a","cc","bbb"]` (the stored predecessor's order). [Q-OR-11](oracle.md#q-or-11) (a).
- **OR-24/d** · source-derived · A loop aggregate comes from storage.
  - **Given:** a loop whose iterations return objects; the next step reports
    `Object.keys(previous_result[0])`.
  - **Expect:** per source, jsonb key order. [Q-OR-11](oracle.md#q-or-11) (b).
- **OR-24/e** · source-derived · Preprocessor-built args keep their order.
  - **Given:** a Bun script with a `preprocessor` that builds `{bbb,a,cc}` and
    reports `Object.keys` of the argument in `main`.
  - **Expect:** per source, the preprocessor's insertion order
    `["bbb","a","cc"]` (no re-sort). [Q-OR-11](oracle.md#q-or-11) (c).

<a id="or-25"></a>
### OR-25 · An empty chosen `branchone` branch yields the JSON string `"{}"`

**Scope:** core · **Finding:** [OR-25](oracle.md#or-25)

- **OR-25/a** · oracle-observed · The next step sees strings.
  - **Given:** probe `branchone_empty` with args `{}`.
  - **Expect:** root result `{"p":"{}","r":"{}"}` (both values are strings);
    module `br` is `Success` with a nil `job` and
    `branch_chosen: {"type":"branch","branch":0}`.

<a id="or-26"></a>
### OR-26 · Empty bodies in `branchall` and `forloopflow`

**Scope:** core · **Finding:** [OR-26](oracle.md#or-26)

- **OR-26/a** · oracle-observed · An empty branch contributes `{}`.
  - **Given:** probes `branchall_seq` and `branchall_par`.
  - **Expect:** result `[0,{},2]` for both; three `flowpreview` children, the
    empty one with result `{}`.
- **OR-26/b** · oracle-observed · An empty loop yields `[]`.
  - **Given:** probe `loop_empty`.
  - **Expect:** root result `{"p":[],"r":[]}`.

<a id="or-27"></a>
### OR-27 · A NUL character in a Bun result fails the step at storage time

**Scope:** core · **Finding:** [OR-27](oracle.md#or-27)

- **OR-27/a** · oracle-observed · A top-level string with U+0000.
  - **Given:** probe `nul`: `a` returns `"x\u0000y"`, then `b`.
  - **Expect:** `a` and the root have `status: failure` with result
    ```json
    {"error":{"name":"InternalErr","message":"Internal: Could not add completed job <job:PATH>: error returned from database: unsupported Unicode escape sequence<loc>"}}
    ```
    (no `step_id`; `PATH` is the failing step's path); `b` is `not_run`. The
    raw message ended in ` @jobs.rs:1133:22`. The duration (about 30 s) is not
    compared.
- **OR-27/b** · source-derived · Other NUL positions and languages.
  - **Given:** variants returning `{"k\u0000":1}` and `{"a":["x\u0000"]}`, a flow
    arg `"x\u0000"`, and a Python step returning the same.
  - **Expect:** record the outcomes; no expectation is set
    ([Q-OR-6](oracle.md#q-or-6)).

<a id="or-28"></a>
### OR-28 · Job kinds the walker meets

**Scope:** core · **Finding:** [OR-28](oracle.md#or-28)

- **OR-28/a** · source-derived · Harness self-test: walker over recorded captures.
  - **Given:** the raw API captures committed since E2
    (`internal/oracle/testdata/captures/`; run 1's captures were not
    retained, [Q-OR-16](oracle.md#q-or-16)).
  - **Expect:** every child of kind `flownode`, `flowpreview` or `flow` (a
    subflow by path) has a `flow_status`; every `flowscript`, `script`,
    `identity` or `preview` leaf has none (an inline failure module or
    preprocessor is a `preview` leaf, a by-path one a `script` leaf); and
    replaying the captures through the walker and the normaliser reproduces
    the committed recordings exactly.
  - **Correction 2026-10-28:** the case named captures "from a future
    recording run" and only the kinds of oracle run 1; the captures now
    exist, and the recordings added `flow` and `script` jobs
    ([OR-28](oracle.md#or-28)).

<a id="or-29"></a>
### OR-29 · Environment seen by a Bun step in a flow (CE, standalone)

**Scope:** core · **Finding:** [OR-29](oracle.md#or-29)

- **OR-29/a** · oracle-observed · Environment of step `a` in `f/wl_probe/env`.
  - **Given:** probe `env`.
  - **Expect:** after masking ids, timestamps and the username:
    `{"bun":"1.3.10","env":{"WM_FLOW_STEP_ID":"a","WM_JOB_PATH":"f/wl_probe/env/a","WM_FLOW_PATH":"f/wl_probe/env","WM_STATE_PATH":"f/wl_probe/env/a_user","WM_WORKSPACE":"<ws>","WM_PERMISSIONED_AS":"u/admin",…}}`.

<a id="or-30"></a>
### OR-30 · Disk and pull behaviour of the oracle image (harness preflight)

**Scope:** core · **Finding:** [OR-30](oracle.md#or-30)

- **OR-30/a** · source-derived · Harness self-test: refuse a pull without the
  reserve.
  - **Given:** free 9 GiB, need 4.9 GiB.
  - **Expect:** no pull;
    `{"error":"insufficient_disk","free_gib":9.0,"need_gib":4.9,"reserve_gib":5}`.
- **OR-30/b** · source-derived · Harness self-test: abort below the floor.
  - **Given:** a watchdog reading of 4.99 GiB free during a pull.
  - **Expect:** the pull aborts, with no automatic resume.
- **OR-30/c** · source-derived · Harness self-test: no oracle after a tight pull.
  - **Given:** settled free space of 4.8 GiB after a completed pull.
  - **Expect:** the oracle is not started and the image is removed.

<a id="or-31"></a>
### OR-31 · `wmill sync push` is destructive: seed after pushing, only into harness-owned workspaces

**Scope:** core · **Finding:** [OR-31](oracle.md#or-31)

- **OR-31/a** · oracle-observed · A variable seeded before the push is removed.
  - **Given:** variable `f/wl_probe/v` created, then a push of a tree that does
    not contain it, then probe `args_vs_received`.
  - **Expect:** the probe fails with `Variable f/wl_probe/v not found`
    ([OR-12/d](#or-12)); after re-seeding the variable post-push, it passes.
- **OR-31/b** · source-derived · Harness integration test around a push.
  - **Given:** create a variable, push a tree without it, then apply
    `setup.yaml` post-push.
  - **Expect:** after the push, `GET /api/w/<ws>/variables/get/<path>` returns
    404; after `setup.yaml`, 200. Pending ([Q-OR-18](oracle.md#q-or-18)).

<a id="or-v1"></a>
### OR-V1 · Full argument and result capture needs dedicated endpoints

**Scope:** core · **Finding:** [OR-V1](oracle.md#or-v1)

- **OR-V1/a** · oracle-observed · Large payloads.
  - **Given:** probe `big_payload`: step `a` returns 300,000 deterministic,
    poorly compressible characters (306,474 bytes of JSON); `b` receives them
    (306,481 bytes of args).
  - **Expect:** generic `jobs_u/get` gives `"result":"WINDMILL_TOO_BIG"` for `a`
    and `{"reason":"WINDMILL_TOO_BIG"}` as `b`'s args; the normalised steps
    contain the complete original object in both places, taken from
    `get_result_maybe` and `get_args`.
- **OR-V1/b** · oracle-observed · A literal sentinel string is kept.
  - **Given:** step `c` of `big_payload` returns `"WINDMILL_TOO_BIG"`.
  - **Expect:** `steps.c.result` and the root result are `"WINDMILL_TOO_BIG"`,
    unchanged.

<a id="or-v2"></a>
### OR-V2 · Distinguish skipped, failed, canceled and never run

**Scope:** core · **Finding:** [OR-V2](oracle.md#or-v2)

- **OR-V2/a** · oracle-observed · An early stop with `skip_if_stopped: true`.
  - **Given:** probe `stop_skip`.
  - **Expect:** poll `success:false`, job `success:true, is_skipped:true`;
    normalised root `{"status":"skipped","result":1,…}`, not failure;
    `steps.a.skipped` is `true`; `steps.b` is `{"status":"not_run","ran":false}`.
- **OR-V2/b** · oracle-observed · An early stop with `skip_if_stopped: false`.
  - **Given:** probe `stop_plain`.
  - **Expect:** root `status: success`, result `1`; `b` is `not_run`.
- **OR-V2/c** · oracle-observed · An empty loop is not "not run".
  - **Given:** probe `loop_empty`.
  - **Expect:** `steps.l` is `{"status":"success","ran":false,"result":[]}`,
    distinct from `not_run`.
- **OR-V2/d** · oracle-observed · A `skip_if` step runs as an identity job.
  - **Given:** probe `skip_if` with `{"skip":true}`, then with `{"skip":false}`.
  - **Expect:** `{"skip":true}`: root result `{"p":"a0","r":"a0"}`; step `a` has
    module `skipped:true` and an `identity` job with `success:true`,
    `is_skipped:false`, args `{"previous_result":"a0"}`, result `"a0"`.
    `{"skip":false}`: root result `{"p":"ran a","r":"ran a"}`.
- **OR-V2/e** · oracle-observed · A throwing `branchone` predicate.
  - **Given:** step `a`, then a `branchone` `br` whose first predicate throws
    (recorded with `expr: "nope"`, an undefined name; a predicate such as
    `(() => { throw new Error('p') })()` takes the same path).
  - **Expect:** the module is `{"status":"failure","ran":false}` with the flow's
    error preserved, never an empty success; no branch body runs; the root
    is `failure` with
    `{"error":{"name":"Anyhow","message":"Error: QuickJS evaluation error: Error: nope is not defined\n    at <anonymous> (eval_script:1:35)\n    at <eval> (eval_script:1:42)\n<loc>"}}`
    and no `step_id`.
  - **Recorded:** [`flows/je/branches`](../../conformance/flows/je/branches)
    run `je31-a`, oracle run 40687124625.
  - **Correction 2026-10-28:** the case left the root shape to be recorded
    ([Q-OR-8](oracle.md#q-or-8)); E3a recorded it.

<a id="or-v3"></a>
### OR-V3 · Normalisation must preserve negative comparisons

**Scope:** core · **Finding:** [OR-V3](oracle.md#or-v3)

All cases are harness self-tests. They are carried as disputed because the
finding's identity encoding is disputed ([Q-OR-17](oracle.md#q-or-17)); the
expectations below are not themselves contested unless stated.

- **OR-V3/a** · disputed · Unknown UUIDs are preserved.
  - **Given:** expected result `"11111111-1111-4111-8111-111111111111"`, actual
    `"22222222-2222-4222-8222-222222222222"`, no mask declared.
  - **Expect:** the comparison fails.
- **OR-V3/b** · disputed · User data that looks like an error is preserved.
  - **Given:** a successful step returns `{"error":{"stack":"user-data"},"tag":"keep"}`.
  - **Expect:** both `stack` and `tag` survive normalisation unchanged.
- **OR-V3/c** · disputed · A walked job id maps to a stable placeholder.
  - **Given:** two identical executions whose step job UUIDs differ.
  - **Expect:** both normalise to the same placeholder for that step.
  - **Contested:** the placeholder's spelling; as drafted `<job:PATH>` over the
    drafted paths, which the correction rejects as non-injective.
- **OR-V3/d** · disputed · Handler-returned data is not masked as a stack.
  - **Given:** a failing step triggers a failure handler that succeeds without
    recovery and returns `{"error":{"stack":"user-A"},"handled":true}`; the
    actual run returns `"user-B"` in that field.
  - **Expect:** the comparison fails (the failed root keeps the handler's data).

<a id="or-v4"></a>
### OR-V4 · Observe key order before the next job's argument boundary

**Scope:** core · **Finding:** [OR-V4](oracle.md#or-v4)

- **OR-V4/a** · oracle-observed · Transform-computed key arrays.
  - **Given:** probe `key_order` with args `{"zz":1,"a":2,"mmm":3}`.
  - **Expect:** `b.result` is
    `{"p_keys":["bbb","a","cc"],"r_keys":["bbb","a","cc"],"p_whole_keys":["a","cc","bbb"],"r_whole_keys":["a","cc","bbb"]}`,
    stable over 8 runs.
- **OR-V4/b** · source-derived · Harness self-test: the normaliser leaves arrays
  alone.
  - **Given:** the observation of OR-V4/a.
  - **Expect:** object-key sorting for comparison does not reorder any of the
    four arrays.

<a id="or-v5"></a>
### OR-V5 · Stable CI observations and volatile provenance are separate

**Scope:** core · **Finding:** [OR-V5](oracle.md#or-v5)

- **OR-V5/a** · source-derived · Harness self-test: provenance does not fail a
  semantic comparison.
  - **Given:** two captures with identical normalised
    `{"status":"success","result":"hi:6",…}` but different dates and durations;
    then a capture whose result is `"hi:7"`.
  - **Expect:** the first pair passes; the `"hi:7"` capture fails at `/result`.
- **OR-V5/b** · source-derived · Harness self-test: pinned identity is checked
  separately.
  - **Given:** a recording whose oracle identity does not match the pin.
  - **Expect:** the preflight fails, independently of the semantic comparison.

<a id="or-v6"></a>
### OR-V6 · Use the outer run identity for effects

**Scope:** core · **Finding:** [OR-V6](oracle.md#or-v6)

- **OR-V6/a** · oracle-observed · Effects from nested scopes group under the outer
  run.
  - **Given:** root R invokes named subflow S and has two parallel loop
    iterations I0 and I1; their leaf jobs POST effects; a concurrent outer run
    T also posts.
  - **Expect:** all effects of S, I0 and I1 are collected under R even though
    their `WM_ROOT_FLOW_JOB_ID` values are S, I0 or I1; T's effects stay
    separate. Membership is asserted; order only where the fixture guarantees
    it.
  - **Recorded:** [`flows/stub/scopes`](../../conformance/flows/stub/scopes)
    runs `r` and `t` (submitted together) and `alone`, oracle run
    40689406044: each effect's `root` is its own run's root job, its
    `root_flow` and `flow` are the subflow's job inside S and the
    iteration's job inside I0 and I1.
  - **Correction 2026-10-28:** this case was `source-derived`, pending
    [Q-OR-10](oracle.md#q-or-10); the expectation is unchanged.

<a id="or-v7"></a>
### OR-V7 · Generated virtual steps require an explicit capture policy

**Scope:** core · **Finding:** [OR-V7](oracle.md#or-v7)

- **OR-V7/a** · source-derived · A trailing sleep.
  - **Given:** a flow ending with step `a` that has `sleep: 2` (the live probe
    of [Q-OR-9](oracle.md#q-or-9) uses `sleep: {type: static, value: 1}`).
  - **Expect:** the run still waits and preserves the final value; the raw
    topology may include a generated `a-v`; the semantic comparison does not
    report a missing user step because windmill-lite has no synthetic `a-v`
    execution. Pending ([Q-OR-9](oracle.md#q-or-9)).
- **OR-V7/b** · source-derived · Harness self-test: a user-authored `a-v` stays.
  - **Given:** a fixture whose sync tree contains a real module named `a-v`.
  - **Expect:** that module remains in the projection and is compared.

<a id="or-v8"></a>
### OR-V8 · Semantic identities must distinguish special slots and literal module ids

**Scope:** core · **Finding:** [OR-V8](oracle.md#or-v8)

- **OR-V8/a** · disputed · An ordinary module named `failure`.
  - **Given:** an ordinary step `failure` that throws, and a failure handler
    named `recover` that returns `{"handled":true}`.
  - **Expect (correction):** both executions and their different results
    survive the projection under distinct keys and distinct placeholders;
    altering either job's result, or swapping a job reference, fails the
    comparison.
  - **Contested:** under the drafted encoding both map to `failure`
    ([Q-OR-17](oracle.md#q-or-17)).
- **OR-V8/b** · disputed · An ordinary module named `preprocessor`.
  - **Given:** an ordinary step `preprocessor` and a separately named actual
    preprocessor.
  - **Expect (correction):** two distinct records and placeholders.
  - **Contested:** as for OR-V8/a.
- **OR-V8/c** · disputed · Literal ids that differ only by escaping.
  - **Given:** if Windmill admits them, modules `a/b` and `a%2Fb`.
  - **Expect (correction):** they stay distinct; if Windmill rejects such ids,
    record the rejection.
  - **Contested:** the drafted escaping maps both to `a%2Fb`.

<a id="or-v9"></a>
### OR-V9 · A probe recipe must specify the actual transform scope

**Scope:** core · **Finding:** [OR-V9](oracle.md#or-v9)

- **OR-V9/a** · disputed · Reconstructed `loop_seq_multi`.
  - **Given:** a sequential for-loop `l` over `["a","b"]`; body `x1` with
    `v = flow_input.iter.value` returning `v+"1"`, then `x2` with
    `p = previous_result` returning `p+"2"`.
  - **Expect:** result `["a12","b12"]`; the iterations are two-step child flows
    (`flownode`).
  - **Contested:** the recorded recipe used a bare `iter.value` next to this
    result; the correction says no bare `iter` binding exists, so the recipe
    and the result do not match as written ([Q-OR-16](oracle.md#q-or-16)).
- **OR-V9/b** · disputed · Literal bare-`iter` variant.
  - **Given:** the same loop with `v = iter.value`.
  - **Expect (correction):** an evaluation failure; capture its exact wrapper,
    do not invent it.
  - **Contested:** as recorded, the recipe was reported as succeeding.
- **OR-V9/c** · disputed · `branchone` with explicit bindings.
  - **Given:** `br` with predicates `flow_input.n > 10` → `big` returns `'big'`,
    `flow_input.n > 0` → `pos` returns `'pos'`, default `def` returns
    `'default'`; args `{"n":5}`, `{"n":50}`, `{"n":-1}`.
  - **Expect:** `"pos"` (branch 1), `"big"` (branch 0), `"default"` (type
    default), reproducing the recorded results.
  - **Contested:** the recorded recipe used undeclared shorthand `n`.
- **OR-V9/d** · disputed · `loop_par` with an explicit transform.
  - **Given:** a parallel for-loop over `[1,2,3]`; body `x` with
    `v = flow_input.iter.value` returning `v*10`.
  - **Expect:** `[10,20,30]`; `flowpreview` iterations, reproducing the
    recorded result.
  - **Contested:** the recorded recipe omitted the transform for `v`.

<a id="licensing"></a>
## Licensing and the clean room

Findings: [licensing.md](licensing.md).

<a id="licensing-notation"></a>
### Notation

The cases in this area check windmill-lite's own repository, not Windmill:
its manifests and its Go OpenFlow types. They are derived from Windmill's
licence files and the pinned OpenFlow schema. No oracle recording applies to
them.

<a id="lic-1"></a>
### LIC-1 · Licence map and the build-team boundary

**Scope:** core · **Finding:** [LIC-1](licensing.md#lic-1)

- **LIC-1/a** · source-derived · The manifest guard accepts the allowed Windmill inputs.
  - **Given:** windmill-lite's Go module and package manifests, plus any
    vendored Windmill files. The only Windmill-derived entries are allowed
    inputs: `openflow.openapi.yaml`, `backend/windmill-api/openapi.yaml`
    (vendored), `go-client`, `python-client`, `powershell-client`,
    `typescript-client`.
  - **Expect:** the guard passes. The vendored
    `backend/windmill-api/openapi.yaml` passes even though its path is under
    `backend/`. Unrelated third-party dependencies are outside the check.
- **LIC-1/b** · source-derived · The manifest guard rejects excluded Windmill inputs.
  - **Given:** a manifest or vendored file that references a
    `windmill-parser-wasm-*` package, or any `backend/` path other than
    `backend/windmill-api/openapi.yaml`.
  - **Expect:** the guard fails.

<a id="lic-3"></a>
### LIC-3 · No usable Go OpenFlow type set exists

**Scope:** core · **Finding:** [LIC-3](licensing.md#lic-3)

- **LIC-3/a** · source-derived · Schema coverage of windmill-lite's Go OpenFlow types.
  - **Given:** the vendored `openflow.openapi.yaml` at `v1.767.0`, and
    windmill-lite's Go OpenFlow types with their lists of mapped and
    explicitly ignored properties.
  - **Expect:** for each of `components.schemas.FlowValue`, `FlowModule`,
    `RawScript`, `PathScript`, `PathFlow`, `ForloopFlow`, `WhileloopFlow`,
    `BranchOne`, `BranchAll`, `Identity`, `Retry`, `StopAfterIf` and
    `InputTransform`, the set of property names in the schema equals the Go
    side's mapped ∪ ignored set. A schema property that is neither mapped nor
    ignored fails the test.
