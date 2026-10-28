# harnesses/bun

**Current state:** the runtime skeleton of milestone C1
([ADR 0012](../../docs/design/0012-layout-and-milestones.md)). This README
covers `harnesses/bun`: `runtime/`, the harness side of the protocol of
[ADR 0004](../../docs/design/0004-harness-protocol.md), and `test/`, its
developer checks. The build helper (`build/`, C2a) and the host SDK
(`host/`, W1 and F3a) do not exist yet. The harness runs on the pinned Bun
1.3.10, the version of Windmill v1.767.0's images
([WR-3](../../docs/semantics/languages.md#wr-3)). It has no dependencies, no
`package.json` and no `node_modules`.

## Running

The engine starts it; `internal/harness` has `BunCommand` for that:

```sh
bun harnesses/bun/runtime/main.ts --bundle <bundle dir>
```

File descriptor 3 must be one end of a stream socketpair, which carries
every frame. stdout and stderr carry no frames, so what a step prints
cannot corrupt the protocol.

## What the runtime does

| File | Contents |
|---|---|
| `runtime/main.ts` | Reads the bundle's digest, connects to fd 3, starts the runtime |
| `runtime/runtime.ts` | The handshake, invocations, cancel, health, drain |
| `runtime/frame.ts` | The codec and the send queue: canonical envelopes, the decoding rules and error codes of the Go side, part streams, and the order of messages about one invocation |

- **Handshake.** It sends `hello` with `Bun.version`, its pid, the SHA-256
  of the bundle's `manifest.json`, and no optional capability (`fetch`,
  `native`, `fresh` and `host` are all false). It accepts only a warm
  welcome.
- **Heartbeat.** After the welcome it sends a `ping` every `heartbeat_ms`
  with its health: invocations in flight, abandoned ones, process-level
  errors (always 0 until C3a installs handlers), the event loop's lag (how
  late the heartbeat timer fired) and RSS. It answers the engine's pings
  with the same.
- **Invocations** run concurrently on the event loop. A script's module,
  `scripts/bun/<id>.mjs` in the bundle, is imported once per process on its
  first invocation (warm module state).
- **Cancel** abandons the invocation. When its call ends, the outcome goes
  out as the `late` frame that settles it.
- **Drain** stops the heartbeat. Once everything queued has gone out, the
  runtime sends `bye`, its last frame, and exits when its socket has closed,
  whatever is still running.

**The step runner is a skeleton.** It calls the export named by `entry`
with the argument object as its only parameter and serialises the value
with `JSON.stringify(value ?? null)`.

| The call | Outcome |
|---|---|
| returns | `ok` with that text |
| throws a value that is not `null` or `undefined` | `error` with `{message, name, stack}`, the fields the value has |
| the export is not a function | `error` with `<entry> function is missing` |
| the module fails to load or evaluate, or the call throws `null` or `undefined` | `escaped`, exit code 1 |
| the error object cannot be built or serialised (a BigInt or a cycle in it, a throwing getter) | `escaped`, exit code 1; the process lives on |
| the result is larger than `max_message` | `escaped`, with a tail that says so |

Milestone C3a replaces this with Windmill's wrapper rules: binding by the
recorded signature, `Date` hydration, the WR-13 replacer, the WR-18 error
shape with `step_id` and `extra`, `process.exit` interception, and the
process-level error handlers. Until then a detached rejection or a
`process.exit` ends the whole process, and the engine fails every
invocation in it as escaped. Per-invocation env, log and `fetch` views
(C3b) are not there either: the runtime sends no `log` frames.

Exit codes: 0 after `bye` or when the engine's end closes, 2 for a rejected
or unusable welcome, 3 for a protocol error by the engine, 4 when the
bundle cannot be read.

## Tests

`test/frames.test.ts` checks this codec against the golden frames that the
Go tests write, `conformance/protocol/frames.json`. `test/sender.test.ts`
checks the send queue against a socket that accepts a few bytes at a time:
priority, the order per invocation, partial writes and `whenFlushed`.
Both are developer checks, run by CI's non-required `Bun harness checks`
job; the merge gate is the Go side ([internal/harness](../../internal/harness/README.md)), which
also drives this runtime through every exchange in
`conformance/protocol/exchanges/`.

```sh
cd harnesses/bun && bun test
```
