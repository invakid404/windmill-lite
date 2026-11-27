# wl/plugin

**Current state:** the package launches the engine as a go-plugin plugin,
pins its executable by SHA-256, calls its `Runner` service and, if the
host wants, supervises it. The engine's side is the
`windmill-lite plugin` command
([cmd/windmill-lite](../../cmd/windmill-lite/README.md#plugin)), which
answers every call, `Health` included, from its own state.
`RunWithHooks` serves a run's hooks on go-plugin's broker, so that Go
callbacks answer them.

This README covers `wl/plugin/`, the Go package
`github.com/invakid404/windmill-lite/wl/plugin`, with its generated code and
`testdata/`.

`wl/plugin` is the host's side of windmill-lite's go-plugin adapter
([host adapters](../../docs/design/0009-host-adapters.md#the-go-plugin-adapter)).
[hashicorp/go-plugin](https://github.com/hashicorp/go-plugin) is a library
that starts a plugin as a child process and calls it over gRPC on a private
socket. A Go [host](../../docs/glossary.md#host) uses this package to start
the [engine](../../docs/glossary.md#engine) binary that way, check that it
is the expected build, and run [flows](../../docs/glossary.md#flow) and
[root](../../docs/glossary.md#root) [scripts](../../docs/glossary.md#script)
of its [bundle](../../docs/glossary.md#bundle) through the `Runner` service.

The service and its messages are defined in
[proto/windmill_lite/v1/runner.proto](../../proto/README.md). Their Go code
is generated into this package: the message types, such as `RunRequest`,
`RunResult` and `BuildInfo`, and the gRPC client and server types.

## Launch the engine

1. Make go-plugin's client configuration with `ClientConfig`, from a
   `Launch` that names the executable, its SHA-256 and its configuration
   file. `ClientConfig` also returns the launch's cleanup.
2. Defer the cleanup.
3. Pass the configuration to go-plugin's `NewClient`, and defer the
   client's `Kill`, which then runs before the cleanup.
4. Connect with the client's `Client`, then call `Dispense(PluginName)`,
   which returns a `*Client`.
5. Wait, within your deadline, until `Health` answers `SERVING`. The
   engine serves while it opens its bundle: until then health answers
   `NOT_SERVING`, and `BuildInfo` and `Run` answer `UNAVAILABLE`.
6. Compare `BuildInfo` with the release record that the host ships: at
   least the bundle digest, the engine version, the Bun version, the
   configuration's digest and the labels.
7. Run flows and scripts with `Run`.

This example, from `example_test.go`, does the seven steps. Its
`waitServing` polls `Health` until the context ends, and its `admit`
compares those five fields with the release record `want`:

```go
cfg, cleanup, err := plugin.ClientConfig(plugin.Launch{
	Path:   "/opt/windmill-lite/windmill-lite",
	SHA256: want.EngineSHA256,
	Config: "/etc/windmill-lite/plugin.json",
	Env:    []string{"PATH=/usr/local/bin:/usr/bin:/bin"},
})
if err != nil {
	return err
}
defer cleanup()
client := goplugin.NewClient(cfg)
defer client.Kill()

conn, err := client.Client()
if err != nil {
	return err
}
raw, err := conn.Dispense(plugin.PluginName)
if err != nil {
	return err
}
engine := raw.(*plugin.Client)

if err := waitServing(ctx, engine); err != nil {
	return err
}
info, err := engine.BuildInfo(ctx)
if err != nil {
	return err
}
if err := admit(info, want); err != nil {
	return err
}

res, err := engine.Run(ctx, &plugin.RunRequest{
	Path: "f/app/main",
	Args: []byte(`{"name": "lite"}`),
})
```

### The launch

| `Launch` field | What it sets | Default |
|---|---|---|
| `Path` | The engine executable, started as `<Path> plugin -config <Config>`. A relative path is resolved against the current directory when `ClientConfig` runs, and the launch hashes and starts that absolute path: it never searches `PATH` | required |
| `SHA256` | The SHA-256 that the executable must have, in hexadecimal | required |
| `Config` | The path of the engine's configuration file | required |
| `Env` | The engine's whole environment, as `KEY=value` entries | none |
| `TempDir` | Where the launch creates its private base directory; a relative one is resolved like `Path` | `os.TempDir()` |
| `MaxMessageBytes` | The largest gRPC message that the host sends or receives, the calls of a run's hooks included | 536870912 bytes (512 MiB), the harness protocol's limit |
| `ReplyMargin` | How long before the context's deadline `Run` sets the run's hard deadline | 1 s |
| `PluginTimeout` | The engine's `plugin_timeout_ms` as the host knows it, after which `RunWithHooks` first asks `GetRun` about a registration | 60 s |
| `StartTimeout` | go-plugin's start timeout, which applies to two intervals one after the other: the check of the SHA-256 and the start of the process, then the wait for the engine's handshake line | go-plugin's, 1 minute |
| `Logger` | The logger of go-plugin and of the engine's standard error | go-plugin's |

`ClientConfig` refuses a `Launch` without `Path` or `Config`, with a
`SHA256` that is not 64 hexadecimal digits, or with a negative
`MaxMessageBytes`, `ReplyMargin` or `PluginTimeout`. Each configuration
serves one launch.

- **The pin.** The launch hashes the executable just before it starts it.
  On a mismatch it starts no process, and go-plugin's `Start` returns
  `ErrChecksumMismatch`. The host keeps the file where no one else can
  change it between the check and the start.
- **A regular file.** The executable must be a regular file. The launch
  opens it without blocking and refuses anything else, such as a named
  pipe or a directory, at once.
- **The start timeout.** The launch checks the start timeout before each
  read of 1 MiB of the file, just before it starts the process, and just
  after. When the timeout has ended at one of these checks, `Start` returns
  `context.DeadlineExceeded` and no process is left: one that has just
  started is killed and reaped. Once the last check has passed, go-plugin's
  wait for the handshake line begins.
- **Why not `SecureConfig`.** go-plugin's own check of the executable
  cannot work here: go-plugin leaves the command's path empty when a runner
  of the host's starts the plugin.
- **The environment.** The engine gets `Env` and go-plugin's handshake
  variables, such as the magic cookie, `PLUGIN_PROTOCOL_VERSIONS` and
  `PLUGIN_UNIX_SOCKET_DIR`, and nothing else of the host's environment
  (go-plugin's `SkipHostEnv`). A handshake variable wins over an entry of
  `Env` with the same name. The engine's standard input is empty.
- **The sockets.** `ClientConfig` creates a fresh base directory of mode
  `0700`, `UnixSocketConfig.TempDir` in the configuration. go-plugin makes
  a directory in it for the engine's socket and for both sides' broker
  sockets, and the launch sets that directory to mode `0700` before the
  engine starts. Both modes hold whatever the host's umask.
- **Their removal.** The launch removes the base directory once the engine
  has exited, after `Kill` or on its own, and when its runner fails to
  start the engine.
- **The cleanup.** The cleanup removes the base directory in the other
  cases too: when go-plugin's `Start` fails before it calls the runner, or
  when the configuration is never started. Call it once `Kill` has
  returned or `Start` has failed.
- **One removal.** The runner and the cleanup remove the base directory
  once between them. After that, the cleanup does nothing, so it never
  removes a later directory of the same name. After a removal that failed,
  it tries again.
- **The length of a socket path.** macOS refuses a Unix socket path of 104
  bytes or more. go-plugin adds up to 38 bytes to the base directory, so
  `ClientConfig` refuses a base directory longer than 65 bytes: set
  `TempDir` to a shorter directory then.
- **gRPC only.** The configuration allows only go-plugin's gRPC protocol,
  so a plugin that speaks go-plugin's `net/rpc` protocol fails to start.

## Call the engine

`Client` holds typed calls of one connection, which are safe for
concurrent use.

| Method | What it does |
|---|---|
| `BuildInfo` | Returns the engine's build identity: the bundle digest, the engine and Bun versions, the configuration's digest and labels, and the `Runner` version of the connection |
| `Health` | Returns the gRPC health status of the service `windmill_lite.v1.Runner`. go-plugin's own health server, gRPC's standard one, answers `SERVING` for the services `""` and `plugin` and has no status for `Runner`, so an engine that does not answer for `Runner` gives an error, not that `SERVING` |
| `Run` | Runs a flow or a root script to its end and returns its `RunResult` |
| `Cancel` | Cancels a run in flight by its id, with the reason and the canceler that its cancellation names |
| `RunWithHooks` | Runs a flow as `Run` does, with the run's hooks answered by a `HookHandler` on go-plugin's broker ([A run's hooks](#a-runs-hooks)) |
| `Registrations`, `Close` | How many runs' hooks the `Client` holds; `Close` drops them all, stops the `Client`'s broker connection and refuses later runs with hooks |

### Deadlines of a run

`Run` gives the run the earlier of two hard deadlines: the request's own
`deadline_ms`, and the context's deadline less the reply margin, in whole
milliseconds. Without either deadline the run has none. `Run` sends a copy
of the request with that deadline, and does not change the caller's
request.

The margin is the time that the engine has, after the run's deadline, to
end the run and send its `RunResult` before the call's own deadline. It
does not guarantee that the result arrives in time. A run-end hook of the
bundle's host plugin can take up to the engine's `plugin_timeout_ms`, so
choose a margin that covers it. When the call ends first, `Run` returns
`ErrOutcomeUnknown`.

| Request's `deadline_ms` | Context's deadline | `deadline_ms` sent |
|---|---|---|
| absent | none | absent |
| 500 | none | 500 |
| absent | in 10 s | 9000, with the default margin of 1 s |
| 500 | in 10 s | 500 |
| 20000 | in 10 s | 9000 |

A context that has already ended starts no call: `Run` returns
`ErrNotStarted` with the context's cause.

### Outcomes of a run

Every run that starts answers a `RunResult`, a failed or canceled one
included. A `Run` error says whether the run started:

| Error | When | What the host may do |
|---|---|---|
| `ErrNotStarted` | The engine refused the run with a gRPC error whose details hold a `google.rpc.ErrorInfo` with the domain `windmill-lite` (`ErrorDomain`) and the reason `RUN_NOT_STARTED` (`ReasonRunNotStarted`), or the context had ended | Retry the run |
| `ErrOutcomeUnknown` | Any other error: the call's deadline, a lost connection, a transport's `UNAVAILABLE` without that detail, `INTERNAL` | Treat the run as one that may have mutated |

The error of a call that `Run` made also wraps the call's gRPC error, so
`status.Code(err)` gives its code, such as `ALREADY_EXISTS` for an id that
a run in flight has.

A flow run's `RunResult` can carry the run-end answer of the bundle's
host plugin: `GetAnswer()` is the JSON text of the answer that the
plugin's `onRunEnd` returned beside its warnings
([wl](../README.md#host-plugins)). An empty field means no answer, and the
text `null` an answer of JSON null. A secret entry of the engine's
`harness_env` is masked in it, which can turn `null` into a string, but
never into no answer.

- **Every outcome of a flow run.** The engine calls `onRunEnd` however a
  flow run ended, a failed, latched or canceled run included, and one
  whose deadline passed before any step. A root script's run calls no
  run-end hook, so its `answer` is always empty.
- **No answer.** A run has none when `onRunEnd` returns warnings or
  nothing, fails, gets no reply within `plugin_timeout_ms`, or answers more
  than 1 MiB of JSON text. Each failure adds a `plugin_error` warning.
- **The reply margin.** The run's `RunResult` comes after its `onRunEnd`,
  so a margin that covers `plugin_timeout_ms` lets the answer arrive
  ([Deadlines of a run](#deadlines-of-a-run)).

### A run's hooks

`RunWithHooks(ctx, req, declaration, h)` gives a flow run hooks that the
host answers in Go ([hooks across the plugin boundary](../../docs/design/0009-host-adapters.md#hooks-across-the-plugin-boundary)).
The declaration is a host plugin's, as JSON, without `around`, which the
engine refuses.

- **The calls.** The engine calls `h` with a `HookCall`: a `host_call`'s
  hook name and payload, the run's id and the site. `h` answers a
  `HookReply`: a JSON object in `Payload`, or a `HookError`. An error that
  `h` returns answers `plugin_error`, the failure of a host plugin's hook.
- **No answer.** A reply to `on_run_end` adds its `warnings` to the run.
  The engine ignores an `answer` in it: the run's answer comes from the
  bundle's host plugin alone ([Outcomes of a run](#outcomes-of-a-run)).
- **One connection.** The `Client` serves `HostHooks` on a broker
  connection, opened at its first run with hooks and reused by later runs,
  with messages bounded by `MaxMessageBytes` both ways. A run waits until
  go-plugin serves the connection; a listen that fails refuses the run
  with `ErrNotStarted`. The next run opens a new connection when the last
  one ended, or when the engine holds no connection to it and no request
  naming it is in flight.
- **Its end.** A replaced connection keeps serving the registrations that
  still name it. When the last of them goes, and for every connection at
  `Close`, the `Client` stops the server at once, ending its handlers'
  calls, and then calls `ReleaseHooks` on the engine's `HooksControl`
  service. A call that the engine sends in between fails; after the
  release, it gets no answer.
- **Release retries.** Each `ReleaseHooks` call waits at most
  `PluginTimeout`, and one that fails is sent again with a growing
  interval. The retries stop when the engine has the call or answers
  `Unimplemented`, and when the engine's generation or connection ends.
- **The registration.** A run without an id gets a fresh UUID. The `Client`
  registers `h` under the id with a fresh token before it sends the
  request. It refuses a run whose id is still registered with
  `ErrNotStarted`, through `Run` too.
- **Unknown calls.** A call whose run and token name no registration, such
  as a delayed call of an earlier run, answers `unknown_run`
  (`HookCodeUnknownRun`) and reaches no handler. The engine takes it as no
  answer, which latches nothing. A handler's reply with that code is sent
  as `plugin_error`.
- **Drops.** The `Client` drops a registration when the engine refuses the
  run, at the engine's call `released` once the run has settled, when
  `GetRun` answers `gone`, and at `Close`. Once the `Client` serves hooks,
  the end of the engine's generation, which go-plugin signals, calls
  `Close`; `Close` detaches that call, so the generation keeps no `Client`.
- **Lookups.** After every call, the `Client` asks `GetRun` about the
  registration: first after `PluginTimeout`, then at twice the last
  interval, at most 10 minutes apart, while the answer is `held` or the
  lookup fails. Each lookup is bounded by `PluginTimeout`, and a drop ends
  it. The call's own result never changes.

## Supervise the engine

`Supervisor` runs the engine one process at a time, a
[generation](../../docs/glossary.md#generation), and routes the host's
calls to the generation that serves
([the supervisor](../../docs/design/0009-host-adapters.md#the-supervisor)).
It is optional: a host can launch and call the engine itself, as
[Launch the engine](#launch-the-engine) shows.

1. Make a `Supervisor` with `NewSupervisor`, from a `SupervisorConfig`
   with the `Launch` and the pinned build identity of the release record.
2. Run its `Run` in a goroutine of its own, with a context that ends when
   the host stops.
3. Make each call with `Call`, which passes the `Client` of the
   generation that serves to a function.
4. Answer the host's own readiness probe with `Ready`.

This example, from `example_test.go`, does the first three steps:

```go
// The configuration's drain_timeout_ms.
drain := 30 * time.Second
sup, err := plugin.NewSupervisor(plugin.SupervisorConfig{
	Launch: plugin.Launch{
		Path:   "/opt/windmill-lite/windmill-lite",
		SHA256: want.EngineSHA256,
		Config: "/etc/windmill-lite/plugin.json",
		Env:    []string{"PATH=/usr/local/bin:/usr/bin:/bin"},
	},
	Identity: plugin.PinnedIdentity{
		BundleDigest:  want.Bundle,
		EngineVersion: want.Engine,
		BunVersion:    want.Bun,
		ConfigDigest:  want.ConfigDigest,
		Labels:        want.Labels,
	},
	DrainTimeout: &drain,
})
if err != nil {
	return err
}
done := make(chan error, 1)
go func() { done <- sup.Run(ctx) }()

err = sup.Call(ctx, func(engine *plugin.Client) error {
	res, err := engine.Run(ctx, &plugin.RunRequest{
		Path: "f/app/main",
		Args: []byte(`{"name": "lite"}`),
	})
	if err != nil {
		return err
	}
	fmt.Printf("%s: %s\n", res.GetStatus(), res.GetResult())
	return nil
})
<-done
return err
```

### A generation's life

| Phase | What the supervisor does | Default |
|---|---|---|
| Launch | Makes a configuration with `ClientConfig` from `Launch`, starts the engine with go-plugin and connects to it. When the context of `Run` ends first, the engine does not start, or gets `SIGKILL`, which ends go-plugin's wait for its handshake | go-plugin's start timeout, 1 minute |
| Admission | Within `AdmissionTimeout` of the connection, health must answer `SERVING`, then `BuildInfo` must equal the pinned identity. Health is checked every 100 ms. Each call has a deadline within what is left of the timeout: a health check's is at most `HealthTimeout`. Otherwise the launch fails with `ErrNotAdmitted`, and the supervisor stops the engine | 30 s |
| Serving | New calls reach the generation, and `Ready` reports true. Every `HealthInterval` one health check runs, bounded by `HealthTimeout`. The first check that fails, times out or answers a status other than `SERVING` withdraws the generation for good. So do the exit of the engine and the end of the context of `Run`, which also ends a check in progress | 1 s, 1 s |
| Drain | The withdrawn generation takes no new call. Its calls in flight go on for up to `DrainTimeout` plus `CloseGrace` | 1 minute plus 15 s |
| Stop | go-plugin's `Kill`, bounded by `StopTimeout`. Past it, the supervisor sends the engine's process `SIGKILL` through the launch's runner. It then waits until that process has exited and been reaped, and removes the launch's base directory. The reap does not wait for the engine's output pipes, which a process that the engine started can hold open | 5 s |
| Restart | The next launch waits gRPC's connection backoff, then starts the next generation | see below |

The first launch starts at once. After a generation's stop, or a launch
that failed, the next launch waits a delay of gRPC's connection backoff,
whose values are fixed:

- the first delay is 1 s, and each later one is 1.6 times the delay
  before, at most 120 s;
- each delay but the first is spread by up to 20 % either way, and is
  still at most 120 s;
- a generation that served for 60 s or more makes the next delay 1 s
  again.

Once the engine drains, health answers `NOT_SERVING`, so the next check
withdraws the generation: a `SIGTERM` to the engine, or one of the
engine's own recycle triggers, replaces it. go-plugin's `Kill` sends a
shutdown call that the engine answers by stopping at once, so the
supervisor sends it only after the drain.

The stop bounds the engine's process. On Linux, it bounds the harness
processes that the engine started too: each gets `SIGKILL` when the
engine's process ends, also while its event loop is stuck. On macOS, a
harness process exits with its connection to the engine only while its
event loop runs.

The children that steps start lead process groups of their own on every
system, so a host that needs a bound on them gives each generation a cgroup
of its own, on Linux
([the supervisor](../../docs/design/0009-host-adapters.md#the-supervisor)).

### The configuration

| `SupervisorConfig` field | What it sets | Default |
|---|---|---|
| `Launch` | The launch of every generation ([The launch](#the-launch)). Its `Logger` also receives the supervisor's log | required |
| `Identity` | The pinned build identity, a `PinnedIdentity`: `BundleDigest`, `EngineVersion`, `BunVersion` and `ConfigDigest`, each required, and `Labels`. `BuildInfo` must have the same values, and the same labels, no label meaning the same as none | required |
| `AdmissionTimeout` | The bound on a generation's admission, from go-plugin's connection | 30 s |
| `HealthInterval` | How often the supervisor checks the health of the generation that serves | 1 s |
| `HealthTimeout` | The bound on each health check | 1 s |
| `DrainTimeout` | A pointer to the engine's `drain_timeout_ms`: how long a withdrawn generation's calls in flight go on, before `CloseGrace`. Set it to the configuration's value, 0 included. Nil means the engine's default, for a configuration without the field | nil: 1 minute |
| `CloseGrace` | The time after `DrainTimeout` that the engine has to close its runtime, which cancels the runs left, and answer their calls | 15 s |
| `StopTimeout` | The bound on go-plugin's `Kill` of a generation | 5 s |

A zero duration other than `DrainTimeout` means its default. The drain's
bound, `DrainTimeout` plus `CloseGrace`, is at most the longest
`time.Duration`. `NewSupervisor` keeps copies of `Launch.Env` and of the
labels. It refuses:

- a `Launch` without `Path` or `Config`, with a `SHA256` that is not 64
  hexadecimal digits, or with a negative `MaxMessageBytes`,
  `ReplyMargin` or `PluginTimeout`, as `ClientConfig` does;
- an empty string field of `Identity`;
- a negative duration of `SupervisorConfig`.

What `ClientConfig` finds only when it creates a launch's base directory,
such as a `TempDir` that does not exist or one too long for the socket
paths, fails each launch instead. `Run` records the failure, which a
`Call` error wraps, and launches again after the backoff.

### Calls and errors

| Method | What it does |
|---|---|
| `Run` | Launches generations until its context ends. It then ends a launch or a health check in progress, withdraws the generation that serves, drains and stops it, and returns nil once its process has exited. It may be called once |
| `Call` | Runs a function with the `Client` of the generation that serves, and returns the function's error. While no generation serves, it waits until its context ends. A context that has ended calls no function. The function must not keep the `Client`: its generation may be stopped after the call |
| `Ready` | Reports whether a generation takes new calls: false before the first admission, from a withdrawal until the next admission, and once the context of `Run` has ended |

| Error | When |
|---|---|
| `ErrNotServing` | `Call`'s context ended before a generation served the call: while it waited, or before `Call` was made. The error wraps the context's cause and, when a launch failed, that launch's error. `Call` did not call the function, so the host may retry |
| `ErrNotAdmitted` | The error of a launch that admission refused, which a `Call` error wraps |
| `ErrSupervisorStopped` | `Call` was made once the context of `Run` had ended, even while `Run` still drains. It did not call the function |

A call can reach a generation that has begun to drain before a check has
withdrawn it. The engine then refuses a run with `ErrNotStarted`, and the
host may call again.

## Handshake and versions

| Name | Value |
|---|---|
| `Handshake` | go-plugin's handshake configuration: the magic cookie `WINDMILL_LITE_PLUGIN` with a fixed value, and `ProtocolVersion` |
| `PluginName` | `runner` |
| `ProtocolVersion` | 1, the major version of `windmill_lite.v1.Runner` |
| `VersionedPlugins` | The plugin sets by `Runner` version, for the engine's side and the host's: version 1 serves a `RunnerPlugin` under `PluginName` |
| `RunnerPlugin` | The go-plugin plugin type, gRPC only. On the engine's side its `Runner` field is the service's implementation, which it registers, as it registers `Hooks`, the `HooksControl` service, when set; on the host's side it returns a `*Client`, with its `ReplyMargin`, `PluginTimeout` and `MaxMessageBytes` |

The magic cookie checks that the engine was started as a plugin; it is not
authentication. go-plugin's handshake picks the highest version that both
sides list, and refuses a plugin that serves none of the host's versions.
A breaking change of the service would define `windmill_lite.v2.Runner`,
which both sides would list beside version 1 while hosts move.

## Licences of the dependencies

hashicorp/go-plugin and its dependency hashicorp/yamux are under the
Mozilla Public License 2.0 (MPL-2.0). This package uses them as Go modules
only, and the repository holds none of their files. A host that imports
`wl/plugin` links them into its own binary, and gives their notices itself.

## Tests

The launch tests start real plugin processes. They build
`testdata/runnerplugin`, a stand-in for the engine that serves a fake
`Runner`, twice with the `go` command on the `PATH`: once serving only
version 1 and once only version 2. They run on Linux and macOS, and fail
instead of skipping when the build fails. Some change the process's umask,
working directory or `PATH`, so no test of the package runs in parallel.

The supervisor's tests build `testdata/faultplugin` into each test's
temporary directory. It is a stand-in for the engine that misbehaves as
its test asks, through files in a directory that the test names. A fake
clock times the admission, the drain, the stop and the backoff, so each
test moves that time itself. The supervisor's tests on the real engine
are in [cmd/windmill-lite](../../cmd/windmill-lite/README.md#plugin-tests).

| Test | What it checks |
|---|---|
| `TestRunnerRoundTrip` | `Run`, `Cancel` and `BuildInfo` over a real gRPC connection to a fake server: JSON values arrive as the same bytes, an absent deadline stays absent, and a deadline of 0 stays 0 |
| `TestClientHealth` | `Health` reads the `Runner` service's status, and fails against gRPC's standard health server, which go-plugin uses, when it has no status for `Runner` |
| `TestClientRunOutcomes` | The deadline that `Run` sends, and its errors: a run that the server answers after the call's deadline, a connection lost after a write, the engine's refusal with and without its detail, and an ended context |
| `TestVersionNegotiationRealHandshake` | A launch refuses the plugin that serves only version 2, and dispenses from the one that serves version 1 |
| `TestClientConfigPin` | A matching SHA-256 starts the plugin, which sees the handshake variables and `Env` but not the test's own environment; a mismatching one starts no process and removes the base directory |
| `TestClientConfigBareFilename` | A bare file name starts the file in the current directory that the launch hashed, not one of that name on `PATH` |
| `TestClientConfigSocketPlacement` | The launch makes its directories under the host umask `0277`. The plugin's socket, made under umask `0077`, and the host's broker listener lie in a directory of mode `0700` in the base directory of mode `0700`. The plugin calls the host's `HostHooks` over that broker connection, and both directories are gone after `Kill` and after a failed launch |
| `TestClientConfigCleanup` | The cleanup removes the base directory after a start that fails before the runner, and of a configuration never started. After `Kill` or a refused start, it leaves a later directory of the same name alone; after a failed removal, it tries again |
| `TestClientConfigStartTimeout` | A start timeout of 50 ms ends the SHA-256 check of a sparse file of 4 GiB: no process starts, and the base directory is gone |
| `TestClientConfigNotARegularFile` | A named pipe that no process writes, and a directory, are refused at once |
| `TestLauncherStartEndsBeforeTheSpawn` | A start context canceled after the SHA-256 check has passed: the runner starts no process |
| `TestLauncherStartEndsAsTheProcessStarts` | A start context canceled right after the process started: the runner kills and reaps it |
| `TestClientConfigRelativeTempDir` | A relative `TempDir` gives an absolute base directory, which is gone after `Kill` though the working directory changed during the launch |
| `TestClientConfigRunnerWaitTwice` | A second `Wait` on the runner of `ClientConfig`'s configuration returns an error, as `exec.Cmd`'s does |
| `TestLauncherAbortBeforeStart` | A supervisor's launch aborted before its runner starts, whether or not go-plugin has made the runner yet: `Start` checks no executable and starts no process |
| `TestClientConfigRefusals` | The `Launch` fields that `ClientConfig` refuses |
| `TestClientLookupSchedule` | With a fake timer and a fake `GetRun`: the lookups come after the plugin timeout, then twice the interval, at most 10 minutes, through `held` and a failed lookup, and stop at `gone`, which drops the registration |
| `TestClientLookupStopsAtDrop` | The release call and `Close` stop a registration's wait, and no lookup follows |
| `TestClientHookCalls` | A registered run's call reaches its handler, whose error answers `plugin_error`, as does its reply of the code `unknown_run`; another run's or another token's call, a release call included, answers `unknown_run`; the release call drops only its own token's registration |
| `TestClientGenerationEnd` | On go-plugin's test connection, the end of the engine's generation closes a `Client` that serves hooks: its registration is dropped and a later run with hooks is refused. In a generation that has ended, a first run with hooks is refused without reaching the engine |
| `TestClientGenerationKeepsNoClient` | While the generation goes on, 100 `Client`s never used for hooks are collected, and so are a `Client` closed after a run with hooks and a `Client` whose first listen failed |
| `TestClientListenRetry` | A first run with hooks whose broker connection cannot listen is refused without reaching the engine; the next run on the same `Client` opens the connection and is served |
| `TestClientLookupTimeout` | A lookup that gets no reply ends at its bound and is tried again; a drop ends a waiting lookup at once, and no wait follows |
| `TestClientRunRefusesRegisteredID` | `Run` refuses an id whose hooks are registered, without a call |
| `TestClientCloseAtFirstUse` | On go-plugin's test connection, `Close` at the same time as a first `RunWithHooks`: the run is refused, or Close stops the broker connection that it opened |
| `TestClientCloseStopsLateServer` | The `HostHooks` server that go-plugin asks for after `Close` is stopped already: its `Serve` returns at once |
| `TestRunnerClientKeepsItsCalls` | A type with only `RunnerClient`'s four calls of version 1 still is a `RunnerClient`: the assignment compiles only so |
| `TestClientReleaseRetryEndsWithGeneration` | With a fake timer: after a release call fails, the end of the engine's generation during the wait ends the retries at once, stopping the wait, with no further call |
| `TestSupervisorReady` | `Ready` is false while the first generation's engine answers `NOT_SERVING`, and true once it is admitted. It is false once a check withdraws it and while the next launch waits for its backoff, true when the next serves, and false once `Run`'s context ends |
| `TestSupervisorCallWaits` | While no generation serves, a call waits, and one whose context ends returns `ErrNotServing` without calling its function. The waiting call runs on the generation that is admitted next. After `Run`'s context ends, a call returns `ErrSupervisorStopped` |
| `TestSupervisorReadsStatus` | A health check that answers `NOT_SERVING` without an error withdraws the generation, whose process then exits and is reaped, and the next launch waits for its backoff. The fake engine reads each status whole, and answered it |
| `TestSupervisorStalledIdentity` | An engine that answers `SERVING` but never `BuildInfo` is still in its admission just before 30 s. At 30 s the launch fails with `ErrNotAdmitted`, the engine's process exits, and the next launch waits for its backoff |
| `TestSupervisorUnresponsiveShutdown` | An engine that never answers go-plugin's shutdown call keeps its process until the bound of 5 s, then gets `SIGKILL` and is reaped, and a process that the test started from the same executable keeps running. In a second case, while a test hook holds the killed process's reap, no next launch is scheduled; released, the next generation starts and sees the old process gone |
| `TestSupervisorRestartBackoff` | Failed launches wait 1 s, unspread, then 1.6 times the delay before, up to 120 s, each spread by the jitter. A generation that served for 59 s does not make the next delay 1 s, and one that served for 60 s does |
| `TestSupervisorDrainBound` | A withdrawn generation's call in flight goes on until the drain's bound, the drain timeout plus the close grace. At 75 s by default, and at 15 s for a drain timeout of 0, the supervisor stops the engine, and the call returns `ErrOutcomeUnknown`. When the sum would overflow, the bound is the longest duration: the call stays in flight until the test releases it, and then succeeds |
| `TestSupervisorAdmissionDeadlines` | A health check of an admission has a deadline of at most 1 s. Once 29.5 s have passed, the check that answers `SERVING` and `BuildInfo` each have one of at most 0.5 s, which the fake engine logs as it receives each call |
| `TestSupervisorCallContexts` | A call whose context has ended calls no function, though a generation serves. Right after the context of `Run` ends, `Ready` is false and a call returns `ErrSupervisorStopped`, while a call in flight goes on and completes |
| `TestSupervisorStopsLaunchAndCheck` | The end of `Run`'s context ends a launch whose engine withholds its handshake, and a health check that is never answered, though both timeouts are 10 minutes |
| `TestSupervisorReapsWithDescendant` | A process that the fake engine started holds its output pipes open. The stopped engine's process is reaped all the same, and the next generation starts and serves. The test then ends each such process through a stop file, and waits for it |
| `TestSupervisorKeepsLaunchCopy` | An entry of `Launch.Env` that the host changes after `NewSupervisor` does not reach the engine |
| `TestSupervisorConfigRefusals` | What `NewSupervisor` refuses, and that `Run` runs once |

The broker tests of [internal/host](../../internal/host/README.md#tests)
run `RunWithHooks` against the engine in their own process.

Run them with `go test -race ./wl/plugin`.

## Files

| File | Contents |
|---|---|
| `doc.go` | The package documentation, and the `go:generate` line that regenerates the code ([proto](../../proto/README.md#generate-the-go-code)) |
| `plugin.go` | `PluginName`, `ProtocolVersion`, `Handshake`, `DefaultReplyMargin`, `VersionedPlugins`, `RunnerPlugin` |
| `client.go` | `Client` and its calls, `ErrNotStarted`, `ErrOutcomeUnknown`, `ErrorDomain`, `ReasonRunNotStarted` |
| `hooks.go` | `RunWithHooks`, `HookHandler`, `Registrations`, `Close`, `DefaultPluginTimeout`, the hook codes, and the `HostHooks` server |
| `launch.go` | `Launch`, `ClientConfig` and its cleanup, `ErrChecksumMismatch`, `DefaultMaxMessageBytes`, and the runner that pins and starts the executable |
| `supervisor.go` | `Supervisor`, `NewSupervisor`, `SupervisorConfig`, `PinnedIdentity`, `ErrNotAdmitted`, `ErrNotServing`, `ErrSupervisorStopped` |
| `runner.pb.go`, `runner_grpc.pb.go` | Generated from `runner.proto`; do not edit |
| `example_test.go` | The examples above, compiled by `go test` |
| `testdata/runnerplugin/` | The test plugin of the launch tests |
| `testdata/faultplugin/` | The fake engine of the supervisor's tests |
