// A stand-in for the engine in the host SDK's heartbeat tests
// (host.test.ts). connect() starts it with the engine's arguments. It
// welcomes the host on file descriptor 3 with a heartbeat of
// WL_TEST_HEARTBEAT_MS milliseconds, then reads the host's frames until
// the host ends the connection.
//
// It answers each run that the host submits with a run_done of status
// success, then a run_settled. The run's arguments say when:
//
// - with neither field below: at once;
// - hold, a file path: once that file exists;
// - invoke, a script id of the bundle: it first sends the host an invoke
//   of that script's main, then answers the run at once, while the step
//   still runs; run_settled follows the invocation's result;
// - echo: true: at once, with a result that holds the run frame's
//   deadline_ms and pre_write_deadline_ms, null where the frame has none,
//   and runs, the count of run frames this engine has received;
// - canceled, a cause: at once, with status canceled and that
//   cancel_cause, written as given, an empty one included.

import { createReadStream, existsSync, writeSync } from "node:fs";
import { DEFAULT_MAX_MESSAGE, decodeAll, encode, frame, FrameReader, MAX_FRAME, Raw } from "../runtime/frame.ts";

const heartbeatMs = Number(process.env.WL_TEST_HEARTBEAT_MS ?? "10");
const reader = new FrameReader(MAX_FRAME);
const socket = createReadStream("", { fd: 3 });
const send = (m: Record<string, unknown>) => writeSync(3, frame(encode(m as { t: string })));
// The runs whose run_settled waits for their invocation's result, by the
// invocation's id.
const settleOnResult = new Map<string, string>();
let runs = 0;

function answer(id: string, settle: boolean) {
  send({ t: "run_done", id, status: "success" });
  if (settle) send({ t: "run_settled", id });
}

socket.on("data", (d) => {
  const b = d as Buffer;
  for (const m of decodeAll(reader.push(new Uint8Array(b.buffer, b.byteOffset, b.length)), DEFAULT_MAX_MESSAGE) as Record<string, unknown>[]) {
    if (m.t === "hello") {
      send({ t: "welcome", proto: 1, engine: "test", bundle: m.bundle, config: { module_state: "warm", heartbeat_ms: heartbeatMs, max_message: 1 << 20 } });
    } else if (m.t === "run") {
      runs++;
      const id = String(m.id);
      const args = (m.args ?? {}) as { hold?: unknown; invoke?: unknown; echo?: unknown; canceled?: unknown };
      if (args.echo === true) {
        const result = { deadline_ms: m.deadline_ms ?? null, pre_write_deadline_ms: m.pre_write_deadline_ms ?? null, runs };
        send({ t: "run_done", id, status: "success", result: new Raw(JSON.stringify(result)) });
        send({ t: "run_settled", id });
      } else if (typeof args.canceled === "string") {
        // Raw, so that an empty cause reaches the host as written.
        writeSync(3, frame(new TextEncoder().encode(JSON.stringify({ t: "run_done", id, status: "canceled", cancel_cause: args.canceled }))));
        send({ t: "run_settled", id });
      } else if (typeof args.invoke === "string") {
        settleOnResult.set(`i-${id}`, id);
        send({ t: "invoke", id: `i-${id}`, run: id, site: "a", script: args.invoke, entry: "main", args: new Raw("{}"), env: {}, boundary: "bun" });
        answer(id, false);
      } else if (typeof args.hold === "string") {
        const hold = args.hold;
        const poll = setInterval(() => {
          if (!existsSync(hold)) return;
          clearInterval(poll);
          answer(id, true);
        }, 2);
      } else {
        answer(id, true);
      }
    } else if (m.t === "result") {
      const run = settleOnResult.get(String(m.id));
      if (run === undefined) continue;
      settleOnResult.delete(String(m.id));
      send({ t: "run_settled", id: run });
    }
  }
});
socket.on("end", () => process.exit(0));
socket.on("error", (e) => {
  // On the test's stderr, so that a host that never got its welcome says why.
  process.stderr.write(`heartbeatengine: file descriptor 3: ${e.message}\n`);
  process.exit(1);
});
