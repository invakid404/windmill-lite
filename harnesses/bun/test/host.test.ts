// Developer checks of the host SDK's connect options (host/index.ts). The
// host-mode gate runs in cmd/windmill-lite against a compiled host.

import { afterAll, afterEach, beforeEach, describe, expect, test } from "bun:test";
import { chmodSync, mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { checkWorkdir, type ConnectOptions, connect, DEFAULT_COLLECT_QUIET_MS, type Host, HostError } from "../host/index.ts";
import { HARNESS_VERSION, versionsPair } from "../runtime/version.ts";

test("importing the SDK publishes the process scope, before any connect, for the host's own modules to capture", () => {
  const scope = (globalThis as Record<symbol, { capture: () => unknown } | undefined>)[Symbol.for("windmill-lite.processScope")];
  expect(typeof scope?.capture).toBe("function");
});

async function code(p: Promise<unknown>): Promise<string | undefined> {
  try {
    await p;
  } catch (e) {
    return e instanceof HostError ? e.code : String(e);
  }
  return undefined;
}

type WorkdirCase = { case: string; title: string; workdir: string; cwd_mode?: string; other_mode?: string; expect: string | null };

// The temporary directories the tests make, removed once they have run.
const made: string[] = [];
function tempDir(prefix: string): string {
  const dir = mkdtempSync(join(tmpdir(), prefix));
  made.push(dir);
  return dir;
}
afterAll(() => {
  for (const dir of made) rmSync(dir, { recursive: true, force: true });
});

// The shared cases of host mode's working-directory checks, which the
// engine runs too (internal/harness, TestHostWorkdirCases).
const workdir = (await Bun.file(new URL("../../../conformance/protocol/host/workdir.json", import.meta.url)).json()) as { cases: WorkdirCase[] };

describe("conformance/protocol/host/workdir.json", () => {
  const root = process.getuid?.() === 0;
  for (const c of workdir.cases) {
    // Root writes through a read-only mode, so those checks cannot fail there.
    test.skipIf(root && (c.cwd_mode !== undefined || c.other_mode !== undefined))(`${c.case}: ${c.title}`, () => {
      const dir = tempDir("wl-host-workdir-");
      const cwd = join(dir, "cwd");
      const other = join(dir, "other");
      mkdirSync(cwd);
      mkdirSync(other);
      symlinkSync(cwd, join(dir, "link"));
      writeFileSync(join(dir, "file"), "");
      if (c.cwd_mode) chmodSync(cwd, Number.parseInt(c.cwd_mode, 8));
      if (c.other_mode) chmodSync(other, Number.parseInt(c.other_mode, 8));
      const back = process.cwd();
      process.chdir(cwd);
      let got: string | null = null;
      try {
        checkWorkdir(c.workdir === "." ? "." : join(dir, c.workdir));
      } catch (e) {
        got = e instanceof HostError ? e.code : String(e);
      } finally {
        process.chdir(back);
        chmodSync(cwd, 0o755);
        chmodSync(other, 0o755);
      }
      expect(got).toBe(c.expect);
    });
  }
});

// The shared cases of the SDK–engine version pairing, which the engine runs
// too (internal/harness, TestVersionPairingCases): the SDK refuses a
// welcome from an engine of another release (ADR 0009, milestone F3b).
type VersionCase = { case: string; title: string; sdk: string; engine: string; pairs: boolean };
const versions = (await Bun.file(new URL("../../../conformance/protocol/host/versions.json", import.meta.url)).json()) as { cases: VersionCase[] };

describe("conformance/protocol/host/versions.json", () => {
  for (const c of versions.cases) {
    test(`${c.case}: ${c.title}`, () => {
      expect(versionsPair(c.sdk, c.engine)).toBe(c.pairs);
    });
  }
  test("the repository's own SDK is unstamped", () => {
    expect(HARNESS_VERSION).toBe("dev");
  });
});

test("connect checks the working directory before it starts the engine", async () => {
  const dir = tempDir("wl-host-test-");
  expect(await code(connect({ engine: "/nonexistent", bundle: dir, workdir: dir }))).toBe("workdir_mismatch");
});

test("an engine that cannot start is engine_start", async () => {
  const dir = tempDir("wl-host-test-");
  writeFileSync(join(dir, "manifest.json"), "{}");
  const cwd = process.cwd();
  process.chdir(dir);
  try {
    expect(await code(connect({ engine: join(dir, "no-such-engine"), bundle: dir, workdir: dir }))).toBe("engine_start");
  } finally {
    process.chdir(cwd);
  }
});

test("connect extracts the bundle's codebase assets into the working directory before it starts the engine", async () => {
  const dir = tempDir("wl-host-test-");
  const bundle = join(dir, "bundle");
  const work = join(dir, "work");
  mkdirSync(join(bundle, "assets", "data"), { recursive: true });
  mkdirSync(work);
  writeFileSync(join(bundle, "manifest.json"), "{}");
  writeFileSync(join(bundle, "assets", "data", "greeting.txt"), "hello from an asset");
  const cwd = process.cwd();
  process.chdir(work);
  try {
    expect(await code(connect({ engine: join(dir, "no-such-engine"), bundle, workdir: work }))).toBe("engine_start");
    expect(await Bun.file(join(work, "data", "greeting.txt")).text()).toBe("hello from an asset");
  } finally {
    process.chdir(cwd);
  }
});

// Review round 1, finding 3: a run's onSettled runs in the scope the run
// was submitted in (H11), outside every invocation, after its run_done has
// removed it from the runs in flight; concurrent runs keep their own.
test("onSettled runs in its run's submission scope", async () => {
  const { AsyncLocalStorage } = await import("node:async_hooks");
  const { Runtime } = await import("../runtime/runtime.ts");
  const { encode, frame } = await import("../runtime/frame.ts");
  const store = new AsyncLocalStorage<string>();
  const runtime = new Runtime({ bundleDir: "/nonexistent", bundleDigest: "sha256:b", host: true, transport: { write: (b) => b.length, end: () => {} }, exit: () => {} });
  runtime.start();
  const send = (m: Record<string, unknown>) => runtime.onData(frame(encode(m as { t: string })));
  send({ t: "welcome", proto: 1, engine: "test", bundle: "sha256:b", config: { module_state: "warm", heartbeat_ms: 60000, max_message: 1 << 20 } });
  const seen: Record<string, string | undefined> = {};
  const submit = (tenant: string) =>
    store.run(tenant, () =>
      runtime.submit("flow", "f/x", "{}", {}, () => {
        seen[tenant] = store.getStore();
      }),
    );
  const a = submit("tenant-a");
  const b = submit("tenant-b");
  const refused = submit("tenant-refused");
  send({ t: "run_done", id: a.id, status: "failure" });
  send({ t: "run_done", id: b.id, status: "success", result: 1 });
  send({ t: "run_done", id: refused.id, error: { code: "draining", message: "m" } });
  await Promise.all([a.done, b.done, refused.done.catch(() => {})]);
  send({ t: "run_settled", id: b.id });
  send({ t: "run_settled", id: a.id });
  send({ t: "run_settled", id: refused.id });
  expect(seen).toEqual({ "tenant-a": "tenant-a", "tenant-b": "tenant-b" });
  runtime.onClose();
});

// A run's own selectors, its write-capable sites (H3) and its halts (H8),
// go in its run frame as the host gave them, for the engine to check.
test("a run frame carries the run's write-capable selectors and halts", async () => {
  const { Runtime } = await import("../runtime/runtime.ts");
  const { DEFAULT_MAX_MESSAGE, decodeAll, encode, frame, FrameReader, MAX_FRAME } = await import("../runtime/frame.ts");
  const reader = new FrameReader(MAX_FRAME);
  const sent: Record<string, unknown>[] = [];
  const write = (b: Uint8Array) => {
    sent.push(...(decodeAll(reader.push(b), DEFAULT_MAX_MESSAGE) as Record<string, unknown>[]));
    return b.length;
  };
  const runtime = new Runtime({ bundleDir: "/nonexistent", bundleDigest: "sha256:b", host: true, transport: { write, end: () => {} }, exit: () => {} });
  runtime.start();
  const send = (m: Record<string, unknown>) => runtime.onData(frame(encode(m as { t: string })));
  send({ t: "welcome", proto: 1, engine: "test", bundle: "sha256:b", config: { module_state: "warm", heartbeat_ms: 60000, max_message: 1 << 20 } });
  const flow = "f/x";
  const fields = {
    write_capable: [{ flow, module: "w" }],
    halt_before: [{ flow, module: "b" }],
    halt_after: [{ flow, module: "a", occurrence: 1 }],
  };
  const r = runtime.submit("flow", flow, "{}", fields);
  for (let i = 0; i < 100 && !sent.some((m) => m.t === "run"); i++) await Bun.sleep(1);
  const run = sent.find((m) => m.t === "run");
  expect(run).toMatchObject({ id: r.id, path: flow, ...fields });
  send({ t: "run_done", id: r.id, status: "halted" });
  expect((await r.done).status).toBe("halted");
  runtime.onClose();
});

// The collect trigger, on hosts connected to test/heartbeatengine.ts, which
// welcomes them with a heartbeat of 10 ms and answers their runs when the
// test says. Each test replaces Bun.gc with a stand-in that records its
// calls, and counts the host's heartbeats with a retired-entry count that
// never trips.
describe("the collect trigger", () => {
  const realGc = Bun.gc;
  let gcCalls: { at: number; beat: number; arg: unknown }[] = [];
  let gcStandIn: () => void = () => {};
  let beats = 0;
  beforeEach(() => {
    gcCalls = [];
    gcStandIn = () => {};
    beats = 0;
    Bun.gc = ((arg: unknown) => {
      gcCalls.push({ at: performance.now(), beat: beats, arg });
      gcStandIn();
      return 0;
    }) as typeof Bun.gc;
  });
  // Every host a test connected, closed after it even when it failed, so
  // that its heartbeats reach no later test.
  const hosts: Host[] = [];
  afterEach(async () => {
    for (const host of hosts.splice(0)) await host.close();
    Bun.gc = realGc;
  });

  const beatCounter = { max: Number.MAX_SAFE_INTEGER, count: () => ++beats };
  async function heartbeatHost(opts: Partial<ConnectOptions>, heartbeatMs = 10) {
    const dir = tempDir("wl-host-collect-");
    writeFileSync(join(dir, "manifest.json"), "{}");
    const diags: string[] = [];
    const back = process.cwd();
    process.chdir(dir);
    process.env.WL_TEST_HEARTBEAT_MS = String(heartbeatMs);
    try {
      const host = await connect({
        engine: process.execPath,
        engineArgs: [join(import.meta.dir, "heartbeatengine.ts")],
        bundle: dir,
        workdir: dir,
        diag: (line) => diags.push(line),
        ...opts,
      });
      hosts.push(host);
      return { host, diags, dir };
    } finally {
      delete process.env.WL_TEST_HEARTBEAT_MS;
      process.chdir(back);
    }
  }
  // Within the tests' timeout of 20 s.
  async function until(what: string, cond: () => boolean) {
    for (const deadline = performance.now() + 10_000; !cond(); await Bun.sleep(5)) {
      if (performance.now() > deadline) throw new Error(`timed out waiting for ${what}`);
    }
  }
  const none = { count: 0, lastMs: null, totalMs: 0, due: 0, deferredMs: 0, maxDeferredMs: 0, dueNow: false };

  test("is off by default: heartbeats make no collection", async () => {
    const { host } = await heartbeatHost({ recycle: { retired: beatCounter } });
    await until("ten heartbeats", () => beats >= 10);
    expect(gcCalls).toEqual([]);
    expect(host.health().collections).toEqual(none);
    await host.close();
  }, 20_000);

  // Each heartbeat's ping reads the resident set once; the runtime calls a
  // heartbeat observer through invocations.exit. With no trigger set, the
  // heartbeats run and no observer is called.
  test("with no trigger set, heartbeats call no observer", async () => {
    const { invocations } = await import("../runtime/context.ts");
    const realRss = process.memoryUsage.rss;
    const realExit = invocations.exit;
    let rssReads = 0;
    let exits = 0;
    process.memoryUsage.rss = () => (rssReads++, realRss());
    invocations.exit = ((fn: (...args: unknown[]) => unknown, ...args: unknown[]) => (exits++, realExit.call(invocations, fn, ...args))) as typeof invocations.exit;
    try {
      const { host } = await heartbeatHost({});
      rssReads = 0;
      exits = 0;
      await until("ten heartbeats", () => rssReads >= 10);
      expect(exits).toBe(0);
      expect(gcCalls).toEqual([]);
      await host.close();
    } finally {
      process.memoryUsage.rss = realRss;
      invocations.exit = realExit;
    }
  }, 20_000);

  test("makes no collection while the resident set is below the mark", async () => {
    const { host } = await heartbeatHost({ recycle: { retired: beatCounter }, collect: { rss: Number.MAX_SAFE_INTEGER } });
    await until("ten heartbeats", () => beats >= 10);
    expect(gcCalls).toEqual([]);
    expect(host.health().collections).toEqual(none);
    await host.close();
  }, 20_000);

  test("at the mark, makes one full collection, then none within the cooldown, and reports its duration", async () => {
    gcStandIn = () => Bun.sleepSync(20);
    const { host } = await heartbeatHost({ recycle: { retired: beatCounter }, collect: { rss: 1, cooldown: 60_000 } });
    await until("ten heartbeats after the first collection", () => gcCalls.length > 0 && beats >= gcCalls[0].beat + 10);
    expect(gcCalls.map((c) => c.arg)).toEqual([true]);
    const { count, lastMs, totalMs } = host.health().collections;
    expect(count).toBe(1);
    expect(lastMs).toBeGreaterThanOrEqual(20);
    expect(totalMs).toBe(lastMs!);
    await host.close();
  }, 20_000);

  test("works without any recycle trigger", async () => {
    const { host } = await heartbeatHost({ collect: { rss: 1, cooldown: 60_000 } });
    await until("a collection", () => gcCalls.length > 0);
    expect(host.health().collections.count).toBe(1);
    expect(host.health().recycle).toBeNull();
    await host.close();
  }, 20_000);

  test("collects again once the cooldown has passed, never within it", async () => {
    const cooldown = 100;
    const { host } = await heartbeatHost({ recycle: { retired: beatCounter }, collect: { rss: 1, cooldown } });
    await until("three collections", () => gcCalls.length >= 3);
    for (let i = 1; i < gcCalls.length; i++) {
      expect(gcCalls[i].at - gcCalls[i - 1].at).toBeGreaterThanOrEqual(cooldown);
      // Heartbeats came in the cooldown and made no collection.
      expect(gcCalls[i].beat - gcCalls[i - 1].beat).toBeGreaterThanOrEqual(2);
    }
    expect(host.health().collections.count).toBe(gcCalls.length);
    await host.close();
  }, 20_000);

  test("leaves the recycle triggers as they are: rss trips on its own samples, and collections go on after it", async () => {
    const reasons: string[] = [];
    const { host } = await heartbeatHost({
      recycle: { rss: 1, samples: 3, retired: beatCounter },
      onRecycle: (r) => reasons.push(r),
      collect: { rss: 1, cooldown: 0 },
    });
    await until("five heartbeats", () => beats >= 5);
    expect(reasons).toEqual(["rss"]);
    expect(host.health().recycle).toBe("rss");
    const after = gcCalls.length;
    await until("a collection after the trip", () => gcCalls.length > after);
    expect(reasons).toEqual(["rss"]);
    await host.close();
  }, 20_000);

  test("a collection never trips a recycle trigger", async () => {
    const { host } = await heartbeatHost({ recycle: { rss: Number.MAX_SAFE_INTEGER, modules: 0, retired: beatCounter }, collect: { rss: 1, cooldown: 0 } });
    await until("five collections", () => gcCalls.length >= 5);
    expect(host.health().recycle).toBeNull();
    await host.close();
  }, 20_000);

  test("a collection that throws is reported, and the heartbeat and the trigger go on", async () => {
    let failed = false;
    gcStandIn = () => {
      if (!failed) {
        failed = true;
        throw new Error("collection refused");
      }
    };
    const { host, diags } = await heartbeatHost({ recycle: { retired: beatCounter }, collect: { rss: 1, cooldown: 50 } });
    await until("a second collection", () => gcCalls.length >= 2);
    const beatsThen = beats;
    await until("more heartbeats", () => beats >= beatsThen + 3);
    expect(diags.filter((l) => l.includes("collect trigger"))).toEqual(["windmill-lite: the collect trigger's collection failed: collection refused"]);
    expect(host.health().collections.count).toBe(gcCalls.length - 1);
    await host.close();
  }, 20_000);

  test("connect refuses a mark that is not a positive number of bytes", async () => {
    for (const collect of [{ rss: Number.NaN }, { rss: 0 }, { rss: 1, cooldown: -1 }] as ConnectOptions["collect"][]) {
      const dir = tempDir("wl-host-collect-");
      const back = process.cwd();
      process.chdir(dir);
      try {
        expect(await code(connect({ engine: join(dir, "no-such-engine"), bundle: dir, workdir: dir, collect }))).toBe("options");
      } finally {
        process.chdir(back);
      }
    }
  }, 20_000);

  // The "idle" policy. The stand-in engine answers a held run once the
  // test has made the run's file, so the test decides when the host has a
  // run open.
  const whenIdle = (o: Partial<NonNullable<ConnectOptions["collect"]>> = {}) => ({ rss: 1, cooldown: 60_000, policy: "idle" as const, ...o });
  function heldRun(host: Host, dir: string, name: string) {
    const file = join(dir, `held-${name}`);
    const done = host.run("f/held", { hold: file });
    // A test that fails before the release leaves the run open; closing
    // its host then ends it with an error that belongs to no later test.
    done.catch(() => {});
    return { done, release: () => writeFileSync(file, "") };
  }
  // A timer can fire up to about 1 ms before its delay has passed on the
  // clock that performance.now() reads.
  const early = 5;

  test("the idle policy: a collection due while a run is open waits for the run's end and the quiet period, then runs once", async () => {
    const { host, dir } = await heartbeatHost({ recycle: { retired: beatCounter }, collect: whenIdle() });
    const run = heldRun(host, dir, "a");
    await until("the collection due", () => host.health().collections.dueNow);
    const dueSeen = performance.now();
    const beatsThen = beats;
    await until("ten more heartbeats", () => beats >= beatsThen + 10);
    expect(gcCalls).toEqual([]);
    expect(host.health().runs).toBe(1);
    const released = performance.now();
    run.release();
    await run.done;
    await until("the collection", () => gcCalls.length > 0);
    expect(gcCalls[0].at - released).toBeGreaterThanOrEqual(DEFAULT_COLLECT_QUIET_MS - early);
    const beatsAfter = beats;
    await until("ten more heartbeats", () => beats >= beatsAfter + 10);
    expect(gcCalls.map((c) => c.arg)).toEqual([true]);
    const c = host.health().collections;
    expect(c).toMatchObject({ count: 1, due: 1, dueNow: false });
    expect(c.maxDeferredMs).toBe(c.deferredMs);
    expect(c.deferredMs).toBeGreaterThanOrEqual(released - dueSeen + DEFAULT_COLLECT_QUIET_MS - early);
    await host.close();
  }, 20_000);

  test("the idle policy: the caller of the run that left the host idle has its outcome before the collection starts", async () => {
    const { host, dir } = await heartbeatHost({ collect: whenIdle({ quiet: 0 }) });
    const run = heldRun(host, dir, "a");
    await until("the collection due", () => host.health().collections.dueNow);
    run.release();
    await run.done;
    const atOutcome = gcCalls.length;
    await until("the collection", () => gcCalls.length > 0);
    expect(atOutcome).toBe(0);
    await host.close();
  }, 20_000);

  test("the idle policy: a run submitted in the quiet period keeps the collection due until that run has ended", async () => {
    const quiet = 200;
    const { host, dir } = await heartbeatHost({ collect: whenIdle({ quiet }) });
    const a = heldRun(host, dir, "a");
    await until("the collection due", () => host.health().collections.dueNow);
    a.release();
    await a.done;
    const b = heldRun(host, dir, "b");
    // Past the quiet period that a's end started.
    await Bun.sleep(quiet + 100);
    expect(gcCalls).toEqual([]);
    expect(host.health().collections.dueNow).toBe(true);
    const released = performance.now();
    b.release();
    await b.done;
    await until("the collection", () => gcCalls.length > 0);
    expect(gcCalls[0].at - released).toBeGreaterThanOrEqual(quiet - early);
    expect(host.health().collections).toMatchObject({ count: 1, due: 1, dueNow: false });
    await host.close();
  }, 20_000);

  test("the idle policy: a run that ends in the quiet period starts the quiet period again", async () => {
    const quiet = 200;
    const { host, dir } = await heartbeatHost({ collect: whenIdle({ quiet }) });
    const a = heldRun(host, dir, "a");
    await until("the collection due", () => host.health().collections.dueNow);
    a.release();
    await a.done;
    const b = heldRun(host, dir, "b");
    await Bun.sleep(quiet / 2);
    const released = performance.now();
    b.release();
    await b.done;
    await until("the collection", () => gcCalls.length > 0);
    expect(gcCalls[0].at - released).toBeGreaterThanOrEqual(quiet - early);
    await host.close();
  }, 20_000);

  test("the idle policy: once the quiet period after the run's end has passed, the collection runs between heartbeats", async () => {
    const { host, dir } = await heartbeatHost({ recycle: { retired: beatCounter }, collect: whenIdle({ quiet: 50 }) }, 500);
    const run = heldRun(host, dir, "a");
    await until("the collection due", () => host.health().collections.dueNow);
    const beatAtRelease = beats;
    run.release();
    await run.done;
    await until("the collection", () => gcCalls.length > 0);
    // No heartbeat came between the run's end and the collection.
    expect(gcCalls[0].beat).toBe(beatAtRelease);
    await host.close();
  }, 20_000);

  test("the idle policy: the collection runs outside every invocation, even for a run submitted inside one", async () => {
    const { invocations } = await import("../runtime/context.ts");
    const stores: unknown[] = [];
    gcStandIn = () => stores.push(invocations.getStore());
    // Heartbeats 500 ms apart: the check after the run's end collects.
    const { host, dir } = await heartbeatHost({ collect: whenIdle({ quiet: 0 }) }, 500);
    const run = invocations.run({ id: "an invocation" } as never, () => heldRun(host, dir, "a"));
    await until("the collection due", () => host.health().collections.dueNow);
    run.release();
    await run.done;
    await until("the collection", () => gcCalls.length > 0);
    expect(stores).toEqual([undefined]);
    await host.close();
  }, 20_000);

  test("the idle policy: at a heartbeat, once the host has been idle for the quiet period, the collection runs there", async () => {
    const quiet = 300;
    const before = performance.now();
    const { host } = await heartbeatHost({ collect: whenIdle({ quiet }) });
    await until("the collection", () => gcCalls.length > 0);
    // No run was ever open: the host has been idle since connect.
    expect(gcCalls[0].at - before).toBeGreaterThanOrEqual(quiet);
    const c = host.health().collections;
    expect(c).toMatchObject({ count: 1, due: 1, dueNow: false });
    expect(c.maxDeferredMs).toBeGreaterThan(0);
    await host.close();
  }, 20_000);

  test("the idle policy: a step still running after its run's outcome keeps the collection due", async () => {
    const { host, dir } = await heartbeatHost({ recycle: { retired: beatCounter }, collect: whenIdle() });
    const held = heldRun(host, dir, "a");
    await until("the collection due", () => host.health().collections.dueNow);
    const release = join(dir, "step-release");
    mkdirSync(join(dir, "scripts", "bun", "u"), { recursive: true });
    writeFileSync(join(dir, "scripts", "bun", "u", "hold.meta.json"), JSON.stringify({ signatures: { main: { params: [] } } }));
    writeFileSync(
      join(dir, "scripts", "bun", "u", "hold.mjs"),
      `import { existsSync } from "node:fs";\nexport async function main() {\n  while (!existsSync(${JSON.stringify(release)})) await new Promise((r) => setTimeout(r, 2));\n  return 1;\n}\n`,
    );
    // The stand-in engine sends the step's invoke, then the run's outcome.
    await host.run("f/step", { invoke: "u/hold" });
    // The held run's end leaves no run open, with the step running.
    held.release();
    await held.done;
    expect(host.health()).toMatchObject({ runs: 0, in_flight: 1 });
    const beatsThen = beats;
    await until("twenty more heartbeats", () => beats >= beatsThen + 20);
    expect(gcCalls).toEqual([]);
    expect(host.health().collections.dueNow).toBe(true);
    writeFileSync(release, "");
    await until("the collection", () => gcCalls.length > 0);
    expect(host.health()).toMatchObject({ runs: 0, in_flight: 0 });
    expect(host.health().collections).toMatchObject({ count: 1, due: 1, dueNow: false });
    await host.close();
  }, 20_000);

  test("the idle policy: below the mark, no collection becomes due", async () => {
    const { host } = await heartbeatHost({ recycle: { retired: beatCounter }, collect: whenIdle({ rss: Number.MAX_SAFE_INTEGER, quiet: 0 }) });
    await until("ten heartbeats", () => beats >= 10);
    expect(gcCalls).toEqual([]);
    expect(host.health().collections).toEqual(none);
    await host.close();
  }, 20_000);

  test("the idle policy: the cooldown runs from the end of a deferred collection", async () => {
    const cooldown = 300;
    const { host, dir } = await heartbeatHost({ collect: whenIdle({ cooldown }) });
    const run = heldRun(host, dir, "a");
    await until("the collection due", () => host.health().collections.dueNow);
    run.release();
    await run.done;
    await until("two collections", () => gcCalls.length >= 2);
    expect(gcCalls[1].at - gcCalls[0].at).toBeGreaterThanOrEqual(cooldown);
    expect(host.health().collections).toMatchObject({ count: 2, due: 2 });
    await host.close();
  }, 20_000);

  test("the heartbeat policy, given or by default, collects at the heartbeat while a run is open, and waits for nothing", async () => {
    for (const policy of [undefined, "heartbeat"] as const) {
      gcCalls = [];
      const { host, dir } = await heartbeatHost({ collect: { rss: 1, cooldown: 60_000, policy } });
      const run = heldRun(host, dir, String(policy));
      await until("a collection", () => gcCalls.length > 0);
      expect(host.health().runs).toBe(1);
      expect(host.health().collections).toMatchObject({ count: 1, due: 1, deferredMs: 0, maxDeferredMs: 0, dueNow: false });
      run.release();
      await run.done;
      await host.close();
    }
  }, 20_000);

  test("the idle policy: a collection that throws is reported, is no longer due, and the trigger goes on after the cooldown", async () => {
    let failed = false;
    gcStandIn = () => {
      if (!failed) {
        failed = true;
        throw new Error("collection refused");
      }
    };
    const cooldown = 50;
    const { host, dir, diags } = await heartbeatHost({ recycle: { retired: beatCounter }, collect: whenIdle({ cooldown }) });
    const run = heldRun(host, dir, "a");
    await until("the collection due", () => host.health().collections.dueNow);
    run.release();
    await run.done;
    await until("a second collection", () => gcCalls.length >= 2);
    const beatsThen = beats;
    await until("more heartbeats", () => beats >= beatsThen + 3);
    expect(diags.filter((l) => l.includes("collect trigger"))).toEqual(["windmill-lite: the collect trigger's collection failed: collection refused"]);
    expect(gcCalls[1].at - gcCalls[0].at).toBeGreaterThanOrEqual(cooldown);
    const c = host.health().collections;
    expect(c.count).toBe(gcCalls.length - 1);
    expect(c.due).toBe(gcCalls.length + (c.dueNow ? 1 : 0));
    await host.close();
  }, 20_000);

  test("the idle policy: close() cancels a pending check, and no collection follows", async () => {
    const quiet = 300;
    const { host, dir } = await heartbeatHost({ collect: whenIdle({ quiet }) });
    const run = heldRun(host, dir, "a");
    await until("the collection due", () => host.health().collections.dueNow);
    run.release();
    await run.done;
    await host.close();
    await Bun.sleep(quiet + 200);
    expect(gcCalls).toEqual([]);
  }, 20_000);

  test("the idle policy: close() with a run open ends the run, and no collection follows", async () => {
    const quiet = 50;
    const { host, dir } = await heartbeatHost({ collect: whenIdle({ quiet }) });
    const run = heldRun(host, dir, "a");
    await until("the collection due", () => host.health().collections.dueNow);
    await host.close();
    expect(await code(run.done)).toContain("the engine connection ended");
    await Bun.sleep(quiet + 200);
    expect(gcCalls).toEqual([]);
  }, 20_000);

  // In a process of its own: once the engine is gone, only the idle
  // policy's pending check is left, with a quiet period of 60 s.
  test("the idle policy: a pending check keeps no process alive", async () => {
    const dir = tempDir("wl-host-collect-");
    writeFileSync(join(dir, "manifest.json"), "{}");
    const script = join(dir, "host.ts");
    writeFileSync(
      script,
      [
        `import { writeFileSync } from "node:fs";`,
        `import { connect } from ${JSON.stringify(join(import.meta.dir, "..", "host", "index.ts"))};`,
        `Bun.gc = () => (console.log("collected"), 0);`,
        `const dir = process.cwd();`,
        `const host = await connect({ engine: process.execPath, engineArgs: [${JSON.stringify(join(import.meta.dir, "heartbeatengine.ts"))}], bundle: dir, workdir: dir, collect: { rss: 1, cooldown: 60_000, policy: "idle", quiet: 60_000 }, onEngineExit: () => console.log("engine exited") });`,
        `const run = host.run("f/held", { hold: dir + "/held" });`,
        `while (!host.health().collections.dueNow) await Bun.sleep(5);`,
        `writeFileSync(dir + "/held", "");`,
        `await run;`,
        `process.kill(host.enginePid, "SIGKILL");`,
        `console.log("armed");`,
      ].join("\n"),
    );
    const child = Bun.spawn([process.execPath, script], { cwd: dir, env: { ...process.env, WL_TEST_HEARTBEAT_MS: "10" }, stdout: "pipe", stderr: "inherit" });
    const timer = setTimeout(() => child.kill(), 10_000);
    const code = await child.exited;
    clearTimeout(timer);
    expect(code).toBe(0);
    expect((await new Response(child.stdout).text()).trim().split("\n")).toEqual(["armed", "engine exited"]);
  }, 20_000);

  test("the idle policy leaves the recycle triggers as they are, and a collection never trips one", async () => {
    const reasons: string[] = [];
    const { host } = await heartbeatHost({
      recycle: { rss: 1, samples: 3, retired: beatCounter },
      onRecycle: (r) => reasons.push(r),
      collect: whenIdle({ cooldown: 0, quiet: 0 }),
    });
    await until("five heartbeats", () => beats >= 5);
    expect(reasons).toEqual(["rss"]);
    const after = gcCalls.length;
    await until("a collection after the trip", () => gcCalls.length > after);
    expect(reasons).toEqual(["rss"]);
    await host.close();
    const second = await heartbeatHost({ recycle: { rss: Number.MAX_SAFE_INTEGER, modules: 0, retired: beatCounter }, collect: whenIdle({ cooldown: 0, quiet: 0 }) });
    gcCalls = [];
    await until("five collections", () => gcCalls.length >= 5);
    expect(second.host.health().recycle).toBeNull();
    await second.host.close();
  }, 20_000);

  test("connect refuses an unknown policy and a quiet period that is not 0 ms or more", async () => {
    for (const collect of [{ rss: 1, policy: "never" }, { rss: 1, policy: "idle", quiet: -1 }, { rss: 1, policy: "idle", quiet: Number.NaN }, { rss: 1, quiet: Number.POSITIVE_INFINITY }] as ConnectOptions["collect"][]) {
      const dir = tempDir("wl-host-collect-");
      const back = process.cwd();
      process.chdir(dir);
      try {
        expect(await code(connect({ engine: join(dir, "no-such-engine"), bundle: dir, workdir: dir, collect }))).toBe("options");
      } finally {
        process.chdir(back);
      }
    }
  }, 20_000);
});

// The runtime calls the heartbeat observer, where the SDK's triggers run,
// once the heartbeat's ping has gone to the transport.
test("the heartbeat observer runs after the heartbeat's ping is written", async () => {
  const { Runtime } = await import("../runtime/runtime.ts");
  const { DEFAULT_MAX_MESSAGE, decodeAll, encode, frame, FrameReader, MAX_FRAME } = await import("../runtime/frame.ts");
  const reader = new FrameReader(MAX_FRAME);
  const sent: Record<string, unknown>[] = [];
  const write = (b: Uint8Array) => {
    sent.push(...(decodeAll(reader.push(b), DEFAULT_MAX_MESSAGE) as Record<string, unknown>[]));
    return b.length;
  };
  const seen: unknown[] = [];
  const runtime = new Runtime({
    bundleDir: "/nonexistent",
    bundleDigest: "sha256:b",
    host: true,
    transport: { write, end: () => {} },
    exit: () => {},
    onHeartbeat: () => seen.push(sent.at(-1)?.t === "ping" && sent.filter((m) => m.t === "ping").length),
  });
  runtime.start();
  runtime.onData(frame(encode({ t: "welcome", proto: 1, engine: "test", bundle: "sha256:b", config: { module_state: "warm", heartbeat_ms: 10, max_message: 1 << 20 } })));
  for (let i = 0; i < 1000 && seen.length < 3; i++) await Bun.sleep(5);
  expect(seen.slice(0, 3)).toEqual([1, 2, 3]);
  runtime.onClose();
});
