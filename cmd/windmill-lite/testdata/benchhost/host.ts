// A Bun host for the host-mode benchmarks of milestone G1
// (cmd/windmill-lite/bench_test.go, ADR 0005's benchmark plan): it embeds
// the host SDK, connects the engine on the bundle, and does nothing else,
// so that its process is a warm Bun 1.3.10 host whose only work is the
// steps the engine invokes in it. The benchmarks compile it with `bun
// build --compile`, as a host's own program is compiled, and submit the
// runs from the engine's side (the SDK's engine is the benchmark's process,
// through the engine proxy of bench_test.go), as `wltest check --mode host`
// does, so that they read each run's trace.
//
// Environment: WL_ENGINE (the engine binary the SDK starts) and WL_BUNDLE
// (the bundle directory). Its working directory is the one it passes.

import { connect } from "../../../../harnesses/bun/host/index.ts";

const env = process.env;
await connect({
  engine: env.WL_ENGINE!,
  bundle: env.WL_BUNDLE!,
  workdir: process.cwd(),
  // The engine is not this host's child here, so it runs no watchdog.
  watchdog: { killAfter: 0 },
  // The benchmark closes the engine when it is done: then the host ends.
  onEngineExit: () => process.exit(0),
});
console.log(`ready ${process.pid}`);
