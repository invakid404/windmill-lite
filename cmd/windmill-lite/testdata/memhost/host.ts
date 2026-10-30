// A Bun host for the module-memory tests (cmd/windmill-lite/memory_test.go,
// ADR 0013): it embeds the host SDK, registers its instances of the
// bundle's host-provided modules, connects the engine, and answers over
// HTTP on a Unix socket with its resident memory after a full garbage
// collection (/rss) and the outcome of runs it submits (/run). The tests
// compile it with `bun build --compile`, as a host's own program is
// compiled, and run it under the supervisor of host_test.go.
//
// Environment: WL_ENGINE, WL_BUNDLE and WL_HOST_SOCKET as for the host of
// testdata/host, and WL_HOST_MODULES, a JSON object from a host-provided
// module's key in the host contract to the file the host imports for it.
// A module that imports packages cannot be imported at run time by a
// compiled host, which resolves no package from disk; an entry file that
// imports such a module statically sets the modules itself, in
// globalThis[Symbol.for("windmill-lite.memhost.modules")], before it
// imports this one.

import { heapStats } from "bun:jsc";
import { connect } from "../../../../harnesses/bun/host/index.ts";

const env = process.env;
const modules: Record<string, unknown> = { ...((globalThis as Record<symbol, unknown>)[Symbol.for("windmill-lite.memhost.modules")] ?? {}) };
for (const [key, file] of Object.entries((JSON.parse(env.WL_HOST_MODULES || "{}") ?? {}) as Record<string, string>)) {
  modules[key] = await import(file);
}
const host = await connect({
  engine: env.WL_ENGINE!,
  bundle: env.WL_BUNDLE!,
  workdir: process.cwd(),
  hostModules: modules,
  watchdog: { killAfter: 0 },
});

// The resident set after a full collection, the peak resident set so far,
// and the script modules loaded. Bun reports the peak in bytes on macOS and
// in kilobytes on Linux, as getrusage does.
function memory() {
  for (let i = 0; i < 3; i++) Bun.gc(true);
  const peak = process.resourceUsage().maxRSS;
  return {
    rss: process.memoryUsage().rss,
    peak: process.platform === "darwin" ? peak : peak * 1024,
    heap: heapStats().heapSize,
    modules: host.health().modules,
  };
}

Bun.serve({
  unix: env.WL_HOST_SOCKET!,
  idleTimeout: 0,
  async fetch(req) {
    const url = new URL(req.url);
    switch (url.pathname) {
      case "/rss":
        return Response.json(memory());
      case "/run": {
        // concurrent runs of the flow at once, reps times over: how many
        // ran and how many succeeded over all rounds, the first that did
        // not, the outcomes of the last round, and how long all rounds took.
        const b = await req.json();
        const concurrent = Number(url.searchParams.get("concurrent") ?? "1");
        const reps = Number(url.searchParams.get("reps") ?? "1");
        const start = performance.now();
        let runs: { status: string; result: unknown }[] = [];
        let total = 0;
        let succeeded = 0;
        let firstFailure: unknown = null;
        for (let r = 0; r < reps; r++) {
          runs = await Promise.all(
            Array.from({ length: concurrent }, async (_, k) => {
              const o = await host.run(b.path, { ...(b.args ?? {}), i: k });
              return { status: o.status, result: o.result };
            }),
          );
          for (const o of runs) {
            total++;
            if (o.status === "success") succeeded++;
            else firstFailure ??= o;
          }
        }
        return Response.json({ ms: performance.now() - start, total, succeeded, firstFailure, runs });
      }
    }
    return new Response("not found", { status: 404 });
  },
});
console.log(`ready ${process.pid}`);
