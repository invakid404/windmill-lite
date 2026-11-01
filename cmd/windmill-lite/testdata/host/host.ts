// A Bun host for the host-mode tests (cmd/windmill-lite/host_test.go). It
// embeds the host SDK and registers its instances of the host-provided
// modules f/host/scopes.ts, ../hostlib/tenant.ts (outside the sync folder),
// f/host/pool.ts, and f/host/lazy/a.ts and f/host/lazy/b.ts (whose cycle
// closes only through a lazy import). It connects the engine on a bundle
// built from testdata/hostfolder, and serves its callers over HTTP on a
// Unix socket.
//
// Each run is submitted inside the caller's tenant scope, in both scope
// modules (scopes.ts and tenant.ts). The tests compile the host with `bun
// build --compile` and run it under a supervisor that only restarts it
// when it exits.
//
// Environment: WL_ENGINE (the engine binary), WL_BUNDLE, WL_HOST_SOCKET,
// WL_KILL_AFTER_MS (the watchdog's threshold), WL_WORKDIR (the working
// directory it passes; its own by default), WL_RECYCLE_MODULES (the
// modules recycle trigger, unset by default), WL_RECYCLE_RETIRED (the
// retired-entry trigger's maximum, whose count /retire raises; unset by
// default), WL_PLUGIN_CASES (a JSON file
// of plugin cases, whose case plugin the host connects with, milestone
// F2c), WL_PLUGIN_REPORT (the file that plugin's onRunSettled reports to)
// and WL_ENGINE_FLAGS (flags for the engine, space-separated).

import { readFileSync } from "node:fs";
import { connect, HostError, type Plugin, type RunOptions } from "../../../../harnesses/bun/host/index.ts";
import { casePlugin } from "../../../../harnesses/bun/test/caseplugin.ts";
import * as lazyA from "../hostfolder/f/host/lazy/a.ts";
import * as lazyB from "../hostfolder/f/host/lazy/b.ts";
import * as pool from "../hostfolder/f/host/pool.ts";
import * as scopes from "../hostfolder/f/host/scopes.ts";
import * as outside from "../hostlib/tenant.ts";

const env = process.env;
let recycled: string | null = null;
// The entries a pooled-connection layer would have retired and not
// released, which /retire counts up (the retired recycle trigger).
let retired = 0;
// The case plugin, whose onRunEnd also reports the tenant scope it runs in
// (H11: a plugin call re-enters the run's scope).
let plugin: Plugin | undefined;
if (env.WL_PLUGIN_CASES) {
  const p = casePlugin(JSON.parse(readFileSync(env.WL_PLUGIN_CASES, "utf8")), env.WL_PLUGIN_REPORT);
  const end = p.onRunEnd!;
  plugin = {
    ...p,
    onRunEnd: async (t) => [...((await end(t)) ?? []), { code: "plugin_scope", message: scopes.store.getStore()?.tenant ?? "" }],
  };
}
let host;
try {
  host = await connect({
    engine: env.WL_ENGINE!,
    bundle: env.WL_BUNDLE!,
    workdir: env.WL_WORKDIR ?? process.cwd(),
    hostModules: {
      "f/host/scopes.ts": scopes,
      "f/host/pool.ts": pool,
      "../hostlib/tenant.ts": outside,
      "f/host/lazy/a.ts": lazyA,
      "f/host/lazy/b.ts": lazyB,
    },
    watchdog: { killAfter: Number(env.WL_KILL_AFTER_MS ?? "30000") },
    recycle:
      env.WL_RECYCLE_MODULES || env.WL_RECYCLE_RETIRED
        ? {
            modules: env.WL_RECYCLE_MODULES ? Number(env.WL_RECYCLE_MODULES) : undefined,
            retired: env.WL_RECYCLE_RETIRED ? { max: Number(env.WL_RECYCLE_RETIRED), count: () => retired } : undefined,
          }
        : undefined,
    onRecycle: (reason) => (recycled = reason),
    plugin,
    engineFlags: env.WL_ENGINE_FLAGS ? env.WL_ENGINE_FLAGS.split(" ") : undefined,
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
// The run options a caller may send besides intercept and kind: the run's
// own hooks and budgets.
type Budgets = Pick<RunOptions, "writeCapable" | "haltBefore" | "haltAfter" | "fatalErrorNames" | "deadlineMs" | "preWriteDeadlineMs" | "clock">;

async function submit(b: { path: string; tenant?: string; args?: Record<string, unknown>; intercept?: Record<string, unknown>; kind?: "flow" | "script" } & Budgets, onSettled?: () => void) {
  try {
    const { writeCapable, haltBefore, haltAfter, fatalErrorNames, deadlineMs, preWriteDeadlineMs, clock } = b;
    const opts: RunOptions = { intercept: b.intercept, kind: b.kind, onSettled, writeCapable, haltBefore, haltAfter, fatalErrorNames, deadlineMs, preWriteDeadlineMs, clock };
    return await scopes.store.run({ tenant: b.tenant ?? "" }, () => outside.tenant.run(b.tenant ?? "", () => host.run(b.path, b.args, opts)));
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
        // The scope onSettled runs in: the run's own (H11).
        let settledTenant: string | null = null;
        const r = await submit(b, () => {
          settledAt = performance.now() - start;
          settledTenant = scopes.store.getStore()?.tenant ?? null;
          markSettled();
        });
        const doneAt = performance.now() - start;
        await Promise.race([settled, Bun.sleep(Number(url.searchParams.get("wait") ?? "10000"))]);
        return Response.json({ ...r, doneAt, settledAt, settledTenant });
      }
      case "/lease": {
        // The resource-release test (ADR 0004 host mode item 5): the run's
        // leases of the host's pool are released at its onSettled, in its
        // scope, and never earlier; the response has the lease as the
        // step left it, with when the outcome came and when it settled.
        const b = await req.json();
        const start = performance.now();
        let settledAt = -1;
        let released = 0;
        let markSettled = () => {};
        const settled = new Promise<void>((resolve) => (markSettled = resolve));
        const r = await submit(b, () => {
          released = pool.release(scopes.store.getStore()?.tenant ?? "");
          settledAt = performance.now() - start;
          markSettled();
        });
        const doneAt = performance.now() - start;
        await Promise.race([settled, Bun.sleep(10000)]);
        const leases = pool.leases
          .filter((l) => l.tenant === b.tenant)
          .map((l) => ({ ...l, lastUse: l.lastUse - start, releasedAt: l.releasedAt < 0 ? -1 : l.releasedAt - start }));
        return Response.json({ ...r, doneAt, settledAt, released, leases });
      }
      case "/drainorder": {
        // Submits a run, drains while it is in flight, and reports in which
        // order the run's caller and the drain finished, with the length of
        // the result the caller got.
        const b = await req.json();
        const order: string[] = [];
        const run = submit(b).then((r) => {
          order.push("run");
          return r as { status?: string; result?: unknown };
        });
        await Bun.sleep(Number(url.searchParams.get("after") ?? "200"));
        const drained = host.drain(Number(url.searchParams.get("ms") ?? "20000")).then(() => order.push("drain"));
        const r = await run;
        await drained;
        return Response.json({ order, status: r.status, length: typeof r.result === "string" ? r.result.length : -1, health: host.health() });
      }
      case "/drain": {
        const start = performance.now();
        await host.drain(Number(url.searchParams.get("ms") ?? "10000"));
        return Response.json({ ms: performance.now() - start, health: host.health() });
      }
      case "/health":
        return Response.json({ ...host.health(), recycled });
      case "/retire":
        retired++;
        return Response.json({ retired });
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
