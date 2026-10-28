# OpenFlow model and the sync folder

This area describes how Windmill parses, validates, stores and prepares an
OpenFlow flow value, and how the `wmill sync` folder lays out scripts, flows,
resources, variables and configuration on disk. It closes with prior art from
windmill-ts. Step and flow execution hand off to [execution.md](execution.md)
and [control-flow.md](control-flow.md), expressions and `$var:`/`$res:`
resolution to [expressions.md](expressions.md), language runtimes to
[languages.md](languages.md), client shims to [client-api.md](client-api.md),
oracle procedures to [oracle.md](oracle.md), and the licence map to
[licensing.md](licensing.md).

**Pinned version:** Windmill `v1.767.0` (commit `6de4ec0f665ad856002495c4d86269105ec28ad7`).
Source pointers are `path:line @ v1.767.0` relative to the Windmill repository
root. Status, scope and confidence tags are defined in the [index](README.md#legend).

**ID prefixes in this file:** `OF-` OpenFlow model and validation; `SY-`
sync-folder layout; `PA-` prior art (windmill-ts). `OF-Vn` and `SY-Vn` findings
were added during validation. Licence findings (`LIC-`) are in
[licensing.md](licensing.md).

Conventions used below:

- A pointer without an explicit pin is at v1.767.0. Pointers into windmill-ts
  carry `@ af2b1e325320`.
- **Schema** means the Apache-2.0 `openflow.openapi.yaml`. **Effective
  decoder** means Windmill's typed model in `backend/windmill-types/src/flows.rs`,
  which decides what the server accepts.
- **Envelope** is a module object `{id, value, ...}` (`FlowModule`); its
  **value** is the `value` field (`FlowModuleValue`), tagged by `type`.
- **Raw**, **full** and **lite** are the three stored forms of a flow value
  (OF-3).
- **Deployed run** means a run by path after the version's dependency job
  succeeded; **preview run** means the same JSON sent to
  `POST /jobs/run/preview_flow`, which executes raw module values.
- Fixture notation (including `ERR(M)`) is in
  [conformance.md](conformance.md#openflow-notation).

## Summary for the design

1. **Decode with the effective rules, not the schema** ([OF-1](#of-1),
   [OF-5](#of-5), [OF-10](#of-10), [OF-V1](#of-v1), [OF-V2](#of-v2)). The
   schema is the field inventory. Acceptance includes a required
   `stop_after_if.skip_if_stopped`, null-versus-omission differences, type
   checks on recognised fields the selected kind does not use, the
   `input_transform` and `custom_debounce_key` aliases, and integer-restricted
   `timeout`/`parallelism` objects. No usable Go type set exists
   ([LIC-3](licensing.md#lic-3)).
2. **A flow value exists in three forms: raw, full and lite** ([OF-3](#of-3),
   [SY-6](#sy-6)). A successful deploy materialises skip-flag defaults, may
   switch `bun`↔`bunnative`, and generates locks; lite preparation adds virtual
   trailing steps ([OF-17](#of-17)). Kept `script`/`flow`/`identity` values
   differ from their canonical form only harmlessly. A pulled `flow.yaml` is the
   full form, which is not canonical for kept values.
3. **Which form a run reads is a two-phase choice** ([OF-V5](#of-v5)). Enqueue
   picks the form that builds the initial status list; the worker
   independently looks up lite first. A status list built from the full form
   does not acquire a trailing virtual slot because the worker later fetches
   lite. OF-3's one-stage sentence is disputed ([Q-OF-11](#q-of-11)), and the
   `[a, a-v]` virtual-step fixture holds for an initial-lite status.
4. **Skip flags** ([OF-7](#of-7)): an absent for-loop `skip_failures` or
   `branchall` branch `skip_failure` behaves as `true` after a successful deploy
   and as `false` in preview runs, during the deploy race and after a failed
   lock. While-loops are unaffected. The fixtures are source-derived and await
   the oracle ([Q-OF-1](#q-of-1)).
5. **Server validation is minimal** ([OF-2](#of-2), [OF-18](#of-18)): a typed
   decode with two error prefixes (`Invalid flow value:` for the outer decode,
   `Module '<id>':` per module value), workspace-path step references, and a
   non-zero exponential retry base. There is no id, schema or existence check,
   and the flow `schema` is never used to fill defaults.
6. **Flow-level corrections** ([OF-16](#of-16), [OF-V3](#of-v3),
   [OF-V4](#of-v4)): `early_return` is a step id for synchronous responses, not
   JavaScript, and does not stop the flow; `pass_flow_input_directly` works only
   on the module envelope; `same_worker` rejects a step that would start more
   than one child job.
7. **A step's language is static** ([OF-13](#of-13), [SY-3](#sy-3),
   [SY-V3](#sy-v3)). Rawscripts carry `language`. Path scripts take it from the
   file extension plus `defaultTs`, and are `bunnative` if and only if a header
   `//native` annotation is present (position- and case-sensitive). CLI codebase
   bundling is Bun-only, first match wins ([SY-8](#sy-8)).
8. **Locks** ([OF-15](#of-15), [OF-V6](#of-v6), [SY-5](#sy-5), [SY-7](#sy-7),
   [SY-V1](#sy-v1)). Inline `!inline` paths are relative to the flow folder;
   script lock references are relative to the sync root and need the exact
   `!inline ` prefix; a flow lock reference is resolved only when the content
   is itself a directive; a missing lock file means `""`. On deploy a selected partial relock always regenerates and an
   unselected module is untouched; an empty lock body can gain a
   workspace-dependency header; a failed lock leaves the saved value unchanged.
   Preserve supplied lock text. Offline executability is a per-language policy
   ([Q-OF-5](#q-of-5)).
9. **Files and filters** ([SY-1](#sy-1), [SY-13](#sy-13), [SY-V2](#sy-v2),
   [SY-V4](#sy-v4), [SY-16](#sy-16) to [SY-19](#sy-19)): an absent
   `includes` differs from `includes: []`, `extraIncludes` only narrows, and
   several special files bypass the globs. Globs are minimatch 10 with
   default options over the root-relative path. The filter tests each file,
   but a passing file pushes its whole item, and a new script is selected
   only by its content file. The walk classifies files by a fixed list and
   ignores the rest, except that a bare `.sql` or a stray `.js` fails the
   push. A UTF-8 byte order mark is stripped. No `wmill.yaml` means
   defaults, not an error; resource and variable files without `value` fail
   the push. `noCdToRoot` keeps config discovery but leaves CLI item paths
   relative to the working directory.
10. **The folder cannot supply everything** ([SY-11](#sy-11), [SY-15](#sy-15),
    [OF-14](#of-14)): hub paths, any `hash` pin, secrets (ciphertext or
    omitted), built-in resource types and some locks need a resolver or a
    policy. What the build team may read, depend on or copy is in
    [licensing.md](licensing.md).

## A. OpenFlow model

Two sources define a flow: the Apache-2.0 schema `openflow.openapi.yaml` (the
documented contract) and the effective decoder (what the server and workers
accept, store and execute). They disagree in several places; where they do,
the effective decoder decides acceptance (OF-1).

<a id="of-1"></a>
### OF-1 · Two layers: documented schema and effective decoder

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - `openflow.openapi.yaml` is the **field inventory** and naming source. Its
    `info.version` is `1.767.0`, so it is version-locked to the pin.
  - Acceptance, required fields, defaults, aliases, integer ranges and null
    handling come from the effective decoder ([OF-5](#of-5), [OF-6](#of-6),
    [OF-9](#of-9), [OF-10](#of-10), [OF-11](#of-11), [OF-16](#of-16),
    [OF-V1](#of-v1), [OF-V2](#of-v2)).
  - Deploy does **not** re-serialise every value. The flow value and each
    module envelope are typed and rewritten; container values (loops,
    branches, AI agents) and re-locked rawscripts are rebuilt; every other
    module `value` is kept as submitted ([OF-3](#of-3)).
  - Execution normally reads a third, lite form. How each phase of a run
    selects its form is in [OF-V5](#of-v5).
- **Evidence:** `openflow.openapi.yaml:1-15` (Apache-2.0, version 1.767.0);
  `backend/windmill-types/src/flows.rs:267-309, 543-588` (typed envelope, raw
  `value`), `:957-1217`.
- **Conformance:** none of its own (framing finding); see
  [OF-3](conformance.md#of-3).
- **Design note (not Windmill behaviour):** a loader should decode with the
  effective rules, apply what a successful deploy materialises (see the OF-3
  design note), and never assume that a pulled `flow.yaml` is canonical.

<a id="of-2"></a>
### OF-2 · Deploy-time validation: a typed decode plus two checks

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:** When a flow is created or updated through the API, the server:
  1. decodes `value` with the effective decoder, in two stages (below);
  2. walks every module (the main list, then `failure_module`, then
     `preprocessor_module`, recursing into loop bodies, branches and AI-agent
     tool modules) and, per module in traversal order:
     - rejects a `retry` with `exponential.attempts > 0` and
       `exponential.seconds == 0`:
       `Module '<id>': Exponential backoff base (seconds) must be greater than 0. A base of 0 would cause immediate retries.`
     - rejects a `script` or `flow` step whose `path` is non-empty and does not
       start with `u/`, `f/`, `g/` or `hub/`:
       `step '<id>' references '<path>', which is not a workspace path (expected u/, f/, g/ or hub/). Absolute or local filesystem paths are not allowed in flow steps.`
       An empty `path` is accepted.
  3. in CE, rejects `ws_error_handler_muted: true` (EE only), and rejects
     debouncing settings when workers are too old.
  - It does **not** check module-id uniqueness or charset, that
    `failure_module.id` is `failure` or `preprocessor_module.id` is
    `preprocessor`, that referenced scripts or flows exist, JavaScript syntax,
    or the flow `schema`. Id rules exist only in the UI.
  - **Error wording.**
    - *Outer decode* covers the `FlowValue` fields and the envelopes of the
      top-level modules and of `failure_module`/`preprocessor_module`. A
      failure is reported as `Invalid flow value: <serde message>`.
    - *Module values* are decoded later, during traversal. A failure is
      reported as `Module '<id>': <serde message>`, without the
      `Invalid flow value:` prefix, where `<id>` is the module whose `value`
      failed. Decoding a container's value also decodes the envelopes (not the
      values) of its body modules, so a bad body-module envelope is attributed
      to the **container's** id, and a bad body-module value to the body
      module's own id.
    - Path and retry checks produce their own messages.
    - Decode messages carry a position suffix (`at line L column C`) and are
      wrapped again by the request-body decoder: compare by "contains", never
      by equality. The HTTP status and body are open ([Q-OF-2](#q-of-2)).
  - The submitted JSON is stored as given until the dependency job rewrites
    it ([OF-3](#of-3)).
- **Evidence:** `backend/windmill-types/src/flows.rs:199-207` (retry check),
  `:209-220` (path prefixes), `:222-265` (walk, including failure and
  preprocessor; `:228-229` outer prefix; `:231-262` per-module checks),
  `:678-729` (traversal order; `:678-690` `Module '<id>':` prefix);
  `backend/windmill-api-flows/src/flows.rs:501-513` (CE mute rejection,
  debounce guard), `:515-640` (raw insert), `:1694-1720` (debounce guard). No
  uniqueness check exists anywhere in `backend/`. UI-only id rules:
  `frontend/src/lib/components/IdEditorInput.svelte:38-56`,
  `frontend/src/lib/components/flows/idUtils.ts:3-21`.
- **Conformance:** [OF-2/a–g](conformance.md#of-2)
- **Design note (not Windmill behaviour):** windmill-lite should reproduce the
  accept/reject decision, not necessarily the HTTP envelope.

<a id="of-3"></a>
### OF-3 · Raw, full and lite forms of a flow value

**Scope:** core · **Confidence:** high (the three forms; differences 1–3 and 5); medium (difference 4 on full-form runs) · **Status:** DISPUTED

- **Rule (as drafted):**
  1. **Raw.** On create or update the submitted JSON is stored at once in both
     `flow` and `flow_version`, and a flow dependency job is queued.
  2. **Full.** Written back to `flow` and `flow_version` only if the
     dependency job succeeds. It is not written if locking any inline script
     fails (`lock_error_logs` is set; [OF-15](#of-15) says what stays saved),
     if the job was cancelled, if a newer version landed meanwhile, or under
     `skip_flow_update`. In the full form:
     - *Flow level:* typed. Unknown keys are dropped except top-level `notes`
       and `groups`; `delete_after_use` is dropped; default-valued booleans
       such as `same_worker: false` are omitted.
     - *Module envelopes:* typed at every depth reached through rebuilt
       containers. Unknown envelope keys and `delete_after_use` are dropped.
     - *Module values rebuilt from the typed form:* `forloopflow`,
       `whileloopflow`, `branchone`, `branchall` (including each branch),
       `aiagent`, and every rawscript that was (re)locked. Rebuilding
       materialises defaults such as `skip_failures`, `parallel`, branch
       `skip_failure` and branch `expr`; it drops while-loop
       `parallel`/`parallelism`, sorts rawscript `input_transforms` keys, and
       drops an empty `tag`.
     - *Module values kept exactly as submitted:* `script`, `flow`,
       `identity`, and rawscripts whose existing lock was kept or that a
       partial relock skipped. Unknown keys, the `input_transform` alias and an
       omitted `input_transforms` map all survive in these.
     - Exports and pulls return this form ([SY-6](#sy-6)).
  3. **Lite.** Built after the full form. Every main-list module value is
     re-serialised from its typed form, so unknown keys and aliases disappear,
     and a rawscript becomes `{"type":"flowscript","id":<numeric node id>,...}`
     whose code and lock live in a side table. The node id is a numeric
     database id unrelated to the module's string `id`. Loop and branch bodies
     are reduced recursively and may be moved into a separate node referenced
     by `modules_node`/`default_node`. `failure_module` and
     `preprocessor_module` are not reduced. Lite preparation also appends
     virtual trailing steps ([OF-17](#of-17)). None of these internal forms
     appears in a sync folder.
  - **Which form a run reads (the contested sentence):** lite when it exists;
    full when lite is disabled (`DISABLE_FLOW_SCRIPT`, or workers older than
    1.432) or not yet written. The last case is the deploy race: the full slot
    can still hold the **raw** value because the dependency job has not
    finished, so such a run can see unnormalised values.
  - **Differences between forms.** For kept `script`/`flow`/`identity` values
    the differences (aliases, unknown keys, an omitted `input_transforms` map)
    have no execution effect, because the executor decodes these values with
    the typed decoder. The observable differences between a raw value and a
    successfully deployed one are:
    1. **Container defaults** for fields the executor reads raw: an absent
       `forloopflow.skip_failures` or `branchall` branch `skip_failure` behaves
       as `false` raw and `true` once materialised ([OF-7](#of-7),
       [CF-6](control-flow.md#cf-6)).
    2. **Language:** a relocked Bun-family rawscript switches between `bun`
       and `bunnative` according to its `native` header annotation
       ([OF-15](#of-15), [SY-V3](#sy-v3)).
    3. **Locks** are generated or regenerated ([OF-15](#of-15)), which changes
       the dependency set the step runs with.
    4. **Virtual steps:** lite preparation appends `<id>-v` to every module
       list whose last module has `sleep` or `suspend`. It shows in the step
       status although the data result is preserved ([OF-17](#of-17)).
    5. **While-loop fields** `parallel`/`parallelism` are dropped, with no
       execution effect ([OF-8](#of-8)).
  - Validation confirmed the three-form description and the list of
    differences. Only the "which form a run reads" sentence is disputed.
- **Correction (validation):** the sentence assigns enqueue's choice to the
  worker. Per [OF-V5](#of-v5), form selection has two independent phases.
  Enqueue chooses full when `DISABLE_FLOW_SCRIPT` is set or the
  minimum-worker gate fails, and otherwise tries lite and falls back to full;
  that choice builds the initial status list. On modern workers with a normal
  version id, the run's execution-data lookup independently tries lite first
  and then full, without consulting `DISABLE_FLOW_SCRIPT`; completion performs
  the same lookup. A status list built from full and lite execution data can
  therefore coexist, so "the run reads full when lite is disabled" does not
  describe both phases. Evidence: `backend/windmill-queue/src/jobs.rs:5914-5965`;
  `backend/windmill-common/src/cache.rs:890-910`;
  `backend/windmill-worker/src/worker.rs:3731-3749, 3815-3824`;
  `backend/windmill-worker/src/worker_flow.rs:487-495, 1394-1397, 1734-1750, 3666-3698`.
- **Open:** [Q-OF-11](#q-of-11).
- **Evidence:** `backend/windmill-api-flows/src/flows.rs:592-640` (raw insert
  and dependency job); `backend/windmill-worker/src/worker_lockfiles.rs:369`
  (flow dependency job), `:606-641` (lock errors return before the
  write-back), `:651-669` (only `notes`/`groups` kept as extras), `:690-745`
  (full write-back), `:749-775` (lite build), `:1048-1315` (rebuilt kinds
  versus unchanged fallback), `:1126-1155` (while-loop rebuilt without
  `parallel`), `:1326-1348` (rawscript relock gate), `:1398-1432` (native
  language switch), `:1594-1706` (lite reduction; `:1706` adds virtual steps);
  `backend/windmill-types/src/flows.rs:833-835, 1038-1052` (numeric node id,
  `flowscript` variant), `:1177-1189`, `:1252-1280`;
  `backend/windmill-worker/src/worker_flow.rs:570-587, 944-955` (raw-read
  flags); `backend/windmill-queue/src/jobs.rs:5914-5928` (lite/full selection
  and the deploy-race fallback); `backend/windmill-common/src/cache.rs:555-570`
  (full fetch).
- **Conformance:** [OF-3/a–e](conformance.md#of-3) (OF-3/d is disputed)
- **Design note (not Windmill behaviour):** to behave like a successfully
  deployed flow, windmill-lite's loader must apply all of: the typed container
  defaults ([OF-6](#of-6)), the native-annotation language rule ([SY-3](#sy-3),
  [SY-V3](#sy-v3)), a lock policy ([OF-15](#of-15)) and the virtual-step rule
  ([OF-17](#of-17)). Container defaults alone are not enough. It does not need
  to reproduce the byte form of any stage.

<a id="of-4"></a>
### OF-4 · Unknown fields are ignored; `notes` and `groups` are editor-only

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - No type rejects unknown fields. A name the shared value decoder does not
    know is accepted anywhere in a flow value and has no effect.
  - "Unknown" means unknown to the shared decoder: a recognised field that the
    selected kind does not use is still type-checked and can reject the value
    ([OF-V1](#of-v1)).
  - Ignored on decode does not mean removed from stored values: kept module
    values retain unknown keys in the full form ([OF-3](#of-3)).
  - `notes` (`FlowNote[]`) and `groups` (`FlowGroup[]`) are UI annotations with
    no execution meaning. The typed `FlowValue` has no such fields; they
    survive only as top-level extras of the full form.
- **Evidence:** `backend/windmill-types/src/flows.rs:1301-1317` (test: notes
  and groups ignored), `:267-309`, `:1067-1100`;
  `openflow.openapi.yaml:116-125, 165-248` (`FlowGroup` says "Does not affect
  execution").
- **Conformance:** [OF-4/a](conformance.md#of-4)

<a id="of-5"></a>
### OF-5 · Module envelope (`FlowModule`) fields, types and defaults

**Scope:** core (envelope parse; field semantics are in execution.md and control-flow.md) · **Confidence:** high (types); medium (run-time consequence of random ids) · **Status:** confirmed

- **Rule:** A module is `{id, value, ...}` with these fields:

  | field | accepted form | default when absent | notes |
  |---|---|---|---|
  | `id` | string | random 6-character alphanumeric | The schema says required; the decoder supplies a random id. Ids are never validated. |
  | `value` | module value ([OF-9](#of-9)) | required | |
  | `summary` | string | none | |
  | `stop_after_if` | `{expr: string, skip_if_stopped: bool, error_message?: string or null, error_include_result?: bool}` | none | **`skip_if_stopped` is required** (the schema requires only `expr`). |
  | `stop_after_all_iters_if` | same shape | none | loops only ([control-flow.md](control-flow.md)) |
  | `skip_if` | `{expr: string}` | none | |
  | `sleep` | ordinary InputTransform object ([OF-10](#of-10)) | none | A bare number is **rejected**. |
  | `timeout` | bare i32, `null`, or a transform-shaped object whose `value` (if present) is an i32 or null and whose `expr` (if present) is a string or null ([OF-V2](#of-v2)) | none | seconds; not an unrestricted InputTransform |
  | `retry` | Retry ([OF-11](#of-11)) | none | |
  | `continue_on_error` | bool | none (false) | |
  | `suspend` | object, all fields optional: `required_events` (u32), `timeout` (u32 seconds), `resume_form` (any JSON), `user_auth_required` (bool), `user_groups_required` (InputTransform), `self_approval_disabled` (bool), `hide_cancel` (bool), `continue_on_disapprove_timeout` (bool) | none | approval steps (out) |
  | `mock` | `{enabled: bool, return_value?: any JSON}` | none | `enabled` is required when `mock` is present. |
  | `cache_ttl` | u32 seconds | none | |
  | `cache_ignore_s3_path` | bool | none | |
  | `priority` | i16 | none | queue priority (out) |
  | `delete_after_use` | bool | none | deserialise-only, dropped on deploy ([OF-12](#of-12)) |
  | `delete_after_secs` | i32 | none | |
  | `apply_preprocessor` | bool | none | not in the schema |
  | `pass_flow_input_directly` | bool | none | Not in the schema. **Envelope only:** when true, the step receives the flow's args object as its args, ignoring `input_transforms` ([OF-V4](#of-v4)). The same-named field inside a `script`/`flow` value is decoded but not used by the flow executor. |
  | `debouncing` | `{debounce_key?, debounce_delay_s?, max_total_debouncing_time?, max_total_debounces_amount?, debounce_args_to_accumulate?}`; `custom_debounce_key` is accepted as an alias of `debounce_key` | none | EE |

  Integer fields reject out-of-range values at parse time (for example
  `priority: 40000` fails i16 and `cache_ttl: -1` fails u32).
- **Evidence:** `backend/windmill-types/src/flows.rs:543-593`, `:415-426`
  (`StopAfterIf`, no default for `skip_if_stopped`), `:512-541` (`Suspend`,
  `Mock`), `:566-571, 798-830` (timeout), `:584-585`;
  `backend/windmill-types/src/more_serde.rs:25-35` (6 random alphanumerics);
  `backend/windmill-types/src/runnable_settings.rs:79-95` (debouncing, alias);
  `backend/windmill-worker/src/worker_flow.rs:3913-3915` (envelope flag uses
  flow args); `openflow.openapi.yaml:280-394`.
- **Conformance:** [OF-5/a–g](conformance.md#of-5)
- **Note:** a module without `id` gets an unpredictable id, so its result is
  keyed by that random id at run time.

<a id="of-6"></a>
### OF-6 · `forloopflow`, `whileloopflow`, `branchone`, `branchall` defaults

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**

  | kind | required | defaults when absent |
  |---|---|---|
  | `forloopflow` | `iterator` (InputTransform), `modules` | `skip_failures: true` (`null` also selects `true`), `parallel: false`, `parallelism`: none (bare u16 or restricted object, [OF-V2](#of-v2)), `squash`: none |
  | `whileloopflow` | `modules` | `skip_failures: false`, `squash`: none; `parallel`/`parallelism` are not part of the typed value ([OF-8](#of-8)) |
  | `branchone` | `branches`, `default` | branch `expr: ""`, branch `summary`: none |
  | `branchall` | `branches` | `parallel: true` (module level); per branch `skip_failure: true` and `parallel: true` (per-branch `parallel` is not in the schema and is not used by name in execution) |

  - `null` is not always the same as omission: branch `skip_failure` and
    `parallel` are plain booleans and reject `null` ([OF-V1](#of-v1)).
  - The typed `Branch` always serialises `expr`, `skip_failure` and `parallel`.
  - The schema instead marks `forloopflow.skip_failures` and
    `whileloopflow.skip_failures` as **required** and gives no defaults.
- **Evidence:** `backend/windmill-types/src/flows.rs:837-850` (`Branch`),
  `:985-1017`, `:1126-1161` (decoder defaults); `openflow.openapi.yaml:794-926`.
- **Conformance:** [OF-6/a](conformance.md#of-6); the for-loop defaults are
  covered by [OF-3/a](conformance.md#of-3).

<a id="of-7"></a>
### OF-7 · Skip-flag defaults differ between raw and deployed values

**Scope:** core · **Confidence:** high (the split); medium (exact result arrays; source-traced, oracle pending) · **Status:** confirmed

- **Rule:**
  - The split covers **`forloopflow.skip_failures`** and **`branchall` branch
    `skip_failure`** only. A while-loop's `skip_failures` defaults to `false`
    on every path.
  - The executor reads both flags from the module JSON it runs and treats an
    absent flag as `false`. A successful dependency job rebuilds every
    container with the typed defaults (`true` for both) in the full and lite
    forms ([OF-3](#of-3), [OF-6](#of-6)).
  - Hence an absent flag behaves as **`true`** when the flow is run by path
    after its exact version deployed successfully, and as **`false`** in
    preview runs (which keep raw module values), in runs during the deploy
    race, and for flows whose locking failed.
  - Result shapes follow control-flow: [CF-4](control-flow.md#cf-4) (loop
    arrays), [CF-5](control-flow.md#cf-5) (sequential failure),
    [CF-7](control-flow.md#cf-7) (parallel completion),
    [CF-15](control-flow.md#cf-15) (`branchall`). A sequential container stops
    at the first failed child when the flag is effectively `false`; a parallel
    one keeps every child result but fails; with the flag `true` failures stay
    in the ordered result array and the container succeeds.
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs:570-587, 944-955, 2336-2340`
  (raw reads, absent means false), `:1134-1192` (next iteration or branch
  requires success or the skip flag), `:1478-1489, 2299-2327` (failed or
  skipped child results stay in order), `:944-975, 1018-1064` (parallel
  completion); `backend/windmill-types/src/flows.rs:595-628` (raw views),
  `:837-850, 1126-1161` (typed defaults);
  `backend/windmill-worker/src/worker_lockfiles.rs:1048-1220` (container
  rebuild); `backend/windmill-api/src/jobs.rs:7503-7560` (preview keeps raw
  values); `backend/windmill-queue/src/jobs.rs:5604-5606, 5914-5928`;
  `backend/windmill-worker/src/bun_executor.rs:1886-1889, 1952-1955` (a Bun
  `Error` supplies `name` and `message`, plus `stack` and an optional
  `step_id`).
- **Conformance:** [OF-7/a–d](conformance.md#of-7)
- **Note:** recording protocol for the oracle ([oracle.md](oracle.md)): wait
  for the version's dependency job to succeed before the by-path run; record
  `flows/get` to show the stored loop or branch carries the explicit flag; use
  preview for the raw case. Tracked by [Q-OF-1](#q-of-1).
- **Design note (not Windmill behaviour):** windmill-lite applies the
  successful-deploy defaults at load time, consistent with
  [CF-6](control-flow.md#cf-6).

<a id="of-8"></a>
### OF-8 · `whileloopflow.parallel` is lost on deploy

**Scope:** later · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - The schema documents `parallel` and `parallelism` on `whileloopflow`, and
    the raw views can read them, but the typed while-loop has neither. Deploy
    rebuilds while-loops from the typed value, so both fields disappear from
    deployed flows.
  - The while-loop executor iterates sequentially and consumes no parallel
    flag, in deployed and raw (preview) execution alike.
  - Malformed values in these discarded fields still reject decoding
    ([OF-V1](#of-v1)).
- **Evidence:** `openflow.openapi.yaml:827-854`;
  `backend/windmill-types/src/flows.rs:998-1006, 1139-1146` (typed while-loop;
  decoder drops `parallel`); `backend/windmill-worker/src/worker_lockfiles.rs:1126-1155`
  (rebuild); `backend/windmill-worker/src/worker_flow.rs:5247-5291`
  (sequential iteration).
- **Conformance:** [OF-8/a](conformance.md#of-8)
- **Design note (not Windmill behaviour):** windmill-lite ignores these fields
  and may warn.

<a id="of-9"></a>
### OF-9 · Module value kinds (`value.type`)

**Scope:** core (rawscript, script, flow, forloopflow, whileloopflow, branchone, branchall, identity); out (aiagent) · **Confidence:** high · **Status:** confirmed

- **Rule:** Accepted `type` values are `rawscript`, `script`, `flow`,
  `forloopflow`, `whileloopflow`, `branchone`, `branchall`, `identity`,
  `aiagent`, plus the internal `flowscript` ([OF-3](#of-3)). The decoder
  accepting `flowscript` does not imply the API accepts internal node ids.
  Anything else rejects with
  ``unknown variant `<x>`, expected one of `script`, `flow`, `forloopflow`, `whileloopflow`, `branchone`, `branchall`, `rawscript`, `identity`, `aiagent` ``.
  Per kind:
  - `rawscript`: required `content`, `language` ([OF-13](#of-13)); optional
    `input_transforms` (default `{}`), `lock`, `path`, `tag`, `is_trigger`,
    `assets[]`, `custom_concurrency_key`, `concurrent_limit`,
    `concurrency_time_window_s`. The schema marks `input_transforms` required;
    the decoder does not.
  - `script`: required `path`; optional `input_transforms` (default `{}`),
    `hash` ([OF-14](#of-14)), `tag_override`, `is_trigger`,
    `pass_flow_input_directly` (not used by the flow executor,
    [OF-V4](#of-v4)).
  - `flow`: required `path`; optional `input_transforms`,
    `pass_flow_input_directly`.
  - `identity`: no fields; the schema's `flow: bool` is ignored.
  - `aiagent`: required `tools[]`; `input_transforms` (default `{}`), `tag`,
    `omit_output_from_conversation` (default `false`). The schema-only
    `parallel` is not in the typed value. A tool is
    `{id, summary?, description?, value}`, where `value` is tagged by
    `tool_type`: `flowmodule` → a module value, `mcp` →
    `{resource_path, include_tools[], exclude_tools[]}`, `websearch` → `{}`.
    An untagged legacy module value is also accepted.
  - `input_transform` is an alias of `input_transforms` for `script`, `flow`,
    `rawscript` and `aiagent`; supplying both is a duplicate-field error
    ([OF-V1](#of-v1)).
- **Evidence:** `backend/windmill-types/src/flows.rs:957-1061` (variants),
  `:1067-1217` (decoder and error list), `:853-951` (tools);
  `openflow.openapi.yaml:614-1117`.
- **Conformance:** [OF-9/a–b](conformance.md#of-9)

<a id="of-10"></a>
### OF-10 · InputTransform parsing: ordinary and restricted slots

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - An ordinary InputTransform (step inputs, `iterator`, `sleep`,
    `suspend.user_groups_required`, AI-agent inputs) is an object with `type`:
    - `static` → `value`, any JSON; **absent means `null`**;
    - `javascript` → `expr`, a string; **absent means `""`**;
    - `ai` → no payload (filled by the AI runtime; out);
    - any other `type` rejects with
      ``got value: <type> for field `type`, expected value: `static` or `javascript` ``.
    A missing `type` key rejects. The schema's `StaticTransform` does not
    require `value`, matching the null default. `$res:`/`$var:` strings inside
    static values are resolved later ([expressions.md](expressions.md)).
  - Two slots are restricted ([OF-V2](#of-v2)): module `timeout` (i32) and loop
    `parallelism` (u16). In both, a bare JSON number means static, and in
    object form any supplied `value` must be an integer of that width or null,
    and any supplied `expr` a string or null, whatever the `type`.
  - Recognised but unused fields are still type-checked ([OF-V1](#of-v1)).
- **Evidence:** `backend/windmill-types/src/flows.rs:731-796` (ordinary),
  `:798-830` (restricted), `:1080-1081`; `openflow.openapi.yaml:396-446`.
- **Conformance:** [OF-10/a–h](conformance.md#of-10)

<a id="of-11"></a>
### OF-11 · Retry fields and ranges

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - `retry` is
    `{constant?: {attempts: u32 = 0, seconds: u16 = 0}, exponential?: {attempts: u32 = 0, multiplier: u16 = 1, seconds: u16 = 0, random_factor: i8 or null = null}, retry_if?: {expr: string}}`.
    Every sub-field defaults as shown, so `{}` is valid and means "no retry".
  - The attempt counts are **retries after the original execution**; the
    maximum retry count is `constant.attempts + exponential.attempts`,
    saturating at the u32 maximum.
  - Out-of-range integers reject at parse (for example `seconds: 70000`,
    `random_factor: 200`).
  - The schema says `random_factor` is 0–100 and `exponential.seconds` ≥ 1.
    Only the latter is enforced, and only when `exponential.attempts > 0`
    ([OF-2](#of-2)).
  - Delay arithmetic is specified in [FX-26](execution.md#fx-26) (exponential
    delay `multiplier * seconds^(n+1)` over the overall previous-attempt count,
    with ± jitter).
- **Evidence:** `backend/windmill-types/src/flows.rs:199-207, 428-510`,
  `:478-482` (saturating maximum); `openflow.openapi.yaml:129-163, 250-258`.
- **Conformance:** [OF-11/a–c](conformance.md#of-11)

<a id="of-12"></a>
### OF-12 · `delete_after_use` is accepted but not persisted

**Scope:** out · **Confidence:** high · **Status:** confirmed

- **Rule:** `delete_after_use` (module and flow level) is decoded but never
  serialised, so it is lost on deploy ([OF-3](#of-3)). Its replacement is
  `delete_after_secs` (i32). Both govern only job-row cleanup in Windmill's
  database.
- **Evidence:** `backend/windmill-types/src/flows.rs:305-308, 574-577`;
  `backend/windmill-types/src/scripts.rs:546-548`.
- **Conformance:** none (no data effect; a loader accepts and ignores both).
- **Design note (not Windmill behaviour):** in a scheduler-free runtime these
  fields have no observable data semantics; ignoring cleanup is a windmill-lite
  scope decision, not a claim that Windmill never reads them before the
  rewrite.

<a id="of-13"></a>
### OF-13 · Languages

**Scope:** core (bun, bunnative, nativets); later (other languages) · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - `language` values (lower-case): `deno`, `bun`, `bunnative`, `nativets`,
    `python3`, `go`, `bash`, `powershell`, `postgresql`, `mysql`, `bigquery`,
    `snowflake`, `mssql`, `oracledb`, `graphql`, `duckdb`, `php`, `rust`,
    `ansible`, `csharp`, `nu`, `java`, `ruby`, `rlang`. Any other string
    rejects (see [Q-OF-12](#q-of-12) on the count).
  - A step's language is known at load time: a `rawscript` carries it in
    `language`; a `script` step's language comes from the referenced script's
    **file extension** plus `defaultTs` plus the `native` annotation
    ([SY-3](#sy-3)), because `.script.yaml` has no `language` field; a `flow`
    step is a subflow whose own steps decide. This holds only once the
    referenced script is actually resolved ([SY-15](#sy-15)).
  - `bun`, `bunnative` and `nativets` share a TypeScript front end, but that
    does not make them behave the same at run time
    ([languages.md](languages.md)).
- **Evidence:** `backend/windmill-types/src/scripts.rs:41-101`;
  `openflow.openapi.yaml:652-680`.
- **Conformance:** [OF-13/a](conformance.md#of-13)

<a id="of-14"></a>
### OF-14 · Script hash format

**Scope:** later · **Confidence:** high (format, deploy, the hash's inputs); medium (the run-time lookup and its message, read not traced) · **Status:** drafted

- **Rule:**
  - `script.hash` pins a specific deployed script version.
  - **Decoding:** a valid hexadecimal string, either letter case, with an even
    number of digits, that decodes to **at least 8 bytes**. The first 8 bytes,
    read big-endian, are the hash (a signed 64-bit integer); extra bytes are
    ignored. A multiple of 16 digits is **not** required.
  - **Errors:** odd length or non-hex characters give the hex library's error;
    fewer than 8 bytes gives `hex string did not decode to an u64: <s>`.
  - **Serialisation:** always exactly 16 lowercase hex digits.
  - **Where a hash comes from.** The server computes a version's hash when
    the script is deployed, from the whole submitted body: path, the parent
    version's hash, summary, description, content, schema, lock, language,
    kind, the settings, the deployment message and more. It uses the Rust
    standard library's default hasher, whose algorithm that library leaves
    unspecified. Hashes are unique per workspace, not globally: the same
    body with the same parent gives the same hash in any workspace, but the
    parent depends on the workspace's deploy history and the body on what
    the CLI regenerates at push time ([SY-4](#sy-4)). So no hash can be
    derived or checked from a folder.
  - **Deploy.** A flow's `script` step may pin any well-formed hash: neither
    the CLI nor the server checks that a version with that hash exists. The
    CLI pushes `hash` as written, and the flow's dependency job only records
    it.
  - **Run.** The step runs the version with that hash **in the flow's
    workspace, whatever its path**; the step's `path` is only the child
    job's path. A pinned step drops the pinned version's own `cache_ttl`
    ([FX-40](execution.md#fx-40)). An unknown hash fails the flow before the
    step's job is created, with a not-found error whose message contains
    `script not found at name <h>`, where `<h>` is the hash as a signed
    decimal integer: `0123456789abcdef` gives `81985529216486895`.
- **Evidence:** `backend/windmill-types/src/scripts.rs:183-233` (decode,
  `:221-233`), `:721-736` (byte rule and encoding), `:575-620` (the fields
  hashed), `:747-751` (the hasher);
  `backend/windmill-api-scripts/src/scripts.rs:954` (hash computed on create);
  `backend/migrations/20220123221903_first.up.sql:33` (key: workspace and
  hash);
  `backend/windmill-worker/src/worker_lockfiles.rs:1225-1236` (the dependency
  job records a pin); `backend/windmill-worker/src/worker_flow.rs:5928-5938`,
  `:6051-6133` (a pin is looked up by hash only); `backend/windmill-common/src/lib.rs:1661-1745`
  (lookup by hash and workspace; not found), `backend/windmill-common/src/utils.rs:338-351`
  (the message); `backend/windmill-queue/src/jobs.rs:5369-5398` (the child
  job runs the hash under the step's path).
- **Conformance:** [OF-14/a–f](conformance.md#of-14)
- **Design note (not Windmill behaviour):** a sync folder holds one version per
  path, a loader cannot fetch old versions, and it cannot compute a hash
  either, so it cannot honour a pin from the folder alone ([SY-15](#sy-15)).
  It needs a policy: refuse the step, or take the hash from a host that
  knows the target workspace.
- **Revision 2026-10-28 (source re-check):** added where hashes come from,
  that deploy accepts any well-formed pin, and how a pin resolves at run
  time. The design note said a loader could honour a hash by checking it
  against the folder's script; it cannot, since the hash depends on the
  deploy history and an unspecified hasher. The format rules are unchanged.

<a id="of-15"></a>
### OF-15 · Inline-script locks on deploy

**Scope:** core · **Confidence:** high (conditions and failure behaviour; header format per OF-V6) · **Status:** confirmed

- **Rule:** For each inline rawscript the dependency job reaches (main list,
  loop and branch bodies, `failure_module`, `preprocessor_module`, AI-agent
  tools), exactly one of these paths applies:

  | selection list (`nodes_to_relock`) supplied? | module id in it? | behaviour |
  |---|---|---|
  | no | n/a | Keep a non-empty (after trimming) existing lock if, for `bun`/`bunnative`, the `native` annotation ([SY-V3](#sy-v3)) agrees with the language **and** every worker meets the minimum version for v2 debouncing (or its debug override is set). Otherwise regenerate. |
  | yes | no | Leave the rawscript value untouched (content, lock, language). |
  | yes | yes | **Always regenerate**, even if the keep conditions hold. The module is recorded as modified; the new lock may equal the old one. |

  - **Regeneration** produces a per-language lock body:
    - a resolver runs for `python3`, `ansible`, `go`, `deno`, `bun`/`bunnative`,
      `php`, `rust`, `csharp`, `java`, `ruby`, `rlang` and `powershell`;
    - every other language (`bash`, `nu`, `nativets`, `graphql`, the SQL
      dialects) gets an empty body;
    - Bun gets an empty body when its lock generator returns no lock; PHP with
      no parsed imports and no workspace-dependency input returns a final `""`
      directly ([OF-V6](#of-v6)).
    - Regenerating a Bun-family script also applies the `native` language
      switch between `bun` and `bunnative`.
  - **Lock header.** A workspace-dependency header is then prepended when the
    workspace's dependency state produces one ([OF-V6](#of-v6)). The final lock
    is `""` only if the body is empty **and** there is no header.
  - **Import base.** Dependencies of an inline script are extracted relative to
    `<rawscript.path or flow path>/flow`.
  - **Failure.** If locking fails for any module, the job returns before
    writing the saved full or lite values (log and dependency-map writes are
    not covered by this). The saved values stay exactly as they were before
    the job: the raw submitted value for a newly created or updated version,
    or that version's previous full/lite data when an existing version is
    relocked. The error is recorded in `lock_error_logs`.
  - An empty or absent lock is therefore not by itself a broken export, and a
    hand-written folder may lack locks that a deploy would generate.
- **Evidence:** `backend/windmill-worker/src/worker_lockfiles.rs:1321-1324`
  (import base), `:1326-1352` (exclusive selection and keep paths; `:1352`
  records the module as modified), `:1398-1432, 1762-1772` (native switch),
  `:2696-2978` (per-language resolver arms), `:2818-2865` (Bun: no lock means
  empty body), `:2867-2882` (PHP), `:2981-3006` (header prepended when
  present), `:606-641` (failure returns before the writes at `:733-775`);
  `backend/windmill-common/src/worker.rs:967-974` (TypeScript annotations
  `npm`, `nodejs`, `native`, `nobundling`, `sandbox`).
- **Conformance:** [OF-15/a–e](conformance.md#of-15)
- **Design note (not Windmill behaviour):** proposed windmill-lite policy,
  awaiting the language owners ([Q-OF-5](#q-of-5)): an empty or absent lock is
  not by itself an error; a script with no external dependencies may run with
  an empty lock; a script with unresolved external imports and no lock is
  refused, naming the step. That a lock is legitimately empty upstream does not
  prove a script runs offline.

<a id="of-16"></a>
### OF-16 · Flow-level fields (`FlowValue`) and flow metadata

**Scope:** core (modules, failure_module, preprocessor_module, skip_expr, flow_env, flow-level timeout); later (early_return response, same_worker rejection, caching); out (queue and placement controls) · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - **`FlowValue` fields:** `modules` (required), `failure_module`,
    `preprocessor_module`, `same_worker` (bool, default `false`),
    `preserve_step_tags` (bool, default `false`), `concurrent_limit` (i32),
    `concurrency_key` (string), `concurrency_time_window_s` (i32), debouncing
    fields (`debounce_key`, with alias `custom_debounce_key`;
    `debounce_delay_s`, `max_total_debouncing_time`,
    `max_total_debounces_amount`, `debounce_args_to_accumulate`), `skip_expr`
    (string), `cache_ttl` (u32), `cache_ignore_s3_path`, `early_return`
    (string), `priority` (i16), `chat_input_enabled`, `flow_env` (map of name
    to raw JSON; `$var:`/`$res:` references allowed), `delete_after_use`
    (dropped, [OF-12](#of-12)), `delete_after_secs`. The schema types
    `concurrent_limit` and `concurrency_time_window_s` as `number`; the decoder
    requires integers.
  - **`failure_module` and `preprocessor_module`** are ordinary modules. The
    schema's "must have id `failure`/`preprocessor`" and "only
    script/rawscript" are **not enforced**. With no failure module, the status
    slot is named `failure`. The preprocessor step runs the referenced script's
    exported `preprocessor` function (entrypoint override) with the flow's args.
  - **`early_return` is a module id, not a JavaScript expression** (the
    schema's description is wrong). It affects only a synchronous run-and-wait
    response; the flow's own result is unchanged. Full rule: [OF-V3](#of-v3) and
    [FX-37](execution.md#fx-37).
  - **`same_worker`:** when a flow continues on the same worker
    (`same_worker: true`, or the equivalent runner mode) and starting a step
    would produce **more than one child job**, the step fails with a message
    containing
    `Cannot continue on same worker with multiple jobs, parallel cannot be used in conjunction with same_worker`.
    A step that produces exactly one child job is allowed, even from a
    parallel container. The full container rule is [CF-7](control-flow.md#cf-7).
    `same_worker` placement is otherwise out; this rejection is later.
  - **Execution-relevant `value` fields:** `modules`, `failure_module`,
    `preprocessor_module`, `skip_expr`, `flow_env`, `early_return` (response
    only), `cache_ttl`/`cache_ignore_s3_path` (caching) and `same_worker`.
  - **Queue and placement controls (out):** `concurrent_limit`,
    `concurrency_key`, `concurrency_time_window_s`, debouncing, `priority`,
    `preserve_step_tags`, `chat_input_enabled`, `delete_after_secs`.
  - **Flow metadata outside `value`** (also in `flow.yaml`): `summary`,
    `description`, `schema`, and:
    - execution input: `timeout` (i32, whole-flow seconds);
    - placement or permission (out or later): `tag`, `dedicated_worker`,
      `on_behalf_of_email`, `visible_to_runner_only`, `ws_error_handler_muted`
      (EE);
    - deploy controls only: `deployment_message`, `preserve_on_behalf_of`,
      `skip_draft_deletion`, `labels`.
- **Evidence:** `backend/windmill-types/src/flows.rs:116-139` (`NewFlow`),
  `:267-309`; `backend/windmill-types/src/runnable_settings.rs:79-115`;
  `backend/windmill-types/src/flow_status.rs:503-545` (status ids, default
  `failure`); `backend/windmill-worker/src/worker_flow.rs:3906-3912`
  (preprocessor entrypoint), `:4793-4799` (multiple-child guard);
  `backend/windmill-api-jobs/src/execution.rs:290-335`;
  `backend/windmill-api/openapi.yaml:29542-29625` (`FlowMetadata`,
  `OpenFlowWPath`); `openflow.openapi.yaml:23-127, 110-112`.
- **Conformance:** [OF-16/a–d](conformance.md#of-16); `early_return` cases are
  under [OF-V3](conformance.md#of-v3).

<a id="of-17"></a>
### OF-17 · Virtual trailing identity for a final sleep or suspend

**Scope:** core · **Confidence:** high (construction, result preservation); medium (status visibility outside initial-lite runs) · **Status:** confirmed

- **Rule:**
  - When a module list's last module has `sleep` or `suspend`, Windmill
    appends a virtual `identity` module with id `<last id>-v` and summary
    `Virtual module needed for suspend/sleep when last module`. Lite
    preparation applies this to the top-level list and to every nested body
    list.
  - On normal successful execution the identity returns the preceding value,
    so the flow's data result is unchanged. The virtual step belongs to the
    containing module list's status, and appears only for steps actually
    reached. It is not an unconditional new root-level `results` key for every
    nested body; visibility across bodies is [Q-OF-4](#q-of-4).
  - Whether the slot exists depends on the form that built the initial status
    list ([OF-V5](#of-v5)): with an initial-lite status, `a-v` exists and the
    final sleep is scheduled before it; with a full-built status on a fresh
    ordinary successful modern-worker run, only `a` appears and no final sleep
    is scheduled. The `[a, a-v]` fixture therefore applies to an initial-lite
    status (or the independently augmented preview path). Its relation to the
    disputed OF-3 sentence is tracked by [Q-OF-11](#q-of-11).
  - **Recorded for a deployed run.** On the oracle, a deployed flow run by
    path whose last module has an invalid `sleep` expression fails when
    that `sleep` is evaluated, so a step after the last module was
    scheduled: the virtual step exists in the reference configuration
    (batch E3a, fixture `conformance/flows/je/waits`, run `je28-c`, oracle
    run 40687124625; [JE-28](expressions.md#je-28)). The recording does not
    list the virtual step among the walked steps.
  - **Only module lists.** The flow's `failure_module` and
    `preprocessor_module` are not in any module list, so neither ever gets
    a virtual step, and a `sleep` on either is never applied
    ([CF-30](control-flow.md#cf-30)).
- **Evidence:** `backend/windmill-types/src/flows.rs:1252-1280`; call sites
  `backend/windmill-worker/src/worker_lockfiles.rs:1706`,
  `backend/windmill-worker/src/worker_flow.rs:5056`,
  `backend/windmill-queue/src/jobs.rs:5605, 5956` (each passes a module
  list); identity result
  `backend/windmill-worker/src/worker_flow.rs:3919-3927, 5141`,
  `backend/windmill-worker/src/worker.rs:4032-4039`.
- **Conformance:** [OF-17/a](conformance.md#of-17) (disputed expectation)
- **Revision 2026-10-28 (oracle, batch E3a; source re-check):** two bullets
  are added: the deployed-run recording, which shows the initial-lite
  branch of the third bullet, and the rule that the failure and
  preprocessor modules never get a virtual step (a question asked by
  windmill-lite's IR compiler). The `[a, a-v]` status list itself is still
  not recorded, so OF-17/a and [Q-OF-11](#q-of-11) are unchanged.

<a id="of-18"></a>
### OF-18 · The flow `schema` has no server-side execution semantics

**Scope:** core · **Confidence:** medium (absence established by search and two traced paths, not every trigger entry point) · **Status:** confirmed

- **Rule:**
  - On the examined run paths (run API, queue push, flow executor), nothing
    validates `flow_input` against the flow's `schema` or fills in its
    defaults. Defaults are applied by clients (UI forms, generated clients).
  - For a plain JSON run by path with no preprocessor, `flow_input` is the
    submitted args object.
  - When a preprocessor runs, its result replaces the input before step 0
    ([FX-35](execution.md#fx-35)), independently of the schema.
    Trigger-specific argument intake is outside this area.
  - Scripts differ: a script may opt into argument validation with a
    `schema_validation` annotation ([languages.md](languages.md)).
- **Evidence:** `backend/windmill-worker/src/worker_flow.rs` (no `schema`
  reference), `:3906-3940` (preprocessor path; transforms consume flow args);
  no schema `default` reads in `backend/windmill-{worker,queue,api,common}/src`;
  `backend/windmill-queue/src/jobs.rs:5914-5965` (flow push without loading the
  input schema); `backend/windmill-common/src/schema.rs:479-482` (script
  opt-in).
- **Conformance:** [OF-18/a–b](conformance.md#of-18)

<a id="of-19"></a>
### OF-19 · Client-side validation is optional and schema-based

**Scope:** later · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - `wmill lint` validates `flow.yaml`, `*.schedule.yaml` and
    `*.<kind>_trigger.yaml` (and the `.yml` spellings) against JSON Schemas
    generated from `openflow.openapi.yaml` and the backend OpenAPI, using the
    npm package `windmill-yaml-validator` (ajv, discriminators on, formats off,
    nullable enums patched). Only the nine trigger kinds that package lists are
    linted, not every trigger sync supports.
  - `wmill sync push` runs lint only with `--lint`.
  - The schema is looser than the server in some places (OF-5
    `skip_if_stopped`) and stricter in others (OF-6 required `skip_failures`,
    OF-9 required `input_transforms`), so passing lint does not imply the
    server accepts the flow, and vice versa.
- **Evidence:** `windmill-yaml-validator/package.json` (version 1.1.1),
  `windmill-yaml-validator/gen_openflow_schema.sh`,
  `windmill-yaml-validator/src/validation/yaml-validator.ts:49-101`;
  `cli/src/commands/lint/lint.ts:636-674`;
  `cli/src/commands/sync/sync.ts:3761-3768`; `cli/package.json:45`. Licence
  status of the package: [LIC-1](licensing.md#lic-1).
- **Conformance:** [OF-19/a–b](conformance.md#of-19)
- **Design note (not Windmill behaviour):** windmill-lite's loader implements
  the server's rules (OF-2 to OF-16) and may offer schema lint as an extra.

<a id="of-v1"></a>
### OF-V1 · Decode rules include cross-kind checks, null distinctions and aliases

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - A value's recognised fields (`language`, `parallel`, `skip_failures`,
    `parallelism`, `iterator`, `branches`, `tools`, …) are type-checked before
    its `type` selects the fields that survive. A field unused by the selected
    kind can therefore reject the value. Truly unknown names are ignored.
  - Null and omission agree for optional fields, but not for every field with
    a default: branch `skip_failure`/`parallel` are plain booleans and reject
    `null`, whereas a for-loop's `skip_failures: null` selects its `true`
    default.
  - `input_transform` is an alias of `input_transforms` for `script`, `flow`,
    `rawscript` and `aiagent`; specifying both is a duplicate-field error.
  - The debouncing object accepts `custom_debounce_key` as an alias of
    `debounce_key`.
- **Evidence:** `backend/windmill-types/src/flows.rs:731-796, 837-850` (`:846-849`
  branch booleans), `:1067-1217` (`:1067-1100` shared value decoder);
  `backend/windmill-types/src/runnable_settings.rs:79-95`.
- **Conformance:** [OF-V1/a–f](conformance.md#of-v1)

<a id="of-v2"></a>
### OF-V2 · `timeout` and `parallelism` restrict object payloads as well as bare numbers

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - `timeout` accepts null or omission, an i32 integer, or a transform-shaped
    object whose optional `value` must be an i32 integer or null and whose
    optional `expr` must be a string or null.
  - `parallelism` follows the same rule with u16.
  - Choosing `javascript` does not exempt a supplied `value` from integer
    decoding; choosing `static` does not exempt a supplied `expr` from string
    decoding. A `static` object with no `value` becomes static `null`.
  - Ordinary InputTransforms elsewhere keep arbitrary static JSON
    ([OF-10](#of-10)).
- **Evidence:** `backend/windmill-types/src/flows.rs:566-571, 599-605, 798-830, 1080-1081`.
- **Conformance:** [OF-V2/a–g](conformance.md#of-v2)

<a id="of-v3"></a>
### OF-V3 · `early_return` selects an API response node

**Scope:** later · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - `FlowValue.early_return` is the string id of a flow node whose available
    result may satisfy a synchronous wait-for-result response before the
    whole flow finishes.
  - It is not evaluated as JavaScript and does not stop the remaining steps.
  - When the selected node fails and a `failure_module` exists, the waiter
    ignores the early error and waits for the completed flow result.
  - The schema's description of it as a JavaScript expression disagrees with
    the implementation.
  - Host/API behaviour (the early response) must be kept separate from the
    flow interpreter's final result.
- **Evidence:** `openflow.openapi.yaml:110-112`;
  `backend/windmill-api-jobs/src/execution.rs:290-335`;
  `backend/windmill-api/src/jobs.rs:8746-8759`;
  `backend/windmill-worker/src/common.rs:1916-1940, 1989-2008`;
  [FX-37](execution.md#fx-37).
- **Conformance:** [OF-V3/a–b](conformance.md#of-v3)

<a id="of-v4"></a>
### OF-V4 · `pass_flow_input_directly` means different things by location

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - The flow executor bypasses `input_transforms` when the enclosing
    envelope's `pass_flow_input_directly` is true; the step then receives the
    flow's args.
  - `script` and `flow` values also decode a field of that name, but setting
    it only inside `value` does not trigger the bypass on the ordinary step
    argument path. The value-level field is read by the AI-agent tool path
    (out), which does not imply an ordinary-flow bypass. A synthesised wrapper
    flow sets the envelope flag itself.
  - An enabled preprocessor has an earlier, dedicated argument and entrypoint
    path.
- **Evidence:** `backend/windmill-types/src/flows.rs:584-585, 973-984, 1110-1124`;
  `backend/windmill-worker/src/worker_flow.rs:3906-3940` (`:3913` envelope
  read); `backend/windmill-worker/src/ai_executor.rs:371-424`;
  `backend/windmill-queue/src/jobs.rs:5798`.
- **Conformance:** [OF-V4/a–b](conformance.md#of-v4)

<a id="of-v5"></a>
### OF-V5 · Enqueue status and worker data select flow forms separately

**Scope:** core (distinguishing the deployed form; the loader's virtual-step rule); later (compatibility switches, deploy-race emulation) · **Confidence:** high (source-traced for the normal modern-worker path; oracle pending) · **Status:** open

- **Status note:** the final independent check recorded this finding as
  confirmed from source. It is also the competing claim in the
  [OF-3](#of-3) dispute, so it is carried `open` until
  [Q-OF-11](#q-of-11) closes.
- **Rule:** For an ordinary path or version flow:
  - **Enqueue** chooses full when `DISABLE_FLOW_SCRIPT` is set or the
    minimum-worker gate fails; otherwise it tries lite and falls back to full.
    That choice creates the initial status list, one slot per supplied module.
  - **Execution**, on modern workers with a normal version id, independently
    tries the lite value first and then full. This lookup does not consult
    `DISABLE_FLOW_SCRIPT`. Completion performs the same lookup.
  - A full-selected status and a lite execution value can therefore coexist.
    "The run reads full when lite is disabled" is not a valid description of
    both phases.
  - **Fresh ordinary successful run, full-built status:** the full top-level
    list ends in `a` with `sleep` and has no virtual identity, so the initial
    status contains only `a`. The last-step decision uses the status-list
    length, so after `a` succeeds the flow completes without starting `a-v`.
    Sleep is evaluated when starting the next module, so no final sleep is
    scheduled. Fetching lite later does not add a status slot.
  - **Initial lite status:** the generated slot `a-v` exists and the final sleep
    is scheduled before it.
  - Both successful cases preserve `a`'s result.
  - Not covered by this trace: legacy workers, restart and single-step paths,
    failures, cancellation, cache hits and durable suspend.
- **Evidence:** `backend/windmill-queue/src/jobs.rs:5914-5965` (enqueue
  selection and status creation); `backend/windmill-types/src/flow_status.rs:503-519`
  (one slot per supplied module); `backend/windmill-worker/src/worker.rs:3731-3749, 3815-3824`
  (versioned run fetch); `backend/windmill-common/src/cache.rs:890-910`
  (lite-first lookup); `backend/windmill-worker/src/worker_flow.rs:487-495`
  (completion lookup), `:1394-1397` (last index from status length),
  `:1734-1750` (successful continuation), `:3666-3698` (previous module's sleep
  applied before the next start); `backend/windmill-worker/src/worker_lockfiles.rs:1706`
  (lite augmentation).
- **Conformance:** [OF-V5/a–b](conformance.md#of-v5)
- **Note:** this finding is the correction to the disputed OF-3 sentence
  ([Q-OF-11](#q-of-11)) and bounds the [OF-17](#of-17) fixture.
- **Design note (not Windmill behaviour):** this is an upstream distinction
  between deployed forms, not a proposal to drop final sleeps from
  windmill-lite's target, which remains the successful lite deployment
  (virtual step and final sleep included).

<a id="of-v6"></a>
### OF-V6 · Workspace-dependency lock header and the PHP early return

**Scope:** core (the loader preserves supplied lock text); later (generating workspace-dependency headers) · **Confidence:** high (source-traced; no resolver or oracle run) · **Status:** confirmed

- **Rule:** On paths that reach common lock assembly:
  - The header is absent if the workspace-dependency worker-version gate fails
    (threshold 1.587.0, with a debug override) or the prepared dependency state
    is absent. Header availability is a property of that state and gate, not a
    test for non-empty script imports.
  - With supported workers, explicit dependency annotations produce a mode line
    and one reference line per resolved external reference, in recorded order;
    an implicit default produces a mode line and one `default` reference.
    Inline-only explicit annotations can produce a mode line with no reference
    lines.
  - The mode is the literal `manual` or `extra`. The labels are
    `workspace-dependencies-mode: ` and `workspace-dependencies: `. A reference
    is `<name>:<hash>`, with `default` for an unnamed dependency and the
    lowercase SHA-256 of its content as the hash.
  - Each line starts with the language's comment prefix: `//` for Bun,
    Bunnative, Deno, Go, PHP, C#, Java and NativeTS; `#` for Python, Bash,
    PowerShell, GraphQL, Ansible, Nu, Ruby and R; `--` for the SQL dialects;
    `//!` for Rust.
  - Header lines are joined by newlines, and assembly adds one newline before
    the lock body, even an empty one.
  - **PHP:** no parsed imports produces an early final `""` only when there is
    also no workspace-dependency input; that path skips header assembly. It
    does not hide a would-be header, because PHP receives a composer input
    whenever an explicit or implicit dependency state exists. A PHP script with
    no inline imports may still have workspace dependencies.
- **Evidence:** `backend/windmill-common/src/workspace_dependencies.rs:28-46, 96-98, 373-435, 534-549, 654-694, 724-730`;
  `backend/windmill-common/src/utils.rs:432-435` (content hash);
  `backend/windmill-types/src/scripts.rs:133-142` (comment prefixes);
  `backend/windmill-worker/src/worker_lockfiles.rs:2867-2882, 2981-3006` (PHP
  exit, assembly).
- **Conformance:** [OF-V6/a–c](conformance.md#of-v6)
- **Note:** a loader must not equate an empty lock body with an empty final
  lock text, nor either with offline executability ([Q-OF-5](#q-of-5)).

## B. Sync folder

The on-disk layout has two authors. The server's workspace export names the
files inside a zip (`backend/windmill-api/src/workspaces_export.rs`); the `wmill`
CLI rewrites that zip into the working tree
(`cli/src/commands/sync/sync.ts:852-1539`) and reads it back on push.
Inline-script naming lives in the npm package `cli/windmill-utils-internal`
(licence status: [LIC-1](licensing.md#lic-1)). Where the export and the CLI
differ, the CLI's on-disk form is what a loader sees.

<a id="sy-1"></a>
### SY-1 · Tree root, recognised paths and path mapping

**Scope:** core (root discovery, path mapping); later (noCdToRoot) · **Confidence:** high (discovery, mapping, first-dot rule); medium (ignore list) · **Status:** confirmed

- **Rule:**
  - **Root.** The sync root is the directory holding `wmill.yaml`. The CLI
    looks for it in the working directory and then in parent directories,
    stopping at the git root (or the filesystem root). This upward search
    **always runs**. The CLI then changes its working directory to the found
    file's directory, unless `noCdToRoot` is set; with `noCdToRoot`, item paths
    stay relative to the working directory ([SY-V4](#sy-v4)). When no
    `wmill.yaml` is found: [SY-13](#sy-13).
  - **Recognised paths.** Workspace items live under the top-level directories
    `u/` (user), `f/` (folder) and `g/` (group). Also recognised: `users/`,
    `groups/`, `dependencies/`, `migrations/`, and at the root
    `*.resource-type.yaml`, `settings.yaml`, `encryption_key.yaml`. `ui/` is
    synced separately. Everything else is ignored.
  - **Walk.** Directories whose name starts with `.`, plus `node_modules` and
    `.claude`, are skipped. Only a fixed list of file extensions is considered;
    [SY-18](#sy-18) gives the list and how each file is classified.
  - **Windmill path** = root-relative POSIX path with the type suffix removed:
    - for `<p>.<type>.yaml` or `<p>.<type>.json`, the `.<type>.yaml|json`
      suffix is stripped;
    - for flat script files the path is everything **before the first `.`** of
      the relative path, so a dot in a folder or file name truncates it
      (`u/my.folder/s.ts` → `u/my`). Multi-part language suffixes rely on this:
      `f/a/x.bun.ts`, `f/a/x.deno.ts`, `f/a/x.fetch.ts` and `f/a/x.pg.sql` all
      map to `f/a/x`;
    - for folder layouts (`.flow`, `__flow`, `__mod`) the marker is stripped.
  - Paths must start with `u/`, `g/` or `f/`; the CLI checks nothing else
    about characters. (The UI restricts each name segment to word characters
    and `-`.)
  - `.wmillignore` is not supported. It is meant to raise a "not supported
    anymore" error, but the error is swallowed, so in practice the file is
    ignored.
- **Evidence:** `cli/src/core/conf.ts:146-193` (`:146-176` unconditional
  upward search, `:178-193` directory change gated on `noCdToRoot`);
  `cli/src/commands/sync/sync.ts:2487-2554` (recognised top-level paths),
  `:1577-1585` (skipped directories), `:1668-1693` (extension list),
  `:2589-2594` (`.wmillignore`); `cli/src/utils/resource_folders.ts:555-571`
  (first-dot rule); `cli/src/types.ts:322-457` (type suffixes);
  `cli/src/core/context.ts:746-757` (prefix check);
  `frontend/src/lib/components/Path.svelte:341`.
- **Conformance:** [SY-1/a–g](conformance.md#sy-1)
- **Design note (not Windmill behaviour):** windmill-lite proposes to reject a
  dotted logical path such as `f/my.dir/x.ts` with a clear error instead of
  reproducing the truncation (a labelled divergence; the multi-part language
  suffixes stay valid). It also proposes to always take the directory of the
  discovered `wmill.yaml` as the sync root, which differs from the CLI under
  `noCdToRoot` ([SY-V4](#sy-v4)).
- **Revision 2026-10-28 (source re-check):** the walk's file list, cited
  before without being enumerated, is now [SY-18](#sy-18), and the case of no
  `wmill.yaml` is [SY-13](#sy-13). The rule above is unchanged.

<a id="sy-2"></a>
### SY-2 · Script files

**Scope:** core (flat layout); later (__mod layout) · **Confidence:** high (flat layout); medium (__mod layout, read only) · **Status:** confirmed

- **Rule:** A script at Windmill path `P` is up to three files:
  - `P.<ext>`, the source (extension per [SY-3](#sy-3));
  - `P.script.yaml`, the metadata (`P.script.json` when the tree was pulled in
    JSON mode);
  - `P.script.lock`, the lock, present only when the lock is non-empty.
  - If more than one source file matches `P.<any known ext>`, the CLI errors.
  - A multi-file script uses the folder layout `P__mod/` containing
    `script.<ext>`, `script.yaml`, `script.lock`, and extra modules directly
    under `P__mod/` at `<relPath>`, with their locks at
    `<relPath minus last extension>.lock`. There is no second nested `__mod/`.
    The `__mod` suffix does not depend on `nonDottedPaths`.
- **Evidence:** `backend/windmill-api/src/workspaces_export.rs:706-748, 785-787`;
  `cli/src/commands/sync/sync.ts:863-940, 1309-1311, 1401-1454`;
  `cli/src/commands/script/script.ts:632-695, 838-876`;
  `cli/src/utils/resource_folders.ts:505`.
- **Conformance:** [SY-2/a](conformance.md#sy-2)

<a id="sy-3"></a>
### SY-3 · Script language comes from the file extension

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:** `.script.yaml` has **no `language` field**. A path script's
  language is inferred from its source file name by **suffix tests in this
  order** (first match wins), with `defaultTs` from `wmill.yaml` (default
  `bun`):

  | suffix test | language |
  |---|---|
  | ends `.py` | `python3` |
  | ends `fetch.ts` | `nativets` |
  | ends `bun.ts` | `bun` |
  | ends `deno.ts` | `deno` |
  | ends `.ts` | `defaultTs` |
  | `.go` | `go` |
  | `.my.sql` / `.bq.sql` / `.odb.sql` / `.duckdb.sql` / `.sf.sql` / `.ms.sql` / `.pg.sql` | `mysql` / `bigquery` / `oracledb` / `duckdb` / `snowflake` / `mssql` / `postgresql` |
  | `.gql` | `graphql` |
  | `.sh` | `bash` |
  | `.ps1` | `powershell` |
  | `.php` / `.rs` / `.cs` / `.playbook.yml` / `.nu` / `.java` / `.rb` / `.r` | `php` / `rust` / `csharp` / `ansible` / `nu` / `java` / `ruby` / `rlang` |
  | anything else, including bare `.sql` | error `Cannot infer script language from extension …` (bare `.sql` adds a dialect hint) |

  - `fetch.ts`, `bun.ts` and `deno.ts` are tested **without a leading dot**,
    so `mybun.ts` is `bun` and `xdeno.ts` is `deno`.
  - Export writes the inverse: with `defaultTs: bun`, `bun` and `bunnative`
    scripts are `.ts` and `deno` scripts `.deno.ts`; with `defaultTs: deno`,
    `deno` scripts are `.ts` and `bun`/`bunnative` scripts `.bun.ts`;
    `nativets` is `.fetch.ts`.
  - `bunnative` never appears on disk. When a Bun-family script is deployed,
    the server sets `bunnative` if the source carries the `native` header
    annotation ([SY-V3](#sy-v3)) and `bun` otherwise. A loader must apply the
    same rule.
- **Evidence:** `cli/src/utils/script_common.ts:59-125` (inference order and
  errors); `backend/windmill-api/src/workspaces_export.rs:708-745` (export
  extensions); `backend/windmill-api-scripts/src/scripts.rs:1181-1190`
  (`bun`/`bunnative` from the annotation);
  `backend/windmill-common/src/worker.rs:967-974`;
  `cli/src/commands/sync/pull.ts:110` (CLI sends `default_ts`, default `bun`).
- **Conformance:** [SY-3/a–b](conformance.md#sy-3)

<a id="sy-4"></a>
### SY-4 · `.script.yaml` contents

**Scope:** core (schema, lock, kind, has_preprocessor, timeout, envs); later (cache_ttl, restart_unless_cancelled); out (tag, dedicated_worker, priority, concurrency, debouncing, permissions) · **Confidence:** high (key list, the default file); medium (regeneration on push, read not traced) · **Status:** drafted

- **Rule:**
  - Keys the CLI writes on pull (an absent key means "not set"): `summary`,
    `description`, `schema` (JSON Schema of `main`'s args), `lock`
    ([SY-5](#sy-5)), `kind` (`script` | `trigger` | `failure` | `approval` |
    `preprocessor`), `envs`, `cache_ttl`, `dedicated_worker`,
    `ws_error_handler_muted` (only when true; EE), `priority`, `tag`, `timeout`
    (seconds), `delete_after_secs`, `restart_unless_cancelled`,
    `visible_to_runner_only`, `has_preprocessor`, `on_behalf_of_email` (or
    `has_on_behalf_of: true` under `syncBehavior: v1`), `labels`, `extra_perms`
    (only if non-empty and preserved), flattened concurrency
    (`concurrency_key`, `concurrent_limit`, `concurrency_time_window_s`) and
    flattened debouncing (`debounce_key`, `debounce_delay_s`,
    `max_total_debouncing_time`, `max_total_debounces_amount`,
    `debounce_args_to_accumulate`).
  - CLI-written metadata never contains `path`, `language`, `content` or
    `hash`; `codebase` and `modules` are removed on pull. This describes what
    the CLI writes, not a prohibition on extra hand-written keys.
  - Legacy keys `is_template`, `no_main_func`, `auto_kind` may appear in old
    trees and mean nothing.
  - **A script without a metadata file** (no `<p>.script.json`, no
    `<p>.script.yaml`, and no `__mod` equivalent) is given one on push, and
    the push writes it into the tree:
    1. The CLI creates `<p>.script.yaml` with summary `""`, description `""`,
       kind `script`, `lock: '!inline <p>.script.lock'` and the schema
       skeleton
       `{"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object", "properties": {}, "required": []}`,
       plus an empty `<p>.script.lock`. Neither file is overwritten if it
       exists.
    2. `wmill sync push` then regenerates that metadata as
       `wmill generate-metadata` would: the schema is inferred from `main`'s
       parameters and, unless the script is a codebase script
       ([SY-8](#sy-8)), the lock is generated. The regenerated file is what
       the push sends.
    3. Only if regeneration fails does the default reach the server: the
       skeleton schema, and the lock as the literal text
       `!inline <p>.script.lock`, which is not resolved on this path.
  - The meaning of each key is in [execution.md](execution.md) and
    [languages.md](languages.md).
- **Evidence:** `backend/windmill-api/src/workspaces_export.rs:69-118, 777-782`;
  `backend/windmill-types/src/scripts.rs:251-257` (`ScriptKind`), `:520-574`
  (`NewScript`); `cli/src/commands/sync/sync.ts:1275-1311, 2253-2268, 3008-3016`;
  `cli/src/utils/metadata.ts:1119-1234` (`:1179-1195` the default file,
  `:1197-1228` regeneration and its failure path), `:197-230, 339-367`
  (schema and lock regeneration); `cli/bootstrap/script_bootstrap.ts:17-30`
  (the default values); `cli/src/commands/script/script.ts:419-435` (the push
  asks for regeneration), `:472-480` (the request body).
- **Conformance:** [SY-4/a–b](conformance.md#sy-4)
- **Design note (not Windmill behaviour):** a loader should ignore, not
  reject, extra hand-written keys.
- **Revision 2026-10-28 (source re-check):** the rule said a script without
  metadata gets "an empty schema" on push, without giving its text. The
  default file holds the skeleton above, not `{}`, and on `wmill sync push`
  that default is regenerated from the script's signature before it is sent,
  so the deployed schema is the inferred one. The key list is unchanged.

<a id="sy-5"></a>
### SY-5 · Script locks

**Scope:** core; later (JSON-mode metadata) · **Confidence:** high (YAML metadata); medium (JSON-mode metadata, read not traced) · **Status:** drafted

- **Rule:** In `.script.yaml`, `lock` is one of:
  - the string `!inline <p>.script.lock`, a path **relative to the sync root**
    (not to the script's directory), when the lock is non-empty and the script
    is not a codebase script;
  - `''` when the lock is empty;
  - absent when there is no lock or the script is a codebase script.
  - On push the referenced file is read; **a missing file counts as an empty
    lock**.
  - A lock may also be an array of strings (legacy), meaning the lines joined
    with `\n`.
  - **Resolving a reference in `.script.yaml`** (flow inline locks follow
    [SY-7](#sy-7) and [SY-V1](#sy-v1) instead):
    1. a legacy array is joined first;
    2. the string is a reference only if it **starts with exactly**
       `!inline ` (with the space). Leading whitespace is not tolerated,
       unlike a flow lock: ` !inline f/a/x.script.lock` is not a reference
       but the lock text itself, sent as written;
    3. the file is the second space-delimited token, read relative to the
       sync root. A missing or unreadable file gives `""`, with a warning;
    4. any other string is the lock text itself.
  - **JSON-mode metadata** (`<p>.script.json`, `later`): pull writes the same
    `!inline` reference, but push does not resolve it, so the reference text
    itself is sent as the lock.
  - The CLI generates locks only for `bun`, `python3`, `php`, `go`, `deno`,
    `rust` and `ansible` scripts (not `powershell`, although it has a
    dependency file). This is a CLI behaviour, not a backend limit
    ([OF-15](#of-15)).
- **Evidence:** `cli/src/commands/sync/sync.ts:1275-1295` (three forms),
  `:1406-1417`; `cli/src/utils/metadata.ts:758-809, 1071-1086` (`:1071-1085`
  the reference rule), `:1119-1176` (only the YAML branches resolve it);
  `cli/src/utils/utils.ts:179-186` (a failed read gives `""`);
  `cli/src/commands/script/script.ts:472-480` (the lock is sent as parsed);
  `cli/src/utils/script_common.ts:37-57`;
  `backend/windmill-types/src/scripts.rs:623-650` (string-or-array lock).
- **Conformance:** [SY-5/a–c](conformance.md#sy-5)
- **Revision 2026-10-28 (source re-check):** added how a `.script.yaml` lock
  reference is recognised and resolved. A reference needs the exact
  `!inline ` prefix, so the leading-whitespace tolerance of flow inline locks
  ([SY-V1](#sy-v1)) does not apply to script locks; JSON-mode metadata leaves
  the reference unresolved. The three forms and the missing-file rule are
  unchanged.

<a id="sy-6"></a>
### SY-6 · Flow folders and what a pulled `flow.yaml` contains

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - A flow at path `P` is the folder `P.flow/` (default) or `P__flow/` (when
    `wmill.yaml` has `nonDottedPaths: true`), containing `flow.yaml` plus
    inline-script files ([SY-7](#sy-7)).
  - `flow.yaml` is always YAML, even for trees pulled in JSON mode, and push
    reads only `<dir>/flow.yaml`.
  - A tree that mixes the two suffix styles, measured against the configured
    one, is rejected with `Found N directory(ies) using … format`.
  - `flow.yaml` holds `summary`, `description`, `value` (the `FlowValue`),
    `schema`, and optionally `tag`, `dedicated_worker`, `timeout`,
    `visible_to_runner_only`, `ws_error_handler_muted`, `labels`,
    `extra_perms`, and `on_behalf_of_email` or `has_on_behalf_of`.
  - A pulled `value` is the server's **full** form ([OF-3](#of-3)), never the
    lite form: the export selects `flow_version.value`, and the CLI only
    extracts inline content and locks, adjusts ownership metadata and writes
    the same flow out. Envelopes and containers are canonical with explicit
    defaults; `script`/`flow`/`identity` values and kept-lock rawscripts appear
    as last submitted; if the latest dependency job failed, the value is the
    raw submitted form. A hand-written tree may be in any of these states.
- **Evidence:** `cli/src/utils/resource_folders.ts:20-36, 167-175, 595-613`;
  `cli/src/commands/sync/sync.ts:953-1025, 1641-1646, 1827-1838`;
  `cli/src/commands/flow/flow.ts:38-50, 185-216`; `cli/src/types.ts:220-225`;
  `backend/windmill-api/src/workspaces_export.rs:360-379, 832-845`;
  `backend/windmill-types/src/flows.rs:26` (flow value kept as raw JSON).
- **Conformance:** [SY-6/a–c](conformance.md#sy-6)

<a id="sy-7"></a>
### SY-7 · Inline scripts inside a flow folder

**Scope:** core · **Confidence:** high · **Status:** drafted

- **Rule:**
  - **Reference form.** In `flow.yaml`, a rawscript's `content` is
    `!inline <file>` and a non-empty `lock` is `!inline <lockfile>`, both
    relative to the **flow folder** and written with `/`. Content is a
    reference when it starts with `!inline`; a lock when, after trimming
    leading whitespace, it starts with `!inline ` (with the space). The file
    path is the second space-separated token of the untrimmed string
    ([SY-V1](#sy-v1)), so paths cannot contain spaces. On disk the CLI writes
    quoted strings; the YAML tag form ([SY-9](#sy-9)) is accepted too.
  - **Coverage.** Extraction and re-inlining cover every rawscript reachable
    from `value.modules`, depth-first in order: for- and while-loop `modules`;
    every `branchall` branch; `branchone` branches, then `default`; AI-agent
    tools that are rawscript flow-module tools. Then `failure_module`, then
    `preprocessor_module`.
  - **File naming on pull (a convention, not a requirement).** Take the
    module's `summary`; if `summary` is absent (not merely empty), use the
    module `id`. Sanitise it:
    1. spaces become `_`;
    2. remove `/ \ : * ? " < > |` and control characters;
    3. collapse runs of `_`;
    4. trim leading and trailing `.` and `_`;
    5. lower-case;
    6. prefix `_` to Windows device names (`con`, `prn`, `aux`, `nul`,
       `com0`–`com9`, `lpt0`–`lpt9`).

    An empty result becomes `inline_script_0`. Duplicates are detected
    case-insensitively and renamed `<name>_<n>` with **one counter shared
    across the whole flow**, so summaries `Foo, Foo, Bar, Bar` give `foo`,
    `foo_1`, `bar`, `bar_2`. The file is `<name>.inline_script.<ext>`, or
    `<name>.<ext>` with `nonDottedPaths`. A fresh pull writes all files flat in
    the flow folder; its id-to-file mapping comes from the server JSON, which
    is normally empty (a directive in server content is refused as
    corruption). A local relock (`generate-metadata`) keeps the existing file
    names from the local `flow.yaml`.
  - **Inline extension map** (differs from [SY-3](#sy-3)): the `defaultTs`
    language and `bunnative` → `ts`; otherwise `bun` → `bun.ts`, `deno` →
    `deno.ts`, `nativets` → `native.ts`, `python3` → `py`, `go` → `go`, `bash`
    → `sh`, `powershell` → `ps1`, `postgresql` → `pg.sql`, `mysql` → `my.sql`,
    `bigquery` → `bq.sql`, `oracledb` → `odb.sql`, `snowflake` → `sf.sql`,
    `mssql` → `ms.sql`, `duckdb` → `duckdb.sql`, `graphql` → `gql`, `php` →
    `php`, `rust` → `rs`, `csharp` → `cs`, `nu` → `nu`, `ansible` →
    `playbook.yml`, `java` → `java`, `ruby` → `rb`, `rlang` → `r`.
  - **Lock file name:** the content file name minus the full language
    extension, plus `lock` (`x.inline_script.deno.ts` →
    `x.inline_script.lock`). The older `x.inline_script.deno.lock` naming is
    legacy.
  - **Push.**
    - The inline language comes from the module's `language` field, never
      from the file extension.
    - Re-inlining does anything only if `content` starts with `!inline`. If
      the content is already embedded source, a lock string `!inline …` is
      left as that literal string ([SY-V1](#sy-v1)).
    - Resolving a reference: read `<flowdir>/<file>`. Any relative path is
      accepted and joined to the flow folder, so subfolders work, and nothing
      checks containment. If the file is missing, try
      `<module id>.<everything after the first dot of file>`, also in the flow
      folder; if still missing, the push fails with
      `Cannot push flow <path>: missing inline script file(s): …`. A missing
      lock file becomes `""`.
    - The fallback is always tried. "After the first dot" counts the whole
      reference, directory part included (`sub.d/a.ts` gives
      `<id>.d/a.ts`), and a reference without a dot leaves nothing after it,
      so `!inline helper` falls back to the file `<id>.`: the module id and
      a trailing dot.
    - Sibling modules are re-inlined concurrently, so file reads have no
      guaranteed completion order (harmless: each read is independent).
- **Evidence:** `cli/windmill-utils-internal/src/inline-scripts/extractor.ts:17-210, 220-271`
  (reference form, traversal, lock naming, legacy lock, name mapping);
  `cli/windmill-utils-internal/src/path-utils/path-assigner.ts:13-58, 119-199`
  (extensions, sanitiser, dedupe);
  `cli/windmill-utils-internal/src/inline-scripts/replacer.ts:10-133`
  (content gate, resolution and fallback, concurrency);
  `cli/src/commands/flow/flow.ts:188-216` (base is the flow folder; failure and
  preprocessor; hard fail); `cli/src/commands/flow/flow_metadata.ts:250-259, 316-340`;
  `cli/src/commands/sync/sync.ts:955-992`.
- **Conformance:** [SY-7/a–f](conformance.md#sy-7); token edge cases under
  [SY-V1](conformance.md#sy-v1).
- **Design note (not Windmill behaviour):** windmill-lite should accept any
  path inside the flow folder and reject paths that escape the sync root (a
  labelled divergence: the CLI would read them).
- **Revision 2026-10-28 (source re-check):** added how the fallback name is
  formed: from the text after the first dot of the whole reference, so a
  reference without a dot falls back to `<id>.`, and a dot in a directory
  name shifts the fallback. The fallback was described before only for names
  with a dot.

<a id="sy-8"></a>
### SY-8 · Codebase scripts

**Scope:** core (Bun) · **Confidence:** high (selection, CLI gate, upload); medium (bundling details, read not traced) · **Status:** drafted

- **Rule:**
  - Codebase selection happens only for path scripts whose **inferred
    language is `bun`** ([SY-3](#sy-3): `.ts` under `defaultTs: bun`, or
    `.bun.ts`). `deno`, `nativets` and every other language are never bundled,
    even when they match a glob. A Bun script carrying the `native` annotation
    is still inferred as `bun` by the CLI, so it is bundled if it matches; the
    server then marks it `bunnative`.
  - Entries of `codebases[]` in `wmill.yaml` are tried **in YAML order; the
    first match wins**. An entry with no `includes` (absent or `null`)
    matches every candidate; otherwise an include glob (minimatch on the
    script's `.ts` file path, [SY-16](#sy-16)) must match, and any matching
    `excludes` glob vetoes. `includes: []` is a list without entries, so that
    entry never matches and the next one is tried. A single string in
    `includes` or `excludes` counts as a one-element list.
  - The CLI bundles a codebase script locally with the entry's settings
    (`relative_path` is the digested shared-source root; `format` is `cjs` or
    `esm`; also `external`, `inject`, `define`, `loader`, `banner`,
    `customBundler`, `assets[{from,to}]`).
  - The push sends the normal script body, **including its source
    `content`**, together with the bundle as a second multipart part. The
    server keeps both and executes the bundle ([languages.md](languages.md)).
  - Codebase metadata has no `lock`, and `codebase` is stripped on pull, so on
    disk a codebase script is recognised **only** by the `wmill.yaml` match.
- **Evidence:** `cli/src/commands/sync/sync.ts:376-412` (codebase lookup),
  `:1275-1298`; `cli/src/core/conf.ts:114-131` (`Codebase`);
  `cli/src/commands/script/script.ts:315-318` (Bun gate), `:317-418`
  (bundling), `:472-476` (source in the body), `:803-819` (multipart upload);
  `cli/src/utils/codebase.ts:22-44` (digest);
  `backend/windmill-worker/src/worker.rs:5381-5398` (Bun and Bunnative pass the
  codebase to the Bun executor).
- **Conformance:** [SY-8/a–c](conformance.md#sy-8)
- **Revision 2026-10-28 (source re-check):** stated what `includes: []` does
  (the entry never matches), that a string counts as a one-element list, and
  which glob options apply ([SY-16](#sy-16)). The rule said only that an
  entry without `includes` matches everything.

<a id="sy-9"></a>
### SY-9 · YAML dialect

**Scope:** core · **Confidence:** high (tags and library; scalar cases checked against the yaml 2.8.3 package, not end to end) · **Status:** confirmed

- **Rule:** The CLI parses YAML with the npm `yaml` package v2 (`^2.7.0`) using
  its defaults (YAML 1.2, core schema) plus two custom scalar tags:
  `!inline <v>` resolves to the string `"!inline <v>"` and
  `!inline_fileset <v>` to `"!inline_fileset <v>"`. A loader must produce the
  same values:
  - there is no timestamp type, so dates stay strings;
  - only `true`/`false` (and case variants) are booleans; `yes`, `no`, `on`,
    `off` are strings;
  - integers are decimal digits, `0o…` octal or `0x…` hex; `017` is 17, and
    `1_000` and `0b11` are strings;
  - `<<` merge keys are not special (merging is off in YAML 1.2), so `<<`
    stays a literal key;
  - duplicate keys are an error;
  - numbers become JavaScript doubles before reaching the server, so integers
    above 2^53 lose precision.
  - The writer emits single-quoted strings (for example `'!inline x.ts'`), no
    anchors, and a fixed key order (`id`, `version`, `type`, `summary`, `name`,
    `display_name`, `description`, `value`, `content`, `modules`,
    `failure_module`, `input_transforms` first; the rest alphabetical, with
    `lock`/`locks` sorted late). Change detection compares parsed values, so
    formatting is not significant. Writer settings are separate from decode
    compatibility.
- **Evidence:** `cli/src/utils/yaml.ts:1-51`; `cli/package.json:47`;
  `cli/src/commands/sync/sync.ts:508-533` (key order), `:2324-2329` (deep
  compare); `cli/test/fixtures/yaml-snapshots/flow_inline_tags.yaml`.
- **Conformance:** [SY-9/a–c](conformance.md#sy-9)
- **Design note (not Windmill behaviour):** a Go YAML library's defaults may
  differ (octal, underscores, binary, merge keys), so the loader needs its own
  scalar resolution, pinned by these fixtures against whichever library is
  chosen ([Q-OF-6](#q-of-6)).

<a id="sy-10"></a>
### SY-10 · Resources and resource types

**Scope:** core (value lookup); later (file and fileset resources) · **Confidence:** medium (read, not traced) · **Status:** drafted

- **Rule:**
  - A resource is `P.resource.yaml` with `value`, `description`,
    `resource_type`, `labels`.
  - If its resource type has a `format_extension`, `value.content` is
    `!inline <P>.resource.file.<ext>` (relative to the **sync root**) and a
    sibling file holds the text.
  - If the type `is_fileset`, `value` is `!inline_fileset <P>.fileset` and the
    files live under that folder.
  - Resource types are `<name>.resource-type.yaml` **at the root only**, with
    `schema`, `description`, `format_extension`, `is_fileset`. Built-in hub
    resource types are not in the tree.
  - Values may contain `$var:`/`$res:` strings, which stay literal on disk and
    are resolved at run time ([expressions.md](expressions.md),
    [client-api.md](client-api.md)). Host resolution policy is separate from
    file parsing.
  - **A resource file without `value`, or with `value: null`, fails the
    push.** Before creating the resource the CLI looks for an inline
    `content` reference inside `value`, and reading a field of a missing or
    `null` value throws a JavaScript `TypeError` (`Cannot read properties of
    undefined (reading 'content')`, or `… of null …`), so the push stops at
    that item. Any other value, including a number or a plain string, is
    sent as written. (The server alone would accept a create request without
    `value` and store `null`; the CLI never sends one.) `resource_type` is
    required by the server.
- **Evidence:** `backend/windmill-api/src/workspaces_export.rs:792-829`;
  `cli/src/commands/sync/sync.ts:1323-1339, 1470-1510, 2520-2525, 2600-2602`;
  `cli/src/commands/resource/resource.ts:68-94` (`:69-72` the inline-content
  check that throws), `:109-130` (create);
  `backend/windmill-store/src/resources.rs:173-181` (`CreateResource`: `value`
  optional, `resource_type` required), `:1059-1060` (an absent value is
  stored as `null`).
- **Conformance:** [SY-10/a–b](conformance.md#sy-10)
- **Revision 2026-10-28 (source re-check):** added what a resource file
  without `value` does. It fails the CLI push; it does not deploy a resource
  whose value is `null`. The rest of the rule is unchanged.

<a id="sy-11"></a>
### SY-11 · Variables and secrets

**Scope:** core · **Confidence:** high; medium (required fields, read not traced through the CLI's error) · **Status:** drafted

- **Rule:**
  - A variable is `P.variable.yaml` with `value`, `is_secret`, `description`,
    `account`, `is_oauth`, `expires_at`, `labels`.
  - Variables with an expiry are never exported.
  - For a secret, `value` is **ciphertext under the workspace encryption
    key**, unless the tree was pulled with plain secrets, in which case it is
    the plain value.
  - With `skipSecrets`, which `wmill init` sets by default, secret variables
    are absent altogether.
  - The key itself (`encryption_key.yaml`) is exported only with `includeKey`.
  - So a folder alone generally cannot supply secret values.
  - **A new variable needs `value` (a string), `is_secret` (a boolean) and
    `description` (a string).** The CLI sends the file's keys as they are,
    and the server refuses a create request missing any of them, naming the
    first missing field in that order: a variable file without `value` fails
    the push with ``missing field `value` ``. There is no default of `""`.
    (Updating a variable that already exists, without `value`, leaves its
    value unchanged; a fresh deploy always creates.)
- **Evidence:** `backend/windmill-api/src/workspaces_export.rs:850-877`
  (variable export; decryption only with plain secrets), `:1607` (key file);
  `cli/src/commands/variable/variable.ts:113-125, 176-196` (`:185-196` create
  with the file's keys);
  `backend/windmill-common/src/variables.rs:99-113` (`CreateVariable`: `value`,
  `is_secret` and `description` have no default);
  `backend/windmill-store/src/variables.rs:568-575` (create decodes that body),
  `:1033-1042` (an update's fields are all optional);
  `cli/src/commands/sync/pull.ts:105-107`; `cli/src/commands/init/template.ts:93`.
- **Conformance:** [SY-11/a–b](conformance.md#sy-11)
- **Design note (not Windmill behaviour):** windmill-lite needs a pluggable
  secret resolver (host-supplied values, the key, or delegation to a real
  Windmill); trees pulled with plain secrets must stay usable. A
  "secret not available" error is a windmill-lite policy, not an upstream
  string. Whether windmill-lite ever decrypts exported ciphertext is a design
  decision; this area specifies no cipher.
- **Revision 2026-10-28 (source re-check):** added the fields a new variable
  must carry. A variable file without `value` fails the push; it does not
  read as `""`. The rest of the rule is unchanged.

<a id="sy-12"></a>
### SY-12 · Other files a loader must recognise (and may ignore)

**Scope:** later (dependencies/ and per-workspace overrides); out (the rest) · **Confidence:** medium · **Status:** confirmed

- **Rule:** Named so that a loader can classify and skip them without
  misreading them as scripts:
  - schedules: `P.schedule.yaml`;
  - triggers: `P.<kind>_trigger.yaml`, kind ∈ {`http`, `websocket`, `kafka`,
    `nats`, `postgres`, `mqtt`, `amqp`, `sqs`, `gcp`, `azure`, `email`};
  - native triggers:
    `<script_path>.<flow|script>.<external_id>.<service>_native_trigger.yaml`;
  - folders: `f/<name>/folder.meta.yaml`;
  - users and groups: `users/<email>.user.yaml`, `groups/<name>.group.yaml`;
  - `settings.yaml`, `encryption_key.yaml`;
  - apps `P.app/` or `P__app/`; raw apps `P.raw_app/` or `P__raw_app/`;
  - workspace dependency files
    `dependencies/[<name>.]{package.json|requirements.in|composer.json|go.mod|modules.json}`;
  - datatable migrations under `migrations/datatable/…`;
  - per-workspace override files `<base>.<workspace>.<type>.yaml`,
    `folder.<workspace>.meta.yaml`, `settings.<workspace>.yaml`, where the
    workspace name has `/ \ : * ? " < > | .` replaced by `_`.
  - An override file must not load as a separate item: it replaces
    `<base>.<type>.yaml` when that workspace is selected. Selection depends on
    the effective workspace and `specificItems` configuration, not merely on
    recognising a dotted file name.
- **Evidence:** `cli/src/types.ts:59-71, 322-486`;
  `backend/windmill-api/src/workspaces_export.rs:684-691, 956-1325, 1342-1421, 1607-1634`;
  `backend/windmill-common/src/workspace_dependencies.rs:339-353`;
  `cli/src/core/specific_items.ts:298-339, 466-474`;
  `cli/src/commands/sync/sync.ts:1708-1766`;
  `windmill-yaml-validator/src/validation/yaml-validator.ts:49-73`.
- **Conformance:** [SY-12/a](conformance.md#sy-12)
- **Note:** `dependencies/` matters because it affects lock generation
  ([OF-V6](#of-v6)).

<a id="sy-13"></a>
### SY-13 · `wmill.yaml`

**Scope:** core (defaultTs, nonDottedPaths, codebases, includes/excludes/extraIncludes, skip flags, no `wmill.yaml`); later (workspaces); out (the rest) · **Confidence:** high (keys, defaults, filters, no `wmill.yaml`); medium (override merging) · **Status:** drafted

- **Rule:** Keys, from the CLI's sync options and codebase types:
  - `defaultTs`: `bun` | `deno`; absent means `bun`, with a warning.
  - Filters: `includes`, `excludes`, `extraIncludes` (minimatch globs over the
    file's relative path); exact semantics below.
  - Skip and include switches: `skipVariables`, `skipResources`,
    `skipResourceTypes`, `skipSecrets`, `skipScripts`, `skipFlows`,
    `skipApps`, `skipFolders`, `skipWorkspaceDependencies`;
    `includeSchedules`, `includeTriggers`, `includeUsers`, `includeGroups`,
    `includeSettings`, `includeKey`.
  - Layout: `nonDottedPaths` (absent means `false`), `json`.
  - Behaviour: `syncBehavior` (only `v1` exists), `parallel`, `locksRequired`,
    `lint`, `plainSecrets`, `message`, `promotion`, `skipBranchValidation`.
  - `codebases[]` ([SY-8](#sy-8)).
  - `workspaces: {<name>: {gitBranch, workspaceId, baseUrl, overrides, promotionOverrides, specificItems{…}}}`
    plus `commonSpecificItems`. The deprecated `gitBranches`, `environments`
    and `git_branches` are normalised to `workspaces`; a non-empty top-level
    `overrides` is rejected.
  - `wmill init` writes `includes: ["f/**"]`, `skipSecrets: true`,
    `nonDottedPaths: true`, `syncBehavior: v1`. A JSON Schema ships as
    `cli/wmill.schema.json`.
  - **Filter semantics** (for configured workspace paths, files only, never
    directories):
    1. **Bypasses, applied first:** root `*.resource-type.yaml` (or `.json` in
       JSON mode) is governed only by `skipResourceTypes`; user, group,
       settings and encryption-key files are kept when `includeUsers`,
       `includeGroups`, `includeSettings` or `includeKey` is set; workspace
       dependency files are kept unless `skipWorkspaceDependencies`.
    2. **Path filter** ([SY-V2](#sy-v2)), active only if `includes` or
       `excludes` is a non-empty list. When active, a file passes if all hold:
       `includes` is **absent** or some include matches (an explicitly empty
       `includes: []` matches nothing); no exclude matches; `extraIncludes` is
       absent or empty, or some entry matches.
    3. `extraIncludes` alone does not activate the filter; it only narrows an
       active one.
    4. Glob options and the matched path are [SY-16](#sy-16); which files of
       an item the filter decides by is [SY-17](#sy-17).
  - **No `wmill.yaml`.** When the upward search ([SY-1](#sy-1)) finds none,
    the CLI does not fail. It warns
    `No wmill.yaml found. Use 'wmill init' to bootstrap it.` and runs with no
    settings, so every key takes its absent value unless a command-line
    option sets it:
    - the working directory is the root (no directory change);
    - `defaultTs` is `bun` and `nonDottedPaths` is `false`;
    - no path filter applies: every file the walk recognises
      ([SY-18](#sy-18)) is in;
    - there are no codebases;
    - every skip and include switch is off, so secret variables in the tree
      are pushed, and schedules, triggers, users, groups, settings and the
      key are not.
- **Evidence:** `cli/src/core/conf.ts:21-43, 67-131, 218-294, 539-627`
  (`:201-214` no file: a warning and empty settings; `:382-387` merge with the
  command-line options);
  `cli/src/commands/init/template.ts:78-226`;
  `cli/src/commands/sync/sync.ts:1708-1789, 2570-2588` (filter), `:2598-2635`
  (bypasses and final rule), `:3690`, `:3848` (push base: the working
  directory); `cli/src/utils/resource_folders.ts:41` (`nonDottedPaths`
  starts `false`).
- **Conformance:** [SY-13/a–b](conformance.md#sy-13); filter edge cases under
  [SY-V2](conformance.md#sy-v2).
- **Revision 2026-10-28 (source re-check):** added the case of no
  `wmill.yaml`, which runs with defaults rather than failing, and pointers to
  the glob and per-file rules. The keys and filter rules are unchanged.

<a id="sy-14"></a>
### SY-14 · `wmill-lock.yaml` is a CLI cache, not part of the model

**Scope:** later (optional staleness check); out (as data) · **Confidence:** medium (format read; hash inputs not traced byte for byte) · **Status:** confirmed

- **Rule:**
  - The file is `{version: v2, locks: {<key>: <sha256 hex>}}`. Keys are flat:
    `<path>` or `<path>+<subpath>`, with `/` separators and no `./` prefix.
    Legacy files have no version and nested maps. Any version other than `v1`
    or `v2` is refused, and some malformed shapes are rejected.
  - Script entry: hash of the workspace dependencies, the script text and the
    raw metadata text. Multi-file scripts also have per-module hashes and
    `__script_hash`.
  - Flow entry: keyed by the flow folder; one entry per inline file, plus
    `__flow_hash` over the sorted per-file map.
  - The file records when the CLI last regenerated locks and schemas and
    drives "stale metadata" warnings on push. The server never reads it.
- **Evidence:** `cli/src/utils/metadata.ts:247-289, 423-443, 1235-1404`;
  `cli/src/commands/flow/flow_metadata.ts:39, 73-96, 376-386`;
  `cli/src/commands/sync/sync.ts:3248-3403, 3891-3901, 4050-4086`.
- **Conformance:** [SY-14/a](conformance.md#sy-14)
- **Design note (not Windmill behaviour):** a loader may ignore the file, or
  use it only to warn that locks are stale; tolerating a missing or corrupt
  cache is a loader policy.

<a id="sy-15"></a>
### SY-15 · What a folder cannot supply

**Scope:** core · **Confidence:** high · **Status:** drafted

- **Rule:** These references cannot be resolved from the tree alone:
  - `script` or `flow` steps whose path is `hub/…` (an accepted `hub/` prefix,
    [OF-2](#of-2), does not make every hub path runnable);
  - a `script` step with any `hash` pin: a folder cannot tell which version
    a hash names, nor whether a deploy of the folder would produce it
    ([OF-14](#of-14));
  - steps referencing paths excluded by `includes` or `skip*`, or living in
    another workspace;
  - secret variables ([SY-11](#sy-11));
  - built-in resource types ([SY-10](#sy-10));
  - inline scripts and scripts with an empty lock that Windmill would lock on
    deploy ([OF-15](#of-15), [SY-5](#sy-5)), where the language and runtime
    actually need a lock.
- **Evidence:** SY-5, SY-10, SY-11, OF-2, OF-14, OF-15.
- **Conformance:** [SY-15/a](conformance.md#sy-15)
- **Design note (not Windmill behaviour):** each needs a windmill-lite policy:
  an error, a host-supplied value, or delegation.
- **Revision 2026-10-28 (source re-check):** the hash bullet said only a hash
  "other than the folder version" was unresolvable, implying a folder could
  recognise its own version's hash. It cannot: hashes depend on the deploy
  history ([OF-14](#of-14)), so every `hash` pin is outside what a folder can
  supply.

<a id="sy-16"></a>
### SY-16 · Globs are minimatch 10 with default options, over the root-relative file path

**Scope:** core · **Confidence:** high (options and subject, source-traced; oracle pending); medium (the bundled version: the published bundle was not inspected) · **Status:** drafted

- **Rule:**
  - **Library.** The CLI calls minimatch 10. Its manifest asks for
    `^10.0.0`, both of its lock files pin `10.2.2`, and the published CLI is
    a bundle built from that install, with minimatch inside it.
  - **Options.** Every `wmill.yaml` glob is matched with minimatch's
    **default options**: `includes`, `excludes` and `extraIncludes`
    ([SY-13](#sy-13)), and a codebase entry's `includes` and `excludes`
    ([SY-8](#sy-8)). In particular:
    - matching is case-sensitive;
    - `*`, `**` and `?` do not match a path segment that starts with `.`
      unless the pattern spells the dot;
    - a pattern without `/` is matched against the whole path, not the file
      name, so `*.ts` matches only a file at the root;
    - braces expand;
    - a leading `!` negates the pattern, and a pattern starting with `#`
      matches nothing.
  - **Subject.** The string matched is the file's path relative to the
    push's base directory (the sync root of [SY-1](#sy-1), or the working
    directory under `noCdToRoot`, [SY-V4](#sy-v4)), with no leading `./`, in
    the platform's separator (`/` on Linux and macOS). Directories are never
    matched ([SY-17](#sy-17)). For a codebase entry the subject is the
    script's content file path, and only paths ending in `.ts` are tried.
- **Evidence:** `cli/package.json:26`; `cli/bun.lock:253`;
  `cli/package-lock.json:1204-1207`; `cli/build-npm.ts:30-41` (everything but
  esbuild, svelte and the parser packages is bundled);
  `cli/src/commands/sync/sync.ts:12`, `:376-412` (codebase globs), `:2556-2584`
  (filter globs), `:463-505` (`:475` the relative path), `:3848` (base).
- **Conformance:** [SY-16/a](conformance.md#sy-16)
- **Note:** windmill-lite's loader observed the same glob results on
  minimatch 10.2.6 and 9.0.9; the reference cases name only the options.

<a id="sy-17"></a>
### SY-17 · The filter decides file by file; a passing file pushes its whole item

**Scope:** core; later (the `__mod` layout) · **Confidence:** high (source-traced; oracle pending) · **Status:** drafted

- **Rule:**
  - The path filter of [SY-13](#sy-13) is applied to each file on its own,
    by the file's path ([SY-16](#sy-16)). Directories are not filtered: the
    walk enters every recognised directory.
  - `wmill sync push` compares the local files that pass with the
    workspace's files, filtered by the same function, and pushes an **item**
    when one of its passing files is new or has changed and that file is
    eligible to select it. The push then reads the item's other files from
    disk, whether or not they pass the filter.
  - **New files that never select an item.** Outside raw apps, a new file
    whose name ends in `.script.yaml`, `.script.json` or `.lock`, or a new
    `<p>.resource.file.<ext>`, is skipped before its item is considered.
    `.lock` covers every lock file: a path script's `.script.lock` and a
    flow's inline-script lock alike. A changed file of these kinds is not
    skipped. Which files select an item:

    | Item | When the file is new | When the file changed |
    |---|---|---|
    | path script | its content file only | its content, metadata or lock file |
    | flow | `flow.yaml` or an inline script's content file, never its lock file | any file in its folder |
    | resource | its `.resource.yaml` | the resource file or its content file |
    | variable, resource type and other one-file items | the item's file | the same |

    In the `__mod` layout (`later`) any module file also selects its script.
  - So on a plain `wmill sync push` into an empty workspace, a script is
    deployed exactly when its content file passes, and a flow when its
    `flow.yaml` or one of its inline content files passes; a flow whose only
    passing files are locks is not deployed. An item none of whose files
    pass is neither pushed nor deleted.
- **Evidence:** `cli/src/commands/sync/sync.ts:1541-1594` (each entry is
  tested on its own), `:1637-1652`, `:2630-2635` (directories), `:4453-4500`
  (a changed file: no such exclusion), `:4604-4650` (a new file;
  `:4618-4625` metadata, every `.lock` and resource content files skipped);
  `cli/src/commands/script/script.ts:241-276`
  (metadata and lock files push their script), `:286-306`, `:419-435`,
  `:449` (the script's files are read from disk); `cli/src/types.ts:189-233`
  (a flow file pushes the whole folder); `cli/src/commands/flow/flow.ts:188-206`.
- **Conformance:** [SY-17/a–b](conformance.md#sy-17)
- **Revision 2026-10-28 (validation round 1):** the table said any new file
  in a flow folder selects the flow. A new lock file never does, inline
  locks included, because every new `.lock` file is skipped before item
  selection, like new metadata files. The exclusions are now stated before
  the table, and SY-17/b covers a flow whose only passing file is its lock.

<a id="sy-18"></a>
### SY-18 · Which files the walk reads, and as what

**Scope:** core · **Confidence:** high (classification and extension list, source-traced); medium (the failing push of a `.js` file, read not traced) · **Status:** drafted

- **Rule:** A file under the recognised top-level directories of
  [SY-1](#sy-1) enters the push only when all of these hold:
  1. **Its path classifies as an item file.** First match wins:
     - inside a `__mod/` folder: a module file of that script;
     - inside a flow, app or raw-app folder (`.flow/`, `__flow/`, `.app/`,
       …): part of that item;
     - under `dependencies/`: a workspace dependency file; under
       `migrations/datatable/`: a datatable migration;
     - `<p>.resource.file.<ext>`, or a file inside a fileset folder: part of
       that resource;
     - a last extension of `.ts`, `.py`, `.go`, `.sh`, `.sql`, `.gql`,
       `.ps1`, `.js`, `.php`, `.rs`, `.cs`, `.nu`, `.java`, `.rb` or `.r`, or
       a name ending `.playbook.yml`: a script source;
     - `folder.meta.yaml`; at the root only, `settings.yaml`,
       `encryption_key.yaml` and `*.resource-type.yaml` (with their
       per-workspace variants, [SY-12](#sy-12));
     - otherwise the last dotted part of the name before its extension:
       `script` (metadata and lock files), `variable`, `resource`,
       `resource-type`, `app`, `schedule`, a trigger kind of
       [SY-12](#sy-12) or `<service>_native_trigger`, `user`, `group`.

     Any other file is ignored without a message: for example `notes.md`,
     `x.yml`, or `x.yaml` without a type part.
  2. **It passes the path filter** ([SY-13](#sy-13), [SY-17](#sy-17)).
  3. **Its last extension is listed.** Except for file and fileset resource
     files, raw-app files and workspace dependency files, the text after the
     file's last dot must be one of `json`, `yaml`, `go`, `sh`, `ts`, `py`,
     `sql`, `gql`, `ps1`, `php`, `js`, `lock`, `rs`, `cs`, `yml`, `nu`,
     `java`, `rb`, `r`. `.json` files are then dropped in YAML mode and
     `.yaml` files in JSON mode. This also drops, for instance, a `README.md`
     inside a flow folder.
  4. **No skip or include switch of [SY-13](#sy-13) drops it.**
  - Two script-like names make the push fail rather than being ignored:
    - a bare `.sql` file, with [SY-3](#sy-3)'s language error;
    - a `.js` file outside an item folder. It classifies as a script source,
      but no language matches it, so the push stops with
      `The item <path> has an unrecognized type ending script`.
- **Evidence:** `cli/src/commands/sync/sync.ts:2487-2539` (`isNotWmillFile`:
  unclassified files are ignored), `:2541-2554` (top-level directories),
  `:1655-1691` (extension list and JSON or YAML mode), `:1710-1766` (switches),
  `:4604-4690` (a new file's push); `cli/src/types.ts:322-439`
  (classification), `:189-272` (no push for a `script` type ending);
  `cli/src/commands/script/script.ts:286-306`, `:942-968` (the script
  extensions, without `.js`); `cli/src/utils/script_common.ts:59-125`.
- **Conformance:** [SY-18/a–b](conformance.md#sy-18)
- **Note:** this enumerates the list that [SY-1](#sy-1) cites as
  `sync.ts:1668-1693`.

<a id="sy-19"></a>
### SY-19 · Text files: a UTF-8 byte order mark is stripped, UTF-16 and UTF-32 are refused

**Scope:** core · **Confidence:** high (decoder and the reference readers, source-traced; oracle pending); medium (the metadata-file outcome, read not traced) · **Status:** drafted

- **Rule:** The CLI reads every text file of the tree through one decoder:
  script sources, metadata, `flow.yaml`, inline script and lock files, lock
  references, resource and variable files, and `wmill.yaml` itself.
  - **The decoder.**
    - A leading UTF-8 byte order mark (bytes `EF BB BF`) is removed, so no
      content or value starts with U+FEFF. A U+FEFF anywhere else is kept.
    - A leading UTF-16 or UTF-32 byte order mark is refused with
      `File <path> is encoded as UTF-16 LE, which is not supported. Please convert it to UTF-8.`,
      or the same with `UTF-16 BE`, `UTF-32 LE` or `UTF-32 BE`. The bytes
      `FF FE 00 00` count as UTF-32 LE. `<path>` is the path the caller
      opened, which may be absolute.
    - Otherwise the bytes are decoded as UTF-8, and each invalid sequence
      becomes U+FFFD.
  - **What a refusal does depends on the caller:**

    | How the file is reached | Outcome |
    |---|---|
    | by the walk: a file that passes the filter and the extension list ([SY-18](#sy-18)) is read to compare with the workspace | the push fails before any item is pushed |
    | as a selected script's content file, read again for the push | the push fails |
    | as `flow.yaml` of a selected flow, or `wmill.yaml` | the push fails (for `wmill.yaml`, with `YAML syntax error in wmill.yaml` around the decoder's message) |
    | as a selected script's `.script.yaml` | the push fails, but with an error about the default metadata file the CLI then tries to create beside it ([SY-4](#sy-4)), not with the decoder's message |
    | as a script lock reference ([SY-5](#sy-5)) or an inline resource `content` reference ([SY-10](#sy-10)) | a warning `Error reading inline path: <path>, <error>`, and the value becomes `""` |
    | as a flow's inline content file ([SY-7](#sy-7)) | read as missing: the fallback name is tried, then the push fails with `missing inline script file(s)` |
    | as a flow's inline lock file ([SY-7](#sy-7)) | a warning, and the lock becomes `""` |

    So a refused file fails the push when the walk or the item's own push
    reads it directly, and reads as `""` when it is reached only as a lock
    or resource-content reference that the walk did not select.
- **Evidence:** `cli/src/utils/utils.ts:135-177` (decoder and readers),
  `:179-186` (the reference reader catches, warns and returns `""`);
  `cli/src/utils/metadata.ts:1071-1085` (script lock references),
  `:1119-1195` (an unreadable `.script.yaml` falls through to creating the
  default file, refused because it exists: `:1194`);
  `cli/src/commands/resource/resource.ts:92` (resource content references);
  `cli/src/utils/yaml.ts:27-35` (YAML files); `cli/src/core/conf.ts:299-316`
  (`wmill.yaml`); `cli/src/commands/sync/sync.ts:491-501`, `:1763` (files read
  by the walk); `cli/src/commands/script/script.ts:449` (script content);
  `cli/src/commands/flow/flow.ts:188-190` (flow folder files);
  `cli/windmill-utils-internal/src/inline-scripts/replacer.ts:27-37` (inline
  content), `:46-53` (inline lock).
- **Conformance:** [SY-19/a–c](conformance.md#sy-19)
- **Revision 2026-10-28 (validation round 1):** the rule said a UTF-16 or
  UTF-32 byte order mark fails the read "and with it the push" for every
  file, lock and resource references included. The decoder refuses the
  file, but the reference reader catches the refusal and gives `""`, as
  [SY-5](#sy-5) states for an unreadable lock. The outcome is now given per
  caller, and SY-19/c covers a refused lock reached only as a reference.

<a id="sy-v1"></a>
### SY-V1 · Inline lock resolution depends on inline content

**Scope:** core (the ordinary quoted or tagged directive path) · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - Flow push's rawscript replacement returns immediately unless `content`
    begins with `!inline`. A lock directive is therefore not resolved when the
    content is already embedded source; the lock stays the literal string.
  - Directive paths are the **second space-delimited token**, not the whole
    remainder of the line. Lock recognition tests the string with leading
    whitespace trimmed, but path extraction uses the untrimmed string: one
    leading space selects the token `!inline` as the file name, two leading
    spaces select an empty name. In both cases the file is normally absent and
    the lock becomes `""`. Content paths follow the same token rule.
  - A fresh pull writes the well-formed one-space form. Existing file-name
    mappings take precedence over newly generated names.
  - Malformed forms are not whitespace-tolerant; their compatibility is
    documented here, not a supported input style.
- **Evidence:** `cli/windmill-utils-internal/src/inline-scripts/replacer.ts:10-57`
  (`:19-24, 39-54`); `cli/windmill-utils-internal/src/inline-scripts/extractor.ts:27-60`;
  `cli/src/commands/sync/sync.ts:957-992`.
- **Conformance:** [SY-V1/a–d](conformance.md#sy-v1)

<a id="sy-v2"></a>
### SY-V2 · Filters distinguish absent `includes` from empty `includes`

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - Path filtering activates when `includes` or `excludes` has entries.
  - With filtering active, an omitted `includes` permits ordinary files unless
    another filter excludes them; an explicitly empty `includes` array permits
    none.
  - `extraIncludes` additionally narrows an active filter and has no effect
    alone.
  - Resource-type files bypass path globs and use `skipResourceTypes`; enabled
    users, groups, settings and key, and non-skipped workspace dependencies,
    have their own bypasses. These exceptions precede ordinary path filtering
    ([SY-13](#sy-13)).
- **Evidence:** `cli/src/commands/sync/sync.ts:2570-2635`. Prior art differs:
  windmill-ts `src/source/local/wmillConfig.ts:132-156 @ af2b1e325320`
  ([PA-1](#pa-1)).
- **Conformance:** [SY-V2/a–d](conformance.md#sy-v2)

<a id="sy-v3"></a>
### SY-V3 · Native annotation spelling and position decide the Bun runtime

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:** `native` is a case-sensitive TypeScript header annotation.
  - Leading blank lines are skipped. The header region is the leading run of
    lines that start exactly with `//`; the first non-blank line that does not
    (including an indented `//` line, or any code) ends it. Ordinary `//`
    comments inside the header do not end it.
  - For each header comment the key is the comment text trimmed, then cut at
    the first `=` and not trimmed again. A key equal to `native` enables the
    annotation; any `=` value is ignored for this boolean flag. Whitespace
    immediately before `=` stays in the key and prevents a match.
  - A `native`-looking comment after executable code is not an annotation.
- **Evidence:** `backend/windmill-common/src/worker.rs:967-974`;
  `backend/windmill-macros/src/lib.rs:48-83`;
  `backend/windmill-api-scripts/src/scripts.rs:1181-1190`;
  `backend/windmill-worker/src/worker_lockfiles.rs:1762-1772`.
- **Conformance:** [SY-V3/a–c](conformance.md#sy-v3)

<a id="sy-v4"></a>
### SY-V4 · Sync item paths stay relative to the working directory under `noCdToRoot`

**Scope:** later · **Confidence:** high (source-traced; no sync command run) · **Status:** confirmed

- **Rule:**
  - Configuration discovery can read a parent's `wmill.yaml` while
    `noCdToRoot` leaves the working directory in a child ([SY-1](#sy-1)).
  - Sync push then walks the working directory and computes relative item
    paths from it. Pull destinations are also based on the working directory
    (its comparison state may use that directory's `.wmill`).
  - The CLI does not adopt the discovered config directory as the filesystem
    base when the directory change is suppressed.
- **Evidence:** `cli/src/core/conf.ts:146-204`;
  `cli/src/commands/sync/sync.ts:464-505` (relative paths), `:3019-3020, 3124-3125`
  (pull bases and destinations), `:3689-3690, 3848-3861` (push config and base).
- **Conformance:** [SY-V4/a](conformance.md#sy-v4)
- **Design note (not Windmill behaviour):** windmill-lite's proposed
  config-directory root ([SY-1](#sy-1)) is a deliberate difference for this
  option.

## C. Prior art: windmill-ts

[windmill-ts](https://github.com/invakid404/windmill-ts) (Unlicense,
TypeScript/Node) generates typed TypeScript clients from a workspace's JSON
Schemas. Its local-folder mode, on `master` and not in the v1.8.2 release, can
read a sync folder instead of a live workspace. All pointers below are at
commit `af2b1e325320` on `master`. These findings describe windmill-ts, not
Windmill.

<a id="pa-1"></a>
### PA-1 · What windmill-ts does, and where it diverges from the CLI

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - windmill-ts's local discovery:
    - metadata suffixes `*.script.yaml|json`, `*.resource.yaml|json`, flat
      `*.flow.json`, and flow folders named `*.flow` **or** `*__flow`
      containing `flow.yaml`/`flow.json`; resource types
      `*.resource-type.yaml|json` only at the root;
    - Windmill path = root-relative POSIX path minus the suffix or folder
      marker (`f/demo/x.flow/flow.yaml` → `f/demo/x`;
      `f/demo/x__flow/flow.yaml` → `f/demo/x`);
    - two files mapping to one logical path is a hard error naming every file
      (YAML and JSON, or `.flow/`, `__flow/` and `.flow.json`);
    - `wmill.yaml` `includes`/`excludes`/`extraIncludes` and `skip*` flags
      filter on the metadata file's relative path with minimatch; the include
      list applies only when `includes` or `excludes` is non-empty;
    - branch-specific dotted variants (`f/x.<branch>.resource.yaml` when
      `gitBranches` declares the branch) are rejected, while ordinary dotted
      paths stay valid;
    - directory symlinks are not followed; file symlinks are accepted only when
      the target stays inside the root;
    - parse errors never echo file content (values may be secrets);
    - output is sorted for determinism.
  - **Divergences from the pinned CLI:**
    1. windmill-ts accepts `.flow/`, `__flow/` and flat `*.flow.json` side by
       side. The CLI enforces the configured `nonDottedPaths` style and push
       reads only `<dir>/flow.yaml`; `*.flow.json` is the server's zip form,
       not a pushable tree file.
    2. windmill-ts treats an empty `includes` like an absent one; the CLI does
       not ([SY-13](#sy-13), [SY-V2](#sy-v2)).
    3. Duplicate-path errors, branch-variant rejection and symlink containment
       are windmill-ts policies; Windmill's own behaviour there is not
       established.
- **Evidence:** windmill-ts `src/source/local/discovery.ts:34-197` (`:41-57, 93-124`),
  `src/source/local/wmillConfig.ts:45-156` (`:132-156`),
  `src/source/local/index.ts:90-263`, `src/source/local/parsing.ts:8-54`,
  `tests/local-source.test.mjs:63-70, 181-226, 612-627` (all
  `@ af2b1e325320`); Windmill `cli/src/utils/resource_folders.ts:167-175`,
  `cli/src/commands/flow/flow.ts:188`, `cli/src/types.ts:220-225`,
  `cli/src/commands/sync/sync.ts:2570-2588`.
- **Conformance:** [PA-1/a–b](conformance.md#pa-1)
- **Design note (not Windmill behaviour):** windmill-lite may adopt the
  policies in divergence 3 but should label them as divergences, and follows
  the CLI for a lone `*.flow.json` (ignore it, with a warning).

<a id="pa-2"></a>
### PA-2 · What windmill-ts does not do, and windmill-lite must

**Scope:** core · **Confidence:** high · **Status:** confirmed

- **Rule:**
  - windmill-ts parses only each item's `schema`. It has no OpenFlow types,
    does not read `value.modules`, script sources, locks, variables,
    `codebases` or `defaultTs`, does not walk into flow folders (so never sees
    `*.inline_script.*` files), and resolves `!inline` only as a quoted string
    prefix for resource values (split on the first space), not as a YAML tag
    and not `!inline_fileset`.
  - windmill-lite's loader needs all of these: full flow parsing with the OF
    rules; the YAML tag forms of `!inline`/`!inline_fileset` alongside the
    string-prefix form; inline-script paths resolved relative to the flow
    folder; path-script language from the extension plus `defaultTs` only
    ([SY-3](#sy-3); `language` exists only on flow rawscripts); `.script.lock`
    files; `*.variable.yaml`; and `codebases` selection for Bun.
- **Evidence:** windmill-ts `src/source/local/parsing.ts:119-152`
  (schema-only), `src/source/local/resourceValues.ts:16-105`,
  `src/source/local/discovery.ts:155-162`, `README.md:101-104` (all
  `@ af2b1e325320`); Windmill tag handling `cli/src/utils/yaml.ts:5-23`.
- **Conformance:** [PA-2/a](conformance.md#pa-2)

<a id="pa-3"></a>
### PA-3 · Schema clean-ups learned from prior art

**Scope:** later · **Confidence:** medium · **Status:** confirmed

- **Rule:** Real Windmill schemas can contain `default: null`, enum defaults
  outside the enum, `enum: null`, empty object schemas, `format: dynselect-*`,
  both `S3Object` and `s3_object`, and resource args as
  `{type: object, format: "resource-<type>"}`. windmill-ts repairs or
  recognises all of these. This shows prior-art handling, not how common they
  are; they have no effect on execution ([OF-18](#of-18)).
- **Evidence:** windmill-ts `src/generator/common.ts:101-164, 252-262 @ af2b1e325320`.
- **Conformance:** [PA-3/a](conformance.md#pa-3)
- **Design note (not Windmill behaviour):** any typed-method generator in
  windmill-lite must tolerate these forms.

## Licence map

The licence map of the Windmill repository, and the rules for what the build
team may read, depend on or copy, live in [licensing.md](licensing.md):
[LIC-1](licensing.md#lic-1) (licence map and boundary),
[LIC-2](licensing.md#lic-2) (learning the sync format without AGPL code) and
[LIC-3](licensing.md#lic-3) (no usable Go OpenFlow type set exists; write one
with a schema-coverage test). Where a finding above names a package
(`windmill-yaml-validator`, `cli/windmill-utils-internal`, the `wmill` CLI),
its licence status is the one given there.

## Open questions

<a id="q-of-1"></a>
- **Q-OF-1** · affects [OF-3](#of-3), [OF-6](#of-6), [OF-7](#of-7),
  [OF-8](#of-8), [OF-16](#of-16), [OF-18](#of-18), [OF-V3](#of-v3),
  [SY-6](#sy-6) · Oracle recordings of the source-derived fixtures: F7-seq,
  F7-par and F7-ba, deployed (after the dependency job succeeds) and preview
  (OF-7/a–d); the `hasX` fixture (OF-18/a–b); the storage examples
  (OF-3/a–b), with a pull round trip as optional corroboration of SY-6/c; the
  native-switch and virtual-step cases (OF-3/c–d); the `same_worker` pair
  (OF-16/c–d); the `early_return` fixture (OF-V3/a–b); and the deploy-shape
  cases OF-6/a and OF-8/a. *Settled by:* harness runs against the pinned CE
  image ([oracle.md](oracle.md)).
<a id="q-of-2"></a>
- **Q-OF-2** · affects [OF-2](#of-2) · What HTTP status and body does the API
  return for the two decode error kinds (`Invalid flow value:` and
  `Module '<id>':`)? *Settled by:* an oracle create-flow request with each
  fixture, recording status and body.
<a id="q-of-3"></a>
- **Q-OF-3** · affects [OF-3](#of-3), [OF-17](#of-17) · Does a run that reads
  the full value show `<id>-v` for a top-level final sleep? answered: see
  [OF-V5](#of-v5) for fresh ordinary successful runs on modern workers (no
  `a-v`, no final sleep scheduled with a full-built status). *Still open:*
  legacy workers, restart and single-step paths, failures, cancellation, cache
  hits and durable suspend are not asserted, and no oracle run corroborates
  the trace (see [Q-OF-11](#q-of-11)).
<a id="q-of-4"></a>
- **Q-OF-4** · affects [OF-17](#of-17) · Is a nested `<id>-v` result visible
  through `results` from outside its body? *Settled by:* the control-flow
  finding [CF-19](control-flow.md#cf-19).
<a id="q-of-5"></a>
- **Q-OF-5** · affects [OF-15](#of-15), [OF-V6](#of-v6), [SY-15](#sy-15) ·
  Which languages can run offline with an empty or absent lock, and when does
  Bun's lock generator return no lock? *Settled by:* a source read of Bun lock
  generation for [languages.md](languages.md), plus the build-team policy
  decision.
<a id="q-of-6"></a>
- **Q-OF-6** · affects [SY-9](#sy-9) · Which YAML scalar forms does the chosen
  Go library resolve differently from the CLI's YAML 1.2 core parse?
  *Settled by:* pinning the SY-9 scalar fixtures against the Go library the
  build team chooses.
<a id="q-of-7"></a>
- **Q-OF-7** · (human decision) · Licence and CE-terms questions; tracked in
  [licensing.md](licensing.md#q-of-7).
<a id="q-of-8"></a>
- **Q-OF-8** · The `OF-` prefix was also used for oracle findings. answered:
  the oracle's findings are published as `OR-` ([oracle.md](oracle.md)).
<a id="q-of-9"></a>
- **Q-OF-9** · affects [OF-15](#of-15) · What exactly is the
  workspace-dependency lock header, and when is it present? answered: see
  [OF-V6](#of-v6). *Still open (optional):* no resolver run or oracle deploy in
  a workspace with dependency files has corroborated it; relevant only if
  windmill-lite generates locks.
<a id="q-of-10"></a>
- **Q-OF-10** · affects [SY-1](#sy-1) · How does the CLI compute item paths
  when `noCdToRoot` keeps it in a subdirectory? answered: see
  [SY-V4](#sy-v4). *Still open (optional):* no CLI dry-run from a subdirectory
  has corroborated it.
<a id="q-of-11"></a>
- **Q-OF-11** · affects [OF-3](#of-3), [OF-17](#of-17), [OF-V5](#of-v5) ·
  OF-3 says a run reads full when `DISABLE_FLOW_SCRIPT` is set; OF-V5 traces
  that flag to enqueue-time status construction only, with the worker still
  looking up lite first. Which account holds, and is the `[a, a-v]`
  virtual-step fixture limited to an initial-lite status? *Settled by:*
  reconciling OF-3 and OF-17 against the OF-V5 trace
  (`backend/windmill-queue/src/jobs.rs:5914-5965`,
  `backend/windmill-common/src/cache.rs:890-910`,
  `backend/windmill-worker/src/worker.rs:3731-3749, 3815-3824`,
  `backend/windmill-worker/src/worker_flow.rs:1394-1397, 1734-1750, 3666-3698`),
  and optionally an oracle run: a fresh, successfully deployed one-step flow
  with a final sleep on modern workers, comparing the flag off and on and
  recording status ids and sleep scheduling, without assuming the flag forces
  full worker data (OF-V5/a–b, OF-3/d, OF-17/a).
<a id="q-of-12"></a>
- **Q-OF-12** · affects [OF-13](#of-13) · The language inventory lists 24
  values, but a second reading of the same source range counted 25. Is a
  language missing from the list? *Settled by:* recounting the `language`
  enum at `backend/windmill-types/src/scripts.rs:41-101`.
<a id="q-of-13"></a>
- **Q-OF-13** · affects [SY-4](#sy-4), [SY-5](#sy-5), [SY-7](#sy-7),
  [SY-8](#sy-8), [SY-10](#sy-10), [SY-11](#sy-11), [SY-13](#sy-13),
  [SY-16](#sy-16) to [SY-19](#sy-19), [OF-14](#of-14) · The sync-folder
  rules revised or added on 2026-10-28 are traced in the pinned CLI and
  server source but not recorded. In particular: that the published
  `windmill-cli@1.767.0` bundle carries minimatch 10 with default options;
  that a stray `.js` file fails the push; that resource and variable files
  without `value` fail it; and how an unknown hash pin fails at run time.
  *Settled by:* oracle sessions that push the trees of SY-4/b, SY-5/c,
  SY-7/f, SY-8/c, SY-10/b, SY-11/b, SY-13/b and SY-16/a to SY-19/b with the
  pinned CLI and read back what was deployed (`scripts/get`, `flows/get`,
  `resources/get`, `variables/get`) or the push's error, plus a run of
  OF-14/f.
