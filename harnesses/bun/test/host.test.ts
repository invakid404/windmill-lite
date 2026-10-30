// Developer checks of the host SDK's connect options (host/index.ts). The
// host-mode gate runs in cmd/windmill-lite against a compiled host.

import { afterAll, describe, expect, test } from "bun:test";
import { chmodSync, mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { checkWorkdir, connect, HostError } from "../host/index.ts";
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
