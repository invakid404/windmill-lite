// A Bun host for the per-run halt test (cmd/windmill-lite/host_test.go,
// TestHostRunHalts). It connects the engine on a bundle built from
// testdata/haltfolder, counts the modules that end in each run with its
// plugin's onModuleEnd, and runs the flow f/repro/prefix on its one
// connection: with a halt after the module check in the run's own options,
// then without, then with halt options the run is refused for, and then
// without again. It prints one JSON line of what each run gave, and exits.
//
// Environment: WL_ENGINE (the engine binary) and WL_BUNDLE.

import { connect, HostError, type RunOptions } from "../../../../harnesses/bun/host/index.ts";

const ended = new Map<string, number>();
const host = await connect({
  engine: process.env.WL_ENGINE!,
  bundle: process.env.WL_BUNDLE!,
  workdir: process.cwd(),
  plugin: {
    onModuleEnd: ({ run }) => {
      ended.set(run, (ended.get(run) ?? 0) + 1);
    },
  },
});

const flow = "f/repro/prefix";

// A run's status, result and count of ended modules, or the code and
// message of the HostError of a run that did not start.
async function outcome(opts: RunOptions) {
  try {
    const r = await host.run(flow, {}, opts);
    return { status: r.status, result: r.result, ended: ended.get(r.id) ?? 0 };
  } catch (e) {
    if (e instanceof HostError) return { refused: e.code, message: e.message };
    throw e;
  }
}

const report = {
  halted: await outcome({ haltAfter: [{ flow, module: "check" }] }),
  whole: await outcome({}),
  malformed: await outcome({ haltAfter: [{ flow }] }),
  notAList: await outcome({ haltBefore: { flow, module: "write" } as unknown as RunOptions["haltBefore"] }),
  after: await outcome({}),
};
console.log(JSON.stringify(report));
await host.close();
