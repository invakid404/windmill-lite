// Developer check: the harness's codec against the golden frames that the
// Go run writes (conformance/protocol/frames.json). Go is the comparator
// that gates a merge (ADR 0003); this keeps the two codecs in step.

import { describe, expect, test } from "bun:test";
import {
  Assembler,
  DEFAULT_MAX_MESSAGE,
  decode,
  decodeAll,
  encodeText,
  FrameReader,
  frame,
  MAX_FRAME,
  type Message,
  type Outgoing,
  ProtocolError,
  Raw,
  RAW_FIELDS,
  split,
} from "../runtime/frame.ts";

type Case = {
  case: string;
  sides?: string[];
  message?: Message;
  message_text?: string;
  split?: { stream: string; chunk_bytes: number };
  payload?: string;
  frames?: string[];
  messages?: Message[];
  bytes_hex?: string;
  max_frame?: number;
  max_message?: number;
  error?: string;
};

const file = await Bun.file(new URL("../../../conformance/protocol/frames.json", import.meta.url)).json();
const cases: Case[] = file.cases.filter((c: Case) => !c.sides || c.sides.includes("bun"));
const enc = new TextEncoder();
const dec = new TextDecoder();

/** Turns a decoded message back into one to encode: raw values become
 * their JSON.stringify text, part chunks their base64. */
function outgoing(m: Message): Outgoing {
  const o: Outgoing = { ...m };
  const raw = RAW_FIELDS[m.t] ?? [];
  for (const k of raw) if (k in m) o[k] = new Raw(JSON.stringify(m[k]));
  if (m.t === "part" && m.chunk instanceof Uint8Array) o.chunk = Buffer.from(m.chunk).toString("base64");
  return o;
}

function stream(c: Case): Uint8Array {
  if (c.bytes_hex !== undefined) return Uint8Array.from(Buffer.from(c.bytes_hex, "hex"));
  return Buffer.concat((c.frames ?? []).map((f) => frame(enc.encode(f))));
}

function readAll(bytes: Uint8Array, maxFrame = MAX_FRAME, maxMessage = DEFAULT_MAX_MESSAGE): Message[] {
  const r = new FrameReader(maxFrame);
  const payloads = r.push(bytes);
  r.end();
  return decodeAll(payloads, maxMessage);
}

describe("conformance/protocol/frames.json", () => {
  test("has cases", () => {
    expect(cases.length).toBeGreaterThan(40);
  });

  for (const c of cases) {
    test(c.case, () => {
      if (c.error) {
        let err: unknown;
        try {
          readAll(stream(c), c.max_frame, c.max_message);
        } catch (e) {
          err = e;
        }
        expect(err).toBeInstanceOf(ProtocolError);
        expect((err as ProtocolError).code).toBe(c.error);
        return;
      }
      if (c.messages) {
        const got = readAll(stream(c));
        expect(got.map((m) => encodeText(outgoing(m)))).toEqual(c.messages.map((m) => encodeText(outgoing(m))));
        return;
      }
      // Encode: the message, decoded from its text or taken as JSON, encodes
      // to the golden payload, and so does the payload decoded again.
      const m = c.message_text !== undefined ? decode(enc.encode(c.message_text)) : c.message!;
      expect(encodeText(outgoing(m))).toBe(c.payload!);
      expect(encodeText(outgoing(decode(enc.encode(c.payload!))))).toBe(c.payload!);
      if (c.split) {
        const parts = split(c.split.stream, enc.encode(c.payload!), c.split.chunk_bytes).map((p) => dec.decode(p));
        expect(parts).toEqual(c.frames!);
        const back = readAll(stream({ case: c.case, frames: parts }));
        expect(back.length).toBe(1);
        expect(encodeText(outgoing(back[0]))).toBe(c.payload!);
      }
    });
  }
});

/** readAll, with the stream pushed size bytes at a time. */
function readPieces(bytes: Uint8Array, size: number, maxFrame = MAX_FRAME, maxMessage = DEFAULT_MAX_MESSAGE): Message[] {
  const r = new FrameReader(maxFrame);
  const payloads: Uint8Array[] = [];
  for (let i = 0; i < bytes.length; i += size) payloads.push(...r.push(bytes.subarray(i, i + size)));
  r.end();
  return decodeAll(payloads, maxMessage);
}

// The reader keeps what a push leaves of a frame and the next completes,
// so a stream read in pieces gives the messages, or the error, it gives
// read whole, however it is cut (FU7: the reader's buffer).
describe("conformance/protocol/frames.json read in pieces", () => {
  for (const c of cases.filter((c) => c.error || c.messages)) {
    for (const size of [1, 3, 4096]) {
      test(`${c.case}, ${size} bytes at a time`, () => {
        const bytes = stream(c);
        if (c.error) {
          expect(() => readPieces(bytes, size, c.max_frame, c.max_message)).toThrow(ProtocolError);
          try {
            readPieces(bytes, size, c.max_frame, c.max_message);
          } catch (e) {
            expect((e as ProtocolError).code).toBe(c.error);
          }
          return;
        }
        expect(readPieces(bytes, size).map((m) => encodeText(outgoing(m)))).toEqual(c.messages!.map((m) => encodeText(outgoing(m))));
      });
    }
  }
});

test("frames a reader keeps across pushes come out whole and apart, and a stream that ends inside one is truncated", () => {
  const payloads = [new Uint8Array(70_000).fill(1), new Uint8Array(3).fill(2), new Uint8Array(9000).fill(3)];
  const bytes = Buffer.concat(payloads.map((p) => frame(p)));
  const r = new FrameReader(MAX_FRAME);
  const got: Uint8Array[] = [];
  for (let i = 0; i < bytes.length; i += 8192) got.push(...r.push(bytes.subarray(i, i + 8192)));
  r.end();
  expect(got.map((p) => [p.length, p[0], p[p.length - 1]])).toEqual(payloads.map((p) => [p.length, p[0], p[p.length - 1]]));
  // A payload is a copy: what the reader reads next does not change it.
  r.push(frame(new Uint8Array(70_000).fill(9)).subarray(0, 100));
  expect(got[0].every((b) => b === 1)).toBe(true);
  expect(() => r.end()).toThrow(ProtocolError);
});

test("an assembler refuses a stream past its limit before buffering it", () => {
  const a = new Assembler(4);
  expect(() => a.add({ t: "part", id: "1", seq: 0, last: false, chunk: new Uint8Array(5) })).toThrow(ProtocolError);
});
