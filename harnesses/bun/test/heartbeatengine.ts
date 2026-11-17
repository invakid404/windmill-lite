// A stand-in for the engine in the host SDK's heartbeat tests
// (host.test.ts). connect() starts it with the engine's arguments. It
// welcomes the host on file descriptor 3 with a heartbeat of
// WL_TEST_HEARTBEAT_MS milliseconds, then reads the host's frames until
// the host ends the connection. It submits no run: the tests need only the
// host's heartbeat.

import { createReadStream, writeSync } from "node:fs";
import { DEFAULT_MAX_MESSAGE, decodeAll, encode, frame, FrameReader, MAX_FRAME } from "../runtime/frame.ts";

const heartbeatMs = Number(process.env.WL_TEST_HEARTBEAT_MS ?? "10");
const reader = new FrameReader(MAX_FRAME);
const socket = createReadStream("", { fd: 3 });
socket.on("data", (d) => {
  const b = d as Buffer;
  for (const m of decodeAll(reader.push(new Uint8Array(b.buffer, b.byteOffset, b.length)), DEFAULT_MAX_MESSAGE)) {
    if (m.t !== "hello") continue;
    const welcome = { t: "welcome", proto: 1, engine: "test", bundle: m.bundle, config: { module_state: "warm", heartbeat_ms: heartbeatMs, max_message: 1 << 20 } };
    writeSync(3, frame(encode(welcome)));
  }
});
socket.on("end", () => process.exit(0));
socket.on("error", (e) => {
  // On the test's stderr, so that a host that never got its welcome says why.
  process.stderr.write(`heartbeatengine: file descriptor 3: ${e.message}\n`);
  process.exit(1);
});
