// Entry point of the Bun harness runtime:
//
//   bun harnesses/bun/runtime/main.ts --bundle <dir> [--plugin <module>]
//
// The engine spawns it with one end of a socketpair as file descriptor 3,
// which carries every protocol frame (ADR 0004); stdout and stderr stay
// free for output no invocation owns.

import { writeSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { exitProcess } from "./context.ts";
import { loadPlugin } from "./plugin.ts";
import { extractAssets, registerHostModules, Runtime } from "./runtime.ts";

const EXIT_STARTUP = 4;

function fail(message: string): never {
  process.stderr.write(`windmill-lite harness: ${message}\n`);
  exitProcess(EXIT_STARTUP);
}

const args = process.argv.slice(2);
const at = args.indexOf("--bundle");
if (at < 0 || at + 1 >= args.length) fail("usage: main.ts --bundle <dir> [--plugin <module>]");
const bundleDir = args[at + 1];
const pluginAt = args.indexOf("--plugin");
if (pluginAt >= 0 && pluginAt + 1 >= args.length) fail("usage: main.ts --bundle <dir> [--plugin <module>]");
const pluginPath = pluginAt >= 0 ? args[pluginAt + 1] : undefined;

// The bundle digest is the SHA-256 of the bundle's manifest.json
// (ADR 0010); the engine rejects a harness that loaded another bundle.
let manifest: Uint8Array;
try {
  manifest = await Bun.file(join(bundleDir, "manifest.json")).bytes();
} catch (e) {
  fail(`cannot read the bundle manifest: ${(e as Error).message}`);
}
const bundleDigest = "sha256:" + new Bun.CryptoHasher("sha256").update(manifest).digest("hex");

let socket: { write(b: Uint8Array): number; flush(): void; end(): void } | undefined;
let started = false;
const pending: Uint8Array[] = [];
let markClosed: () => void;
const closed = new Promise<void>((resolve) => (markClosed = resolve));

// The runtime installs every process-wide hook when it is made (the
// environment view, the process scope, fetch, the process-level handlers,
// the process.exit interception), before any module of the bundle loads:
// a host-provided module that captures the process scope at load finds
// it. It sends nothing before start().
const runtime = new Runtime({
  bundleDir,
  bundleDigest,
  transport: {
    write: (b) => socket!.write(b),
    // A fresh process ends synchronously once its last frames are on the
    // socket: they go straight to file descriptor 3, which the socket
    // shares, after what the socket already took (a Bun socket keeps no
    // buffer of its own). A socket that is full is waited for.
    writeSync: (b) => {
      const deadline = Date.now() + 30_000;
      for (let off = 0; off < b.length; ) {
        try {
          off += writeSync(3, b, off, b.length - off);
        } catch (e) {
          if ((e as { code?: string }).code !== "EAGAIN" || Date.now() > deadline) throw e;
          Bun.sleepSync(1);
        }
      }
    },
    end: () => {
      socket!.flush();
      socket!.end();
    },
  },
  // A write counts bytes the socket accepted, which it may still hold in
  // its own buffer, so the process exits only once the socket has closed
  // (or a second later): otherwise the last frame, such as bye, is lost.
  // It exits through the real process.exit: this may run in an
  // invocation's context, where process.exit is intercepted (D11).
  exit: (code) => {
    Promise.race([closed, Bun.sleep(1000)]).then(() => exitProcess(code));
  },
});

// The bundle's codebase assets are extracted into the working directory
// before any module loads (WR-50, D10).
try {
  extractAssets(bundleDir, process.cwd());
} catch (e) {
  fail(`cannot extract the bundle's assets: ${(e as Error).message}`);
}

// The bundle's copies of its host-provided modules are registered before
// any step loads, so each shim finds its module (D12), and before the
// harness greets the engine: a harness whose modules fail to load never
// says hello.
try {
  await registerHostModules(bundleDir, JSON.parse(new TextDecoder().decode(manifest)));
} catch (e) {
  fail(`cannot register the bundle's host-provided modules: ${(e as Error).message}`);
}

// The host plugin the engine named (ADR 0008, F2c), loaded once the
// runtime's process-wide hooks are in place and before the harness greets
// the engine, whose hello carries its declaration.
if (pluginPath !== undefined) {
  try {
    runtime.usePlugin(await loadPlugin(pathToFileURL(pluginPath).href));
  } catch (e) {
    fail(`cannot load the host plugin ${pluginPath}: ${(e as Error).message}`);
  }
}

socket = await Bun.connect({
  fd: 3,
  socket: {
    data(_s, data) {
      if (started) runtime.onData(data);
      else pending.push(new Uint8Array(data));
    },
    drain() {
      runtime.onDrain();
    },
    end() {
      runtime.onClose();
    },
    close() {
      markClosed();
      runtime.onClose();
    },
    error(_s, e) {
      process.stderr.write(`windmill-lite harness: socket error: ${e.message}\n`);
      runtime.onClose();
    },
  },
} as Parameters<typeof Bun.connect>[0]);

runtime.start();
started = true;
for (const d of pending.splice(0)) runtime.onData(d);
