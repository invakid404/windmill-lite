// Developer checks of the settle-time fence and the per-invocation views
// of milestone C3b (runtime/context.ts): timers, the fetch view, the log
// view, the Bun.env write trap and child processes. The gate is the step
// suite (cmd/wltest) and the protocol exchanges (internal/harness), which
// run them in the real harness; these pin the rules in one process.

import { expect, test } from "bun:test";
import { createRequire } from "node:module";
import { promisify } from "node:util";
import {
  installChildProcesses,
  installEnvView,
  installFetch,
  installLogs,
  installTimers,
  type InvocationContext,
  InvocationLog,
  InvocationResources,
  invocations,
  shadow,
  TAIL_LENGTH,
} from "../runtime/context.ts";

const require = createRequire(import.meta.url);

installEnvView();
installTimers();
installLogs();
installChildProcesses();

/** A context whose work registers in its own resources, or, with module,
 * in none, as while a step module evaluates. */
function context(env: Record<string, string> = {}, module = false): InvocationContext {
  const e = Object.assign(Object.create(null), env);
  shadow(e);
  const resources = new InvocationResources();
  return { id: "i", run: "r", env: e, signal: new AbortController().signal, resources, work: module ? undefined : resources, log: new InvocationLog() };
}

test("the timers an invocation leaves pending are counted, then cleared at settlement; fired and cleared ones are not", async () => {
  const c = context();
  const fired: string[] = [];
  await invocations.run(c, async () => {
    setTimeout(() => fired.push("fired"), 0);
    await Bun.sleep(10);
    setTimeout(() => fired.push("timeout"), 5);
    setInterval(() => fired.push("interval"), 5);
    setImmediate(() => fired.push("immediate"));
    clearTimeout(setTimeout(() => fired.push("cleared"), 5));
    clearTimeout(Number(setTimeout(() => fired.push("cleared by id"), 5)));
    clearImmediate(setImmediate(() => fired.push("cleared immediate")));
    require("node:timers").setTimeout(() => fired.push("node:timers"), 5);
  });
  expect(c.resources.settle()).toEqual({ immediate: 1, interval: 1, timeout: 2 });
  await Bun.sleep(30);
  expect(fired).toEqual(["fired"]);
});

test("a timer made after settlement is cleared at once; one outside every invocation, or in a module's evaluation, is not registered", async () => {
  const settled = context();
  settled.resources.settle();
  const module = context({}, true);
  const fired: string[] = [];
  invocations.run(settled, () => setTimeout(() => fired.push("after settlement"), 1));
  invocations.run(module, () => setTimeout(() => fired.push("module"), 1));
  setTimeout(() => fired.push("outside"), 1);
  expect(module.resources.pending()).toEqual({});
  await Bun.sleep(20);
  expect(fired.sort()).toEqual(["module", "outside"]);
});

test("util.promisify(setTimeout) keeps Bun's promise form", async () => {
  expect(await invocations.run(context(), () => promisify(setTimeout)(1, "v"))).toBe("v");
});

test("a log merges lines per stream, drops empty ones, keeps a 600-character tail and drops everything once closed", () => {
  let scheduled = 0;
  const log = new InvocationLog(() => scheduled++);
  log.write("stdout", "one\n\ntw");
  log.write("stderr", "err ");
  log.write("stdout", "o\n");
  expect(log.take()).toEqual(["one", "two"]);
  expect(log.take(true)).toEqual(["err "]);
  expect(scheduled).toBe(2);
  log.lines("x".repeat(TAIL_LENGTH) + "\n" + "🙂".repeat(3));
  expect(Array.from(log.tail()).length).toBe(TAIL_LENGTH);
  expect(log.tail().endsWith("\n🙂🙂🙂")).toBe(true);
  log.close();
  log.write("stdout", "dropped\n");
  log.lines("dropped");
  expect(log.take(true)).toEqual([]);
});

test("console and process.stdout and stderr write to the invocation's log, with counters of its own", async () => {
  const c = context();
  const flushed: unknown[] = [];
  await invocations.run(c, async () => {
    console.log("a %d", 1, { b: 2 });
    console.count();
    console.error(new Error("boom"));
    process.stdout.write("partial", () => flushed.push("callback"));
    process.stderr.write(new TextEncoder().encode(" bytes\n"));
    await Bun.sleep(1);
  });
  const lines = c.log.take(true);
  expect(lines.slice(0, 3)).toEqual(["a 1 { b: 2 }", "default: 1", "Error: boom"]);
  expect(lines).toContain(" bytes");
  expect(lines.at(-1)).toBe("partial");
  expect(flushed).toEqual(["callback"]);
  expect(invocations.run(context(), () => (console.count(), "")).length).toBe(0);
  c.log.close();
  invocations.run(c, () => console.log("after settlement"));
  expect(c.log.take(true)).toEqual([]);
});

test("Bun.env writes of a variable the process lacks stay with the invocation", () => {
  const got = invocations.run(context(), () => {
    Bun.env.WL_FENCE_TEST_NEW = "inv";
    return [Bun.env.WL_FENCE_TEST_NEW, process.env.WL_FENCE_TEST_NEW];
  });
  expect(got).toEqual(["inv", "inv"]);
  expect(Bun.env.WL_FENCE_TEST_NEW).toBeUndefined();
  expect(Object.hasOwn(Bun.env, "WL_FENCE_TEST_NEW")).toBe(false);
});

test("a child started without an environment gets the invocation's; one given an environment keeps it", async () => {
  const cp = require("node:child_process");
  const c = context({ WL_FENCE_TEST_CHILD: "inv" });
  const got = await invocations.run(c, async () => {
    const out = [
      String(cp.execSync("printenv WL_FENCE_TEST_CHILD")).trim(),
      String(cp.spawnSync("printenv", ["WL_FENCE_TEST_CHILD"]).stdout).trim(),
      new TextDecoder().decode(Bun.spawnSync(["printenv", "WL_FENCE_TEST_CHILD"]).stdout).trim(),
      String(cp.execSync("printenv WL_FENCE_TEST_CHILD || true", { env: { PATH: process.env.PATH! } })).trim(),
      await new Promise((resolve) => cp.exec("printenv WL_FENCE_TEST_CHILD", (_e: unknown, so: string) => resolve(so.trim()))),
    ];
    out.push((await promisify(cp.exec)("printenv WL_FENCE_TEST_CHILD")).stdout.trim());
    const child = Bun.spawn(["sleep", "0.05"]);
    out.push(JSON.stringify(c.resources.pending()));
    await child.exited;
    await Bun.sleep(1);
    out.push(JSON.stringify(c.resources.pending()));
    return out;
  });
  // promisify(exec) keeps Node's {stdout, stderr} form (c3b review).
  expect(got).toEqual(["inv", "inv", "inv", "", "inv", "inv", '{"child":1}', "{}"]);
  expect(process.env.WL_FENCE_TEST_CHILD).toBeUndefined();
});

test("a request the invocation leaves in flight is aborted at settlement, and a platform call after it is refused", async () => {
  const facade = "http://127.0.0.1:9";
  const calls: string[] = [];
  installFetch(
    () => facade,
    async (call) => {
      calls.push(call.url);
      return { status: 200, headers: {} };
    },
  );
  const server = Bun.serve({ port: 0, hostname: "127.0.0.1", fetch: () => new Promise<Response>(() => {}) });
  try {
    const c = context();
    const pending = invocations.run(c, () => fetch(server.url).then(() => "answered", (e) => e.name));
    await Bun.sleep(20);
    expect(c.resources.pending()).toEqual({ fetch: 1 });
    c.resources.settle();
    expect(await pending).toBe("AbortError");
    const refused = await invocations.run(c, () => fetch(`${facade}/api/w/ws/x`).then(() => "sent", (e) => `${e.name}: ${e.message}`));
    expect(refused).toBe("TypeError: windmill-lite: the invocation has settled, so its platform calls are refused");
    expect(calls).toEqual([]);
  } finally {
    server.stop(true);
  }
});

test("a request stays in flight until its body has been read to the end, canceled or failed, whatever reads it (c3b review 1, R1-2)", async () => {
  const server = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    fetch: (req) => (new URL(req.url).pathname === "/empty" ? new Response(null, { status: 204 }) : new Response(JSON.stringify({ a: 1 }))),
  });
  try {
    const left = async (read: (r: Response) => Promise<unknown>) => {
      const c = context();
      const res = await invocations.run(c, () => fetch(server.url));
      const before = JSON.stringify(c.resources.pending());
      await invocations.run(c, () => read(res));
      await Bun.sleep(1);
      return [before, JSON.stringify(c.resources.pending())];
    };
    const inFlight = '{"fetch":1}';
    expect(await left((r) => r.text())).toEqual([inFlight, "{}"]);
    expect(await left((r) => r.json())).toEqual([inFlight, "{}"]);
    expect(await left((r) => r.arrayBuffer())).toEqual([inFlight, "{}"]);
    expect(await left((r) => r.bytes())).toEqual([inFlight, "{}"]);
    expect(await left(async (r) => { for await (const _ of r.body!) {} })).toEqual([inFlight, "{}"]);
    expect(await left((r) => r.body!.cancel())).toEqual([inFlight, "{}"]);
    expect(await left(async (r) => r.status)).toEqual([inFlight, inFlight]);
    // Looking at body leaves the reading methods working, and the request
    // ends when one of them has read it.
    expect(await left(async (r) => (r.body, r.json()))).toEqual([inFlight, "{}"]);
    // A response without a body is done at once.
    const c = context();
    const empty = await invocations.run(c, () => fetch(new URL("/empty", server.url)));
    const head = await invocations.run(c, () => fetch(server.url, { method: "HEAD" }));
    expect([empty.status, head.status, JSON.stringify(c.resources.pending())]).toEqual([204, 200, "{}"]);
    // The Response API is unchanged.
    const res = await invocations.run(context(), () => fetch(server.url));
    expect([res instanceof Response, res.url, res.ok, res.bodyUsed, res.body === res.body]).toEqual([true, server.url.href, true, false, true]);
    expect(await res.json()).toEqual({ a: 1 });
    expect(res.bodyUsed).toBe(true);
  } finally {
    server.stop(true);
  }
});

test("a refresh() that re-arms a fired timeout registers it again, with the invocation that made it, and does nothing once that invocation has settled (c3b review 1, R1-3)", async () => {
  const c = context();
  let fired = 0;
  let t!: ReturnType<typeof setTimeout>;
  await invocations.run(c, () => new Promise<void>((r) => (t = setTimeout(() => (fired++, r()), 1))));
  expect(c.resources.pending()).toEqual({});
  // Refreshed outside the invocation's context: the timer is still its.
  t.refresh();
  expect(c.resources.pending()).toEqual({ timeout: 1 });
  // A cleared timeout is not re-armed by refresh, so it is not registered.
  const cleared = invocations.run(c, () => setTimeout(() => fired++, 1));
  clearTimeout(cleared);
  cleared.refresh();
  expect(c.resources.settle()).toEqual({ timeout: 1 });
  t.refresh();
  await Bun.sleep(20);
  expect(fired).toBe(1);
  // A timer of the module's own work is not the invocation's: refresh is
  // Bun's.
  const module = context({}, true);
  let moduleFired = 0;
  const m = invocations.run(module, () => setTimeout(() => moduleFired++, 1));
  await Bun.sleep(10);
  m.refresh();
  await Bun.sleep(10);
  expect([moduleFired, module.resources.pending()]).toEqual([2, {}]);
});
