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
  const raw = ({ invoke: ["args"], result: ["body"], late: ["body"] } as Record<string, string[]>)[m.t] ?? [];
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

test("an assembler refuses a stream past its limit before buffering it", () => {
  const a = new Assembler(4);
  expect(() => a.add({ t: "part", id: "1", seq: 0, last: false, chunk: new Uint8Array(5) })).toThrow(ProtocolError);
});
