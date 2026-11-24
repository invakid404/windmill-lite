# harnesses/bun

**Current state:** the Bun [harness runtime](../../docs/glossary.md#harness-runtime)
runs TypeScript [steps](../../docs/glossary.md#step) for Bun, `warm` or
`fresh`, and native steps (`nativets` and `//native`), in a harness that
the engine starts or in a host's own process. The
[Bun host SDK](../../docs/glossary.md#bun-host-sdk) connects a Bun program
to the engine, runs its flows, and takes a
[host plugin](../../docs/glossary.md#host-plugin). Its test entry checks a
host's process-level code without an engine. Steps in other languages
have no harness yet.

This README covers `harnesses/bun`:

- `runtime/`: the harness runtime, the harness side of the
  [harness protocol ADR](../../docs/design/0004-harness-protocol.md);
- `host/`: the Bun host SDK of the
  [host adapters ADR](../../docs/design/0009-host-adapters.md), `connect()`,
  and its test entry;
- `test/`: the runtime's and the SDK's own tests and the type check.

The [build helper](build/README.md), which bundles scripts and reads their
signatures, has its own README. The npm package `windmill-lite`, whose root
is `harnesses/`, ships the runtime and the SDK built
([harnesses/README.md](../README.md)): it says how to install the package,
get the engine, build a bundle, and run a first flow.

A host's author reads the sections up to [Test a host](#test-a-host).
[Work on the harness](#work-on-the-harness) is for contributors who change
the runtime or the SDK.

## How a step runs

The harness runtime runs each step's script for the
[engine](../../docs/glossary.md#engine). It runs in one of two places:

- in a [harness](../../docs/glossary.md#harness) process that the engine
  starts, for `windmill-lite run` and for a Go program that uses
  [`wl`](../../wl/README.md);
- in the [host](../../docs/glossary.md#host)'s own process, in
  [host mode](../../docs/glossary.md#host-mode): the Bun host SDK starts
  the engine, and the engine sends the steps to the host.

Each run of a step's code is an [invocation](../../docs/glossary.md#invocation).
The [module state](../../docs/glossary.md#module-state) decides how
invocations share a process:

| Module state | What a process runs | What a step's module keeps |
|---|---|---|
| [`warm`](../../docs/glossary.md#warm), the default | Many invocations at once, on one event loop | The module loads once in the process. Its module-level state lives as long as the process. |
| [`fresh`](../../docs/glossary.md#fresh) | One invocation. The process exits once its result is written. | Nothing: each invocation loads the module in a new process. |

Host mode runs `warm` only. The
[module state ADR](../../docs/design/0006-module-state.md) gives the
reasons.

### What a step returns

The runtime applies Windmill's rules for a Bun step: it binds the
arguments to the entrypoint's parameters by name, awaits the call, and
stores `JSON.stringify(value ?? null)`. A `Date` parameter is built from
its argument. [The step runner](#the-step-runner) gives each rule.

| The step | Outcome |
|---|---|
| Returns | The result |
| Throws | A failure, with an object of the error's `message`, `name` and `stack`, the step's `step_id`, `job_id` and `flow_path`, and the error's other properties in `extra` |
| Fails to load, throws `null` or an error that does not serialise, or has an uncaught error attributed to it | An escaped failure, exit code 1. The last 600 characters of its log are the error's text. |
| Calls `process.exit(n)` | An escaped failure with exit code `n`, or an empty result for 0. The step's code goes on in a `warm` process; a `fresh` process exits. |
| Returns a result larger than the protocol's frame limit | An escaped failure that says so |

### The per-invocation context

Many invocations share a `warm` process, so the runtime gives each one its
own view of the process's channels. It installs these views once, before
any step module loads
([WA-31](../../docs/semantics/client-api.md#wa-31)). This is what a step's
code sees:

| Channel | What the step sees |
|---|---|
| `process.env`, `Bun.env`, `import.meta.env` | Its invocation's variables first, and its writes and deletes stay with the invocation. Two exceptions, through `Bun.env` or `import.meta.env` in code that the build does not rewrite: a write of a process variable that no invocation has claimed reaches the process, and so does a delete ([the limits](#inside-the-per-invocation-context)). Outside every invocation, each reads the process environment. |
| `fetch` | A request to the [platform facade](../../docs/glossary.md#platform-facade) becomes a platform call of the invocation. Any other request goes out as usual, except that a dispatch to a real [Windmill](../../docs/glossary.md#windmill) that the engine names first loses its links to the invocation's own job ids ([the fetch view](#inside-the-per-invocation-context)). |
| `setTimeout`, `setInterval`, `setImmediate` | The timers work as usual, and are registered with the invocation, on `globalThis` and on `node:timers`. |
| `console`, `process.stdout`, `process.stderr` | Each line goes to the invocation's log ([WR-23](../../docs/semantics/languages.md#wr-23)). `Bun.stdout`, a raw file descriptor and a child's inherited output are not attributed. |
| Child processes | A child started without an `env` gets the invocation's variables. In a `warm` process, a child that the invocation starts asynchronously, and not detached, leads a process group of its own. The engine kills that group on the step's timeout or cancellation ([WR-55](../../docs/semantics/languages.md#wr-55)). A synchronous call's child stays in the harness's group, unless the step starts it detached ([process groups](#inside-the-per-invocation-context)). |
| `process.exit` | In a `warm` or host process, the call fixes the invocation's outcome and throws, so `finally` blocks run and the code goes on. In a `fresh` process, the process exits at once, and no `catch` or `finally` block runs. Outside every invocation, it is the real `process.exit`. |
| Uncaught errors and unhandled rejections | An error in a running invocation's async context fails that invocation as escaped. One with no invocation fails nothing: health counts it, and each invocation in flight gets an `unattributed_error` warning. |
| The step module's top-level code | It runs once, in the context of the module's first invocation: it reads that invocation's environment and writes to its log. The timers, requests and children that it starts are never fenced. Once that invocation has settled, what they write goes to the process's streams, and their platform calls are refused. |
| A module imported at run time from a file outside the bundle | It evaluates outside every invocation: it reads the process's environment, and writes to the process's streams. A lazy `import()` of bundled code is inlined by the build, and keeps the caller's context. |
| `WebSocket` listeners | On Bun 1.4.3, they run outside every invocation, so they read the process environment and the facade refuses their requests. This is a named [deviation](../../docs/glossary.md#deviation) of the [equivalence contract](../../docs/design/0001-equivalence-contract.md#5-named-deviations). |

When an invocation [settles](../../docs/glossary.md#settle), the
[settle-time fence](../../docs/glossary.md#settle-time-fence) releases what
it left behind:

- its pending timers are cleared, and its requests in flight are aborted;
- its later log writes are dropped, and its later platform calls are
  refused;
- in a `warm` process, its children's process groups are killed. A child
  that the step started detached is left running, where Bun honours
  `detached`: for `spawn`, `fork` and the sync forms, not for `exec` and
  `execFile`;
- the invocation gets a `left_behind` warning that counts what it left, by
  kind, such as `{ interval: 1 }`.

The fence cannot stop promise continuations that are already queued, such
as `Bun.sleep` or `node:timers/promises`. What they then do through the
fenced channels is refused.

#### The step API

The runtime sets `wl` on `globalThis` for step code:

- `wl.latch(error)` [latches](../../docs/glossary.md#latch) the run with
  `error`, after the step's result.
- `wl.signal()` is the invocation's abort signal, which aborts when the
  engine cancels the invocation.
- `wl.reserve(bytes)` reserves `bytes` of the run's byte budget and the
  runtime's total, which the engine keeps across processes: it resolves
  once they are reserved, and rejects with a `RangeError` when they do
  not fit. What an invocation reserves returns when it settles. Only an
  `invoke` with `budget` makes it send `reserve` frames; without one it
  takes nothing and resolves at once.

All three throw a `TypeError` outside an open invocation (`wl.reserve`
rejects with it). An `around` wrapper runs inside its step's invocation,
so it can call them too. A step cannot replace `wl`, and a native step
has none. `wl.run.data` is for host plugins
([Host plugins](#host-plugins)): step code reads `undefined`.

#### The process scope

Work that a host's code starts for the whole process, such as a database
pool's maintenance timer, must belong to no invocation. Otherwise the
fence clears it when the invocation that happened to start it settles.
The [process scope](../../docs/glossary.md#process-scope) runs a function
outside every invocation, and keeps the caller's other async-context
stores, such as a host's tenant scope.

A module that the host provides to its steps captures the scope when it
loads, and runs each factory through it:

```ts
type InProcess = <R>(fn: (...args: any[]) => R, ...args: any[]) => R;
type ProcessScope = { capture(): InProcess };

const scope = (globalThis as unknown as Record<symbol, ProcessScope | undefined>)[
  Symbol.for("windmill-lite.processScope")
];
const inProcess: InProcess = scope?.capture() ?? ((fn, ...args) => fn(...args));

let monitor: ReturnType<typeof setInterval> | undefined;

export function startPool(): void {
  inProcess(() => {
    monitor ??= setInterval(() => {}, 30_000);
  });
}
```

- A capture at load is not enough on its own: a function that a module
  exports runs in its caller's context, so `startPool` re-enters the
  captured scope.
- The runtime publishes the API when it loads: in a spawned harness before
  the bundle's host-provided modules load, and in a host when it imports
  the SDK. So a host imports `windmill-lite/bun` before its own modules
  that capture the scope.
- An error that work in the process scope throws is unattributed.

### Native steps

A `nativets` step, or a Bun step whose header says `//native`, runs with
the rules of Windmill's native runtime
([WR-40](../../docs/semantics/languages.md#wr-40) to
[WR-45](../../docs/semantics/languages.md#wr-45)), in any module state:

- Each invocation gets a new JavaScript realm, as each native job gets a
  new isolate on Windmill. No module state or global survives it.
- The realm has the ECMAScript built-ins and Windmill's web APIs, such as
  `fetch`, `URL` and `crypto`. It has nothing of Bun's: no `Bun`, `Buffer`,
  `require`, `process` or `setImmediate`.
- `process.env`, `BASE_URL` and `BASE_INTERNAL_URL` are declared for the
  step. A `nativets` step also gets a `const` for each job variable whose
  name is a usable identifier.
- A `nativets` step imports Windmill's client from `file:///windmill.ts`.
  Any other import fails as a missing file does on Windmill.
- `fetch` sends `User-Agent: windmill/beta`, or the text of a
  `//useragent` header, unless the request sets one.
- The step ends when its entrypoint settles; settlement then clears its
  timers and aborts its requests. While the entrypoint's promise is
  pending and nothing could still settle it, the step fails at once, as
  on Windmill. A pending timer or request, or an event listener, keeps
  that failure off.

[The native boundary mode](#the-native-boundary-mode) gives the full rules.
The differences from Windmill are a named deviation of the
[equivalence contract](../../docs/design/0001-equivalence-contract.md#5-named-deviations).

## Host mode and the host SDK

A Bun host is a Bun program that runs flows of a bundle, with their steps
in its own process. It imports `connect()` from `windmill-lite/bun`.
[harnesses/README.md](../README.md#install) says how to install the
package at the engine's version, and shows a minimal host
([Run a flow from a host](../README.md#run-a-flow-from-a-host)).

A host that vendors the sources instead copies `host/` and `runtime/`
together, from one commit, and checks the copy's provenance
([Vendoring the Bun host SDK](../../docs/design/0009-host-adapters.md#vendoring-the-bun-host-sdk)).

This host runs the flow `f/app/main` of the `internal/bundle` build
fixture. Build that bundle with the
[`build` command](../../cmd/windmill-lite/README.md#build)'s example, with
`-o bundle` to write a directory, because host mode does not open an
archive. Put the host beside the engine and `bundle`, and run it with
`bun host.ts`. On a platform other than macOS arm64, change the engine's
file name.

```ts
import { resolve } from "node:path";
import { connect, HostError, type Plugin } from "windmill-lite/bun";

// The host's instance of the bundle's host-provided module f/shared/ctx.ts.
const ctx = { store: new Map<string, unknown>() };

const plugin: Plugin = {
  onModuleEnd: ({ site, record }) => {
    console.log("module", site.module, record.status);
  },
};

const host = await connect({
  engine: resolve("windmill-lite-darwin-arm64"),
  bundle: resolve("bundle"),
  workdir: process.cwd(),
  hostModules: { "f/shared/ctx.ts": ctx },
  plugin,
});

try {
  const run = await host.run("f/app/main", { name: "lite" }, { deadlineMs: 10_000 });
  console.log(run.status, run.result, ctx.store.get("last"));
} catch (e) {
  if (!(e instanceof HostError)) throw e;
  console.error("the run did not start:", e.code);
}

await host.drain(5_000);
await host.close();
```

It prints `module greet success` and `module report success`, then the
run's status, its result, and `lite`: the flow's first step wrote it into
the host's own instance of `f/shared/ctx.ts`.

### What connect() does

`connect(options)` resolves with a [`Host`](#the-host) once the engine has
welcomed the host. When a step below fails, it rejects with a `HostError`
whose `code` names that step. A `plugin` option of the wrong shape rejects
with an ordinary `Error` instead, and a bundle's own plugin of the wrong
shape with `bundle` ([Errors and refusals](#errors-and-refusals)):

1. It checks `workdir`: a directory that this process can write
   (`workdir_not_writable`), and already this process's working directory,
   once both are resolved without symbolic links (`workdir_mismatch`).
   Then it checks the fields of the `collect` option (`options`).
2. It reads the bundle's manifest (`bundle`). For a bundle with a plugin
   of its own, it refuses the `plugin` option (`options`). It checks the
   bundle's plugin (`bundle`): a key, a module of `plugins/` that the
   manifest lists, reached through no symbolic link, with the manifest's
   digest.
   - **Other bytes from the same path** are refused while another
     `connect` in progress holds that path, or once this process has copied
     a plugin from it (`bundle`). An attempt that fails before its copy
     gives the path back when no other attempt holds it.
3. It registers the host's instances of the bundle's
   [host-provided modules](../../docs/glossary.md#host-provided-module)
   (`hostModules`), before any step loads. It writes a diagnostic for a
   key that the bundle does not list, and for a listed module that the host
   left out: a step that imports it fails when it loads.
4. It extracts the bundle's codebase assets into `workdir` (`assets`).
5. It starts the engine as `<engine> host --bundle <bundle> --workdir
   <workdir> --kill-after <ms>ms`, then `-read-cache`, `-skip-args-file`,
   `-result-polls-for-all-jobs`, a `-foreign-windmill` flag for each
   origin, and `engineFlags`, as the
   options ask (`engine_start`). The
   [`host` command](../../cmd/windmill-lite/README.md#host) describes
   those flags. For a bundle with a plugin of its own, it first runs the
   same command with `-check-bundle` and the digest of the manifest it read.
   The engine reads the flags as at start and checks the bundle against its
   manifest, any `-digest` pin and that digest.
   - **The check's outcomes.** Exit 0 lets `connect` go on. Exit 1, a
     failed check, stops it with `bundle`; another exit, such as a usage
     error in `engineFlags`, with `rejected`; an engine that does not
     start, with `engine_start`.
6. It runs the harness runtime in this process, loads the bundle's own
   plugin from a private copy of the bytes it checked, and greets the
   engine. A plugin that cannot be copied or loaded rejects with `bundle`,
   and an engine that ends while it loads with `rejected`.
   The engine checks the host's Bun, the bundle, the working directory, the
   SDK's version and the plugin, and welcomes or refuses the host. A
   refusal rejects `connect` with the engine's code.

Load the host's own modules that import `node:timers` or
`node:child_process` by name only after `connect()` has resolved, for
example with `await import()`. `connect()` installs the runtime's timer and
child-process wrappers, and on Bun 1.4.3 the first named import of a
built-in module fixes its named bindings for every later importer. A
static named import in the host runs before `connect()`, so a step's
named import would miss the wrappers. A default import or `require` always
sees them.

### Connect options

| Option | Type | Default | What it sets |
|---|---|---|---|
| `engine` | `string` | Required | The path of the `windmill-lite` engine binary |
| `bundle` | `string` | Required | The bundle directory, which the engine and this process both load. Give an absolute path: in versions 0.2.0, 0.3.0, 0.4.0 and 0.4.1, `connect()` fails on a relative one. |
| `workdir` | `string` | Required | A writable directory that is already this process's working directory. The assets go there, and the engine writes each step's `args.json` there. |
| `engineArgs` | `string[]` | `[]` | Arguments before the engine's `host` command |
| `engineFlags` | `string[]` | `[]` | Flags after the `host` command's own, such as `-variables`, `-resources` or `-cpuprofile` |
| `hostModules` | `Record<string, object>` | None | The host's instances of the bundle's host-provided modules, by the keys that the host contract gives them |
| `plugin` | `Plugin` | None, or the bundle's own | The host's plugin ([Host plugins](#host-plugins)). Leave it out for a bundle built with a plugin of its own, which `connect()` then loads; it refuses both with `HostError` `options`. |
| `watchdog` | `{ killAfter: number }` | `{ killAfter: 30000 }` | The engine kills this process when no frame has come from it for `killAfter` ms. `0` turns the watchdog off. |
| `readCache` | `boolean` | `false` | Each run keeps the answer of its first read of a host's or bundle's resource or variable, the plugin's `resource` and `variable` hooks included, and answers its later reads of that path from it. An error is not kept, and a variable that a step updates and a stored state are read again ([the read cache](../../internal/platform/README.md#the-read-cache)). Windmill reads them for each job, so this is a named deviation of the [equivalence contract](../../docs/design/0001-equivalence-contract.md#5-named-deviations). |
| `resourceStore` | `{ kind: "memory" }` or `{ kind: "redis"; url: string; prefix?: string; allowEviction?: boolean }` | `{ kind: "memory" }` | The store of the resources that steps write through the job API, such as the stock client's state ([The resource store](#the-resource-store)). Available from version 0.4.0. |
| `skipArgsFile` | `boolean` | `false` | States that the host's own code, its plugin included, never reads `args.json`. For a bundle none of whose scripts can read it, the engine then writes none. |
| `identity` | `Identity` | windmill-lite's placeholders, such as the workspace `windmill-lite` | The job identity of every job ([Job identity](#job-identity)) |
| `foreignWindmillOrigins` | `string[]` | None | The origins of real Windmill instances that the host's steps call through their own client, such as `https://windmill.example.com`. A step's dispatch to one of them loses its links to the step's own job ids ([wl](../../wl/README.md#job-ids-on-a-real-windmill)). `connect()` refuses a value with user information, a query or a fragment (`options`) before it starts the engine, and the engine refuses the host with `engine_refused` when a value is not an origin that `wl` accepts. Neither message repeats the value. |
| `resultPollsForAllJobs` | `boolean` | `false` | A step's result polls answer for every job of a live run that its job read answers for, as Windmill answers them, and not only for the runs that the host's steps dispatched (`ResultPollsForAllJobs` in [wl](../../wl/README.md#options)). The engine gets it as `-result-polls-for-all-jobs`. |
| `recycle` | `{ rss?: number; samples?: number; modules?: number; retired?: { max: number; count: () => number } }` | None | Recycle triggers ([Recycle a host](#recycle-a-host)) |
| `onRecycle` | `(reason: string) => void` | None | Called once, with the first trigger that trips |
| `collect` | `{ rss: number; cooldown?: number; policy?: "idle" \| "heartbeat"; quiet?: number }` | None | A full garbage collection at a soft mark on the resident set ([Collect garbage](#collect-garbage)). Available from version 0.3.0. |
| `onEngineExit` | `(status: { code: number \| null; signal: string \| null }) => void` | Exit with code 1 | Called when the connection to the engine ends other than by `close()` |
| `diag` | `(line: string) => void` | Write to stderr | Receives each diagnostic line |

### The resource store

The option `resourceStore` chooses where the engine keeps the resources
that steps write, such as the stock client's state
([Choosing a resource store](../../docs/resource-stores.md)):

- **`{ kind: "memory" }`** keeps them in the engine for this connection
  only, a named deviation of the
  [equivalence contract](../../docs/design/0001-equivalence-contract.md#5-named-deviations).
- **`{ kind: "redis", url, prefix, allowEviction }`** keeps them on the
  Redis that `url` names, under `prefix` (default `windmill-lite:`; one or
  more characters without a colon, then a colon). Every
  engine on that Redis and prefix shares them.
- **The URL** reaches the engine only in its environment, as
  `WL_REDIS_URL`: a command line shows in `ps`. `prefix` and
  `allowEviction` become the engine's flags `-redis-prefix` and
  `-redis-allow-eviction`.
- **Refusals:** `connect()` refuses any other kind, and a `redis` store
  without a `url`, with `options`. An engine that cannot start the store
  refuses the host with `engine_refused`, and the message gives the reason.

### Job identity

An `Identity`, a type that `windmill-lite/bun` exports, sets what a job's
environment carries of its instance and user
([wl](../../wl/README.md#job-identity)). Each field is an optional string:

| Field | Job variables |
|---|---|
| `workspace` | `WM_WORKSPACE`, `WM_ROOT_WORKSPACE` |
| `username` | `WM_USERNAME` |
| `email` | `WM_EMAIL`, `WM_END_USER_EMAIL` |
| `permissionedAs` | `WM_PERMISSIONED_AS`; none means `u/<username>` |
| `baseUrl` | `WM_BASE_URL` |
| `workerGroup` | `WM_WORKER_GROUP` |

- **A job is in its run's workspace.** A step finds only the jobs of its
  workspace's runs; the jobs of a run of another workspace answer as
  unknown ids.
- **Connect's identity** reaches the engine as a file that `-identity`
  names, which `connect()` removes once the engine has answered.
- **A run's identity** goes in its run frame, as `identity`, with the
  fields in snake case (`permissioned_as`).
- **Refusals:** an identity that is not an object, has another field, or
  a field that is not a string, refuses the run with a `HostError`
  `refused` before any frame, or fails `connect()` with `options`.

### The Host

| Member | What it does |
|---|---|
| `run(path, args?, options?)` | Runs the flow at `path`, or a script with `kind: "script"`, with `args` (`{}` by default) and the [run options](#run-options). It resolves with the [run's result](#run-results). It rejects with a `HostError` `draining` or `refused` when the engine does not start the run. |
| `health()` | The process's [health](#health) |
| `drain(timeoutMs)` | Refuses new runs (`draining`), here and in the engine. It resolves once every run in flight has ended and each caller has its outcome, or at the timeout. The host may then exit. |
| `close()` | Ends the connection, and resolves once the engine has exited. The host process lives on. |
| `enginePid` | The engine's process id |

Every invocation of a run re-enters the async context that the caller had
when it called `run`, such as its tenant scope. A step reads that scope
only through a host-provided module: the step's bundle imports the module
through a shim that the build generated, which takes each export from the
host's instance.

### Run options

The run's own [hooks](../../docs/glossary.md#hook) and budgets come ahead
of the plugin's. The [host hooks ADR](../../docs/design/0008-host-hooks.md#the-hooks)
gives each hook's rules.

| Option | Type | Default | What it does |
|---|---|---|---|
| `kind` | `"flow"` or `"script"` | `"flow"` | What `path` names: a flow, or a root script, which runs as a script job |
| `intercept` | `Record<string, unknown>` | None | Static results that stand in for modules of the flow, by module id |
| `writeCapable` | `Selector[]` | None | The run's write-capable sites. The plugin's `beforeFirstWrite` is asked before the first of them launches, and the run then may have mutated. |
| `haltBefore`, `haltAfter` | `Selector[]` | None | End this run alone with status `halted`: before the first attempt of a module that a selector matches, or once such a module has its final outcome |
| `fatalErrorNames` | `string[]` | None | Error names that latch the run instead of failing a step |
| `deadlineMs` | `number` | None | The run's hard deadline, in ms from its submission. The run ends `canceled`, with `cancelCause` `deadline`, and steps that it left running are abandoned. |
| `preWriteDeadlineMs` | `number` | None | Ends the run `canceled`, with `cancelCause` `pre_write_deadline`, if it passes before the first write is approved |
| `clock` | `"real"` or `"record"` | `"real"` | `"record"` returns every sleep and retry delay at once, and the run's deviations then say so |
| `identity` | `Identity` | Connect's | The run's own job identity: each field that it sets replaces connect's for this run's jobs ([Job identity](#job-identity)) |
| `data` | any JSON value | None | The run's data, which the host plugin reads as `wl.run.data` until the run has settled. `null` means none. A value that `JSON.stringify` throws on rejects the run with that error, and one it encodes as nothing, such as a function, with `HostError` `refused`. |
| `signal` | `AbortSignal` | None | Cancels the run when it aborts |
| `onSettled` | `() => void` | None | Called once every invocation of the run has settled, after its outcome, which may be never. Release here the resources that an abandoned step may still use. It runs in the run's scope, outside every invocation. |

A deadline ends only its run. The SDK rounds it up to a whole
millisecond, and sends 0 for one of 0 or less, which has already passed:
the engine then ends the run before its first module. A deadline that is
not a finite number, or that is above `Number.MAX_SAFE_INTEGER`, refuses
the run with a `HostError` `refused`, and nothing reaches the engine.

A [selector](../../docs/glossary.md#selector) is a `Selector` object, a
type that `windmill-lite/bun` exports.
[wl](../../wl/README.md#selectors) gives how the engine matches each
form. The plugin's halts apply to every run of the host, while a run's own
halts apply to that run only.

| Field | Type | What it names |
|---|---|---|
| `flow`, `module` | `string` | The module with that id in any module list of the flow at that path |
| `script` | `string` | Every call of a script: a script step by its path, or an inline script by `<flow path>/<module id>` |
| `occurrence` | `number` | One module when its id is repeated in one module list |
| `iterations` | `number[]` | Only those iterations of the innermost loop around the module |
| `branches` | `string[]` | Only those branches of the innermost branch around the module |

For example, `{ flow: "f/app/main", module: "report" }` names the module
`report` of the flow `f/app/main`.

### Run results

| Field | Type | Meaning |
|---|---|---|
| `id` | `string` | The run's job id |
| `status` | `string` | The run's status: `success`, `failure`, `canceled`, `skipped`, `latched` or `halted` |
| `result` | `unknown` | The run's final result, parsed from JSON |
| `mayHaveMutated` | `boolean` | Whether the run may have made a write: `true` once its first write was approved |
| `warnings` | `{ code, message }[]` | The run's warnings, such as those that the plugin's `onRunEnd` adds |
| `latched` | `string` | For a latched run, the error it latched, such as `provider_declined` |
| `deviations` | `string[]` | The run's deviations that depend on a run-time setting, such as the record clock |
| `cancelCause` | `string` | For a canceled run, why: `deadline`, `pre_write_deadline`, `canceled` (its `signal` aborted, or another cancellation), `closing` (the engine closed) or `flow_timeout` (the flow's own limit) |

### Health

`health()` returns:

| Field | Meaning |
|---|---|
| `in_flight` | Invocations whose outcome is still open |
| `abandoned` | Invocations that a timeout or a cancellation abandoned, and that have not settled |
| `oldest_abandoned_ms` | The age of the oldest abandoned invocation |
| `errors` | Process-level errors that no invocation owned |
| `lag_ms` | The event loop's lag: how late the heartbeat timer fired |
| `rss` | The resident set, in bytes |
| `modules` | The script modules loaded |
| `runs_held` | The runs whose data the process holds, until the engine releases each |
| `runs` | Runs submitted and not yet done |
| `draining` | Whether `drain()` was called |
| `recycle` | The recycle trigger that tripped, or `null` |
| `collections` | The collections of the [collect trigger](#collect-garbage), in the fields below. Each duration is in ms. |
| `collections.count` | The collections that ran. One that threw is not counted. |
| `collections.lastMs` | How long the last successful one took, or `null` before the first |
| `collections.totalMs` | How long the successful ones took in all. A collection that threw counts in neither field. |
| `collections.due` | How many times a collection became due: `count`, plus the collections that threw, plus 1 while `dueNow` is `true` |
| `collections.deferredMs`, `collections.maxDeferredMs` | The total and the longest wait from due to the start of a collection, failed ones included. Both stay 0 under `"heartbeat"`. |
| `collections.dueNow` | Whether a collection is due and has not started |

### Recycle a host

A module stays loaded for the life of the process, and an abandoned step
can hold resources until the process exits. So a long-lived host starts a
new process from time to time. The `recycle` option sets the triggers,
none by default:

| Trigger | Trips when |
|---|---|
| `rss` | The resident set is at least `rss` bytes on `samples` consecutive heartbeats (default 2) |
| `modules` | More than `modules` script modules are loaded |
| `retired: { max, count }` | `count()`, the host's own count of pooled entries that are retired and not released, is more than `max` |

The runtime checks the triggers at every heartbeat. `health()` reports the
first that trips, and `onRecycle` hears it once. The host decides what to
do, usually `drain()` and exit, so that its supervisor starts a new
process. The SDK never drains on its own.

### Collect garbage

The `collect` option makes the SDK run a full garbage collection,
`Bun.gc(true)`, once the resident set reaches a soft mark. It is for a
long-lived host whose resident set grows far above its live memory between
the collections that Bun makes on its own. It is off by default.

| Field | Default | What it sets |
|---|---|---|
| `rss` | Required | The mark, in bytes. A collection becomes due at a heartbeat whose resident set is at least `rss`. |
| `cooldown` | 3000 | How long, in ms, no collection becomes due after one ends or fails: three heartbeats at the engine's default interval of 1 s |
| `policy` | `"idle"` | When a due collection runs ([the policies](#the-policies-of-collect)) |
| `quiet` | 100 | How long, in ms, the host must stay idle before `"idle"` collects |

Set the mark above what a full collection leaves of the resident set.
Otherwise a collection becomes due again at the first heartbeat after
each cooldown. A due collection stays due until it runs, even when the
resident set falls below the mark. `health().collections` counts the collections, their
durations and their waits ([Health](#health)).

#### The policies of collect

| Policy | When a due collection runs | What it costs |
|---|---|---|
| `"idle"`, the default | Once no run is open and no invocation is running (`health().runs` and `in_flight` are 0), and the host has stayed so for `quiet` ms | A collection never stops a run that is open, but it can wait ([Limits of collect](#limits-of-collect)). |
| `"heartbeat"` | At the heartbeat that found it due, even while runs are open | It can collect sooner, which kept the peak resident set lower in the measured workload below. Each collection stops the steps that are running. |

In one measured workload, with a mark of 640 MiB and a cooldown of 5 s,
the peak resident set was about 137 MiB lower than without the trigger
with `"heartbeat"`, and about 100 MiB lower with `"idle"`. `"heartbeat"`
added latency: the 95th percentile was about 9 % higher in one of two
runs. `"idle"` did not, so it is the default.

Under `"idle"`, the SDK checks whether the host is idle:

- at each heartbeat;
- when the run count drops to 0. The caller of the run whose end left the
  host idle has its outcome before any collection starts;
- with a collection due, once the quiet period has passed.

A step whose invocation is still running after its run has ended keeps
the host busy. The quiet period then starts at the first heartbeat that
finds the step ended. The
quiet period itself is for a host that runs several flows one after
another for one request. It keeps the collection out of the moment
between two of them, when no run is open, if that moment is shorter than
the quiet period.

#### Limits of collect

- **The collection is synchronous.** It stops the event loop, and every
  invocation in the process, while it runs. The host sends no frame
  meanwhile, so with the watchdog on, a collection longer than
  `watchdog.killAfter` ms makes the engine kill the host.
- **No maximum wait under `"idle"`.** A due collection waits as long as
  the host stays busy, and a host that never goes idle never collects.
  Set [recycle](#recycle-a-host) too, and act on its trigger: it stays the
  bound on memory.
- **What idle cannot see.** The SDK sees only its own runs and
  invocations. Work that the host does outside a run counts as idle, such
  as its own request handlers. A request that reaches the host while a
  collection runs waits for it.
- **Invocations that no caller waits for.** An invocation that a timeout
  abandoned, or whose outcome was fixed early, counts as idle, though its
  code may still run. The engine already has its outcome, and a
  collection starts only once no run is open, so it holds back no outcome
  that a caller waits for. Counting such an invocation would let one step
  that never settles stop every collection.
- **A failed collection.** A collection that throws is not counted. The
  SDK writes a diagnostic line, the collection is no longer due, and the
  cooldown starts as after a collection.

A collection frees only what is unreachable, so it never frees a loaded
module. It never trips or resets a recycle trigger, and the two options
can be set together. Once the connection to the engine has ended, the
trigger makes no collection, even in a host that `onEngineExit` keeps
alive.

### When the engine exits

- When the connection to the engine ends other than by `close()`, the SDK
  writes a line to stderr and exits the host with code 1, unless
  `onEngineExit` is given. Without its engine, a host runs nothing, and its
  supervisor restarts it.
- The engine's watchdog kills the host when no frame comes from it for
  `watchdog.killAfter` ms, for example when a step blocks the event loop.
  The runs in flight end with the host.

### Deploy a host on Linux

- **The working directory on tmpfs.** The engine writes each invocation's
  `args.json` in the host's working directory, and `connect` extracts the
  bundle's assets there. On tmpfs (`/dev/shm`, or a `tmpfs` mount of the
  host's container), each write is a copy in memory. Nothing there has to
  survive a restart.
- **The data directory** (`-data` in `engineFlags`, which holds the object
  store) stays where its persistence needs it. A host that can leave
  `args.json` out (`skipArgsFile`) saves the write altogether.
- **The engine's garbage collector.** The engine inherits the host's
  environment, so `GOGC` set for the host reaches it.
  [wl](../../wl/README.md#garbage-collection) gives the effect of
  `GOGC=400`.

The working directory on `/dev/shm` against the same build on the
runner's disk, measured on
[the embedder shapes](../../docs/benchmarks.md#the-embedder-shapes)
([one job per architecture](https://github.com/invakid404/windmill-lite/actions/runs/41756851459),
5 rounds). Each range is over the twelve workloads, of each one's median
change over paired rounds:

| Runner | p50 | Engine CPU per step | Host time per invocation |
|---|---|---|---|
| linux/amd64 (AMD EPYC 9V45) | −3 to −12 % | −11 to −28 % | −5 to −26 % |
| linux/arm64 (Neoverse-N2) | +1 to −7 % | −8 to −25 % | +1 to −9 % |

## Host plugins

A host plugin gives a host's hooks and providers in TypeScript. The engine
calls them over the connection, each in the scope of the run that it
serves. A plugin is a `Plugin` object, a type that `windmill-lite/bun`
exports. [wl](../../wl/README.md#host-plugins) gives the engine's rules
for plugins, and the [host hooks ADR](../../docs/design/0008-host-hooks.md)
the rules of each hook.

- **Where it comes from.** In host mode, `connect`'s `plugin` option. In a
  `warm` harness that the engine starts, the default export, or the export
  `plugin`, of the module that `--plugin` names (`wl.Options.Plugin`).
- **The bundle's own.** A bundle built with a plugin (the host contract's
  `plugin`) holds it in `plugins/`, with host-provided modules resolved to
  their shims. The engine names it with `--plugin` when `wl` names none,
  and `connect` loads it when its `plugin` option is absent
  ([What connect() does](#what-connect-does)). Once this process has copied
  a plugin, even one that fails to load, other bytes from its path are
  refused, and an unchanged file keeps its module: restart the host for a
  release.
- **When it loads.** A harness loads its plugin after the runtime has
  installed its process-wide hooks and the native realms have taken the
  web APIs, and `connect` loads a bundle's plugin at the same point. A
  `fetch` wrapper that such a plugin installs at load reaches ordinary
  steps, and no native step, and stays for the rest of the process. Once
  `connect` starts loading a bundle's plugin, even one whose load fails,
  later native realms take the first fetch view.
- **The declaration.** The harness sends the plugin's hooks and selectors
  to the engine in its hello. The engine refuses a host whose declaration
  it cannot use with `plugin_mismatch`: a malformed or ambiguous selector,
  or a provider that the host contract names and the plugin lacks.
- **A wrong shape** is refused before the hello: an interception with both
  or neither of `static` and `fn`, or a hook that is not a function.
- **Run data.** `wl.run.data` is the data of the run that a hook or an
  `around` wrapper serves: the host's `data` run option, or
  `wl.RunOptions.Data`. It is frozen, and `undefined` for a run without
  data, at load, in step code and inside `next()`, where an inner wrapper
  reads its own. A run that a hook submits through the host reads its own
  `data`, or `undefined` without it, never the hook's.
  - The process holds the data until the run has settled. Plugin code
    that runs on past that, or past the end of the connection, reads
    `undefined`.

Every hook may return a promise. `Site` is `{ path, flow, module, script }`,
and `Call` is `{ run, site, args }`. The hooks with a selector apply only
where it matches; the others see every module or every run.

| Hook | Form | When the engine calls it | What it can do |
|---|---|---|---|
| `intercept` | `{ selector, static?, fn?: (c: Call) => Outcome \| undefined }[]` | When a matching site would launch its job | Answer for the job with `static`, or with `fn`'s `Outcome`, `{ result, failed? }`. `fn` can return `undefined` to decline. The first match answers. |
| `rewriteArgs` | `{ selector, fn: (c: Call) => Record<string, unknown> }[]` | When a matching site launches its job | Return the arguments that the step runs with. Every match runs, in order. |
| `around` | `{ selector, fn: (c: AroundCall, next) => unknown }[]` | Around each invocation of a matching step, in this process | Run code around the step. `c` is `{ run, site, script, args, env }`. `next()` runs the step once, and resolves with its `{ outcome, result? }`, which stays the step's own. |
| `haltBefore`, `haltAfter` | `Selector[]` | Before a matching module's first attempt, or once it has its final outcome | End every run of the host there with status `halted` |
| `writeCapable`, `beforeFirstWrite` | `Selector[]`, `(e: { run, site }) => void` | Before the first matching site launches in a run | Veto the write by throwing, which latches the run |
| `fatalErrorNames` | `string[]` | When a module fails, in every module | Latch the run instead of retrying or continuing |
| `onModuleStart`, `onModuleEnd` | `(e: { run, site }) => void`, `(e: { run, site, record: StepRecord }) => void` | At the start and end of every module | Observe |
| `invocationEnv` | `(e: { run, site, env }) => Record<string, string> \| void` | Before every invocation | Return the invocation's environment, or change `e.env` |
| `onRunEnd` | `(t: Trace) => Warning[] \| void` | Once the run's outcome is decided | Return warnings to add to the run, each `{ code, message }` |
| `onRunSettled` | `(t: Trace) => void` | Once every invocation of the run has settled | Release what step code could still reach |
| `providers` | `Record<string, (c: Call) => Outcome \| undefined>` | At a mandatory provided site that names the provider | Answer for the site, or return `undefined`, which declines and fails the run (`provider_declined`) |
| `resource` | `(path: string, e: { run }) => { type: string; value: unknown } \| undefined` | When a run reads a resource that its own platform data does not hold | Return the resource's type and raw value, or `undefined` for none. The engine interpolates the value. |
| `variable` | `(path: string, e: { run }) => { value: string; secret?: boolean } \| undefined` | When a run reads a variable that its own platform data does not hold | Return the variable's value, or `undefined` for none. Any other answer is a `plugin_error`. |

- **A hook that throws** answers with `plugin_error`, which latches the
  run. A failed `onRunEnd` only adds a `plugin_error` warning. A hook that
  the plugin lacks answers `unknown_hook`.
- **An `around` wrapper that throws** or never calls `next()` latches the
  run. A step that never ran then fails with an error that the harness
  generated.
- **A late `next()`.** A first call of `next()` after the wrapper has
  returned, or after the invocation has ended, rejects with a `TypeError`
  and runs nothing. A further call after a first call in time returns that
  first call's promise.
- **The payloads.** A hook's `StepRecord` and `Trace` carry every field
  that a Go hook gets, in snake_case: the arguments before and after a
  rewrite, as stored and as resolved, timings, late output, child flows,
  spans, deviations and platform calls.

#### Trace detail

The whole trace costs the engine and the host about 3 µs per KB for every
run, and a step adds 2 to 4 KB. A run-end hook that reads little of it can
declare less in `traceDetail`:

| `traceDetail.onRunEnd` or `.onRunSettled` | What the hook gets |
|---|---|
| `"full"`, the default | The whole `Trace` |
| `"summary"` | A `TraceSummary`: the run's own fields, without the steps, child flows, spans, platform calls and the result |
| `"none"` | A `TraceNone`: the run's id and status |

A hook that declares less types its argument as `TraceSummary` or
`TraceNone`. The declaration refuses a level for a hook that the plugin
lacks, and a key or a level that does not exist.

## Errors and refusals

`connect()` and `run()` reject with a `HostError` for the causes in the
table below. Its `code` names the cause, and its message is
`windmill-lite: <code>: <reason>`. Two mistakes reject with an ordinary
error that has no `code`:

- `connect()` rejects with an `Error` that starts `windmill-lite plugin:`
  when the `plugin` option has a wrong shape, such as an interception with
  both or neither of `static` and `fn`. Fix the plugin. A bundle's own
  plugin of a wrong shape rejects with a `HostError` `bundle` instead.
- `run()` rejects with the `TypeError` of `JSON.stringify` when `args`
  does not serialise, such as a cycle or a `BigInt`. Pass JSON values.

| Code | From | When | What to do |
|---|---|---|---|
| `workdir_not_writable` | `connect`, or the engine | `workdir` is not a directory that this process can write. | Pass a writable directory. |
| `workdir_mismatch` | `connect`, or the engine | `workdir` is not the process's working directory. | `process.chdir(workdir)` before `connect`, or pass `process.cwd()`. |
| `options` | `connect` | The `collect` option has an `rss` that is not above 0 (missing, 0, negative or `NaN`), a `cooldown` or a `quiet` that is negative, `NaN` or infinite, or a `policy` other than `"idle"` and `"heartbeat"`. The `rss` check compares values and does not test the type, so a numeric string such as `"1"` passes it; pass a number, as the type says. `cooldown` and `quiet` must be finite numbers, so a string is refused. | Fix the option. The message names the rule that failed. |
| `options` | `connect` | The bundle has a host plugin of its own, and the `plugin` option gives another. | Leave the option out, or build the bundle without the contract's `plugin`. |
| `bundle` | `connect` | The bundle's `manifest.json` does not read. Or the bundle's own host plugin is not a module of `plugins/` that the manifest lists, does not have the manifest's digest, fails the engine's check, cannot be copied, does not load or has a wrong shape. | Pass the bundle directory, as an absolute path. Rebuild the bundle, or fix the plugin, whose message the error carries. |
| `assets` | `connect` | The bundle's assets do not extract into `workdir`. | Check the directory's space and permissions. |
| `engine_start` | `connect` | The engine binary does not start, for the check of a bundle with its own plugin or for the host command. | Check `engine`'s path and its execute permission. |
| `version_mismatch` | The engine, or the SDK | The SDK's version is not the engine's. An unversioned engine or SDK pairs with any. | Take the SDK and the engine from the same release. |
| `runtime_mismatch` | The engine | The host does not run on Bun 1.4.3. | Run the host on Bun 1.4.3. |
| `engine_refused` | The engine | The bundle does not open: another engine version built it, or it does not verify. Or a file that `engineFlags` names does not open. | Build the bundle with this engine. The message gives the reason. |
| `plugin_mismatch` | The engine | The plugin's declaration has a malformed or ambiguous selector, or lacks a provider that the host contract names. | Fix the plugin. The message names the selector or provider. |
| `bundle_mismatch` | The engine | The bundle that the host loaded is not the engine's. | Pass the same directory as `bundle`. |
| `pid_mismatch` | The engine | The host that greets the engine is not its parent process. | Start the engine through `connect`. |
| `proto_mismatch`, `harness_mismatch` | The engine | The SDK speaks another protocol, or is no host. | Take the SDK and the engine from the same release. |
| `rejected` | `connect` | The connection ended before a welcome, without a code. Or, for a bundle with its own plugin, the engine's check exited with a status other than 0 and 1, such as a usage error in `engineFlags`. | Read the engine's stderr. |
| `refused` | `run` | The engine has no such target, the arguments are not an object, or a selector option is not a list of objects, or the engine finds a selector malformed or ambiguous. The message names the selector, such as `the run's haltAfter[0]`. | Fix the path or the options. |
| `draining` | `run` | The host is draining. | Submit the run to the next host process. |

A run that starts and then fails does not reject: `run` resolves with its
status, such as `failure` or `latched`.

## Test a host

The test entry `windmill-lite/bun/testing` runs a host's process-level
code as a step would, without an engine or a bundle, and reports what the
settle-time fence released. It runs on Bun 1.4.3 and on Node 22 or newer.
[Test a host's process-level code](../README.md#test-a-hosts-process-level-code)
gives the steps and an example test.

### The test entry

| Export | What it does |
|---|---|
| `install()` | Installs the runtime's step machinery in this process: the timer fence, the process scope and the step API `wl` |
| `invoke(fn, options?)` | Runs `fn` as one invocation, settles it, and resolves with `{ result, left }`: `fn`'s result, and what settlement released, by kind |
| `currentInvocation()` | The invocation of the caller's async context, as `{ id, run, settled }`, or `undefined` |

- `install()` installs nothing that routes to a connected runtime, such
  as the `fetch` view.
- `invoke` settles the invocation with the runtime's own settlement code,
  as the runtime settles a step.
- A host that vendors the SDK imports `host/testing.ts` from its copy.

## Work on the harness

This part is for contributors who change the runtime or the SDK.

### Files

| File | What it holds |
|---|---|
| `runtime/main.ts` | A spawned harness's entry: reads the bundle's digest, extracts its codebase assets, registers its host-provided modules, loads the plugin that `--plugin` names, connects to file descriptor 3, and starts the runtime |
| `runtime/runtime.ts` | The handshake, the step runner and how it loads a step module, cancellation, health, drain, platform calls, host mode's runs, and the benchmark timings |
| `runtime/context.ts` | The per-invocation context: the environment, fetch and log views, the timer fence, child-process wrapping, the process-level error handlers, the `process.exit` interception, the registry and the process scope |
| `runtime/wrapper.ts` | Windmill's wrapper rules: binding, `Date` hydration, the result and the error's serialisation |
| `runtime/native.ts` | The native boundary mode: a realm for each invocation, the boundary between it and the web APIs, the preamble, imports and the embedded client, binding, the result, the error shapes and a stack's source line |
| `runtime/plugin.ts` | Host plugins: the `Plugin` type, its declaration, the answer to each `host_call`, and `loadPlugin` |
| `runtime/frame.ts` | The codec and the send queue: canonical envelopes, the decoding rules and error codes of the Go side, the frame reader, part streams, and the order of messages about one invocation |
| `runtime/version.ts` | The harness version and `versionsPair` |
| `host/index.ts` | The Bun host SDK: `connect`, `checkWorkdir`, `HostError`, `HOST_MODULES` and the types |
| `host/testing.ts` | The test entry |
| `test/*.test.ts`, `test/caseplugin.ts`, `test/heartbeatengine.ts`, `test/typecheck/` | The tests ([Tests](#tests)) and the type check ([Type check](#type-check)). `test/heartbeatengine.ts` stands in for the engine in the collect trigger's tests and the run deadlines' test. It welcomes the host with a heartbeat of `WL_TEST_HEARTBEAT_MS` ms, and answers each run at once, once a file exists, or after it has sent an invocation of a step. It can also answer with the run frame's deadlines, or with a canceled run's cause. |

The runtime and the SDK have no dependencies and no `package.json`. The
build helper has one, for its TypeScript parser, and the type check has
two, in `test/typecheck` and `test/typecheck/oldest`.

### Start a spawned harness

The engine starts a harness (`internal/harness`'s `BunCommand`) as:

```sh
bun harnesses/bun/runtime/main.ts --bundle <bundle dir> [--plugin <module>] [--umask <octal>]
```

With `--umask`, such as `--umask 0022`, the harness sets that umask before
it reads the bundle, so the files that it and its steps create get it.
Without it, the harness keeps the umask that it inherits. An engine that
serves with a stricter umask of its own passes the one that it was
started with.

File descriptor 3 must be one end of a stream socket pair, which carries
every frame. Standard output and standard error carry no frames, so what a
step prints cannot corrupt the protocol.

| Exit code | When |
|---|---|
| 0 | After `bye`, when the engine's end of the socket closes, or in `fresh` mode after the result (otherwise the step's own code) |
| 2 | The engine rejected the harness, or its welcome is unusable. |
| 3 | The engine broke the protocol. |
| 4 | The bundle cannot be read: its assets do not extract, or a host-provided module does not load. |
| 5 | No frame that ends an invocation fits `max_message`. |

An invocation stays in flight until a frame that ends it is queued, so the
engine always learns how it ended, by that frame or by the exit.

### The protocol side

The [harness protocol ADR](../../docs/design/0004-harness-protocol.md)
defines the frames, and
[internal/harness](../../internal/harness/README.md) is the engine's side.

- **Handshake.** The harness sends `hello` with `Bun.version`, its pid,
  the SHA-256 of the bundle's `manifest.json`, and the `fetch`, `native`
  and `fresh` capabilities. In host mode, it sends `host` in place of
  `fresh`, and the working directory as `workdir`.
- **Welcome.** The harness accepts a `warm` welcome, or a `fresh` one
  outside host mode. A refusal goes to `onReject` with its code and
  message.
- **Harness version.** The hello names the release of the harness
  (`runtime/version.ts`): `dev` in the repository. The npm package's build
  stamps `v` and the package's version with `--define WL_RELEASE`. In host
  mode, the harness refuses a welcome from an engine of another release
  with `version_mismatch` (`versionsPair`).
- **Heartbeat.** After the welcome, the harness sends a `ping` every
  `heartbeat_ms` with its health, and answers the engine's pings with the
  same.
- **Cancel** abandons the invocation. When its call ends, the outcome goes
  out as the `late` frame that settles it.
- **Fresh mode.** The process serves one invocation; a second `invoke` is
  a protocol error. It exits as soon as its result is written, however
  large. The engine kills the process on timeout or cancellation.
- **A `fresh` exit.** `process.exit`, an uncaught error or an unhandled
  rejection writes what is queued straight to the socket
  (`Transport.writeSync`), then calls the real `process.exit`. So no
  `catch` or `finally` block of the step runs.
- **Drain** stops the heartbeat. Once everything queued has gone out, the
  runtime sends `bye`, its last frame, and exits when its socket closes,
  whatever is still running.
- **The frame reader's buffer** grows by doubling, so a frame read in
  small pieces is copied a bounded number of times.

### How the runtime loads a step

- **Invocations** run concurrently on the event loop, each inside its own
  `AsyncLocalStorage` context with its id, run, environment, abort signal
  and resource registry.
- **The metadata** `scripts/bun/<id>.meta.json` and the module are read
  once in each process, on the script's first invocation and inside its
  context. The runtime keeps only the fields that it reads (`runtimeMeta`:
  `bundler`, `format`, `boundary`, `native`, `signatures`, `shared`).
- **The module** is `scripts/bun/<id>.mjs`. A codebase script (metadata
  `bundler: "esbuild"`) that is CommonJS is `<id>.cjs`, which Bun's
  CommonJS loader runs.
- **A codebase module** loads from a copy in the working directory,
  `.windmill-lite-<16 hex digits of the id's SHA-256><ext>`. Windmill
  places a codebase bundle as `main.js` in the job directory beside its
  assets ([WR-50](../../docs/semantics/languages.md#wr-50)). So the
  module's `__dirname`, `__filename`, `import.meta` and the relative paths
  of its `require` are the working directory's (`moduleFile`).
- **Codebase assets.** A spawned harness copies the bundle's `assets/`
  into its working directory before any module loads (`extractAssets`).
  A failure fails the start with exit code 4. In host mode, `connect`
  extracts them into the host's working directory.
- **Host-provided modules.** A spawned harness registers the bundle's copy
  of each, `scripts/bun/provided/<id>.mjs`, in the order of the manifest's
  `host_modules`, before it greets the engine
  ([packaging ADR](../../docs/design/0010-packaging.md)). A copy that does
  not load fails the start with exit code 4. In host mode, the host
  registers its own instances.
- **Order.** `main.ts` makes the runtime before it registers the copies,
  so every process-wide hook, the process scope included, is in place
  when they load.

Shared code ([shared module code ADR](../../docs/design/0013-shared-module-code.md)):

- In `warm` and host mode, a script whose metadata names a closure
  (`shared.closure`) loads its shared-layout module, `<id>.shared.mjs` or
  `<id>.shared.cjs`, from the same working-directory copy for a codebase
  script. `fresh` mode, and a script that keeps its per-script bundle
  (`shared.fallback`), load the per-script one.
- Before any module loads, the runtime installs a process-wide registry,
  `globalThis[Symbol.for("windmill-lite.closures")]` (`installClosures`).
  Its `instance(id, scope)` loads the closure factory
  `scripts/bun/closures/<id>.cjs` once in each process, or the ES module
  `<id>.mjs` of an ES module codebase group, whose factory takes no
  `module` or `exports`.
- Each call makes a new instance with the calling module's `module` and
  `exports` (an ES module passes neither), `require`, file names and
  `import.meta`, which its dependency code sees as in the per-script
  bundle. The closure hands the instance's table back through
  `globalThis[Symbol.for("windmill-lite.closure-table")]`, which the
  registry clears at once.
- Each step module has an instance of its own, so module state stays per
  script while the dependency code is compiled once. A dependency module
  initialises in an instance on its first reference, and loading a closure
  runs none of its modules.

How a step module is evaluated:

- Bun 1.4.3 evaluates a module that `import()` loads outside every async
  context, and one that `require()` loads inside the caller's. So
  `loadModule` evaluates a step module with `require()`, then takes its
  namespace from `import()` of the same file, which does not evaluate it
  again.
- A pending top-level `await` makes `require()` throw a `TypeError` once
  the evaluation has started; `import()` then waits for that evaluation.
  An ES module's failure stays with the module, so `import()` rejects with
  the same error. A CommonJS module's is thrown as it is: `import()` would
  evaluate it again.
- Against `import()` alone, only stack text changes. An error thrown at
  evaluation has Bun's `at unknown` frame and `loadModule`'s below the
  module's frames, and a link error gets a stack. An exported `then`, or a
  CommonJS module's thenable exports, is resolved by `import()` alone.
- An error from work that a module started while it evaluated stays the
  loading invocation's: it fails that invocation while it runs, and is
  `late` once it has settled.
- What that work writes goes to the loading invocation's log while it
  runs, and to the process's streams once it has settled (`logContext`).
  Its platform calls are then refused, as from a process scope.

### Inside the per-invocation context

The fetch wrapper, the error handlers and the `process.exit` interception
are installed once in each process, and answer to the runtime that
installed them last. So a host that connects again counts its errors and
sends its facade calls on its current connection.

The environment view. On Bun 1.4.3, `process.env`, `Bun.env` and
`import.meta.env` start as one object. Assigning `process.env` leaves the
other two on it, and neither `Bun.env` nor `Bun` can be redefined. So the
view has three parts:

1. `process.env` becomes a Proxy over the process environment. It keeps a
   step's writes and deletes in its own copy, and lists its variables when
   the environment is copied or enumerated.
2. The original object gets a Proxy as its prototype, which answers a read
   of a variable that the object does not hold from the current
   invocation. This reaches code that reads `Bun.env` or `import.meta.env`
   at run time, without the build's define rewrite.
3. Bun's object takes only data properties, so a variable that an
   invocation is given, writes or deletes is claimed for good: its value
   moves into the view's own store, and the prototype answers it.

The limits of the environment view:

- A write through the original object (`Bun.env.X = …` in code that the
  build does not rewrite) of a variable that it does not hold reaches the
  prototype's `set` trap, so it stays with the invocation.
- A write of a process variable that no invocation has claimed reaches the
  process, and so does a delete of one. A delete through the original
  object of a claimed variable changes nothing.
- Enumerating `process.env` lists the invocation's view. Enumerating
  `Bun.env`, or asking it for an own property, sees only the process's
  variables that no invocation has claimed.
- [internal/harness](../../internal/harness/README.md)'s test that
  concurrent invocations each see their own `WM_*` variables measures
  every channel, `warm` and `fresh`, with and without the variables in the
  process.

The fetch view:

- A request to the welcome's `facade` origin becomes a `platform` frame,
  with its body's bytes. Its `platform_reply` becomes the `Response`, with
  the reason phrase of Go's `net/http` as `statusText`, and no body for
  `HEAD` or a 204, 205 or 304 status. So a client sees the response that
  the facade's TCP transport gives
  ([WA-30](../../docs/semantics/client-api.md#wa-30),
  [WA-41](../../docs/semantics/client-api.md#wa-41)).
- Only a `warm` or host-mode welcome names the facade. A `fresh`
  process's requests to it go over TCP.
- A facade request made outside every invocation, or after its invocation
  settled, is refused with a `TypeError`. One whose signal has already
  aborted is not sent. One still waiting at settlement is aborted, and its
  late reply is dropped.
- Any other request passes through, with a signal of the fence's added.
  It stays registered until a reading method (`text`, `json` and the rest)
  of the response, or of a clone made while the body was unused, consumed
  the body and fulfilled (`observeTransfer`).
- No other event is evidence that a transfer ended: no stream, reader or
  pipe event, no rejection, no cancellation. Bun marks a body used as soon
  as a read starts, so a call on a used body is no evidence either. A body
  that is never read stays registered until settlement, which counts it.
- The step gets Bun's own response: only its reading methods and `clone`
  are own properties, which call the native ones. A rejection reaches the
  step unchanged. A reading method borrowed for another receiver
  (`res.text.call(other)`) calls the native one and leaves the request
  registered.
- A response with no body ends the request at once.
- A request made in an invocation's context to one of the welcome's
  `foreign_windmill_origins`, each compared as a URL's origin, under
  `/api/w/{workspace}/jobs/run/` loses each `parent_job` and `root_job`
  query parameter whose value is one of the invocation's own job ids. Those
  are the values of `WM_JOB_ID` and `WM_ROOT_FLOW_JOB_ID` in its `invoke`
  frame, which the step's writes to its environment do not change. A link
  whose value is neither of the two stays, whichever variable it came from.
- That edit removes only those pairs from the query, and keeps every other
  byte of the URL. A `Request` is made again with its method, headers,
  body and signal. A native realm's `fetch` calls this view, so a native
  step's request gets the same edit. The welcome of every module state,
  `fresh` included, carries the origins.
- The runtime reads each origin with the URL parser that reads a request's
  URL, so an internationalised name matches in either spelling. It leaves
  out an origin that the parser refuses, with a diagnostic line: no request
  can go to a host that the parser refuses.

The timer fence:

- The clear functions, and a timeout or an immediate that fires,
  unregister a handle. A `refresh()` of a timeout that fired registers it
  again, with the invocation that made it. Once that invocation has
  settled, `refresh()` re-arms nothing. Bun's `util.promisify` form is
  kept.
- Handles created outside every invocation, in a process scope, or while
  a step module evaluates are not registered. The runtime's own timers use
  `realTimers`, the functions as Bun has them.
- `node:timers` is patched before any step module imports it. In host
  mode, the SDK takes `node:child_process` through `require` for the same
  reason.

A socket's timers. The timers that Bun's networking modules set are part
of a socket, which runs on after settlement, so the fence leaves them
alone. On Bun 1.4.3 they time a socket's idle time and its connection
attempts, close a destroyed socket's handle, and track a server's
connections.

- The fence tells them by the file and function of the frame that calls
  the timer function: `node:net`'s `setTimeout`, `closeSocketHandle` and
  `internalConnectMultiple`, and `node:_http_server`'s `setTimeout` and
  `setupConnectionsTracking`.
- JavaScriptCore drops the frame of a function that ends by returning a
  call, so a step's `lookup` callback that returns `setTimeout(…)` shows
  `node:net` as the caller. The module alone would not do: that timer
  stays fenced.
- Reading the frame runs none of the step's code. The fence sets
  `Error.prepareStackTrace` and `Error.stackTraceLimit` for the capture only
  while each is a writable data property, and puts them back. A step that
  changed one fences every timer, a socket's included.

The log view:

- A console call is formatted by a Node `Console` of the invocation's own,
  without colours, so its counters, timers and groups are its own.
- Lines are merged per stream, empty ones dropped, and sent as `log`
  frames. They are batched until the code that wrote them has run on, and
  always go ahead of any other frame about the invocation. A line too long
  for a frame is cut, ending in `…`.
- The log keeps the last 600 characters for the tail of an escaped
  outcome ([WR-20](../../docs/semantics/languages.md#wr-20)). The runtime's
  own diagnostics always go to the real stderr.

Child processes:

- The wrappers cover `child_process`'s `spawn`, `exec`, `execFile` and
  `fork`, their sync forms, and `Bun.spawn` and `Bun.spawnSync`. On Bun
  1.4.3, `Bun.spawn` would otherwise pass the environment that the process
  started with. `util.promisify(exec)` and `util.promisify(execFile)` keep
  their `{stdout, stderr}` form.
- On Bun 1.4.3, every `node:child_process` function starts its child
  through `Bun.spawn` or `Bun.spawnSync`, once. So the wrappers of the two
  Bun functions alone register children, and a child is counted once.
- `Bun.$` starts its processes by neither Bun function, and is not
  wrapped. windmill-lite has no log limit, which is the third of Windmill's
  triggers that kill a job's process group.

Process groups. A Windmill job leads its own process group, which its
children join unless they start a session of their own. A timeout or a
cancellation kills the group, and a normal exit kills nothing
([WR-55](../../docs/semantics/languages.md#wr-55)).

- **In a `warm` or host process** (`setChildGroups(true)`, from the
  welcome), each child that an invocation starts is started detached, so
  it leads a group that stands for the job's. It is registered as `child`:
  alive while a process is left in the group, released by killing the
  group with `SIGKILL`.
- **The announcement.** Before the step's code goes on, a `child` frame
  announces the group to the engine (`setChildAnnouncer`).
  `Sender.flushFramesSync` writes it straight to the socket's file
  descriptor, after the frame in progress, so it arrives even if the step
  then blocks the event loop. A host writes it on its own channel instead
  ([Inside the host SDK](#inside-the-host-sdk)).
- **Who signals the group.** The engine alone, at the invocation's
  abandonment, its settlement and the process's end, and only while the
  group is still the child's. The harness cannot tell the group from a
  later one with the same id.
- **`cancel`** revokes `child` (`InvocationResources.revoke`), so those
  children, and any that the call starts afterwards, are no longer
  counted.
- **The guardian.** Each grouped child starts through `guardedCommand`'s
  `/bin/sh -c` prologue. Its subshell starts the group's guardian
  (`sleep 999999999.<token>`, with descriptors 0 to 9 closed), and exits
  before the shell runs the step's command. So `child.pid`, the streams and
  the exit status are the command's, and the token goes in the `child`
  frame.
- A command that cannot be run as given, or an `argv0` other than the
  command, is spawned without a guardian. A guarded child counts as
  `child` while its own process runs; what it left in the group is killed
  with the group, but not counted.
- **In a `fresh` process**, which the engine started as the leader of the
  job's group, each child stays in that group, which the engine waits for
  or kills. `child` is registered with a release that does nothing.
- **Detached children.** A child that the step started detached, where
  Bun honours it (`spawn`, `fork` and the sync forms; Bun drops `detached`
  for `exec` and `execFile`), is registered as `detached`. It is alive
  while its group has a process, and never killed: it is outside the job's
  group on Windmill too.
- **Synchronous calls** (`Bun.spawnSync`, and so `spawnSync`, `execSync`
  and `execFileSync`) get no group: the child's pid is known only once it
  has exited. Unless detached, the child stays in the harness's group,
  which the engine kills when the process ends.

Killing a `child` at settlement is windmill-lite's reading. Windmill holds
the job open until such a process exits, which a `warm` process cannot do
without letting the step's code run after it settles: an open question in
the [design README](../../docs/design/README.md). A `fresh` process does
what Windmill does.

Process-level errors. The `uncaughtException` and `unhandledRejection`
handlers never exit (the shared-process failure model of the harness
protocol ADR). Each error is written to stderr and attributed through the
async context that the handler runs in.

- On Bun 1.4.3, a throw from a timer, or from a `node:fs` or `node:dns`
  callback, keeps its invocation's context. An unhandled rejection or a
  microtask's throw has none.
- In a running invocation's context, the error fixes its outcome:
  `escaped`, exit code 1 ([Q-WR-2](../../docs/semantics/languages.md#q-wr-2)),
  with the error's text as its tail, in a `result` frame with `settled`
  false. How the call then ends is the `late` frame that settles it.
- In an invocation whose outcome is fixed, that was abandoned or that has
  settled, it is a `late` error that settles nothing.
- In no invocation's context, it fails nothing: health counts it in
  `errors`, and every invocation in flight gets an `unattributed_error`
  warning, cut to fit `max_message` (`fitString`).
- The handlers never throw: a failure to report goes to the diagnostics,
  since a throw from one handler would reach the other.

`process.exit`:

- Inside an invocation of a `warm` or host process, the call fixes the
  outcome, `empty` for status 0 and `escaped` with the status otherwise,
  and throws a `ProcessExit` sentinel. So `finally` blocks run, and a
  `catch` can swallow it. A sentinel that ends the call gives a `late`
  error without a body.
- In a `fresh` process, the call writes what is queued and exits at once
  ([The protocol side](#the-protocol-side)).
- The status is Bun's (`exitStatus`): a safe integer, or a string other
  than `""` that `Number` reads as one (`" "` is 0), modulo 256. No code
  means `process.exitCode`, or 0.
- A code that Bun refuses makes the call throw Bun's own `RangeError` or
  `TypeError`, and fixes nothing. `test/context.test.ts` checks
  `exitStatus` against a real Bun's `process.exit` for each of 38 codes.
- A call from an invocation whose outcome is fixed, or that has settled,
  changes nothing and is reported late. The runtime and the SDK exit
  through `exitProcess`, the real one. `process.reallyExit`,
  `process.abort` and signals still end the process.

The registry:

- Each context holds an `InvocationResources`. `track(kind, release,
  alive?)` registers something that the invocation started, and returns
  the function that unregisters it once it ended by itself. `alive`, for a
  resource whose end no event reports (a child's group), says whether it
  still runs, and `pending()` drops one that has ended.
- When the runtime queues the frame that settles the invocation, it first
  sends the `left_behind` warning with `pending()`'s counts. Then
  `settleInvocation` runs every release still pending, once, and closes
  the invocation's log, as the test entry does.
- Anything registered after settlement, or after `revoke(kind)` for its
  kind, is released at once. The connection's end settles every
  invocation too.
- Nothing global is keyed by invocation, and the counts are the only leak
  report: `process._getActiveHandles()` lists nothing on Bun 1.4.3. The
  fence never touches sockets or shared objects.

### Inside the host SDK

- **Host mode's runs.** `Runtime.submit` sends a `run` frame, and resolves
  with its `run_done`, or rejects with a `RunError` (`draining`,
  `refused`). Its `onSettled` is called on `run_settled`, in the snapshot
  of the submission's async context. `connect`'s `run` checks the
  deadlines first (`runBudget`), so the frame carries only an integer of
  0 to 2^53 − 1.
- **Drain.** `drainEngine` sends the host's `drain` behind every `run`
  queued before it (`Sender.sendAfter`), and calls back on the engine's
  `bye`.
- **Health.** `hostHealth` adds the age of the oldest abandoned invocation
  and the script modules loaded to what the pings carry, and `onHeartbeat`
  hears it at every heartbeat.
- **The heartbeat observer.** `connect` installs `onHeartbeat` only when
  `recycle` or `collect` is set, so a heartbeat without a trigger costs
  only its ping. The runtime calls it outside every invocation, once it
  has queued the heartbeat's ping: under backpressure, the socket may
  take the ping later. It checks the recycle triggers first, then the
  collect trigger (`maybeCollect`), so a collection changes neither the
  ping's health nor the recycle triggers.
- **A collection** (`collectNow`) clears `dueAt` and adds the wait, then
  calls `Bun.gc(true)`. It sets `collectedAt` at the call's end, also
  when the call throws, and only a call that returns updates `count`,
  `lastMs` and `totalMs`.
- **The idle state.** `idleSince` starts at `connect`. A submitted run
  clears it, and so does a check that finds an invocation running; the
  next check that finds none sets it again.
- **The idle check** (`checkIdle`). Each check first clears the pending
  timer, then returns while a run is open, after `close()`, or once the
  engine connection has ended. With a
  collection due and the host idle, it collects once the quiet period has
  passed, or arms a timer for the rest. At the run count's zero it never
  collects: where it would, it arms the timer, so that the caller's
  continuation runs first.
- **The quiet timer** runs outside every invocation (`invocations.exit`),
  on `realTimers`, so the fence leaves it alone. It is `unref`'d, so it
  keeps no process alive. `close()` clears it, and so does the end of the
  engine connection (`exit`), so nothing collects once the engine has
  gone.
- **The socket.** `connect` starts the engine with one end of a socket
  pair as its file descriptor 3: Node's extra `"pipe"` standard I/O, which
  Bun 1.4.3 makes a socket. The socket's backpressure reaches the
  runtime's send queue, so a small frame waits behind at most one part
  frame.
- **The child channel.** A second socket pair, the engine's file
  descriptor 4, carries only the `child` frames. The runtime's `children`
  option writes each one synchronously to the host's end, by its
  descriptor (`_handle.fd` on Bun 1.4.3), and waits for a full channel.
- **Why a channel of its own.** The socket may hold bytes that it has not
  written yet, which a synchronous write would overtake, and a step can
  fill it and then block the event loop. Without a descriptor, the
  announcements fall back to the socket, with a diagnostic.

### Inside host plugins

- **Answers.** The runtime answers a `host_call` with `dispatch`, outside
  every invocation. In host mode, it runs the hook inside the scope that
  the host had when it submitted the run; that scope is kept until the run
  settles, for `onRunSettled`. A harness without a plugin replies
  `unknown_hook` to every call.
- **`around`** (`runAround`). The runtime runs the step inside the
  matching wrappers, outermost first, in the invocation's context. A
  failed wrapper is reported in a `plugin_error` warning before the
  invocation's result. A late first call of `next()` rejects and starts
  nothing, so a continuation that the wrapper left behind never runs the
  step after its invocation settled.
- **The step API** is `installStepAPI` in `context.ts`. `wl.latch(error)`
  is sent as a `latch` warning ahead of the step's result.
- **Run data.** The runtime keeps the `run_data` of an `invoke` or a
  `host_call`, deeply frozen, in a cell under its `run_data_id`
  (`keepRunData`). It drops the cell at the `run_released` that names it,
  or when the connection ends, and empties it then.
  - `asPlugin` runs a hook or a wrapper inside `pluginRun`, an
    `AsyncLocalStorage` whose store is the frame's cell, and `next()`
    leaves it. A frame without a `run_data_id` gets an empty cell.
  - `submit` captures the host's scope without `pluginRun`, so a run that
    a plugin hook submits does not inherit the hook's cell.
  - Health's `runs_held` counts the cells kept.
- `test/caseplugin.ts` is the plugin of the cases of
  `conformance/plugins/hooks.yaml`, which the Go tests load in `warm`
  processes and in a compiled host. Its `onRunSettled` writes a report
  file of each run, and of whether one of its hooks still ran.

### The step runner

`run` applies Windmill's Bun wrapper rules (`wrapper.ts`) in the wrapper's
order ([WR-2](../../docs/semantics/languages.md#wr-2)):

1. **The signature** is what the build recorded under the entrypoint's
   name in `<id>.meta.json`: for every export and every top-level function
   declaration, as Windmill's parser reads them
   ([WR-7](../../docs/semantics/languages.md#wr-7)). One that did not
   parse fails the step before its module loads, as an `error` outcome
   with `origin` `harness`. Its object is Windmill's worker error:
   `{ message: "execution error:\n" + <the parser's message>, name:
   "ExecutionErr", step_id }`
   ([WR-37](../../docs/semantics/languages.md#wr-37)). Metadata that cannot
   be read is `escaped`.
2. **The module** loads. A failure to load or evaluate it is `escaped` on
   every invocation of the script
   ([WR-19](../../docs/semantics/languages.md#wr-19)). An intercepted
   `process.exit` whose sentinel escapes the evaluation ends every
   invocation of it as that exit did.
3. **The export** must be a function, or the step fails with `<entry>
   function is missing` in the wrapper-catch shape
   ([WR-12](../../docs/semantics/languages.md#wr-12)). An exported function
   whose signature a Bun-built bundle does not record is `escaped`: the
   build signs every export. A codebase module's export that its metadata
   does not sign gets an empty parameter list
   ([WR-6](../../docs/semantics/languages.md#wr-6)).
4. **Binding** ([WR-6](../../docs/semantics/languages.md#wr-6)): with
   parameters, each one's value by name, as destructuring reads it (a
   missing key gives `undefined`, `null` stays, extra keys are dropped,
   nothing is coerced). A `Date` parameter gets `new Date(value)` for a
   truthy value, so a number is epoch milliseconds, and `undefined` for a
   falsy one ([WR-8](../../docs/semantics/languages.md#wr-8)).
5. **Binding without parameters:** the values of the non-underscore keys
   when the arguments carry `_ENTRYPOINT_OVERRIDE`
   ([WR-V1](../../docs/semantics/languages.md#wr-v1)), and no arguments
   otherwise. The engine chose the export from the same argument
   (`engine.Entrypoint`).
6. **The call** is awaited, after the BigInt patch is installed. The
   result is serialised as `JSON.stringify(value ?? null)` with the
   `undefined` replacer ([WR-13](../../docs/semantics/languages.md#wr-13),
   [WR-14](../../docs/semantics/languages.md#wr-14)).
7. **A throw** from the export check, the binding, the call or the
   serialisation is printed to the invocation's log, shaped as the object
   of [WR-18](../../docs/semantics/languages.md#wr-18): `message`, `name`,
   `stack`, the step context keys, then `extra`. When that object cannot be
   built or serialised (`throw null`, a cycle in `extra`), the failure is
   `escaped`, with the handler's error at the end of its log.

- **The step context keys** are `step_id`, `job_id` and `flow_path`, from
  `WM_FLOW_STEP_ID`, `WM_JOB_ID` and `WM_FLOW_PATH` as the step sees them
  ([FX-12](../../docs/semantics/execution.md#fx-12)). Each is left out when
  its variable is empty. An unparsable signature's error has `step_id`
  alone.
- **A result larger than `max_message`** is `escaped`, with a tail that
  says so, or with none when that frame does not fit either. If no frame
  fits, the harness exits with code 5.
- **The stored value.** The engine maps each outcome to the value that
  Windmill stores (`engine.StoreOutcome`,
  [internal/engine](../../internal/engine/README.md)), which keeps the
  tail's text after the last `CODE EXECUTION ---` marker, trimmed.

### The native boundary mode

`native.ts` runs an invocation whose `boundary` is `native`, with the
rules of Windmill's in-worker native runtime
([WR-40](../../docs/semantics/languages.md#wr-40) to
[WR-45](../../docs/semantics/languages.md#wr-45)).

The realm:

- Each invocation gets a new `node:vm` context
  ([WR-41](../../docs/semantics/languages.md#wr-41)). Its global object
  has the ECMAScript built-ins and the web APIs that the native runtime
  has ([WR-45](../../docs/semantics/languages.md#wr-45)), taken
  from this realm once `fetch` is wrapped and before any step loads. Bun
  1.4.3 has no `FileReader`.
- It also has a `Deno` object that holds only an empty `core`, and
  `addEventListener`, `removeEventListener` and `dispatchEvent` on
  `globalThis`. It has no `ShadowRealm`.

The boundary fails closed. Every promise and object that a web API hands
the realm is tracked as the calling invocation's at once, or handed over
untracked and counted. The boundary sits on the realm's only ways into the
web APIs, so what it cannot tell apart is counted rather than missed.

- **Where it sits.** Every listed web API class is given to the realm as
  a realm-facing subclass, made once in each process, which inherits from
  its parent's (`CustomEvent` from `Event`). Bun's `File` shares `Blob`'s
  prototype, so the realm's `File` is chained to the realm's `Blob`.
- **Members.** A subclass's constructor is the only way that the realm
  makes one. Each member, own, inherited, symbol-named or static, calls
  the original and hands on what it returns or throws, and so do `atob`,
  `btoa`, `reportError` and `structuredClone`. `crypto`, `crypto.subtle`
  and `performance` are objects of their subclasses that forward to
  them.
- **Callbacks.** What the harness calls back is wrapped where the realm
  hands it over. These are a method's callback argument
  (`FormData.forEach`), the callbacks of a stream's source, sink,
  transformer and queuing strategy, and every listener and `on…` handler.
  A listener runs in the async context that registered it, and its
  identity is kept for `removeEventListener`.
- **Primitives and the realm's own objects** are handed on as they are.
- **A promise of the harness realm** is tracked in place: it takes the
  prototype of a `Promise` subclass whose `then` records the invocation on
  every promise that it derives. So a rejection nobody handles is
  attributed to the invocation, while one that the web API's own code
  handles stays handled.
- **Values.** An instance of a listed class takes its subclass's
  prototype. An array, `Map`, `Set` or plain object has its contents
  handed on, once, and an iterator gets a forwarder whose results are
  handed on. Buffers, views, dates, regular expressions, errors, boxed
  primitives and parsed JSON are data, handed on as they are.
- **What it only counts.** Anything else of the harness realm, such as
  Bun's `performance.timing` or a harness function, is handed over and
  counted. `runtime.ts` sends the count as a `native_untracked` warning
  just before the frame that settles the invocation. One handed out in no
  native invocation's context goes to every invocation in flight, or to a
  diagnostic line.
- A fetched response's `url`, `redirected` and `type` are kept for it and
  every clone. The harness realm's classes are never changed, so a web
  API's promise is not an `instanceof` the realm's `Promise`.

Timers, fetch and the module:

- `setTimeout` and `setInterval` return numeric ids. The realm registers
  each pending timer and each fetch in flight itself, on `realTimers` and
  through the fetch view run with no work registry, so each is registered
  once.
- A fetch stays registered until its body has been read to its end, has
  failed or was cancelled, whatever reads it. A body still open at
  settlement is aborted and counted in the `left_behind` warning.
- The module is the script's native form, `scripts/bun/<id>.native.js`
  ([build/](build/README.md)): one async arrow function, so its scope has
  no `arguments`, and its module-level `this` is `undefined`.
- Its preamble declares `process = { env }`, `BASE_URL` and
  `BASE_INTERNAL_URL`, and for `nativets` a `const` for each job variable
  whose name is an ASCII identifier that is neither a strict-mode reserved
  word nor one of those three names. Any other variable is in
  `process.env` alone.
- The values are the invocation's job variables. Both base URLs are the
  facade's origin: the welcome's, or in a `fresh` process the `BASE_URL`
  that it was started with.

Compiled once, run in each realm. Never compile a script from a string
with `node:vm` on an invocation's path. Compile it once, as a
module-level `vm.Script` or in a bounded cache, and run it in each new
context with `script.runInContext(ctx)`, which keeps nothing.
`cmd/windmill-lite/realm_test.go` relies on this rule.

- **Why.** Under Bun 1.3.10, every script that `node:vm` compiled from a
  string stayed reachable after a full collection, so a process grew with
  every native invocation. On Bun 1.4.3 none stays, but compiling once
  still saves a compile for each invocation.
- **What that rules out** on an invocation's path: `vm.runInContext`,
  `vm.runInNewContext` and `vm.runInThisContext` with a string,
  `new vm.Script`, and `vm.compileFunction`, which keeps about 72 KiB of
  heap per call. The realm's own `eval` and `Function` keep nothing.
- **What is compiled once.** The realm's global read (`GLOBAL_SCRIPT`) and
  its helpers (`REALM_SCRIPT`), once in each process. A module, once in
  each process for each of its first 16 preambles (`MAX_VARIANTS`). A
  further preamble is compiled by the realm's `eval` on each load, with the
  module's URL in a `sourceURL` comment, so bindings and stacks match.
- **Which names a preamble declares.** Of the variables that get a
  `const`, each one that the module's text spells, with its `\u` escapes or
  without, or every one when the text spells `eval`. Also `eval` and
  `arguments`, whose `const` fails to compile, so a variable of either name
  fails the load.
- **Why the bound holds.** The reserved job variables have the same names
  in every invocation. A host's environment hook may add names that change
  between invocations. A module that never spells them keeps one preamble,
  and any other keeps at most 16 compiled variants.

Imports, the call and errors:

- Imports resolve against `file:///eval.ts`. For `nativets`, every
  spelling of `file:///windmill.ts` is the embedded client
  (`scripts/native/windmill.js`), loaded in the same realm with its own
  preamble. Anything else cannot be loaded, since the harness reads no
  file of the host ([WR-40](../../docs/semantics/languages.md#wr-40)).
- A static import that cannot load fails the load with `Failed to load
  <url>: No such file or directory (os error 2)`. A dynamic `import()`
  rejects with a `TypeError`: `Failed to load <url>`, then the line
  `  Caused by:`, then the same reason indented by four spaces. A named
  import that the module does not export fails the link.
- `nativets` always calls `main`; `//native` calls the entry, so an
  override applies. Each parameter takes its argument parsed in the realm,
  `undefined` when missing, never hydrated. The result is
  `JSON.stringify(value ?? null)` in the realm, with no replacer.
- A thrown value is `{message, stack, name}`. For an `Error` of the realm
  or of a web API, the stack has a source line in front when its first
  frame is in the script. Anything else gives three `null`s.
- The source line of a `//native` script is the line that the frame
  points at. A `nativets` script's is the one that Windmill 1.862.0 reads
  ([WR-43](../../docs/semantics/languages.md#wr-43)): the frame's line,
  after Windmill's preamble, is looked up in the transpile's line map.
- Windmill's preamble (`NativeJob.windmillPreamble`) has four fixed lines,
  then each variable, sorted, then an empty line. A variable is its
  `const`, when it gets one, and its `process.env` entry, with an empty
  line between two. So with n variables, all `const`s, the code starts on
  line 3n + 5.
- What can differ: only the preamble's third line is recorded, the code
  lines are the native form's, and the map is of Bun's transpile, not
  Windmill's.
- The step context keys (`step_id`, `job_id`, `flow_path`) come from the
  invocation's environment, not from the realm's `process.env`
  ([FX-12](../../docs/semantics/execution.md#fx-12)).
- A transpile error, a signature that did not parse, a `//proxy` header
  (an Enterprise Edition feature) and a load or link failure are the
  worker's `execution error:` object, with `step_id` alone. Their `result`
  frame says `origin` `harness`.

What holds a native invocation:

- Its process-level errors are its own: by its async context, by the realm
  of the promise, or directly. One fails a running invocation; after its
  outcome, it is a late error.
- The invocation ends when its entrypoint settles, whatever it still
  holds; settlement then releases that. The checks below apply only while
  the entrypoint's promise is pending.
- When the entrypoint's promise is pending and nothing could still settle
  it, the invocation fails with `Promise resolution is still pending but
  the event loop has already resolved`
  ([WR-43](../../docs/semantics/languages.md#wr-43)). The harness checks
  every 10 ms, and needs two such checks in a row.
- What keeps that failure off until the work ends: a pending timer or
  request, or a pending promise that a web API handed it.
- What keeps it off for good, so the step waits for its timeout: an event
  listener or `on…` handler, `Atomics.waitAsync` or WebAssembly's
  asynchronous compilation, and anything handed to it untracked.

### Benchmark timings

The runtime records when each invocation reaches the
[stages](../../docs/benchmarks.md#stages) of its round trip, for the step
boundary benchmarks. It records only while
`globalThis[Symbol.for("windmill-lite.timings")]` holds an array, the
sink, which only a benchmark's own step sets. Unset, the runtime reads the
sink once per frame and once per settled invocation, and records nothing.

While the sink is set, the runtime appends one record for each invocation
that it settles with a `result` frame. Each reading is a
`Bun.nanoseconds()` value:

| Field | When |
|---|---|
| `id` | The invocation's id |
| `rx` | The frame that completes its `invoke` was read |
| `dec` | That `invoke` was decoded |
| `call` | Its entrypoint was called, after the module loaded and the arguments were bound |
| `ret` | The entrypoint's promise settled with a value |
| `ser` | The result was serialised |
| `sent` | The `result` frame was handed to the socket |

An invocation that did not reach a stage, such as one whose module failed
to load, has no `call`, `ret` or `ser`. The benchmarks' probe step sets
the sink before the measured runs, and takes the records after them. The
engine side reads its own clock for the same invocations
([internal/harness](../../internal/harness/README.md#benchmarks)).

### Tests

The tests run with `bun test`, after the build helper's dependencies are
installed:

```sh
(cd harnesses/bun/build && bun install --frozen-lockfile)
cd harnesses/bun && bun test
```

| File | What it checks |
|---|---|
| `test/frames.test.ts` | The codec, against the golden frames that the Go tests write, `conformance/protocol/frames.json` |
| `test/wrapper.test.ts` | The wrapper's rules |
| `test/native.test.ts` | The native boundary mode: the build's native form, the realm's globals, the fetch user agent, settlement, the error shapes and which are generated. Also the boundary for every listed class, untracked counts, a pending entrypoint that nothing can settle, and the preamble's names and variants. |
| `test/fit.test.ts` | How a warning's message is cut to fit `max_message` |
| `test/describe.test.ts` | The text that the harness's own messages give an error: its stack as Bun builds it, which keeps the message on Bun 1.4.3 |
| `test/sender.test.ts` | The send queue, against a socket that accepts a few bytes at a time: priority, the order per invocation, partial writes, `whenFlushed`, and the barrier of a host's `drain` behind large runs, with a pong still overtaking them |
| `test/context.test.ts` | The environment view's rules in one process, the registry, the process scope, the exit status and the step API |
| `test/fence.test.ts` | The settle-time fence: timers and `refresh()`, the fetch view and the evidence that ends a transfer, the log view, the `Bun.env` write trap, children's environment, and their process groups |
| `test/plugin.test.ts` | Host plugins: the declaration, its trace detail, each hook's answer, the `unknown_hook` and `plugin_error` refusals, and the answer in the run's scope. A run's data as `wl.run.data` until `run_released`: two runs of one id apart, a continuation's late read, and the end of the connection |
| `test/host.test.ts` | The SDK: the shared working-directory cases (`conformance/protocol/host/workdir.json`) through `checkWorkdir`, the shared version cases (`versions.json`) through `versionsPair`, the refusals before the engine starts, the assets, a run's `onSettled` in its own scope, and the run frame's selectors, halts and deadlines. `test/argsengine.ts`, a stand-in engine, records the flags that `resourceStore` passes, and whether its `WL_REDIS_URL` is set and equals the expected URL. With the variable unset and set in the test, the URL reaches the engine's environment, never its arguments. |
| `test/host.test.ts`, the collect trigger | Hosts that `test/heartbeatengine.ts` welcomes, mostly with a 10 ms heartbeat, with a stand-in for `Bun.gc`. The tests check the mark, the cooldown, the counters, failed collections and the `options` refusals. For the default policy, `"idle"`, they check its waits for runs, steps and the quiet period, and how `close()` and an engine exit clear its timer. They also check `"heartbeat"`, the recycle triggers left as they are, and when the heartbeat observer runs. |
| `test/load.test.ts` | How a step module is evaluated: the caller's context at an ES module's top level, its static imports and a CommonJS module, after a top-level `await`, `import()`'s namespace, one evaluation, and a failure's own error |
| `test/testing.test.ts` | The test entry against the runtime: timers by kind, the process scope and its control, sequential, nested and concurrent steps, a returned thenable, the invocation query, a throw, other async-context stores, and the step API |
| `test/build.test.ts` | The build helper's scans and parse |

CI's `Bun harness checks` job runs them, and does not block a merge. The
required checks are the Go side's:

- [internal/harness](../../internal/harness/README.md) drives this runtime
  through every exchange in `conformance/protocol/exchanges/`;
- [cmd/wltest](../../cmd/wltest/README.md) runs the step suite of
  `conformance/steps` through it;
- [internal/bundle](../../internal/bundle/README.md) runs the build helper;
- [cmd/windmill-lite](../../cmd/windmill-lite/README.md#tests) runs host
  mode end to end, in a host compiled with `bun build --compile`.

### Type check

`test/typecheck/check.sh` type-checks `host/` and `runtime/` as a host
that vendors them as source sees them
([host adapters ADR](../../docs/design/0009-host-adapters.md)). It copies
both directories into a temporary package of type module, and runs
TypeScript 6.0.3 against `@types/bun` 1.4.3, the type declarations of the
pinned Bun.

`@types/bun` accepts any version of `@types/node`, and a host brings its
own. So the check runs against two sets of declarations, each pinned by
its own lockfile:

| Directory | `@types/node` | Which version |
|---|---|---|
| `test/typecheck` | 26.6.6 | The newest |
| `test/typecheck/oldest` | 24.19.1 | The oldest that a host is expected to use |

- Its `tsconfig.json` resolves modules as Node.js does (`NodeNext`).
- For each set, it runs `tsc` four times: with and without the DOM
  library, each in strict and in non-strict mode. It exits 1 if any of the
  eight runs reports an error.
- CI's `Bun SDK types` job runs it, and does not block a merge.
- Renovate keeps `@types/bun` at 1.4.3 in both sets, and the oldest set's
  `@types/node` within major version 24.

The code uses only names that Bun's type declarations define, with or
without the DOM library, and that both versions of `@types/node` declare.
For example, `@types/node` 24 exports no `Console` type from
`node:console`, so the runtime types a console as
`InstanceType<typeof import("node:console").Console>`. Where the
declarations miss what Bun does, the code names the gap in a comment, and
casts once: `RequestOf`, and the file-descriptor form of `Bun.connect`.

To run it:

1. Install the newest set:
   `(cd harnesses/bun/test/typecheck && bun install --frozen-lockfile)`.
2. Install the oldest set:
   `(cd harnesses/bun/test/typecheck/oldest && bun install --frozen-lockfile)`.
3. Run `harnesses/bun/test/typecheck/check.sh`.
