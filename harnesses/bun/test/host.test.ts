// Developer checks of the host SDK's connect options (host/index.ts). The
// host-mode gate runs in cmd/windmill-lite against a compiled host.

import { describe, expect, test } from "bun:test";
import { chmodSync, mkdirSync, mkdtempSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { checkWorkdir, connect, HostError } from "../host/index.ts";

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

// The shared cases of host mode's working-directory checks, which the
// engine runs too (internal/harness, TestHostWorkdirCases).
const workdir = (await Bun.file(new URL("../../../conformance/protocol/host/workdir.json", import.meta.url)).json()) as { cases: WorkdirCase[] };

describe("conformance/protocol/host/workdir.json", () => {
  const root = process.getuid?.() === 0;
  for (const c of workdir.cases) {
    // Root writes through a read-only mode, so those checks cannot fail there.
    test.skipIf(root && (c.cwd_mode !== undefined || c.other_mode !== undefined))(`${c.case}: ${c.title}`, () => {
      const dir = mkdtempSync(join(tmpdir(), "wl-host-workdir-"));
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

test("connect checks the working directory before it starts the engine", async () => {
  const dir = mkdtempSync(join(tmpdir(), "wl-host-test-"));
  expect(await code(connect({ engine: "/nonexistent", bundle: dir, workdir: dir }))).toBe("workdir_mismatch");
});

test("an engine that cannot start is engine_start", async () => {
  const dir = mkdtempSync(join(tmpdir(), "wl-host-test-"));
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
  const dir = mkdtempSync(join(tmpdir(), "wl-host-test-"));
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
