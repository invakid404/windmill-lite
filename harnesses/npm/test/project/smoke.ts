// The smoke host of the packaging test (../check.sh): a Bun host that
// imports the host SDK from the installed package windmill-lite, as a
// host does. Its working directory is the host's working directory,
// which connect() requires.
//
//   bun smoke.ts run       connects the engine WL_ENGINE on the bundle
//                          WL_BUNDLE and runs the flow f/smoke/hello; exits
//                          0 when the run succeeds with "hello, npm"
//   bun smoke.ts mismatch  connects WL_ENGINE, an engine of another
//                          version, on a bundle it built; exits 0 when
//                          connect() rejects with version_mismatch

import { connect, HostError, type ConnectOptions, type RunResult } from "windmill-lite/bun";

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
  const host = await connect(options);
  const run: RunResult = await host.run("f/smoke/hello", { name: "npm" });
  await host.close();
  if (run.status !== "success" || run.result !== "hello, npm") {
    fail(`f/smoke/hello: ${JSON.stringify(run)}, want status success and result "hello, npm"`);
  }
  console.log(`smoke.ts: f/smoke/hello ended ${run.status} with ${JSON.stringify(run.result)}`);
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
