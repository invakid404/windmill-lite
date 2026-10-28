// Developer check: the harness's send queue, against a socket that accepts
// a few bytes at a time, as Bun's sockets do under load.

import { expect, test } from "bun:test";
import { decodeAll, FrameReader, type Message, type Outgoing, Raw, Sender } from "../runtime/frame.ts";

const MAX_FRAME = 128;
const CHUNK = 30;

/** A sender over a fake socket that takes at most `per` bytes per write,
 * and nothing more until drain() when `stall` is set. */
function rig(per = 1 << 30, stall = false) {
  const written: Uint8Array[] = [];
  let open = true;
  let full = false;
  const sender = new Sender(
    (b) => {
      if (!open) return -1;
      if (full) return 0;
      const n = Math.min(per, b.length);
      written.push(b.slice(0, n));
      if (stall) full = true;
      return n;
    },
    1 << 20,
    MAX_FRAME,
    CHUNK,
  );
  return {
    sender,
    drain() {
      full = false;
      sender.flush();
    },
    close() {
      open = false;
    },
    messages(): Message[] {
      const r = new FrameReader(MAX_FRAME);
      const payloads = r.push(Buffer.concat(written));
      r.end();
      return decodeAll(payloads);
    },
    frames(): string[] {
      const r = new FrameReader(MAX_FRAME);
      return r.push(Buffer.concat(written)).map((p) => {
        const m = JSON.parse(new TextDecoder().decode(p));
        return m.t === "part" ? `part:${m.id}` : m.t;
      });
    },
  };
}

const big = (id: string): Outgoing => ({ t: "result", id, outcome: "ok", body: new Raw(JSON.stringify("x".repeat(200))), settled: true });

test("a small frame overtakes a large message after at most one part", () => {
  const r = rig(1 << 30, true);
  r.sender.send(big("i1"));
  // The frame being written when the ping is queued finishes first.
  const before = r.frames().length;
  r.sender.send({ t: "ping", id: "h1" });
  for (let i = 0; i < 20; i++) r.drain();
  const frames = r.frames();
  const at = frames.indexOf("ping");
  expect(at).toBeGreaterThan(-1);
  expect(at).toBeLessThanOrEqual(before + 1);
  expect(frames.length).toBeGreaterThan(at + 2);
  expect(r.messages().map((m) => m.t)).toEqual(["ping", "result"]);
});

test("messages about one invocation keep their order", () => {
  const r = rig(1 << 30, true);
  r.sender.send(big("i1"));
  r.sender.send({ t: "late", id: "i1", kind: "log", body: new Raw('["after"]'), settled: false });
  r.sender.send(big("i1"));
  r.sender.send({ t: "late", id: "i1", kind: "error", settled: true });
  r.sender.send({ t: "ping", id: "h1" });
  r.sender.send({ t: "log", id: "i2", lines: ["other invocation"] });
  for (let i = 0; i < 50; i++) r.drain();
  const mine = r.messages().filter((m) => m.id === "i1").map((m) => `${m.t}${m.kind ? ":" + m.kind : ""}`);
  expect(mine).toEqual(["result", "late:log", "result", "late:error"]);
  // Frames about other invocations, or none, are not held back.
  const all = r.messages().map((m) => m.id);
  expect(all.indexOf("h1")).toBeLessThan(all.indexOf("i1"));
  expect(all.indexOf("i2")).toBeLessThan(all.indexOf("i1"));
});

test("partial writes resume on drain, and whenFlushed waits for them", () => {
  const r = rig(7, true);
  let flushed = false;
  r.sender.send(big("i1"));
  r.sender.send({ t: "bye" });
  r.sender.whenFlushed(() => (flushed = true));
  let rounds = 0;
  while (!flushed && rounds++ < 1000) {
    expect(flushed).toBe(false);
    r.drain();
  }
  expect(flushed).toBe(true);
  expect(r.messages().map((m) => m.t)).toEqual(["bye", "result"]);
});

test("a closed socket stops the queue", () => {
  const r = rig();
  r.sender.send({ t: "bye" });
  r.close();
  r.sender.send({ t: "bye" });
  expect(r.sender.closed).toBe(true);
  expect(r.messages().length).toBe(1);
});
