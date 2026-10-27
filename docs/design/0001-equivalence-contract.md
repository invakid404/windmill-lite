# 0001 · Equivalence contract

**Status:** proposed · **Topic:** what windmill-lite promises to match, what it
leaves out, and how unsettled findings are handled.

## Context

The [semantics reference](../semantics/README.md) describes Windmill
`v1.767.0` as 321 findings: 251 `core`, 58 `later`, 7 `out`, 5 `EE`.
Twenty-one of them are not settled (`DISPUTED`, `needs-evidence` or `open`), and 110
question entries track what would settle them. Several findings say outright
that an equivalence decision is needed: loop collapse
([FX-46](../semantics/execution.md#fx-46)), module state
([Q-WR-19](../semantics/languages.md#q-wr-19)), jsonb ordering at the step
boundary ([Q-WR-18](../semantics/languages.md#q-wr-18)), unsupported platform
calls ([Q-WA-11](../semantics/client-api.md#q-wa-11)).

"Windmill's semantics" also needs a reference point. The same flow behaves
differently as a preview, during the deploy race or after a failed lock
([OF-3](../semantics/openflow.md#of-3),
[OF-7](../semantics/openflow.md#of-7), [CF-8](../semantics/control-flow.md#cf-8)).

## Options considered

1. **Match everything Windmill does**, platform included (queues, job rows,
   suspend). This rebuilds Windmill and defeats the project.
2. **Match data semantics against one fixed reference configuration, list
   everything else as out of scope or as a named deviation.** Chosen.
3. **Best effort**, fixing differences as users report them. This is how a
   hand-written mirror drifts; rejected.

## Decision

### 1. The reference configuration

windmill-lite matches Windmill CE `v1.767.0` running a flow that was
**deployed successfully** (the lite form, with materialised defaults and
virtual steps), started **by path as a root job**, on **one worker**, and
observed through the API and through platform effects. Concretely:

- container skip flags take their deployed defaults
  ([OF-7](../semantics/openflow.md#of-7), [CF-6](../semantics/control-flow.md#cf-6));
- a module list whose last module sleeps or suspends gets the `<id>-v`
  identity step ([OF-17](../semantics/openflow.md#of-17),
  [FX-4](../semantics/execution.md#fx-4));
- loops collapse as on a deployed `Flow` job reading the lite form, in which
  nested bodies are stored nodes, so the deployed rules apply at every depth
  ([FX-46](../semantics/execution.md#fx-46),
  [CF-V5](../semantics/control-flow.md#cf-v5)). windmill-lite has no preview
  mode and does not emulate the full-form selection that
  `DISABLE_FLOW_SCRIPT` can force, so neither half of the
  [CF-8](../semantics/control-flow.md#cf-8) dispute reaches it;
- the reference Bun is the one in the image, `1.3.10`
  ([WR-3](../semantics/languages.md#wr-3)).

### 2. What is matched exactly

Every `core` finding in the first release, and each `later` finding once the
milestone that covers it lands ([0012](0012-layout-and-milestones.md)). The
table groups findings by area; the ranges include `later` findings, and the
reference gives each finding's scope.



| Area | Matched | Main findings |
|---|---|---|
| Flow advance | the start and completion decision list, three outcomes per module, entering the failure module at cursor `len`; cursor-0 checks (`later`) | FX-1, FX-2, FX-V2, FX-V4; FX-22, FX-49 |
| Data in scope | `flow_input`, `previous_result`, `results.<id>` routes, `flow_env`, `iter`/`iter_parent` | FX-6, FX-7, FX-10, FX-11, CF-1, CF-3, CF-19, JE-22, JE-36, JE-37 |
| Expressions | QuickJS-ng semantics, shortcuts, textual rewrites, statement mode, serialisation, error texts, limits | JE-1 to JE-46 |
| Step controls | `skip_if`, `stop_after_if` (clean and error stops), `sleep`, `timeout`, `retry` (arithmetic and jitter distribution), `continue_on_error`, `mock` | FX-17 to FX-30, FX-44, FX-V1 |
| Containers | for and while loops (sequential, collapsed, parallel with `parallelism`), `branchone`, `branchall`, subflows by path, `identity`; result shapes, failure and stop propagation | CF-1 to CF-29, CF-V1 to CF-V4 |
| Error handling | `failure_module` (when, what it sees, recovery, innermost level), error shapes and names | FX-12 to FX-15, FX-31 to FX-34, FX-47 |
| Final result | result selection, cancellation | FX-3, FX-36 (executor part), FX-38 |
| JSON boundaries | producer text versus jsonb, unspecified outer order, NUL rejection | FX-42, FX-48, FX-V5, JE-16, JE-44, WR-10, OR-27 |
| Bun steps | process model as observed by one step, argument binding, `Date` hydration, result serialisation, error shapes, environment, imports and bundling | WR-1 to WR-32 (core parts), WR-37 to WR-39, WR-V1 to WR-V4, WR-V8 |
| Platform calls | the `wmill` client surface the core findings cover, error bodies, the three root notions | WA-1 to WA-16, WA-31, WA-40 to WA-42, WA-V1, WA-V3 |

**Observables.** A run is compared on:

1. the final status and result;
2. per module, its status, whether it ran, the stored result, the carried
   value and, for leaves, the arguments the script received;
3. error objects, field by field, with exact message text;
4. platform effects (variables, resources, state, flow user state, object
   store, dispatched jobs) and external effects that fixtures record.

**Normalised before comparison** ([OR-14](../semantics/oracle.md#or-14),
[OR-V5](../semantics/oracle.md#or-v5)):

- job ids, replaced by the typed site path of [0011](0011-differential-testing.md);
- `stack` strings, only with runtime-error provenance;
- the Rust location suffix ` @<file>.rs:<line>:<col>` of server-side
  messages, which windmill-lite cannot reproduce
  ([Q-FX-13](../semantics/execution.md#q-fx-13)). The catalogue already strips
  it for expression cases; the oracle normaliser of
  [OR-14](../semantics/oracle.md#or-14) masks it only for the NUL
  `InternalErr`, and that rule stays as it is for oracle-to-oracle
  comparisons. Comparisons between windmill-lite and the oracle strip the
  suffix from every message, and every other part of a message is matched
  byte for byte;
- timestamps and durations.

**Unspecified on purpose.** The top-level key order of `flow_input` and of
every step's argument map is unspecified in Windmill
([FX-42](../semantics/execution.md#fx-42), [OR-23](../semantics/oracle.md#or-23)).
windmill-lite shuffles these orders with a per-run seed, so that nothing can
come to depend on an order Windmill does not guarantee. Comparisons treat
those keys as a set.

### 3. Out of scope

| Feature | What windmill-lite does | Findings |
|---|---|---|
| Durable `suspend`, approvals, resume | rejects the flow at load | FX-39 |
| Workflow-as-code scripts | rejects at load | WA-33, WR-32 |
| `aiagent` modules | rejects at load; an `ai` input transform on an ordinary step is left out of the arguments, as Windmill does | OF-9, FX-7, JE-47 |
| Queues, worker tags, `priority`, concurrency limits, debouncing, `dedicated_worker` | decoded and ignored | FX-41, OF-16 |
| Job rows, logs as a contract, the UI | not provided; logs are captured for error tails only | WR-20 |
| `delete_after_use`, `delete_after_secs` | no effect, as on CE | OR-21 |
| Schedules, triggers, apps | recognised by the loader and skipped | SY-12 |
| EE behaviour (codebase bundles server-side, dedicated workers, OIDC) | out unless observable on CE; see [0002](0002-ir-and-loader.md) for codebase scripts | WR-30, WR-36 |

A load rejection is an error of kind `unsupported_feature` naming the
feature (`suspend`, `workflow_as_code`, `aiagent`), with the message
`unsupported: <feature>`; that satisfies both policy cases, FX-39/a and
WA-33/c.

### 4. Delegated

Steps or subflows a host chooses to run on a real Windmill
([0007](0007-platform-interface.md)) follow Windmill's own behaviour; the
contract covers how their outcome enters the local run, not what happens
remotely.

### 5. Named deviations

Each deviation has an id, is listed in the bundle manifest when it is active,
and has a conformance case that shows the difference.

| Id | Deviation | Why | Record |
|---|---|---|---|
| D1 | Module state survives between invocations in the default `warm` mode, and every script in a process shares one `globalThis` | per-step re-evaluation costs 0.6 to 2.1 ms per invocation for 0.3 to 1.3 MB bundles, before any dependency initialisation | [0006](0006-module-state.md) |
| D2 | A timed-out or canceled step is abandoned, not killed; its JavaScript may run on until the harness process is recycled | one warm process serves many invocations; killing it would fail unrelated steps | [0004](0004-harness-protocol.md) |
| D3 | Job ids are windmill-lite UUIDs; job introspection answers from the run, not from a job table | there is no queue | [0007](0007-platform-interface.md) |
| D4 | The `cache_ttl` result cache is per process unless the host plugs in a shared store | Windmill's cache lives in its database | [0007](0007-platform-interface.md) |
| D5 | `nativets` and `//native` scripts run in the Bun harness under a native boundary mode, not in Windmill's V8 isolate | one JavaScript harness; the reference covers the native runtime only in outline ([WR-31](../semantics/languages.md#wr-31), `later`), so the mode's rules wait for a semantics request (README) and the API surface is checked at build | [0004](0004-harness-protocol.md), [0002](0002-ir-and-loader.md) |
| D6 | Module ids missing from a never-deployed folder are derived from their position, not random | reproducible bundle identity | [0002](0002-ir-and-loader.md) |
| D7 | `//nobundling` scripts are bundled at build time instead of resolving imports and installing packages at run time | a bundle must run offline; results are the same, but `import.meta`, file paths and stack text differ, as they already do between Windmill's two modes ([WR-27](../semantics/languages.md#wr-27)) | [0010](0010-packaging.md) |
| D8 | An expression attempt that hits the 20 s limit is interrupted; Windmill leaves the evaluation running on its thread | frees the CPU; the error text, the three attempts and the 5 s pauses are unchanged | §7, [JE-2](../semantics/expressions.md#je-2) |

Anything not in this table and not out of scope is a bug when it differs.

### 6. Findings that are not settled

Each contested rule is implemented once, as one function in the engine or
the expression compiler, with both readings selectable at build time. The
manifest records the readings in effect, the conformance suite carries the
cases for both, and an oracle recording settles the default. A changed
default is a one-line change plus the updated case status.

| Finding | Readings | Default and why | Settled by |
|---|---|---|---|
| [FX-10](../semantics/execution.md#fx-10) / [FX-V6](../semantics/execution.md#fx-v6) | engine reads the preceding result from memory when (a) a literal preceding-id spelling appears, or (b) any binding trigger applies (`previous_result`, a spelling, or `error`) | (b): it agrees with the confirmed binding rule [JE-10](../semantics/expressions.md#je-10) | Q-FX-15, case FX-V6/a |
| [CF-19](../semantics/control-flow.md#cf-19) / [CF-V6](../semantics/control-flow.md#cf-v6) / FX-10 route 2 | direct `results` lookup when (a) the whole expression matches, or (b) the first qualifying line matches | (b): it is the confirmed [JE-9](../semantics/expressions.md#je-9) rule | Q-CF-16, cases CF-V6/a,b |
| [FX-33](../semantics/execution.md#fx-33) | failure-module recovery from (a) an object only, or (b) an object or a one-element positional array | (b): the correction traces the decoder | Q-FX-19, cases FX-33/c–g |
| [CF-12](../semantics/control-flow.md#cf-12) | after a sequential loop breaks on an unhandled error stop with `skip_failures: false`, the flow (a) continues, or (b) fails without running the next step | (b): the correction traces that a break clears the stop, not the failure | Q-CF-14, case CF-12/f |
| [FX-42](../semantics/execution.md#fx-42) | `$jsonvar:` output keeps (a) its text, or (b) its insertion order with numbers re-serialised | (b). The readings differ only in number spelling, which a JavaScript consumer cannot see; the difference matters for Python (`later`) | Q-FX-16, case FX-42/h |
| [OF-3](../semantics/openflow.md#of-3) / [OF-V5](../semantics/openflow.md#of-v5) | one or two form selections | not applicable: both readings agree on the successful lite deployment, which is the reference configuration | Q-OF-11 |
| [CF-8](../semantics/control-flow.md#cf-8) | how far the raw-submission no-collapse rule reaches, and whether the stored form actually selected (lite or full) decides nested collapse | not applicable: windmill-lite never runs raw submissions and always reads the lite form (§1) | Q-CF-15 |
| [FX-49](../semantics/execution.md#fx-49) (`later`) | a cursor-0 `skip_expr` that turns true mid-run (a) is left open, or (b) emits a stop | re-evaluation itself is not contested and is implemented; (b) for the outcome | Q-FX-11 |
| [FX-40](../semantics/execution.md#fx-40) (`later`) | collapsed iterations (a) never, or (b) can share cache entries | (b): it follows the key derivation as corrected | Q-FX-17 |
| [FX-36](../semantics/execution.md#fx-36) (HTTP part `later`) | array results decoded positionally as response metadata, or not | only matters to an HTTP run-and-wait adapter, which comes later | Q-FX-12 |
| [WR-16](../semantics/languages.md#wr-16), [WR-28](../semantics/languages.md#wr-28) (`later` parts) | stream classification; dynamic-import rewrite reach | the corrected readings, when those features land | Q-WR-15, Q-WR-16 |
| [OR-11](../semantics/oracle.md#or-11), [OR-14](../semantics/oracle.md#or-14), [OR-V3](../semantics/oracle.md#or-v3), [OR-V8](../semantics/oracle.md#or-v8) | identity encoding for compared steps | decided in [0011](0011-differential-testing.md): typed segments, with the escape character escaped | Q-OR-17 |
| [OR-15](../semantics/oracle.md#or-15), [OR-V9](../semantics/oracle.md#or-v9) | whether the probe catalogue fills the fixture format | the format is adopted; the catalogue is rebuilt as executable fixtures (milestone E3) | Q-OR-16 |

### 7. Policy questions the reference left to the design

| Question | Decision |
|---|---|
| [Q-CF-12](../semantics/control-flow.md#q-cf-12): a subflow can read its caller's `results.<id>` | Match it. The contract is exact semantics, and flows may rely on the leak without knowing. |
| [Q-CF-13](../semantics/control-flow.md#q-cf-13): absent container fields | Apply the deployed defaults. A `strict` build option rejects absent skip flags instead, for authors who want the folder to say what runs. |
| [Q-WR-17](../semantics/languages.md#q-wr-17): `.ts` rewrite collisions | Reproduce them: the rewrite is textual and cheap to copy, and a script that fails to build on Windmill must fail here too. |
| [Q-WR-18](../semantics/languages.md#q-wr-18): layer-specific jsonb order at the step boundary | Emulate it ([0005](0005-json-step-boundaries.md)). |
| [Q-WR-19](../semantics/languages.md#q-wr-19): module state | [0006](0006-module-state.md). |
| [Q-FX-13](../semantics/execution.md#q-fx-13): how much error text to match | All of it, except the Rust location suffix and stacks. |
| [Q-WA-9](../semantics/client-api.md#q-wa-9): secret masking in logs | Not in the first release: windmill-lite keeps no log store, and logs are not part of the contract. Hosts that forward logs own their masking. |
| [Q-WA-11](../semantics/client-api.md#q-wa-11): unsupported platform calls | Fail by default with `UnsupportedPlatformCall`; an `observe` mode keeps stock client behaviour for oracle comparison ([0007](0007-platform-interface.md)). |
| [JE-2](../semantics/expressions.md#je-2) (`later`): runaway expressions | Keep the message, the 20 s limit and the three attempts with 5 s pauses. windmill-lite also stops the engine instance when an attempt times out, which its design note calls a deviation to list: D8. |
| [FX-26](../semantics/execution.md#fx-26): retry arithmetic | Emulate the unsigned 16-bit wrap, saturate and cap steps exactly; the random source is injectable so tests can fix jitter. |

## Consequences

- The engine tracks three outcomes per module (stored result, status,
  carried value) because the contract compares all three
  ([FX-V2](../semantics/execution.md#fx-v2)).
- A flow using an out-of-scope feature fails at load, never midway through a
  run.
- Every deviation is visible in the bundle manifest, so a host can tell what
  it is running.
- Contested rules cost a build-time switch each. That is cheap compared with
  re-deriving a rule after an oracle run disagrees.

## Spec IDs relied on

FX-1, FX-2, FX-4, FX-10, FX-26, FX-33, FX-36, FX-39, FX-40, FX-41, FX-42,
FX-46, FX-49, FX-V2, FX-V6, CF-6, CF-8, CF-12, CF-19, CF-V5, CF-V6, OF-3,
OF-7, OF-17, OF-V5, JE-2, JE-9, JE-10, WR-1, WR-3, WR-16, WR-28, WR-30,
WR-32, WR-36, WA-33, OR-11, OR-14, OR-15, OR-21, OR-23, OR-V3, OR-V5, OR-V8,
OR-V9, and the questions named in the tables.
