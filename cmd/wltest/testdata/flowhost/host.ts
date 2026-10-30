// The Bun host of the flow suite in host mode (cmd/wltest, milestone F3b):
// it connects the host SDK to its engine on the fixture's bundle, in its
// working directory, and serves its steps until the engine ends; then it
// exits. The steps run in this process, as every host's do.
//
// Environment: WL_ENGINE (the engine the SDK spawns: wltest itself, in
// proxy mode, which hands this host's socket to the checker's runtime) and
// WL_BUNDLE.

import { connect } from "../../../../harnesses/bun/host/index.ts";

const env = process.env;
await connect({
  engine: env.WL_ENGINE!,
  bundle: env.WL_BUNDLE!,
  workdir: process.cwd(),
  // The checker's runtime keeps the watchdog off: the engine is not this
  // process's child there.
  watchdog: { killAfter: 0 },
  onEngineExit: () => process.exit(0),
});
