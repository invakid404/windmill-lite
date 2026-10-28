// Entry point of the Bun harness runtime:
//
//   bun harnesses/bun/runtime/main.ts --bundle <dir>
//
// The engine spawns it with one end of a socketpair as file descriptor 3,
// which carries every protocol frame (ADR 0004); stdout and stderr stay
// free for output no invocation owns.

import { join } from "node:path";
import { Runtime } from "./runtime.ts";

const EXIT_STARTUP = 4;

function fail(message: string): never {
  process.stderr.write(`windmill-lite harness: ${message}\n`);
  process.exit(EXIT_STARTUP);
}

const args = process.argv.slice(2);
const at = args.indexOf("--bundle");
if (at < 0 || at + 1 >= args.length) fail("usage: main.ts --bundle <dir>");
const bundleDir = args[at + 1];

// The bundle digest is the SHA-256 of the bundle's manifest.json
// (ADR 0010); the engine rejects a harness that loaded another bundle.
let manifest: Uint8Array;
try {
  manifest = await Bun.file(join(bundleDir, "manifest.json")).bytes();
} catch (e) {
  fail(`cannot read the bundle manifest: ${(e as Error).message}`);
}
const bundleDigest = "sha256:" + new Bun.CryptoHasher("sha256").update(manifest).digest("hex");

let runtime: Runtime | undefined;
let socket: { write(b: Uint8Array): number; flush(): void; end(): void } | undefined;
const pending: Uint8Array[] = [];
let markClosed: () => void;
const closed = new Promise<void>((resolve) => (markClosed = resolve));

socket = await Bun.connect({
  fd: 3,
  socket: {
    data(_s, data) {
      if (runtime) runtime.onData(data);
      else pending.push(new Uint8Array(data));
    },
    drain() {
      runtime?.onDrain();
    },
    end() {
      runtime?.onClose();
    },
    close() {
      markClosed();
      runtime?.onClose();
    },
    error(_s, e) {
      process.stderr.write(`windmill-lite harness: socket error: ${e.message}\n`);
      runtime?.onClose();
    },
  },
} as Parameters<typeof Bun.connect>[0]);

runtime = new Runtime({
  bundleDir,
  bundleDigest,
  transport: {
    write: (b) => socket!.write(b),
    end: () => {
      socket!.flush();
      socket!.end();
    },
  },
  // A write counts bytes the socket accepted, which it may still hold in
  // its own buffer, so the process exits only once the socket has closed
  // (or a second later): otherwise the last frame, such as bye, is lost.
  exit: (code) => {
    Promise.race([closed, Bun.sleep(1000)]).then(() => process.exit(code));
  },
});
runtime.start();
for (const d of pending.splice(0)) runtime.onData(d);
