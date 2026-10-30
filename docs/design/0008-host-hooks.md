# 0008 · Host hooks

**Status:** proposed · **Topic:** the generic ways a host changes or observes
a run beyond what the flow says: per-step interception and overrides,
injected inputs, a signal before the first write-capable step, cancellation
and budgets, run-fatal latches, skipping Windmill-only permits, replay
support, and host scopes.

## Context

A host embeds windmill-lite to run real flows instead of a hand-written copy
of them, but it still owns things a plain flow run does not: its own
deadlines, evidence it gathered before the run, resources it already holds,
and signals that must stop a run whatever the flow says. Three properties of
Windmill semantics shape the hooks:

- **Errors become data.** `continue_on_error`, `skip_failures` and
  `skip_failure` turn a failure into a value the flow continues with
  ([FX-30](../semantics/execution.md#fx-30), [CF-5](../semantics/control-flow.md#cf-5)),
  and wrapper subflows can rewrite an inner failure into a generic one. A host
  signal must therefore never travel as an error value.
- **Waits are long.** Sleeps, retry delays and polling loops dominate slow
  paths, so cancellation has to reach every wait
  ([FX-23](../semantics/execution.md#fx-23), [FX-26](../semantics/execution.md#fx-26)).
- **Steps are identified by where they are called.** The same subflow can be
  called from several places, and a host may want to replace it at only one
  of them.

## Options considered

1. **Hooks as Go callbacks only.** Simple for Go hosts; useless when the
   replacement has to call step code or run inside the host's async scopes,
   which only exist in the harness process.
2. **Hooks as edits to the flow YAML** (generate a patched folder). The
   bundle then no longer runs the real flows, and the host's patch drifts.
3. **A fixed set of hooks with selectors, each implementable as a Go
   callback or as a TypeScript host plugin in the harness.** Chosen.

## Decision

### Selectors

A **selector** names call sites. It is an object, written in configuration
as YAML or JSON:

- `{flow: <path>, module: <id>}`: the module with that id in any module list
  of the flow at `<path>` (the root or a by-path flow; body modules of
  containers belong to their enclosing flow);
- optional `occurrence: <k>`, required when the id occurs more than once in
  one module list (Windmill does not require ids to be unique,
  [OF-2](../semantics/openflow.md#of-2)); a selector that leaves such an id
  ambiguous fails at load with `ambiguous_selector`;
- optional `iterations` or `branches`, to narrow a body module to some
  iterations or branches;
- or `{script: <path>}`: every call of a script.

An id that appears in several different module lists of one flow (for
example the same id in two container bodies) matches every one of those
sites; that is intended for optional hooks, which often want all of them. A
**mandatory provided-site** selector must match exactly one site, because
every match becomes opaque to the loader; one that matches more fails at load
with `ambiguous_selector`.

Sites themselves are typed paths ([0011](0011-differential-testing.md)).
Hooks declare their selectors up front, so the engine asks a host plugin only
for matching sites and pays nothing elsewhere.

### Mandatory host-provided call sites

A selector listed in the build's **host contract** as `mandatory`
([0002](0002-ir-and-loader.md)) is a different kind of hook from optional
interception: its target is opaque to the loader (not loaded, validated or
bundled), so a target windmill-lite could not run, such as a subflow that
suspends, does not block the bundle. The engine treats the site as H1 with
two differences: `wl.Open` fails with `missing_provider` if no provider is
registered for it, and a provider that declines latches `provider_declined`
and fails the run closed. The site keeps its own envelope, so the flow's
transforms, `skip_if`, `stop_after_if`, retries and `continue_on_error`
apply around the provided value exactly as around a real module.

### Where hooks run

- **Go callbacks**, for hosts that embed the Go library
  ([0009](0009-host-adapters.md)).
- **Host plugins**: TypeScript run by the Bun harness: in host mode the
  host's own (the host SDK's `plugin`), elsewhere a module the runtime
  names, which each `warm` harness process loads. The engine calls it with
  `host_call` frames ([0004](0004-harness-protocol.md), §Host plugins).
  It declares its hooks and their selectors up front, and the engine turns
  the declaration into the same hooks a Go host passes, so a TypeScript
  hook takes the Go hook's path through the engine. A plugin runs in the
  run's host scope and can call any code in the bundle. Platform calls it
  makes are attributed to the site it is serving
  ([0007](0007-platform-interface.md)).

**Where the seams live.** The contracts milestone K1 and the first engine
milestone N1a define the internal hook interface with no-op defaults, the
precedence below, and the `latched` and `halted` statuses, so every engine
milestone after them implements the decision list with the seams in place. The hook milestones only expose those
seams to hosts ([0012](0012-layout-and-milestones.md)).

### The hooks

| # | Hook | Semantics |
|---|---|---|
| H1 | **Intercept** a site | Runs when the module would launch a job: after `skip_if`, `mock`, its arguments and its timeout are decided, at the launch of [FX-2](../semantics/execution.md#fx-2) start step 10. A module that `skip_if` or `mock` already answered is not intercepted. Receives the evaluated arguments and the run context. Returns a result, an error, or *decline* (the real module runs). The completion phase (stop, retry, `continue_on_error`) then applies to that outcome as if the module had run. Static overrides (a fixed value per selector, from configuration) are the no-code form. A host plugin may instead **wrap** a leaf's real invocation (`around(site, next)`), running code before and after it in the harness, for example to enter a scope or account for resources around one step; the wrapped step's outcome is still its own. |
| H2 | **Rewrite arguments** of a site | Gets the evaluated arguments, returns the arguments the module runs with. The trace keeps both. |
| H3 | **Write-capable steps and the first-write signal** | The host marks selectors write-capable. Before the first write-capable module of a run launches, the engine calls `beforeFirstWrite(site)` once; the host can veto, which latches (H5). When several write-capable modules launch at once (parallel branches or iterations), the others wait for that one verdict, and a veto stops them all. From then on the run reports `mayHaveMutated: true`, including in a failed run's outcome. |
| H4 | **Cancellation and budgets** | A run has a cancellation source and two deadlines: a hard one, and an optional *pre-write* one that only applies until H3 fires. Both are checked at every module boundary, iteration, `sleep`, retry delay and expression evaluation ([0004](0004-harness-protocol.md)). Cancellation outranks `continue_on_error`, `skip_failures`, `retry` and the failure module ([FX-38](../semantics/execution.md#fx-38)), except where an error stop that coincides with the cancel continues the flow, as FX-38 and FX-19 allow (precedence below, [0004](0004-harness-protocol.md)). In-flight JavaScript is not killed (D2); plugins and step code reach the run's signal through `wl.signal()` and can tie their I/O to it. |
| H5 | **Run-fatal latch** | Any hook, plugin, observer or step (through `wl.latch(error)`) can latch an error on its run. A latched run starts no further module, iteration, branch or retry, outranks every later step outcome, ends with status `latched` and the host's error, and never converts the error into a step result. Iterations already running finish (their effects stand) or are abandoned at the deadline. |
| H6 | **Observers** | `onModuleStart` and `onModuleEnd(site, outcome)` for every module, nested ones and iterations included. Observers can latch (H5). |
| H7 | **Fatal error names** | The host lists error `name`s that bypass `retry`, `continue_on_error`, `skip_failures` and `skip_failure` and latch instead. Using it is a recorded deviation in the run trace, because Windmill has no such rule ([FX-28](../semantics/execution.md#fx-28) is per-module YAML). |
| H8 | **Halt** | `haltBefore(selector)` or `haltAfter(selector)` ends the run successfully at that point with status `halted` and its trace: evaluating a read-only prefix of a flow. A halt is a run's own, so one run halts while other runs of the same flow run whole. |
| H9 | **Trace and run end** | Every run returns its trace: per site the status, whether it ran, recorded and resolved arguments, stored result, carried value, error, attempt number and timings. `onRunEnd(trace)` may append warnings to the run's outcome without changing its status, which is where replay of effects on a resumed attempt belongs. `onRunSettled(run)` fires later, when the run's abandoned invocations have all settled (possibly never), and is where resources step code can reach are released ([0004](0004-harness-protocol.md)). |
| H10 | **Clock and randomness** | The engine's clock and the retry-jitter source are injectable: `real`; `record`, which records every requested wait and returns at once (quick-waits mode, for tests and latency measurement); and a fixed jitter seed. |
| H11 | **Host scopes** | In host mode ([0009](0009-host-adapters.md)), submitting a run captures the host's `AsyncLocalStorage` context; every invocation of that run, every plugin call and every parallel iteration re-enters it. A step sees a scope only through a module instance the host shares with it, so a host that wants step code to read its scopes lists those modules as **host-provided** ([0010](0010-packaging.md), D12); a separately bundled copy of the same module has its own `AsyncLocalStorage` and reads nothing. Run data the host attaches is readable by plugins as `wl.run.data`. Policy case: the host sets a store in a host-provided module, and a step importing that module reads it, for two concurrent runs with different stores. |
| H12 | **Invocation environment** | Set or unset variables in an invocation's environment ([0007](0007-platform-interface.md)). |
| H13 | **Platform providers per run** | Choose service implementations for a run from its run data, for example a resource resolver per tenant ([0007](0007-platform-interface.md)). |

**Precedence.** At each start phase the engine checks, in order: latch,
cancellation and deadlines, halt, then Windmill's own start checks, with
interception at launch. At each completion: latch, cancellation, fatal error
names, then Windmill's continue decision ([FX-2](../semantics/execution.md#fx-2)).
Two further rules complete it:

- **Cancellation's one exception.** An error stop that coincides with the
  cancellation, in a flow with a failure module, passes the cancellation
  check and goes to Windmill's continue decision, which checks a stop before
  the cancel flag ([FX-2](../semantics/execution.md#fx-2) completion rule
  7.1, [FX-38](../semantics/execution.md#fx-38)). What that decision asks for
  still starts: another attempt of the module when a retry is due, the next
  module under `continue_on_error`, or else the failure module
  ([FX-19](../semantics/execution.md#fx-19)). The run then ends canceled at
  its next completion.
- **Halting never hides a failure or a cancellation.** `haltBefore` applies
  at a module's first attempt, never to a failure module. `haltAfter` is
  checked after the continue decision: it applies once the module has its
  final outcome (a container has finished its last iteration or branch, and
  no retry is due) and the run would start something other than a failure
  module. A completion that ends the run anyway keeps the
  status Windmill gives it. A halt that falls on a start the cancellation
  exception lets through stops that start, and the run ends canceled, not
  halted.

`internal/engine` encodes the whole order as `Start`, `Completion` and
`AfterContinue`.

### Patterns built from the hooks

- **Replacing a polling subflow with host evidence:** H1 on that call site;
  the plugin checks the evidence (it may call step code) and returns a result
  or declines.
- **Skipping Windmill-only permits:** mandatory host-provided sites on the
  permit subflows' call sites (their targets suspend, so they must be opaque
  to the loader), plus providers for any step that reads the permit's job
  status. No special permit API is needed.
- **A host-wide limit on outbound requests:** a plugin that wraps
  `globalThis.fetch` at load, inside the warm harness.
- **Resumed attempts:** run data flags the resume (H11); H2 changes inputs
  that depend on it; H1 replaces a check the host already made; H9 replays
  effects and records warnings.
- **Fail-fast parallel loops** (start nothing new after the first failed
  iteration): an observer on the loop's iterations that latches (H5). This is
  a host choice; the default is Windmill's continue-all behaviour
  ([CF-7](../semantics/control-flow.md#cf-7)).

## Consequences

- Host behaviour never needs a patched flow, so flow fixes reach the host
  unchanged.
- Host signals have their own channel (latch, cancellation) and cannot be
  swallowed by error-tolerant flow settings.
- The hooks that change semantics (H1, H2, H7, H8, H10 in `record` mode, and
  latching) are visible in the trace, so a comparison against the oracle can
  exclude or account for them.
- Milestone F2c exposed H1, H2, H6, H8, H9, H12 and H13, mandatory
  provided sites and `around` to host plugins. It settled what the table
  left open. A plugin hook's error, or a `host_call` whose reply does not
  come within the plugin timeout (`host_call_timeout`), latches the run,
  as a Go hook's error does, and at a provided site fails it closed; a
  failed `onRunEnd` can only add a warning. An `around` wrapper calls
  `next()` once, the step's outcome stays its own whatever the wrapper
  returns, and a wrapper that throws, or never runs the step, latches the
  run. A run's own hooks come before its plugin's: the first matching
  interception answers, rewrites apply in order, and both sets of
  observers run. Platform calls a plugin makes outside an invocation are
  not attributed yet: the harness refuses a facade request outside every
  invocation, so only an `around` wrapper's calls, made in the
  invocation, reach the facade. A later milestone gives plugin calls the
  site's identity. F2c also gave step code `wl.latch` and `wl.signal`
  (on `globalThis`, [0004](0004-harness-protocol.md) §Host plugins): a
  step's latch latches its run, a script job's included, and its signal
  is the invocation's, which aborts when the engine abandons the step.
  Plugin hooks get no signal of their own: a hook runs outside every
  invocation, and the run's end only stops the engine waiting for it. A
  host-mode run carries its own hooks and budgets (H3, H4, H7, H8, H10)
  in its `run` frame, and `onRunEnd` runs for a canceled run too, with the
  plugin timeout alone. Review round 1 settled four more points: a
  TypeScript H13 provider's failure or timeout latches the run, through
  the job API and reference resolution alike; a hook holds the run's
  settlement from the moment it is called until it has finished, even
  after the engine stopped waiting for it, so a run's resources outlive
  its hooks (review round 2: a provider call a step started, whose step
  settled first, included); an `around` wrapper's `next()` is
  closed once the wrapper returns; and a TypeScript hook's records and
  traces carry every field a Go hook's do.
- **Halts are carried per run.** A Go host gives a run's halts in that
  run's hooks, and a host-mode host in that run's `run` frame
  (`halt_before`, `halt_after`), which the engine decodes into the run's
  own hooks ahead of its plugin's ([0004](0004-harness-protocol.md)). The
  engine checks a frame's selectors as it checks a plugin's, and refuses
  the run when one fails. A plugin's halts, declared once for its host,
  apply to every run of that host.
- **A run's own halt follows the precedence above** exactly as a
  plugin's or a Go host's halt does. At a module's start it comes after
  the latch and the cancellation checks and before the first-write
  signal, and a completion that ends the run keeps its status. The
  `run_halt_*` cases of `conformance/plugins/hooks.yaml` show it through
  Go callbacks, a TypeScript plugin, and in host mode.
- Milestone K1 added the two rules after the precedence paragraph, and
  widened H4's FX-38 exception from "the failure module" to whatever the
  continue decision asks for, which FX-19 shows can be a retry or the next
  module. The paragraph named neither how that exception passes the next
  start check nor where halting applies, and the engine needs both to
  implement one order.

## Spec IDs relied on

FX-2, FX-19, FX-23, FX-26, FX-28, FX-30, FX-38, FX-39, OF-2, CF-5, CF-7, and the
identity encoding of [0011](0011-differential-testing.md).
