// A Bun host for the walking skeleton's host-mode tests (milestone W1,
// cmd/windmill-lite/host_test.go). It embeds the host SDK, registers its
// instance of the host-provided module f/w1/scopes.ts, connects the
// engine, and serves its callers over HTTP on a Unix socket. Each run is
// submitted inside the caller's tenant scope. The tests compile it with
// `bun build --compile` and run it under a supervisor that only restarts
// it when it exits.
//
// Environment: WL_ENGINE (the engine binary), WL_BUNDLE, WL_HOST_SOCKET,
// WL_KILL_AFTER_MS (the watchdog's threshold).

import { connect } from "../../../../harnesses/bun/host/index.ts";
import * as scopes from "./scopes.ts";

const env = process.env;
const host = await connect({
  engine: env.WL_ENGINE!,
  bundle: env.WL_BUNDLE!,
  workdir: process.cwd(),
  hostModules: { "f/w1/scopes.ts": scopes },
  watchdog: { killAfter: Number(env.WL_KILL_AFTER_MS ?? "30000") },
});

function percentiles(ms: number[]) {
  const s = [...ms].sort((a, b) => a - b);
  const at = (q: number) => s[Math.min(s.length - 1, Math.floor(q * s.length))];
  return { n: s.length, p50_us: Math.round(at(0.5) * 1000), p95_us: Math.round(at(0.95) * 1000), p99_us: Math.round(at(0.99) * 1000) };
}

Bun.serve({
  unix: env.WL_HOST_SOCKET!,
  async fetch(req) {
    const url = new URL(req.url);
    switch (url.pathname) {
      case "/run": {
        const b = await req.json();
        const r = await scopes.store.run({ tenant: b.tenant }, () => host.run(b.path, b.args, { intercept: b.intercept }));
        return Response.json(r);
      }
      case "/health":
        return Response.json(host.health());
      case "/pid":
        return Response.json({ host: process.pid, engine: host.enginePid });
      case "/bench": {
        // Per-step latency of host mode: a one-step flow, run by the host
        // sequentially, timed around host.run.
        const n = Number(url.searchParams.get("n") ?? "200");
        for (let i = 0; i < 20; i++) await host.run("f/w1/echo", { x: i });
        const ms: number[] = [];
        for (let i = 0; i < n; i++) {
          const t = performance.now();
          const r = await host.run("f/w1/echo", { x: i });
          ms.push(performance.now() - t);
          if (r.status !== "success") return Response.json({ error: r }, { status: 500 });
        }
        return Response.json(percentiles(ms));
      }
    }
    return new Response("not found", { status: 404 });
  },
});
console.log(`ready ${process.pid}`);
