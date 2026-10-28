// Developer checks of the host SDK's connect options (host/index.ts). The
// host-mode gate runs in cmd/windmill-lite against a compiled host.

import { expect, test } from "bun:test";
import { chmodSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { connect, HostError } from "../host/index.ts";

async function code(p: Promise<unknown>): Promise<string | undefined> {
  try {
    await p;
  } catch (e) {
    return e instanceof HostError ? e.code : String(e);
  }
  return undefined;
}

test("the working directory must be the process's own", async () => {
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

// Root writes through a read-only mode, so the check cannot fail there.
test.skipIf(process.getuid?.() === 0)("the working directory must be writable", async () => {
  const dir = mkdtempSync(join(tmpdir(), "wl-host-test-"));
  chmodSync(dir, 0o500);
  const cwd = process.cwd();
  process.chdir(dir);
  try {
    expect(await code(connect({ engine: "/nonexistent", bundle: dir, workdir: dir }))).toBe("workdir_not_writable");
  } finally {
    process.chdir(cwd);
    chmodSync(dir, 0o700);
  }
});
