// The smoke host of the packaging test (../check.sh): a Bun host that
// imports the host SDK from the installed package windmill-lite, as a
// host does. Its working directory is the host's working directory,
// which connect() requires.
//
//   bun smoke.ts run       connects the engine WL_ENGINE on the bundle
//                          WL_BUNDLE and runs the flow f/smoke/hello; exits
//                          0 when the run succeeds with "hello, npm" and
//                          the SDK and its test entry share one runtime
//                          state (below)
//   bun smoke.ts mismatch  connects WL_ENGINE, an engine of another
//                          version, on a bundle it built; exits 0 when
//                          connect() rejects with version_mismatch
//
// One runtime state: this process also loads the test entry,
// windmill-lite/bun/testing, as a host whose tests use it does. The two
// entries must share one invocation context, one timer fence and one
// process scope. If each held its own copy of the runtime:
//
// - the process scope that the SDK published, captured inside the test
//   entry's invocation, would not leave that invocation, so a timer
//   started through it would be reported;
// - connect() would install the SDK's own timer fence over the test
//   entry's, replacing setTimeout, setInterval and setImmediate;
// - currentInvocation() would not see the invocation that the runtime
//   runs the flow's step in, which an around wrapper of the host's plugin
//   reports.

import { connect, HostError, type ConnectOptions, type RunResult } from "windmill-lite/bun";
import { currentInvocation, install, type Invocation, invoke } from "windmill-lite/bun/testing";

function fail(message: string): never {
  console.error(`smoke.ts: ${message}`);
  process.exit(1);
}

const mode = process.argv[2];
const engine = process.env.WL_ENGINE;
const bundle = process.env.WL_BUNDLE;
if ((mode !== "run" && mode !== "mismatch") || !engine || !bundle) {
  console.error("usage: WL_ENGINE=<engine> WL_BUNDLE=<bundle directory> bun smoke.ts run|mismatch");
  process.exit(2);
}
const options: ConnectOptions = { engine, bundle, workdir: process.cwd() };

if (mode === "run") {
  install();
  const timers = [globalThis.setTimeout, globalThis.setInterval, globalThis.setImmediate];
  const scope = (globalThis as unknown as Record<symbol, { capture(): <R>(fn: () => R) => R }>)[Symbol.for("windmill-lite.processScope")]!;
  const before = await invoke(
    () => {
      // Captured inside the invocation: a scope captured outside every
      // invocation is a snapshot without any invocation's store, whichever
      // copy of the runtime made it, so only this capture tells them apart.
      const inProcess = scope.capture();
      inProcess(() => setTimeout(() => {}, 1));
      setTimeout(() => {}, 1000);
      return [currentInvocation(), inProcess(() => currentInvocation())];
    },
    { id: "smoke-step", run: "smoke-run" },
  );
  const want = { result: [{ id: "smoke-step", run: "smoke-run", settled: false }, undefined], left: { timeout: 1 } };
  if (JSON.stringify(before) !== JSON.stringify(want)) {
    fail(`one process scope and invocation context: invoke() settled as ${JSON.stringify(before)}, want ${JSON.stringify(want)}`);
  }
  const seen: (Invocation | undefined)[] = [];
  const host = await connect({
    ...options,
    plugin: {
      around: [
        {
          selector: { flow: "f/smoke/hello", module: "greet" },
          fn: (_call, next) => {
            seen.push(currentInvocation());
            return next();
          },
        },
      ],
    },
  });
  const now = [globalThis.setTimeout, globalThis.setInterval, globalThis.setImmediate];
  const run: RunResult = await host.run("f/smoke/hello", { name: "npm" });
  await host.close();
  if (run.status !== "success" || run.result !== "hello, npm") {
    fail(`f/smoke/hello: ${JSON.stringify(run)}, want status success and result "hello, npm"`);
  }
  if (now.some((f, i) => f !== timers[i])) {
    fail("one timer fence: connect() replaced the timer functions that the test entry's install() had fenced");
  }
  if (seen.length !== 1 || seen[0]?.run !== run.id || seen[0].settled || typeof seen[0].id !== "string") {
    fail(`one invocation context: currentInvocation() in the step's around wrapper gave ${JSON.stringify(seen)}, want the invocation of run ${run.id}`);
  }
  console.log(`smoke.ts: f/smoke/hello ended ${run.status} with ${JSON.stringify(run.result)}; the SDK and its test entry share one runtime state`);
} else {
  let host;
  try {
    host = await connect(options);
  } catch (e) {
    if (e instanceof HostError && e.code === "version_mismatch") {
      console.log(`smoke.ts: connect rejected as it should: ${e.message}`);
      process.exit(0);
    }
    fail(`connect rejected with ${String(e)}, want a HostError version_mismatch`);
  }
  await host.close();
  fail("connect to an engine of another version succeeded");
}
process.exit(0);
