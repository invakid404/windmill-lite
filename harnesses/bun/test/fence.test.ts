// Developer checks of the settle-time fence and the per-invocation views
// of milestone C3b (runtime/context.ts): timers, the fetch view, the log
// view, the Bun.env write trap and child processes. The gate is the step
// suite (cmd/wltest) and the protocol exchanges (internal/harness), which
// run them in the real harness; these pin the rules in one process.

import { expect, test } from "bun:test";
import { mkdtempSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { join } from "node:path";
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
  setChildGroups,
  shadow,
  TAIL_LENGTH,
} from "../runtime/context.ts";

const require = createRequire(import.meta.url);
// Bun's fetch, before the fetch view wraps it.
const nativeFetch = globalThis.fetch;

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

/** The process group of pid, or undefined once no process has pid. */
function pgidOf(pid: number): number | undefined {
  const out = require("node:child_process").spawnSync("ps", ["-o", "pgid=", "-p", String(pid)]).stdout;
  const n = Number(String(out).trim());
  return String(out).trim() === "" ? undefined : n;
}

/** Whether a process is left in the process group pgid. */
function groupAlive(pgid: number): boolean {
  try {
    process.kill(-pgid, 0);
    return true;
  } catch {
    return false;
  }
}

/** Waits until cond holds, or fails after a second. */
async function until(what: string, cond: () => boolean) {
  for (let i = 0; i < 100; i++) {
    if (cond()) return;
    await Bun.sleep(10);
  }
  throw new Error(`timed out waiting for ${what}`);
}

test("a resource with a liveness check counts while it runs; revoke releases a kind now and at once afterwards", () => {
  const r = new InvocationResources();
  let alive = true;
  const released: string[] = [];
  r.track("child", () => released.push("child"), () => alive);
  r.track("timeout", () => released.push("timeout"));
  expect(r.pending()).toEqual({ child: 1, timeout: 1 });
  alive = false;
  expect(r.pending()).toEqual({ timeout: 1 });
  alive = true;
  // A dead entry was dropped: it neither counts again nor is released.
  expect(r.pending()).toEqual({ timeout: 1 });
  r.track("child", () => released.push("child 2"), () => true);
  r.revoke("child");
  expect(released).toEqual(["child 2"]);
  expect(r.pending()).toEqual({ timeout: 1 });
  r.track("child", () => released.push("child 3"));
  expect(released).toEqual(["child 2", "child 3"]);
  expect(r.settle()).toEqual({ timeout: 1 });
  expect(released).toEqual(["child 2", "child 3", "timeout"]);
});

test("in a warm process a child leads a process group of its own, which settlement kills, though its first process exited (WR-55)", async () => {
  // spec: WR-55
  const cp = require("node:child_process");
  setChildGroups(true);
  const stay = join(mkdtempSync(join(tmpdir(), "wl-fence-")), "stay.mjs");
  writeFileSync(stay, "setInterval(() => {}, 1000);\n");
  const c = context();
  try {
    const pids = invocations.run(c, () => {
      const a = cp.spawn("sleep", ["30"], { stdio: "ignore" });
      // The shell exits at once and leaves its sleep in the group.
      const b = cp.spawn("sh", ["-c", "sleep 30 &"], { stdio: "ignore" });
      const d = Bun.spawn(["sleep", "30"], { stdio: ["ignore", "ignore", "ignore"] });
      const e = cp.fork(stay, [], { stdio: "ignore" });
      return [a.pid, b.pid, d.pid, e.pid] as number[];
    });
    for (const pid of pids) expect(pgidOf(pid) ?? pid).toBe(pid);
    await until("the shell to exit", () => pgidOf(pids[1]) === undefined);
    expect(groupAlive(pids[1])).toBe(true);
    expect(c.resources.pending()).toEqual({ child: 4 });
    expect(c.resources.settle()).toEqual({ child: 4 });
    await until("every group to be killed", () => pids.every((pid) => !groupAlive(pid)));
  } finally {
    c.resources.settle();
  }
});

test("Bun.spawnSync's child leads a group, registered when the child left a process in it (WR-55)", async () => {
  // spec: WR-55
  setChildGroups(true);
  const c = context();
  const pid = invocations.run(c, () => Bun.spawnSync(["sh", "-c", "sleep 30 > /dev/null 2>&1 &"]).pid);
  expect(groupAlive(pid)).toBe(true);
  expect(c.resources.pending()).toEqual({ child: 1 });
  c.resources.settle();
  await until("the group to be killed", () => !groupAlive(pid));
  // One that leaves nothing behind registers nothing.
  const quiet = context();
  invocations.run(quiet, () => Bun.spawnSync(["true"]));
  expect(quiet.resources.pending()).toEqual({});
});

test("exec, execFile and the sync forms, where Bun ignores the step's detached, start a child in the step's group, as on Windmill (WR-55)", async () => {
  // spec: WR-55
  const cp = require("node:child_process");
  setChildGroups(true);
  const c = context();
  const pids = await invocations.run(c, async () => {
    const a = cp.exec("sleep 30", { detached: true });
    const b = cp.execFile("sleep", ["30"], { detached: true });
    cp.execSync("sleep 30 > /dev/null 2>&1 & echo $! > /dev/null", { detached: true });
    const s = cp.spawnSync("sh", ["-c", "sleep 30 > /dev/null 2>&1 &"]);
    return [a.pid, b.pid, s.pid] as number[];
  });
  try {
    for (const pid of pids) expect(pgidOf(pid) ?? pid).toBe(pid);
    // exec, execFile, execSync's leftover and spawnSync's leftover.
    expect(c.resources.pending()).toEqual({ child: 4 });
  } finally {
    c.resources.settle();
  }
  await until("every group to be killed", () => pids.every((pid) => !groupAlive(pid)));
});

test("a revoked invocation's children are killed now, and one it starts afterwards at once (WR-55, WR-37)", async () => {
  // spec: WR-55
  const cp = require("node:child_process");
  setChildGroups(true);
  const c = context();
  const first = invocations.run(c, () => cp.spawn("sleep", ["30"], { stdio: "ignore" }).pid as number);
  c.resources.revoke("child");
  await until("the first child's group to be killed", () => !groupAlive(first));
  const later = invocations.run(c, () => cp.spawn("sleep", ["30"], { stdio: "ignore" }).pid as number);
  await until("the later child's group to be killed", () => !groupAlive(later));
  expect(c.resources.pending()).toEqual({});
});

test("a child the step starts detached is its own and left running; in a fresh process a child stays in the process's group, which the engine kills (WR-55)", async () => {
  // spec: WR-55
  const cp = require("node:child_process");
  const kept: number[] = [];
  try {
    setChildGroups(true);
    const warm = context();
    const detached = invocations.run(warm, () => cp.spawn("sleep", ["30"], { stdio: "ignore", detached: true }).pid as number);
    kept.push(detached);
    expect(pgidOf(detached)).toBe(detached);
    expect(warm.resources.settle()).toEqual({ detached: 1 });
    await Bun.sleep(50);
    expect(groupAlive(detached)).toBe(true);

    setChildGroups(false);
    const fresh = context();
    const inGroup = invocations.run(fresh, () => cp.spawn("sleep", ["30"], { stdio: "ignore" }).pid as number);
    kept.push(inGroup);
    expect(pgidOf(inGroup)).toBe(pgidOf(process.pid));
    expect(fresh.resources.settle()).toEqual({ child: 1 });
    await Bun.sleep(50);
    expect(pgidOf(inGroup)).toBe(pgidOf(process.pid));
  } finally {
    setChildGroups(true);
    for (const pid of kept) {
      try {
        process.kill(pid, "SIGKILL");
      } catch {
        // gone
      }
    }
  }
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

test("an intercepted reply has no body for HEAD or a null-body status, as over TCP, and keeps the reason phrase (pf1 review 1, P2-2)", async () => {
  const facade = "http://127.0.0.1:9";
  const status = new Map<string, number>([["/api/head", 501], ["/api/204", 204], ["/api/205", 205], ["/api/304", 304], ["/api/get", 501]]);
  installFetch(
    () => facade,
    async (call) => ({
      status: status.get(new URL(call.url).pathname) ?? 500,
      headers: { "content-type": "text/plain" },
      body: new TextEncoder().encode("Not implemented in windmill-lite: x"),
    }),
  );
  const c = context();
  const answer = (path: string, method = "GET") =>
    invocations.run(c, async () => {
      const res = await fetch(`${facade}${path}`, { method });
      return [res.status, res.statusText, res.body === null, await res.text()];
    });
  expect(await answer("/api/head", "HEAD")).toEqual([501, "Not Implemented", true, ""]);
  expect(await answer("/api/204")).toEqual([204, "No Content", true, ""]);
  expect(await answer("/api/205")).toEqual([205, "Reset Content", true, ""]);
  expect(await answer("/api/304")).toEqual([304, "Not Modified", true, ""]);
  // The control: a GET keeps its body.
  expect(await answer("/api/get")).toEqual([501, "Not Implemented", false, "Not implemented in windmill-lite: x"]);
  c.resources.settle();
});

test("a request stays in flight until a reading method has read its body to the end; read any other way, it stays counted until settlement (c3b review 1, R1-2; review 3)", async () => {
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
    // A stream's or a cancel's end is no evidence that the transfer ended,
    // so the request is counted until settlement: too high, never too low.
    expect(await left(async (r) => { for await (const _ of r.body!) {} })).toEqual([inFlight, inFlight]);
    expect(await left((r) => r.body!.cancel())).toEqual([inFlight, inFlight]);
    expect(await left(async (r) => r.status)).toEqual([inFlight, inFlight]);
    // A failed read is no evidence either.
    expect(await left((r) => r.formData().catch(() => {}))).toEqual([inFlight, inFlight]);
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

/** A server whose /gate/<n> responses send "first" and hold "last" until
 * release(n), and whose other paths answer "whole" at once. */
function gatedServer() {
  const gates = new Map<string, () => void>();
  const server = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    idleTimeout: 30,
    fetch(req) {
      const path = new URL(req.url).pathname;
      if (!path.startsWith("/gate/")) return new Response("whole", { headers: { "content-type": "text/plain;charset=utf-8" } });
      return new Response(
        new ReadableStream({
          start(c) {
            c.enqueue(new TextEncoder().encode("first"));
            gates.set(path, () => {
              try {
                c.enqueue(new TextEncoder().encode("last"));
                c.close();
              } catch {
                // The request was aborted.
              }
            });
          },
        }),
      );
    },
  });
  return { server, release: (path: string) => gates.get(path)?.() };
}

test("a reading method or clone borrowed for another response says nothing of the request it came from (c3b review 4, R4-1)", async () => {
  const { server, release } = gatedServer();
  const settled = async (path: string, act: (res: Response, other: Response) => Promise<unknown>) => {
    const c = context();
    const res = await invocations.run(c, () => fetch(new URL(path, server.url)));
    const other = await nativeFetch(new URL("/whole", server.url));
    const got = await invocations.run(c, () => act(res, other));
    const reading = res.text();
    await Bun.sleep(5);
    const left = c.resources.settle();
    release(path);
    return [got, left, await reading.then((v) => v, (e) => `${e.name}`)];
  };
  try {
    // res.text, borrowed for another response, reads that one to its end.
    expect(await settled("/gate/borrowed-text", (res, other) => res.text.call(other))).toEqual(["whole", { fetch: 1 }, "AbortError"]);
    // res.clone, borrowed, clones the other response; reading that clone
    // to its end says nothing of res either.
    expect(await settled("/gate/borrowed-clone", (res, other) => (res.clone.call(other) as Response).text())).toEqual([
      "whole",
      { fetch: 1 },
      "AbortError",
    ]);
    // The control: res's own text() releases it once it has read the body.
    const c = context();
    const res = await invocations.run(c, () => fetch(new URL("/whole", server.url)));
    expect(await res.text()).toBe("whole");
    expect(c.resources.settle()).toEqual({});
  } finally {
    server.stop(true);
  }
});

test("a read that fails without reading, or a branch canceled while another reads, leaves the request in flight, and settlement aborts the active read (c3b review 2, R2-1)", async () => {
  const { server, release } = gatedServer();
  // act starts the read that stays active, and returns it in a box: an
  // async function would otherwise wait for it.
  const settled = async (path: string, act: (res: Response) => Promise<{ active: Promise<unknown> }>) => {
    const c = context();
    const res = await invocations.run(c, () => fetch(new URL(path, server.url)));
    const { active } = await invocations.run(c, () => act(res));
    await Bun.sleep(5);
    const left = c.resources.settle();
    release(path);
    return [left, await active.then((v) => v, (e) => `${e.name}`)];
  };
  try {
    // A second text() fails with a TypeError, without reading.
    expect(
      await settled("/gate/second-read", async (res) => {
        const first = res.text();
        expect(await res.text().then(() => "read", (e) => e.name)).toBe("TypeError");
        return { active: first };
      }),
    ).toEqual([{ fetch: 1 }, "AbortError"]);
    // One clone's body is canceled while the other reads.
    expect(
      await settled("/gate/clone-cancel", async (res) => {
        const reading = res.clone().text();
        void res.body!.cancel();
        return { active: reading };
      }),
    ).toEqual([{ fetch: 1 }, "AbortError"]);
    // One tee branch is canceled while the other reads.
    expect(
      await settled("/gate/tee-cancel", async (res) => {
        const [x, y] = res.body!.tee();
        void x.cancel();
        const reader = y.getReader();
        const reading = (async () => {
          let s = "";
          for (;;) {
            const chunk = await reader.read();
            if (chunk.done) return s;
            s += new TextDecoder().decode(chunk.value);
          }
        })();
        return { active: reading };
      }),
    ).toEqual([{ fetch: 1 }, "AbortError"]);
    // c3b review 3, R3-1: no stream operation ends the request. A cancel
    // that fails because a reader holds the body:
    expect(
      await settled("/gate/locked-cancel", async (res) => {
        const reading = res.text();
        expect(await res.body!.cancel().then(() => "canceled", (e) => e.name)).toBe("TypeError");
        return { active: reading };
      }),
    ).toEqual([{ fetch: 1 }, "AbortError"]);
    // A read on a reader already released, while a new reader reads:
    expect(
      await settled("/gate/released-read", async (res) => {
        const old = res.body!.getReader();
        old.releaseLock();
        const reader = res.body!.getReader();
        expect(await old.read().then(() => "read", (e) => e.name)).toBe("TypeError");
        return { active: (async () => { for (;;) if ((await reader.read()).done) return "read"; })() };
      }),
    ).toEqual([{ fetch: 1 }, "AbortError"]);
    // A pipe that fails with preventCancel, while a clone reads:
    expect(
      await settled("/gate/pipe-prevent-cancel", async (res) => {
        const reading = res.clone().text();
        const sink = new WritableStream({ write: () => Promise.reject(new Error("sink")) });
        expect(await res.body!.pipeTo(sink, { preventCancel: true }).then(() => "piped", (e) => e.message)).toBe("sink");
        return { active: reading };
      }),
    ).toEqual([{ fetch: 1 }, "AbortError"]);
    // A reader canceled while its read is pending, which then reports done,
    // while a clone reads:
    expect(
      await settled("/gate/canceled-reader", async (res) => {
        const reading = res.clone().text();
        const reader = res.body!.getReader();
        await reader.read();
        const pending = reader.read();
        void reader.cancel();
        expect((await pending).done).toBe(true);
        return { active: reading };
      }),
    ).toEqual([{ fetch: 1 }, "AbortError"]);
    // Controls: a reading method that reads the body to its end, on the
    // response or a clone, ends the request; a pipe or a cancel on every
    // branch reads it natively, but is no evidence, so it stays counted.
    for (const [consume, left] of [
      [(res: Response) => res.text(), {}],
      [(res: Response) => Promise.all([res.clone().text(), res.text()]), {}],
      [(res: Response) => res.clone().clone().text(), {}],
      [(res: Response) => res.body!.pipeTo(new WritableStream()), { fetch: 1 }],
      [
        async (res: Response) => {
          for await (const _ of res.body!.pipeThrough(new TextDecoderStream())) {
          }
        },
        { fetch: 1 },
      ],
      [
        (res: Response) => {
          const c = res.clone();
          return Promise.all([res.body!.cancel(), c.body!.cancel()]);
        },
        { fetch: 1 },
      ],
    ] as const) {
      const c = context();
      const res = await invocations.run(c, () => fetch(new URL("/whole", server.url)));
      await invocations.run(c, () => consume(res));
      await Bun.sleep(1);
      expect(c.resources.pending()).toEqual(left);
    }
  } finally {
    server.stop(true);
  }
});

test("the response of the fetch view is Bun's own: locking, disturbance, bodyUsed, clone and tee behave as without the fence (c3b review 2, R2-2)", async () => {
  const { server } = gatedServer();
  const settle = (p: Promise<unknown>) => p.then((v) => (v instanceof Blob ? `blob ${v.type} ${v.size}` : JSON.stringify(v)), (e) => `rejects ${e.name}`);
  const call = (f: () => unknown) => {
    try {
      const v = f();
      return v instanceof Response ? "a response" : String(v);
    } catch (e) {
      return `throws ${(e as Error).name}`;
    }
  };
  const scenarios: Record<string, (res: Response) => Promise<string[]>> = {
    "a reader locks the body, then text": async (res) => (res.body!.getReader(), [await settle(res.text())]),
    "a reader locks the body, then clone": async (res) => (res.body!.getReader(), [call(() => res.clone())]),
    "a reader is taken and released, then text": async (res) => (res.body!.getReader().releaseLock(), [await settle(res.text()), String(res.bodyUsed)]),
    "a chunk is read, the lock released, then text": async (res) => {
      const reader = res.body!.getReader();
      await reader.read();
      reader.releaseLock();
      return [await settle(res.text()), String(res.bodyUsed)];
    },
    "text twice": async (res) => {
      const first = res.text();
      const second = await settle(res.text());
      return [await settle(first), second];
    },
    "clone, then both read": async (res) => {
      const c = res.clone();
      return [await settle(res.text()), await settle(c.text())];
    },
    "text, then clone": async (res) => (await res.text(), [call(() => res.clone())]),
    "clone, lock the original, read the clone": async (res) => {
      const c = res.clone();
      res.body!.getReader();
      return [await settle(c.text())];
    },
    "tee, then text": async (res) => (res.body!.tee(), [await settle(res.text())]),
    "body looked at": async (res) => {
      const body = res.body;
      return [String(res.bodyUsed), String(body === res.body), String(body!.locked)];
    },
    blob: async (res) => [await settle(res.blob())],
    "a read's own reaction": async (res) => {
      // A rejection reaches the step as the native one does.
      const p = res.json();
      return [await settle(p), String(p instanceof Promise)];
    },
    "json of text": async (res) => [await settle(res.json())],
    "pipeThrough while locked": async (res) => (res.body!.getReader(), [call(() => res.body!.pipeThrough(new TransformStream()))]),
    "pipeThrough of no pair": async (res) => [call(() => res.body!.pipeThrough({} as TransformStream))],
    "pipeTo while locked": async (res) => (res.body!.getReader(), [await settle(res.body!.pipeTo(new WritableStream()))]),
    // c3b review 3, R3-2: the pipes are Bun's own, their option checks too.
    "pipeThrough with a signal that is not one": async (res) => [call(() => res.body!.pipeThrough(new TransformStream(), { signal: {} as AbortSignal }))],
    "pipeTo with a signal that is not one": async (res) => [await settle(res.body!.pipeTo(new WritableStream(), { signal: {} as AbortSignal }))],
    "pipeThrough, read": async (res) => [await settle(new Response(res.body!.pipeThrough(new TransformStream())).text())],
    "pipeTo, then bodyUsed": async (res) => [await settle(res.body!.pipeTo(new WritableStream())), String(res.bodyUsed)],
    "Bun.readableStreamToText": async (res) => [await settle(Bun.readableStreamToText(res.body!))],
    "metadata": async (res) => [res.url === server.url.href + "whole" ? "url" : res.url, res.type, String(res.redirected), String(res.status), String(res instanceof Response)],
  };
  try {
    for (const [name, scenario] of Object.entries(scenarios)) {
      const native = await scenario(await nativeFetch(new URL("/whole", server.url)));
      const fenced = await scenario(await invocations.run(context(), () => fetch(new URL("/whole", server.url))));
      expect([name, fenced]).toEqual([name, native]);
    }
  } finally {
    server.stop(true);
  }
});
