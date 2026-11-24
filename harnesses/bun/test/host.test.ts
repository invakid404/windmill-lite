// Developer checks of the host SDK's connect options (host/index.ts). The
// host-mode gate runs in cmd/windmill-lite against a compiled host.

import { afterAll, afterEach, beforeEach, describe, expect, test } from "bun:test";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { checkWorkdir, type ConnectOptions, connect, DEFAULT_COLLECT_POLICY, DEFAULT_COLLECT_QUIET_MS, type Host, HostError, type RunOptions } from "../host/index.ts";
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
// welcome from an engine of another release (ADR 0009).
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

// A bundle directory whose manifest names a plugin of its own, at file, and
// records digest for it (by default, the digest of source), with source
// written at file when file is under the directory.
function pluginBundle(source: string, o: { file?: string; digest?: string; listed?: boolean } = {}) {
  const dir = tempDir("wl-host-plugin-");
  const file = o.file ?? "plugins/f/app/plugin.mjs";
  const digest = o.digest ?? "sha256:" + new Bun.CryptoHasher("sha256").update(source).digest("hex");
  mkdirSync(join(dir, file, ".."), { recursive: true });
  writeFileSync(join(dir, file), source);
  const files = o.listed === false ? {} : { [file]: digest };
  writeFileSync(join(dir, "manifest.json"), JSON.stringify({ host_modules: [], plugin: { key: "f/app/plugin.ts", file }, files }));
  return dir;
}

// connect() in dir, which is also the working directory.
async function connectIn(dir: string, o: Partial<ConnectOptions>): Promise<string | undefined> {
  const back = process.cwd();
  process.chdir(dir);
  try {
    return await code(connect({ engine: join(dir, "no-such-engine"), bundle: dir, workdir: dir, diag: () => {}, ...o }));
  } finally {
    process.chdir(back);
  }
}

test("connect refuses a plugin option for a bundle with a plugin of its own, before it starts the engine", async () => {
  const dir = pluginBundle("export default {};\n");
  expect(await connectIn(dir, { plugin: {} })).toBe("options");
  // Without the option, connect gets as far as starting the engine.
  expect(await connectIn(dir, {})).toBe("engine_start");
}, 20_000);

test("connect verifies the bundle's plugin before it runs any of it", async () => {
  const marker = join(tempDir("wl-host-marker-"), "ran");
  const changed = `require("node:fs").writeFileSync(${JSON.stringify(marker)}, "ran");\nexport default {};\n`;
  const built = "sha256:" + new Bun.CryptoHasher("sha256").update("export default {};\n").digest("hex");
  // Changed since the build: its digest is not the manifest's.
  expect(await connectIn(pluginBundle(changed, { digest: built }), {})).toBe("bundle");
  // Not a module of plugins/ that the manifest lists.
  expect(await connectIn(pluginBundle(changed, { listed: false }), {})).toBe("bundle");
  expect(await connectIn(pluginBundle(changed, { file: "scripts/plugin.mjs" }), {})).toBe("bundle");
  expect(await connectIn(pluginBundle(changed, { file: "plugins/../plugin.mjs" }), {})).toBe("bundle");
  expect(existsSync(marker)).toBe(false);
}, 20_000);

test("connect rejects at once when the engine ends while the bundle's plugin loads, and sends no hello", async () => {
  // The plugin's evaluation never ends; the engine exits without a hello.
  const dir = pluginBundle("await new Promise(() => {});\nexport default {};\n");
  const started = performance.now();
  const got = await Promise.race([
    connectIn(dir, { engine: "/bin/sh", engineArgs: ["-c", "exit 0"] }),
    Bun.sleep(5_000).then(() => "still pending"),
  ]);
  expect(got).toBe("rejected");
  expect(performance.now() - started).toBeLessThan(5_000);
}, 20_000);

test("a bundle's plugin that throws a value that is not an Error rejects connect with bundle", async () => {
  for (const thrown of ["undefined", "null", "42"]) {
    const dir = pluginBundle(`throw ${thrown};\nexport default {};\n`);
    expect(await connectIn(dir, { engine: "/bin/sh", engineArgs: ["-c", "exec sleep 30"] })).toBe("bundle");
  }
}, 20_000);

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

// foreignWindmillOrigins: the engine is started with one -foreign-windmill
// flag for each origin, in their order, and without the flag when the
// option is left out. A value that is not an array of strings, and an
// origin with user information, a query or a fragment, are refused before
// the engine starts, without repeating the value. The engine checks each
// origin itself (TestHostForeignWindmillLinks in cmd/windmill-lite runs the
// real one).
test("foreignWindmillOrigins passes each origin to the engine as a -foreign-windmill flag", async () => {
  const dir = tempDir("wl-host-foreign-");
  writeFileSync(join(dir, "manifest.json"), "{}");
  const file = join(dir, "args.json");
  const back = process.cwd();
  process.chdir(dir);
  process.env.WL_TEST_ARGS_FILE = file;
  try {
    const start = (o: Partial<ConnectOptions>) =>
      code(connect({ engine: process.execPath, engineArgs: [join(import.meta.dir, "argsengine.ts")], bundle: dir, workdir: dir, diag: () => {}, ...o }));
    for (const [o, want] of [
      [{ foreignWindmillOrigins: ["https://windmill.example.com", "http://127.0.0.1:8000"] }, ["-foreign-windmill", "https://windmill.example.com", "-foreign-windmill", "http://127.0.0.1:8000"]],
      [{ foreignWindmillOrigins: [] }, []],
      [{}, []],
    ] as [Partial<ConnectOptions>, string[]][]) {
      rmSync(file, { force: true });
      expect(await start(o)).toBe("rejected");
      const args = JSON.parse(await Bun.file(file).text()) as string[];
      expect(args.flatMap((a, i) => (a === "-foreign-windmill" ? [a, args[i + 1]] : []))).toEqual(want);
    }
    // The flags are the values that connect() checked: a change that the
    // caller makes to its array while connect() is pending reaches nothing.
    rmSync(file, { force: true });
    const shared = ["https://windmill.example.com"];
    const pending = start({ foreignWindmillOrigins: shared });
    shared[0] = "https://user:s3cr3t-marker@windmill.example.com";
    shared.push("https://other.example");
    expect(await pending).toBe("rejected");
    const launched = JSON.parse(await Bun.file(file).text()) as string[];
    expect(launched.flatMap((a, i) => (a === "-foreign-windmill" ? [a, launched[i + 1]] : []))).toEqual(["-foreign-windmill", "https://windmill.example.com"]);
    // A value that could hold a secret never reaches the engine's command
    // line, and no message repeats it.
    const marker = "s3cr3t-marker";
    for (const bad of [
      "https://windmill.example.com",
      [1],
      { origin: "https://windmill.example.com" },
      null,
      [`https://user:${marker}@windmill.example.com`],
      ["https://windmill.example.com", `https://windmill.example.com/?token=${marker}`],
      [`https://windmill.example.com/#${marker}`],
      [marker, null],
    ]) {
      rmSync(file, { force: true });
      let message = "";
      const got = await connect({
        engine: process.execPath,
        engineArgs: [join(import.meta.dir, "argsengine.ts")],
        bundle: dir,
        workdir: dir,
        diag: () => {},
        foreignWindmillOrigins: bad as unknown as string[],
      }).then(
        () => "connected",
        (e) => {
          message = (e as Error).message;
          return (e as { code?: string }).code;
        },
      );
      expect(got).toBe("options");
      expect(message.includes(marker)).toBe(false);
      expect(await Bun.file(file).exists()).toBe(false);
    }
  } finally {
    delete process.env.WL_TEST_ARGS_FILE;
    process.chdir(back);
  }
});

// resourceStore: the engine is started with -resource-store and the kind,
// and without the flag when the option is left out; kind "redis" adds
// -redis-prefix and -redis-allow-eviction when given, and passes the URL
// as WL_REDIS_URL in the engine's environment, never in its arguments; any
// other kind, and a redis store without a URL, is refused before the
// engine starts. The cases run with WL_REDIS_URL unset in this process and
// set to another synthetic URL, as a shell may have it: a redis store's URL
// replaces it, and memory leaves it as it is. The stand-in engine reports
// only whether the URL matches, so no URL is written or printed.
test("resourceStore passes the store to the engine, the Redis URL in its environment only", async () => {
  const dir = tempDir("wl-host-store-");
  writeFileSync(join(dir, "manifest.json"), "{}");
  const file = join(dir, "args.json");
  const envFile = join(dir, "env.json");
  const back = process.cwd();
  const ambient = process.env.WL_REDIS_URL;
  process.chdir(dir);
  process.env.WL_TEST_ARGS_FILE = file;
  process.env.WL_TEST_ENV_FILE = envFile;
  const url = "redis://user:s3cr3t-marker@redis.invalid:6379/2";
  const shell = "redis://shell:s3cr3t-shell@redis.invalid:6379/3";
  try {
    const start = (o: Partial<ConnectOptions>) =>
      code(connect({ engine: process.execPath, engineArgs: [join(import.meta.dir, "argsengine.ts")], bundle: dir, workdir: dir, diag: () => {}, ...o }));
    for (const inShell of [undefined, shell]) {
      if (inShell === undefined) delete process.env.WL_REDIS_URL;
      else process.env.WL_REDIS_URL = inShell;
      for (const [o, want, wantURL] of [
        [{ resourceStore: { kind: "memory" } }, ["-resource-store", "memory"], inShell],
        [{}, [], inShell],
        [{ resourceStore: { kind: "redis", url } }, ["-resource-store", "redis"], url],
        [
          { resourceStore: { kind: "redis", url, prefix: "app-1:", allowEviction: true } },
          ["-resource-store", "redis", "-redis-prefix", "app-1:", "-redis-allow-eviction"],
          url,
        ],
        [{ resourceStore: { kind: "redis", url, allowEviction: false } }, ["-resource-store", "redis"], url],
      ] as [Partial<ConnectOptions>, string[], string | undefined][]) {
        rmSync(file, { force: true });
        rmSync(envFile, { force: true });
        if (wantURL === undefined) delete process.env.WL_TEST_EXPECT_URL;
        else process.env.WL_TEST_EXPECT_URL = wantURL;
        expect(await start(o)).toBe("rejected");
        const args = JSON.parse(await Bun.file(file).text()) as string[];
        const at = args.indexOf("-resource-store");
        expect(at < 0 ? [] : args.slice(at, at + want.length)).toEqual(want);
        expect(args.some((a) => a.includes("s3cr3t"))).toBe(false);
        expect(JSON.parse(await Bun.file(envFile).text())).toEqual({ set: wantURL !== undefined, expected: wantURL !== undefined });
      }
    }
    for (const bad of [
      { kind: "postgres" },
      { kind: "redis" },
      { kind: "redis", url: "" },
      { kind: "redis", url, prefix: 1 },
      { kind: "redis", url, allowEviction: "yes" },
    ]) {
      rmSync(file, { force: true });
      let message = "";
      const got = await connect({
        engine: process.execPath,
        engineArgs: [join(import.meta.dir, "argsengine.ts")],
        bundle: dir,
        workdir: dir,
        diag: () => {},
        resourceStore: bad as unknown as ConnectOptions["resourceStore"],
      }).then(
        () => "connected",
        (e) => {
          message = (e as Error).message;
          return (e as { code?: string }).code;
        },
      );
      expect(got).toBe("options");
      expect(message.includes("s3cr3t")).toBe(false);
      expect(await Bun.file(file).exists()).toBe(false);
    }
  } finally {
    delete process.env.WL_TEST_ARGS_FILE;
    delete process.env.WL_TEST_ENV_FILE;
    delete process.env.WL_TEST_EXPECT_URL;
    if (ambient === undefined) delete process.env.WL_REDIS_URL;
    else process.env.WL_REDIS_URL = ambient;
    process.chdir(back);
  }
});

// A run's onSettled runs in the scope the run was submitted in (H11),
// outside every invocation, after its run_done has removed it from the runs
// in flight; concurrent runs keep their own.
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

// A run's deadlines go in its run frame rounded up to whole milliseconds
// and at least 0, a deadline that has passed, which ends that run alone;
// absent, the frame has none. A deadline that the frame cannot carry, which
// the engine would decode as a broken frame and end the connection, refuses
// the run before any frame is sent. On a host connected to
// test/heartbeatengine.ts, which echoes each run frame's deadlines and
// counts the run frames, and names the cause it is told to cancel with.
test("a run's deadlines go in its run frame as whole milliseconds of at least 0, and one the frame cannot carry refuses the run without a frame", async () => {
  const dir = tempDir("wl-host-budget-");
  writeFileSync(join(dir, "manifest.json"), "{}");
  const back = process.cwd();
  process.chdir(dir);
  process.env.WL_TEST_HEARTBEAT_MS = "60000";
  let host: Host;
  try {
    host = await connect({ engine: process.execPath, engineArgs: [join(import.meta.dir, "heartbeatengine.ts")], bundle: dir, workdir: dir, diag: () => {} });
  } finally {
    delete process.env.WL_TEST_HEARTBEAT_MS;
    process.chdir(back);
  }
  try {
    for (const bad of [Number.NaN, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY, 2 ** 53, "5" as unknown as number, null as unknown as number]) {
      expect(await code(host.run("f/x", { echo: true }, { deadlineMs: bad }))).toBe("refused");
      expect(await code(host.run("f/x", { echo: true }, { preWriteDeadlineMs: bad }))).toBe("refused");
    }
    const sent = async (opts: RunOptions) => (await host.run("f/x", { echo: true }, opts)).result;
    // runs counts only the frames that reached the engine: none of the
    // refused runs sent one.
    expect(await sent({ deadlineMs: 0.2, preWriteDeadlineMs: -5 })).toEqual({ deadline_ms: 1, pre_write_deadline_ms: 0, runs: 1 });
    expect(await sent({ deadlineMs: Number.MAX_SAFE_INTEGER, preWriteDeadlineMs: 0 })).toEqual({ deadline_ms: Number.MAX_SAFE_INTEGER, pre_write_deadline_ms: 0, runs: 2 });
    expect(await sent({ deadlineMs: -0.5, preWriteDeadlineMs: 1.0000001 })).toEqual({ deadline_ms: 0, pre_write_deadline_ms: 2, runs: 3 });
    expect(await sent({})).toEqual({ deadline_ms: null, pre_write_deadline_ms: null, runs: 4 });
    // The run's outcome names the cause of its cancellation, as run_done
    // gives it.
    const r = await host.run("f/x", { canceled: "pre_write_deadline" });
    expect([r.status, r.cancelCause]).toEqual(["canceled", "pre_write_deadline"]);
    // An empty cause on the wire counts as none, as Go reads it.
    const empty = await host.run("f/x", { canceled: "" });
    expect([empty.status, empty.cancelCause]).toEqual(["canceled", undefined]);
    expect((await host.run("f/x", { echo: true })).cancelCause).toBeUndefined();
  } finally {
    await host.close();
  }
});

// The collect trigger, on hosts connected to test/heartbeatengine.ts, which
// welcomes them with a heartbeat of 10 ms, or of the interval that a test
// gives, and answers their runs when the test says. Each test replaces
// Bun.gc with a stand-in that records its calls. Some tests count the
// host's heartbeats with a retired-entry callback that never trips.
describe("the collect trigger", () => {
  const realGc = Bun.gc;
  let gcCalls: { at: number; beat: number; arg: unknown }[] = [];
  let gcStandIn: () => void = () => {};
  let beats = 0;
  let beatTimes: number[] = [];
  beforeEach(() => {
    gcCalls = [];
    gcStandIn = () => {};
    beats = 0;
    beatTimes = [];
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

  const beatCounter = { max: Number.MAX_SAFE_INTEGER, count: () => (beatTimes.push(performance.now()), ++beats) };
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
    const { host } = await heartbeatHost({ recycle: { retired: beatCounter }, collect: { rss: Number.MAX_SAFE_INTEGER, policy: "heartbeat" } });
    await until("ten heartbeats", () => beats >= 10);
    expect(gcCalls).toEqual([]);
    expect(host.health().collections).toEqual(none);
    await host.close();
  }, 20_000);

  test("at the mark, makes one full collection, then none within the cooldown, and reports its duration", async () => {
    // The resident set reads exactly the mark, which is due.
    const mark = 5_000_000;
    const realRss = process.memoryUsage.rss;
    process.memoryUsage.rss = () => mark;
    try {
      gcStandIn = () => Bun.sleepSync(20);
      const { host } = await heartbeatHost({ recycle: { retired: beatCounter }, collect: { rss: mark, cooldown: 60_000, policy: "heartbeat" } });
      await until("ten heartbeats after the first collection", () => gcCalls.length > 0 && beats >= gcCalls[0].beat + 10);
      expect(gcCalls.map((c) => c.arg)).toEqual([true]);
      const { count, lastMs, totalMs } = host.health().collections;
      expect(count).toBe(1);
      expect(lastMs).toBeGreaterThanOrEqual(20);
      expect(totalMs).toBe(lastMs!);
      await host.close();
    } finally {
      process.memoryUsage.rss = realRss;
    }
  }, 20_000);

  test("works without any recycle trigger", async () => {
    const { host } = await heartbeatHost({ collect: { rss: 1, cooldown: 60_000, policy: "heartbeat" } });
    await until("a collection", () => gcCalls.length > 0);
    expect(host.health().collections.count).toBe(1);
    expect(host.health().recycle).toBeNull();
    await host.close();
  }, 20_000);

  test("collects again once the cooldown has passed, never within it", async () => {
    const cooldown = 100;
    const { host } = await heartbeatHost({ recycle: { retired: beatCounter }, collect: { rss: 1, cooldown, policy: "heartbeat" } });
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
    // The heartbeats counted when the trigger tripped. Each heartbeat checks
    // rss before it counts, so the third heartbeat trips with two counted.
    const tripBeats: number[] = [];
    const { host } = await heartbeatHost({
      recycle: { rss: 1, samples: 3, retired: beatCounter },
      onRecycle: (r) => (reasons.push(r), tripBeats.push(beats)),
      collect: { rss: 1, cooldown: 0, policy: "heartbeat" },
    });
    await until("five heartbeats", () => beats >= 5);
    expect(reasons).toEqual(["rss"]);
    expect(tripBeats).toEqual([2]);
    expect(host.health().recycle).toBe("rss");
    const after = gcCalls.length;
    await until("a collection after the trip", () => gcCalls.length > after);
    expect(reasons).toEqual(["rss"]);
    await host.close();
  }, 20_000);

  test("a collection never trips a recycle trigger", async () => {
    const { host } = await heartbeatHost({ recycle: { rss: Number.MAX_SAFE_INTEGER, modules: 0, retired: beatCounter }, collect: { rss: 1, cooldown: 0, policy: "heartbeat" } });
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
    const { host, diags } = await heartbeatHost({ recycle: { retired: beatCounter }, collect: { rss: 1, cooldown: 50, policy: "heartbeat" } });
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

  test("the default quiet period is 100 ms", () => {
    expect(DEFAULT_COLLECT_QUIET_MS).toBe(100);
  });

  test("the idle policy: a collection due while a run is open waits for the run's end and the quiet period, then runs once", async () => {
    // quiet left out: the default, 100 ms, written here, not read from the SDK.
    const quiet = 100;
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
    expect(gcCalls[0].at - released).toBeGreaterThanOrEqual(quiet - early);
    const beatsAfter = beats;
    await until("ten more heartbeats", () => beats >= beatsAfter + 10);
    expect(gcCalls.map((c) => c.arg)).toEqual([true]);
    const c = host.health().collections;
    expect(c).toMatchObject({ count: 1, due: 1, dueNow: false });
    expect(c.maxDeferredMs).toBe(c.deferredMs);
    expect(c.deferredMs).toBeGreaterThanOrEqual(released - dueSeen + quiet - early);
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

  test("the idle policy: a host idle since connect collects once the quiet period has passed", async () => {
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

  test("the idle policy: a heartbeat that finds a collection due after the quiet period has passed collects inside that heartbeat", async () => {
    // Below the mark until the host has been idle for longer than quiet.
    const realRss = process.memoryUsage.rss;
    let rss = 10;
    process.memoryUsage.rss = () => rss;
    // inBeat is true from a heartbeat's retired count until the microtasks
    // after that heartbeat's callback: no timer's callback sees it.
    let inBeat = false;
    const inBeats: boolean[] = [];
    gcStandIn = () => inBeats.push(inBeat);
    const counter = {
      max: Number.MAX_SAFE_INTEGER,
      count: () => {
        inBeat = true;
        queueMicrotask(() => (inBeat = false));
        return beatCounter.count();
      },
    };
    try {
      const quiet = 100;
      const { host } = await heartbeatHost({ recycle: { retired: counter }, collect: whenIdle({ rss: 1000, quiet }) });
      await Bun.sleep(quiet + 50);
      expect(gcCalls).toEqual([]);
      rss = 2000;
      await until("the collection", () => gcCalls.length > 0);
      expect(inBeats).toEqual([true]);
      expect(host.health().collections).toMatchObject({ count: 1, due: 1, dueNow: false });
      await host.close();
    } finally {
      process.memoryUsage.rss = realRss;
    }
  }, 20_000);

  // A step of the bundle in dir, u/hold, that runs until the returned file
  // exists. The stand-in engine sends its invoke, then its run's outcome.
  function holdStep(dir: string): string {
    const release = join(dir, "step-release");
    mkdirSync(join(dir, "scripts", "bun", "u"), { recursive: true });
    writeFileSync(join(dir, "scripts", "bun", "u", "hold.meta.json"), JSON.stringify({ signatures: { main: { params: [] } } }));
    writeFileSync(
      join(dir, "scripts", "bun", "u", "hold.mjs"),
      `import { existsSync } from "node:fs";\nexport async function main() {\n  while (!existsSync(${JSON.stringify(release)})) await new Promise((r) => setTimeout(r, 2));\n  return 1;\n}\n`,
    );
    return release;
  }

  test("the idle policy: a step still running after its run's outcome keeps the collection due, and the quiet period starts once it has ended", async () => {
    const quiet = 100;
    const { host, dir } = await heartbeatHost({ recycle: { retired: beatCounter }, collect: whenIdle() });
    const release = holdStep(dir);
    const held = heldRun(host, dir, "a");
    await until("the collection due", () => host.health().collections.dueNow);
    await host.run("f/step", { invoke: "u/hold" });
    // The held run's end leaves no run open, with the step running.
    held.release();
    await held.done;
    expect(host.health()).toMatchObject({ runs: 0, in_flight: 1 });
    const beatsThen = beats;
    await until("twenty more heartbeats", () => beats >= beatsThen + 20);
    expect(gcCalls).toEqual([]);
    expect(host.health().collections.dueNow).toBe(true);
    const stepReleased = performance.now();
    writeFileSync(release, "");
    await until("the collection", () => gcCalls.length > 0);
    expect(gcCalls[0].at - stepReleased).toBeGreaterThanOrEqual(quiet - early);
    expect(host.health()).toMatchObject({ runs: 0, in_flight: 0 });
    expect(host.health().collections).toMatchObject({ count: 1, due: 1, dueNow: false });
    await host.close();
  }, 20_000);

  test("the idle policy: a step that ends just before the quiet period after its run's end would pass delays the collection by a whole quiet period", async () => {
    const quiet = 100;
    const { host, dir } = await heartbeatHost({ recycle: { retired: beatCounter }, collect: whenIdle() });
    const release = holdStep(dir);
    const held = heldRun(host, dir, "a");
    await until("the collection due", () => host.health().collections.dueNow);
    await host.run("f/step", { invoke: "u/hold" });
    held.release();
    await held.done;
    await Bun.sleep(quiet - 10);
    const stepReleased = performance.now();
    writeFileSync(release, "");
    await until("the collection", () => gcCalls.length > 0);
    expect(gcCalls[0].at - stepReleased).toBeGreaterThanOrEqual(quiet - early);
    await host.close();
  }, 20_000);

  test("the idle policy: once a heartbeat finds the step ended, the collection runs a quiet period later, between heartbeats", async () => {
    const quiet = 50;
    const { host, dir } = await heartbeatHost({ recycle: { retired: beatCounter }, collect: whenIdle({ quiet }) }, 500);
    const release = holdStep(dir);
    const held = heldRun(host, dir, "a");
    await until("the collection due", () => host.health().collections.dueNow);
    await host.run("f/step", { invoke: "u/hold" });
    held.release();
    await held.done;
    const stepReleased = performance.now();
    writeFileSync(release, "");
    await until("the collection", () => gcCalls.length > 0);
    const at = gcCalls[0].at;
    // The last heartbeat before the collection found the step ended.
    const lastBeat = beatTimes.filter((t) => t <= at).at(-1)!;
    expect(lastBeat).toBeGreaterThanOrEqual(stepReleased);
    expect(at - lastBeat).toBeGreaterThanOrEqual(quiet - early);
    await host.close();
  }, 20_000);

  test("the idle policy: a due collection stays due when the resident set falls below the mark", async () => {
    const realRss = process.memoryUsage.rss;
    let rss = 2000;
    process.memoryUsage.rss = () => rss;
    try {
      const quiet = 50;
      const { host, dir } = await heartbeatHost({ recycle: { retired: beatCounter }, collect: whenIdle({ rss: 1000, quiet }) }, 300);
      const run = heldRun(host, dir, "a");
      await until("the collection due", () => host.health().collections.dueNow);
      rss = 10;
      const beatsThen = beats;
      await until("two more heartbeats", () => beats >= beatsThen + 2);
      expect(gcCalls).toEqual([]);
      expect(host.health().collections.dueNow).toBe(true);
      run.release();
      await run.done;
      await until("the collection", () => gcCalls.length > 0);
      const beatsAfter = beats;
      await until("two more heartbeats", () => beats >= beatsAfter + 2);
      expect(gcCalls.length).toBe(1);
      expect(host.health().collections).toMatchObject({ count: 1, due: 1, dueNow: false });
      await host.close();
    } finally {
      process.memoryUsage.rss = realRss;
    }
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

  test("the idle policy: the cooldown runs from the end of a collection that takes time, or fails after it", async () => {
    const cooldown = 300;
    for (const fails of [false, true]) {
      gcCalls = [];
      const ends: number[] = [];
      gcStandIn = () => {
        Bun.sleepSync(100);
        ends.push(performance.now());
        if (fails && ends.length === 1) throw new Error("collection refused");
      };
      const { host, dir } = await heartbeatHost({ collect: whenIdle({ cooldown }) });
      const run = heldRun(host, dir, `a-${fails}`);
      await until("the collection due", () => host.health().collections.dueNow);
      run.release();
      await run.done;
      await until("two collections", () => gcCalls.length >= 2);
      expect(gcCalls[1].at - ends[0]).toBeGreaterThanOrEqual(cooldown);
      expect(host.health().collections.count).toBe(fails ? 1 : 2);
      await host.close();
    }
  }, 20_000);

  test("the idle policy: deferredMs adds every wait, a failed collection's too, and maxDeferredMs keeps the longest", async () => {
    gcStandIn = () => {
      if (gcCalls.length === 3) throw new Error("collection refused");
    };
    // The cooldown keeps the next collection from becoming due before the
    // test has opened the next run.
    const { host, dir } = await heartbeatHost({ collect: whenIdle({ cooldown: 200, quiet: 0 }) });
    const deferred = async (name: string, holdMs: number, n: number) => {
      const run = heldRun(host, dir, name);
      await until(`collection ${n} due`, () => host.health().collections.due === n);
      await Bun.sleep(holdMs);
      run.release();
      await run.done;
      await until(`collection ${n}`, () => gcCalls.length === n);
      return host.health().collections;
    };
    const first = await deferred("a", 300, 1);
    expect(first.deferredMs).toBeGreaterThanOrEqual(300);
    expect(first.maxDeferredMs).toBe(first.deferredMs);
    const second = await deferred("b", 100, 2);
    const secondWait = second.deferredMs - first.deferredMs;
    expect(secondWait).toBeGreaterThanOrEqual(100);
    expect(secondWait).toBeLessThan(first.maxDeferredMs);
    expect(second.maxDeferredMs).toBe(first.maxDeferredMs);
    const third = await deferred("c", 50, 3);
    expect(third.deferredMs - second.deferredMs).toBeGreaterThanOrEqual(50);
    expect(third).toMatchObject({ count: 2, due: 3, dueNow: false, maxDeferredMs: first.maxDeferredMs });
    await host.close();
  }, 20_000);

  test("the heartbeat policy, given, collects at the heartbeat while a run is open, and waits for nothing", async () => {
    const { host, dir } = await heartbeatHost({ collect: { rss: 1, cooldown: 60_000, policy: "heartbeat" } });
    const run = heldRun(host, dir, "heartbeat");
    await until("a collection", () => gcCalls.length > 0);
    expect(host.health().runs).toBe(1);
    expect(host.health().collections).toMatchObject({ count: 1, due: 1, deferredMs: 0, maxDeferredMs: 0, dueNow: false });
    run.release();
    await run.done;
    await host.close();
  }, 20_000);

  test("the default policy is idle", () => {
    // The literal, not read from the SDK.
    expect(DEFAULT_COLLECT_POLICY).toBe("idle");
  });

  test("with no policy given, a collection due while a run is open waits for the run's end and the quiet period, as under the idle policy", async () => {
    // policy and quiet left out: the defaults, idle and 100 ms.
    const quiet = 100;
    const { host, dir } = await heartbeatHost({ recycle: { retired: beatCounter }, collect: { rss: 1, cooldown: 60_000 } });
    const run = heldRun(host, dir, "a");
    // Due, whatever the policy: the heartbeat policy would collect here.
    await until("the collection due", () => host.health().collections.due > 0);
    const dueSeen = performance.now();
    const beatsThen = beats;
    await until("ten more heartbeats", () => beats >= beatsThen + 10);
    expect(gcCalls).toEqual([]);
    expect(host.health().runs).toBe(1);
    expect(host.health().collections.dueNow).toBe(true);
    const released = performance.now();
    run.release();
    await run.done;
    await until("the collection", () => gcCalls.length > 0);
    expect(gcCalls[0].at - released).toBeGreaterThanOrEqual(quiet - early);
    expect(gcCalls.map((c) => c.arg)).toEqual([true]);
    const c = host.health().collections;
    expect(c).toMatchObject({ count: 1, due: 1, dueNow: false });
    expect(c.deferredMs).toBeGreaterThanOrEqual(released - dueSeen + quiet - early);
    await host.close();
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

  // The pending check reads the host's health, and so the resident set,
  // when its timer fires.
  test("the idle policy: close() clears a pending check, and no collection follows", async () => {
    const quiet = 300;
    const { host, dir } = await heartbeatHost({ collect: whenIdle({ quiet }) });
    const run = heldRun(host, dir, "a");
    await until("the collection due", () => host.health().collections.dueNow);
    run.release();
    await run.done;
    await host.close();
    // Past the last heartbeat that close() may still have let through.
    await Bun.sleep(50);
    const realRss = process.memoryUsage.rss;
    let rssReads = 0;
    process.memoryUsage.rss = () => (rssReads++, realRss());
    try {
      await Bun.sleep(quiet + 200);
    } finally {
      process.memoryUsage.rss = realRss;
    }
    expect(rssReads).toBe(0);
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

  // The host lives on after its engine: onEngineExit returns.
  test("the idle policy: an engine exit clears a pending check, and no collection follows", async () => {
    const quiet = 300;
    let exits = 0;
    const { host, dir } = await heartbeatHost({ collect: whenIdle({ quiet }), onEngineExit: () => void exits++ });
    const run = heldRun(host, dir, "a");
    await until("the collection due", () => host.health().collections.dueNow);
    run.release();
    await run.done;
    const realRss = process.memoryUsage.rss;
    let rssReads = 0;
    process.memoryUsage.rss = () => (rssReads++, realRss());
    try {
      process.kill(host.enginePid, "SIGKILL");
      await until("the engine's exit", () => exits > 0);
      rssReads = 0;
      await Bun.sleep(quiet + 200);
    } finally {
      process.memoryUsage.rss = realRss;
    }
    expect(rssReads).toBe(0);
    expect(gcCalls).toEqual([]);
    expect(host.health().collections.dueNow).toBe(true);
  }, 20_000);

  test("the idle policy: an engine exit with a run open ends the run, and no collection follows", async () => {
    const quiet = 50;
    let exits = 0;
    const { host, dir } = await heartbeatHost({ collect: whenIdle({ quiet }), onEngineExit: () => void exits++ });
    const run = heldRun(host, dir, "a");
    await until("the collection due", () => host.health().collections.dueNow);
    process.kill(host.enginePid, "SIGKILL");
    expect(await code(run.done)).toContain("the engine connection ended");
    await until("the engine's exit", () => exits > 0);
    await Bun.sleep(quiet + 200);
    expect(gcCalls).toEqual([]);
  }, 20_000);

  // In a process of its own, with a quiet period of 60 s: the child records
  // the timers set through realTimers, and prints whether the pending
  // check's timer holds the process while the engine is still connected.
  // Then it kills the engine, which clears that timer, and has nothing
  // left that keeps it alive.
  test("the idle policy: a pending check's timer is unref'd, and the host exits once its engine has gone", async () => {
    const dir = tempDir("wl-host-collect-");
    writeFileSync(join(dir, "manifest.json"), "{}");
    const script = join(dir, "host.ts");
    writeFileSync(
      script,
      [
        `import { writeFileSync } from "node:fs";`,
        `import { connect } from ${JSON.stringify(join(import.meta.dir, "..", "host", "index.ts"))};`,
        `import { realTimers } from ${JSON.stringify(join(import.meta.dir, "..", "runtime", "context.ts"))};`,
        `const quietTimers = [];`,
        `const realSetTimeout = realTimers.setTimeout;`,
        `realTimers.setTimeout = (fn, ms, ...args) => { const t = realSetTimeout(fn, ms, ...args); if (ms > 50_000) quietTimers.push(t); return t; };`,
        `Bun.gc = () => (console.log("collected"), 0);`,
        `const dir = process.cwd();`,
        `const host = await connect({ engine: process.execPath, engineArgs: [${JSON.stringify(join(import.meta.dir, "heartbeatengine.ts"))}], bundle: dir, workdir: dir, collect: { rss: 1, cooldown: 60_000, policy: "idle", quiet: 60_000 }, onEngineExit: () => console.log("engine exited") });`,
        `const run = host.run("f/held", { hold: dir + "/held" });`,
        `while (!host.health().collections.dueNow) await Bun.sleep(5);`,
        `writeFileSync(dir + "/held", "");`,
        `await run;`,
        `while (quietTimers.length === 0) await Bun.sleep(5);`,
        `console.log("armed", quietTimers.length, quietTimers.map((t) => t.hasRef()).join());`,
        `process.kill(host.enginePid, "SIGKILL");`,
      ].join("\n"),
    );
    const child = Bun.spawn([process.execPath, script], { cwd: dir, env: { ...process.env, WL_TEST_HEARTBEAT_MS: "10" }, stdout: "pipe", stderr: "inherit" });
    const timer = setTimeout(() => child.kill(), 10_000);
    const code = await child.exited;
    clearTimeout(timer);
    expect(code).toBe(0);
    expect((await new Response(child.stdout).text()).trim().split("\n")).toEqual(["armed 1 false", "engine exited"]);
  }, 20_000);

  test("the idle policy leaves the recycle triggers as they are, and a collection never trips one", async () => {
    const reasons: string[] = [];
    const tripBeats: number[] = [];
    const { host } = await heartbeatHost({
      recycle: { rss: 1, samples: 3, retired: beatCounter },
      onRecycle: (r) => (reasons.push(r), tripBeats.push(beats)),
      collect: whenIdle({ cooldown: 0, quiet: 0 }),
    });
    await until("five heartbeats", () => beats >= 5);
    expect(reasons).toEqual(["rss"]);
    expect(tripBeats).toEqual([2]);
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
// once it has queued the heartbeat's ping. Here the transport takes every
// write at once, so the ping is written before the observer runs.
test("the heartbeat observer runs after the heartbeat's ping, on a transport that takes every write", async () => {
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
