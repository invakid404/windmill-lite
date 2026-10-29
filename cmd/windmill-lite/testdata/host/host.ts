// A Bun host for the host-mode tests (cmd/windmill-lite/host_test.go,
// milestones W1 and F3a). It embeds the host SDK, registers its instance
// of the host-provided module f/host/scopes.ts, connects the engine on a
// bundle built from testdata/hostfolder, and serves its callers over HTTP
// on a Unix socket. Each run is submitted inside the caller's tenant
// scope. The tests compile it with `bun build --compile` and run it under
// a supervisor that only restarts it when it exits.
//
// Environment: WL_ENGINE (the engine binary), WL_BUNDLE, WL_HOST_SOCKET,
// WL_KILL_AFTER_MS (the watchdog's threshold), WL_WORKDIR (the working
// directory it passes; its own by default) and WL_RECYCLE_MODULES (the
// modules recycle trigger, unset by default).

import { connect, HostError } from "../../../../harnesses/bun/host/index.ts";
import * as scopes from "../hostfolder/f/host/scopes.ts";

const env = process.env;
let recycled: string | null = null;
let host;
try {
  host = await connect({
    engine: env.WL_ENGINE!,
    bundle: env.WL_BUNDLE!,
    workdir: env.WL_WORKDIR ?? process.cwd(),
    hostModules: { "f/host/scopes.ts": scopes },
    watchdog: { killAfter: Number(env.WL_KILL_AFTER_MS ?? "30000") },
    recycle: env.WL_RECYCLE_MODULES ? { modules: Number(env.WL_RECYCLE_MODULES) } : undefined,
    onRecycle: (reason) => (recycled = reason),
  });
} catch (e) {
  // The supervisor's test reads why the host could not connect.
  console.log(`refused ${e instanceof HostError ? e.code : "other"} ${(e as Error).message}`);
  process.exit(3);
}

function percentiles(ms: number[]) {
  const s = [...ms].sort((a, b) => a - b);
  const at = (q: number) => s[Math.min(s.length - 1, Math.floor(q * s.length))];
  return { n: s.length, p50_us: Math.round(at(0.5) * 1000), p95_us: Math.round(at(0.95) * 1000), p99_us: Math.round(at(0.99) * 1000) };
}

// A run's outcome, or the code of a HostError for a run that did not start.
async function submit(b: { path: string; tenant?: string; args?: Record<string, unknown>; intercept?: Record<string, unknown>; kind?: "flow" | "script" }, onSettled?: () => void) {
  try {
    return await scopes.store.run({ tenant: b.tenant ?? "" }, () => host.run(b.path, b.args, { intercept: b.intercept, kind: b.kind, onSettled }));
  } catch (e) {
    if (e instanceof HostError) return { refused: e.code };
    throw e;
  }
}

Bun.serve({
  unix: env.WL_HOST_SOCKET!,
  idleTimeout: 0,
  async fetch(req) {
    const url = new URL(req.url);
    switch (url.pathname) {
      case "/run":
        return Response.json(await submit(await req.json()));
      case "/settle": {
        // When the run's outcome came, and when it settled (-1: not within
        // wait milliseconds of its outcome).
        const b = await req.json();
        const start = performance.now();
        let settledAt = -1;
        let markSettled = () => {};
        const settled = new Promise<void>((resolve) => (markSettled = resolve));
        const r = await submit(b, () => {
          settledAt = performance.now() - start;
          markSettled();
        });
        const doneAt = performance.now() - start;
        await Promise.race([settled, Bun.sleep(Number(url.searchParams.get("wait") ?? "10000"))]);
        return Response.json({ ...r, doneAt, settledAt });
      }
      case "/drain": {
        const start = performance.now();
        await host.drain(Number(url.searchParams.get("ms") ?? "10000"));
        return Response.json({ ms: performance.now() - start, health: host.health() });
      }
      case "/health":
        return Response.json({ ...host.health(), recycled });
      case "/pid":
        return Response.json({ host: process.pid, engine: host.enginePid });
      case "/bench": {
        // Per-step latency of host mode: a one-step flow, run by the host
        // sequentially, timed around host.run.
        const n = Number(url.searchParams.get("n") ?? "200");
        for (let i = 0; i < 20; i++) await host.run("f/host/echo", { x: i });
        const ms: number[] = [];
        for (let i = 0; i < n; i++) {
          const t = performance.now();
          const r = await host.run("f/host/echo", { x: i });
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
