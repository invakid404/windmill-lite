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

A **selector** names call sites: `<flow path>#<module id>`, where the flow
path is the root or by-path flow whose module list contains the module (body
modules of containers belong to their enclosing flow), optionally narrowed to
iterations or branches, or `script:<path>` for every call of a script. Sites
themselves are typed paths ([0011](0011-differential-testing.md)). Hooks
declare their selectors up front, so the engine asks a host plugin only for
matching sites and pays nothing elsewhere.

### Where hooks run

- **Go callbacks**, for hosts that embed the Go library
  ([0009](0009-host-adapters.md)).
- **Host plugins**: a TypeScript module the host ships in its bundle, loaded
  by the Bun harness. The engine calls it with `host_call` frames
  ([0004](0004-harness-protocol.md)). A plugin runs in the run's host scope
  and can call any code in the bundle.

### The hooks

| # | Hook | Semantics |
|---|---|---|
| H1 | **Intercept** a site | Runs when the module would launch a job: after `skip_if`, `mock`, its arguments and its timeout are decided, at the launch of [FX-2](../semantics/execution.md#fx-2) start step 10. A module that `skip_if` or `mock` already answered is not intercepted. Receives the evaluated arguments and the run context. Returns a result, an error, or *decline* (the real module runs). The completion phase (stop, retry, `continue_on_error`) then applies to that outcome as if the module had run. Static overrides (a fixed value per selector, from configuration) are the no-code form. A host plugin may instead **wrap** a leaf's real invocation (`around(site, next)`), running code before and after it in the harness, for example to enter a scope or account for resources around one step; the wrapped step's outcome is still its own. |
| H2 | **Rewrite arguments** of a site | Gets the evaluated arguments, returns the arguments the module runs with. The trace keeps both. |
| H3 | **Write-capable steps and the first-write signal** | The host marks selectors write-capable. Before the first write-capable module of a run launches, the engine calls `beforeFirstWrite(site)` once; the host can veto, which latches (H5). From then on the run reports `mayHaveMutated: true`, including in a failed run's outcome. |
| H4 | **Cancellation and budgets** | A run has a cancellation source and two deadlines: a hard one, and an optional *pre-write* one that only applies until H3 fires. Both are checked at every module boundary, iteration, `sleep`, retry delay and expression evaluation ([0004](0004-harness-protocol.md)). Cancellation outranks `continue_on_error`, `skip_failures`, `retry` and the failure module ([FX-38](../semantics/execution.md#fx-38)), except where FX-38 itself lets an error stop that coincides with the cancel reach the failure module ([0004](0004-harness-protocol.md)). In-flight JavaScript is not killed (D2); plugins and step code reach the run's signal through `wl.signal()` and can tie their I/O to it. |
| H5 | **Run-fatal latch** | Any hook, plugin, observer or step (through `wl.latch(error)`) can latch an error on its run. A latched run starts no further module, iteration, branch or retry, outranks every later step outcome, ends with status `latched` and the host's error, and never converts the error into a step result. Iterations already running finish (their effects stand) or are abandoned at the deadline. |
| H6 | **Observers** | `onModuleStart` and `onModuleEnd(site, outcome)` for every module, nested ones and iterations included. Observers can latch (H5). |
| H7 | **Fatal error names** | The host lists error `name`s that bypass `retry`, `continue_on_error`, `skip_failures` and `skip_failure` and latch instead. Using it is a recorded deviation in the run trace, because Windmill has no such rule ([FX-28](../semantics/execution.md#fx-28) is per-module YAML). |
| H8 | **Halt** | `haltBefore(selector)` or `haltAfter(selector)` ends the run successfully at that point with status `halted` and its trace: evaluating a read-only prefix of a flow. |
| H9 | **Trace and run end** | Every run returns its trace: per site the status, whether it ran, recorded and resolved arguments, stored result, carried value, error, attempt number and timings. `onRunEnd(trace)` may append warnings to the run's outcome without changing its status, which is where replay of effects on a resumed attempt belongs. |
| H10 | **Clock and randomness** | The engine's clock and the retry-jitter source are injectable: `real`; `record`, which records every requested wait and returns at once (quick-waits mode, for tests and latency measurement); and a fixed jitter seed. |
| H11 | **Host scopes** | In host mode ([0009](0009-host-adapters.md)), submitting a run captures the host's `AsyncLocalStorage` context; every invocation of that run, every plugin call and every parallel iteration re-enters it. Run data the host attaches is readable by plugins as `wl.run.data`. |
| H12 | **Invocation environment** | Set or unset variables in an invocation's environment ([0007](0007-platform-interface.md)). |
| H13 | **Platform providers per run** | Choose service implementations for a run from its run data, for example a resource resolver per tenant ([0007](0007-platform-interface.md)). |

**Precedence.** At each start phase the engine checks, in order: latch,
cancellation and deadlines, halt, then Windmill's own start checks, with
interception at launch. At each completion: latch, cancellation, fatal error
names, then Windmill's continue decision ([FX-2](../semantics/execution.md#fx-2)).

### Patterns built from the hooks

- **Replacing a polling subflow with host evidence:** H1 on that call site;
  the plugin checks the evidence (it may call step code) and returns a result
  or declines.
- **Skipping Windmill-only permits:** H1 static overrides on the permit
  subflows' call sites, plus overrides of any step that reads the permit's
  job status. No special permit API is needed.
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

## Spec IDs relied on

FX-2, FX-23, FX-26, FX-28, FX-30, FX-38, CF-5, CF-7, and the identity
encoding of [0011](0011-differential-testing.md).
