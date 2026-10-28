// The harness side of the frame codec of ADR 0004 §Transport and framing.
// It mirrors internal/harness in Go: the same canonical envelope, the same
// part streams and the same error codes, checked by both against the golden
// frames in conformance/protocol/frames.json.

/** The largest frame payload: 1 MiB. Longer messages go as part frames. */
export const MAX_FRAME = 1 << 20;
/** The largest message unless the welcome lowers it: 512 MiB. */
export const DEFAULT_MAX_MESSAGE = 512 * 1024 * 1024;
/** Message bytes per part frame when splitting: the largest multiple of 3
 * whose base64 form leaves 1 KiB of the frame for the part envelope. */
export const PART_CHUNK = Math.floor((MAX_FRAME - 1024) / 4) * 3;

export class ProtocolError extends Error {
  constructor(
    readonly code: string,
    message: string,
  ) {
    super(`harness protocol: ${code}: ${message}`);
  }
}

const encoder = new TextEncoder();
// ignoreBOM keeps a leading byte order mark, which JSON.parse then
// refuses, as the Go decoder does.
const strictDecoder = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true });

/** A raw JSON value, spliced into an envelope byte for byte. */
export class Raw {
  constructor(readonly text: string) {}
}

export type Health = {
  in_flight: number;
  abandoned: number;
  errors: number;
  lag_ms: number;
  rss: number;
};

/** A message as it is encoded: raw values are Raw, the rest plain. */
export type Outgoing = { t: string; [k: string]: unknown };

/** Field order of each message type, after `t` (ADR 0004 message table). */
const FIELDS: Record<string, readonly string[]> = {
  hello: ["proto", "harness", "runtime", "caps", "bundle", "pid"],
  welcome: ["proto", "engine", "bundle", "config", "reject"],
  invoke: ["id", "run", "site", "script", "entry", "args", "env", "boundary"],
  result: ["id", "outcome", "body", "tail", "exit_code", "settled"],
  late: ["id", "kind", "body", "settled"],
  cancel: ["id", "reason"],
  log: ["id", "lines"],
  ping: ["id", "in_flight", "abandoned", "errors", "lag_ms", "rss"],
  pong: ["id", "in_flight", "abandoned", "errors", "lag_ms", "rss"],
  part: ["id", "seq", "last", "chunk"],
  drain: [],
  bye: [],
};

/** Field order of nested objects. */
const NESTED: Record<string, readonly string[]> = {
  harness: ["kind", "version"],
  caps: ["fetch", "native", "fresh", "host"],
  config: ["module_state", "heartbeat_ms", "max_message"],
  reject: ["code", "message"],
};

function value(v: unknown, nestedOrder?: readonly string[]): string {
  if (v instanceof Raw) return v.text;
  if (typeof v === "number") {
    if (!Number.isSafeInteger(v)) throw new ProtocolError("bad_field", `number ${v} is not an integer`);
    return String(v);
  }
  if (typeof v === "string" || typeof v === "boolean") return JSON.stringify(v);
  if (Array.isArray(v)) return "[" + v.map((x) => value(x)).join(",") + "]";
  if (v && typeof v === "object") {
    const o = v as Record<string, unknown>;
    // Protocol objects use their fixed order; data objects (env) sort
    // their keys by UTF-16 code units, as Go sorts env bytewise for the
    // ASCII names the cases use.
    const keys = nestedOrder ? nestedOrder.filter((k) => o[k] !== undefined) : Object.keys(o).sort();
    return "{" + keys.map((k) => JSON.stringify(k) + ":" + value(o[k])).join(",") + "}";
  }
  throw new ProtocolError("bad_field", `cannot encode ${typeof v}`);
}

/** The canonical payload text of a message. */
export function encodeText(m: Outgoing): string {
  const order = Object.hasOwn(FIELDS, m.t) ? FIELDS[m.t] : undefined;
  if (!order) throw new ProtocolError("unknown_type", `message type ${JSON.stringify(m.t)}`);
  let s = '{"t":' + JSON.stringify(m.t);
  for (const k of order) {
    const v = m[k];
    if (v === undefined) continue;
    s += "," + JSON.stringify(k) + ":" + value(v, NESTED[k]);
  }
  return s + "}";
}

/** The canonical payload bytes of a message. */
export function encode(m: Outgoing): Uint8Array {
  return encoder.encode(encodeText(m));
}

/** A frame: the payload's length as 4 bytes big-endian, then the payload. */
export function frame(payload: Uint8Array): Uint8Array {
  const out = new Uint8Array(4 + payload.length);
  new DataView(out.buffer).setUint32(0, payload.length);
  out.set(payload, 4);
  return out;
}

function toBase64(b: Uint8Array): string {
  return Buffer.from(b.buffer, b.byteOffset, b.length).toString("base64");
}

/** Strict, padded standard base64; anything else is bad_chunk. */
function fromBase64(s: string): Uint8Array {
  const b = Buffer.from(s, "base64");
  if (s.length === 0 || b.length === 0 || b.toString("base64") !== s) {
    throw new ProtocolError("bad_chunk", "part chunk is not non-empty padded base64");
  }
  return new Uint8Array(b.buffer, b.byteOffset, b.length);
}

/** The part frame payloads of stream id for payload, chunk bytes each. */
export function split(id: string, payload: Uint8Array, chunk: number): Uint8Array[] {
  const out: Uint8Array[] = [];
  for (let seq = 0, off = 0; off < payload.length; seq++) {
    const end = Math.min(off + chunk, payload.length);
    out.push(encode({ t: "part", id, seq, last: end === payload.length, chunk: toBase64(payload.subarray(off, end)) }));
    off = end;
  }
  return out;
}

export type Message = { t: string; [k: string]: any };

const isInt = (v: unknown) => typeof v === "number" && Number.isSafeInteger(v);

type Check = "string" | "int" | "bool" | "raw" | "strings" | "env" | "object";

const REQUIRED: Record<string, Record<string, Check>> = {
  hello: { proto: "int", harness: "object", runtime: "string", caps: "object", bundle: "string", pid: "int" },
  welcome: { proto: "int", engine: "string", bundle: "string" },
  invoke: { id: "string", run: "string", site: "string", script: "string", entry: "string", args: "raw", env: "env", boundary: "string" },
  result: { id: "string", outcome: "string", settled: "bool" },
  late: { id: "string", kind: "string", settled: "bool" },
  cancel: { id: "string", reason: "string" },
  log: { id: "string", lines: "strings" },
  ping: { id: "string" },
  pong: { id: "string" },
  part: { id: "string", seq: "int", last: "bool", chunk: "string" },
  drain: {},
  bye: {},
};

function checkField(t: string, o: Record<string, unknown>, k: string, c: Check) {
  const v = o[k];
  // A raw value may be JSON null (a step that returned null); any other
  // field that is null counts as absent, as in Go.
  if (v === undefined || (v === null && c !== "raw")) throw new ProtocolError("missing_field", `${t} ${k}`);
  const ok =
    c === "raw" ||
    (c === "string" && typeof v === "string") ||
    (c === "int" && isInt(v)) ||
    (c === "bool" && typeof v === "boolean") ||
    (c === "object" && typeof v === "object" && !Array.isArray(v)) ||
    (c === "strings" && Array.isArray(v) && v.every((x) => typeof x === "string")) ||
    (c === "env" && typeof v === "object" && !Array.isArray(v) && Object.values(v as object).every((x) => typeof x === "string"));
  if (!ok) throw new ProtocolError("bad_field", `field ${JSON.stringify(k)} has the wrong type`);
}

function checkNested(t: string, o: Record<string, unknown>, k: string, fields: Record<string, Check>) {
  const n = o[k] as Record<string, unknown>;
  for (const [f, c] of Object.entries(fields)) checkField(t, n, f, c);
}

/** Decodes one frame payload. Raw values arrive parsed: the harness reads
 * arguments with JSON.parse, as Windmill's wrapper does (WR-9). */
export function decode(payload: Uint8Array): Message {
  let text: string;
  try {
    text = strictDecoder.decode(payload);
  } catch {
    throw new ProtocolError("invalid_utf8", "payload is not valid UTF-8");
  }
  let o: unknown;
  try {
    o = JSON.parse(text);
  } catch {
    throw new ProtocolError("invalid_json", "payload is not JSON");
  }
  if (!o || typeof o !== "object" || Array.isArray(o)) throw new ProtocolError("not_object", "payload is not a JSON object");
  const m = o as Message;
  if (typeof m.t !== "string") throw new ProtocolError("missing_type", "object has no string field t");
  // Own properties only: "constructor" or "__proto__" is not a type.
  const req = Object.hasOwn(REQUIRED, m.t) ? REQUIRED[m.t] : undefined;
  if (!req) throw new ProtocolError("unknown_type", `message type ${JSON.stringify(m.t)}`);
  for (const [k, c] of Object.entries(req)) checkField(m.t, m, k, c);
  switch (m.t) {
    case "hello":
      checkNested(m.t, m, "harness", { kind: "string", version: "string" });
      checkNested(m.t, m, "caps", { fetch: "bool", native: "bool", fresh: "bool", host: "bool" });
      break;
    case "welcome":
      if (m.config != null && m.reject != null) throw new ProtocolError("bad_field", "welcome has both config and reject");
      if (m.config != null) {
        checkField(m.t, m, "config", "object");
        checkNested(m.t, m, "config", { module_state: "string", heartbeat_ms: "int", max_message: "int" });
      } else if (m.reject != null) {
        checkField(m.t, m, "reject", "object");
        checkNested(m.t, m, "reject", { code: "string", message: "string" });
      } else throw new ProtocolError("missing_field", "welcome has neither config nor reject");
      break;
    case "result":
      switch (m.outcome) {
        case "ok":
        case "error":
          checkField(m.t, m, "body", "raw");
          break;
        case "escaped":
          checkField(m.t, m, "tail", "string");
          checkField(m.t, m, "exit_code", "int");
          break;
        case "empty":
          break;
        default:
          throw new ProtocolError("bad_field", `result outcome ${JSON.stringify(m.outcome)}`);
      }
      break;
    case "late":
      if (m.kind === "result" || m.kind === "log") checkField(m.t, m, "body", "raw");
      else if (m.kind !== "error") throw new ProtocolError("bad_field", `late kind ${JSON.stringify(m.kind)}`);
      if (m.kind === "log" && m.settled) throw new ProtocolError("bad_field", "a late log cannot settle an invocation");
      break;
    case "cancel":
      if (!["timeout", "canceled", "run_ended"].includes(m.reason)) throw new ProtocolError("bad_field", `cancel reason ${JSON.stringify(m.reason)}`);
      break;
    case "ping":
    case "pong": {
      // Null counts as absent here too, as everywhere outside raw values.
      const present = ["in_flight", "abandoned", "errors", "lag_ms", "rss"].filter((k) => m[k] !== undefined && m[k] !== null);
      if (present.length > 0) for (const k of ["in_flight", "abandoned", "errors", "lag_ms", "rss"]) checkField(m.t, m, k, "int");
      break;
    }
    case "part":
      m.chunk = fromBase64(m.chunk);
      if (m.seq < 0) throw new ProtocolError("bad_field", "negative part seq");
      break;
  }
  return own(m);
}

/** The message with only the fields its type has, as Go's decoder keeps
 * them: unknown fields, and fields of another outcome, are dropped. */
function own(m: Message): Message {
  const out: Message = { t: m.t };
  for (const k of FIELDS[m.t]) {
    if (m[k] === undefined) continue;
    if (m.t === "result" && (k === "body" ? !["ok", "error"].includes(m.outcome) : (k === "tail" || k === "exit_code") && m.outcome !== "escaped")) continue;
    if ((m.t === "ping" || m.t === "pong") && k !== "id" && (m.in_flight === undefined || m.in_flight === null)) continue;
    const nested = Object.hasOwn(NESTED, k) ? NESTED[k] : undefined;
    if (nested && m[k] !== null) {
      const o: Record<string, unknown> = {};
      for (const n of nested) if (m[k][n] !== undefined) o[n] = m[k][n];
      out[k] = o;
    } else if (m[k] !== null || ["args", "body"].includes(k)) {
      out[k] = m[k];
    }
  }
  return out;
}

/** Splits a byte stream into frame payloads. */
export class FrameReader {
  private buf = new Uint8Array(0);
  constructor(private maxFrame = MAX_FRAME) {}

  /** Adds bytes and returns the payloads they complete. */
  push(data: Uint8Array): Uint8Array[] {
    const merged = new Uint8Array(this.buf.length + data.length);
    merged.set(this.buf);
    merged.set(data, this.buf.length);
    const out: Uint8Array[] = [];
    let off = 0;
    while (merged.length - off >= 4) {
      const n = new DataView(merged.buffer, merged.byteOffset + off, 4).getUint32(0);
      if (n === 0) throw new ProtocolError("frame_empty", "frame of length 0");
      if (n > this.maxFrame) throw new ProtocolError("frame_too_large", `frame of ${n} bytes exceeds ${this.maxFrame}`);
      if (merged.length - off - 4 < n) break;
      out.push(merged.slice(off + 4, off + 4 + n));
      off += 4 + n;
    }
    this.buf = merged.slice(off);
    return out;
  }

  /** Reports a stream that ended inside a frame. */
  end() {
    if (this.buf.length > 0) throw new ProtocolError("truncated", "stream ended inside a frame");
  }
}

/** Reassembles part streams. */
export class Assembler {
  private streams = new Map<string, { chunks: Uint8Array[]; size: number; next: number }>();
  constructor(public maxMessage = DEFAULT_MAX_MESSAGE) {}

  /** Takes one part; returns the reassembled payload when the stream ends. */
  add(p: Message): Uint8Array | undefined {
    let s = this.streams.get(p.id);
    if (p.seq === 0) {
      if (s) throw new ProtocolError("part_sequence", `part stream ${JSON.stringify(p.id)} started twice`);
      s = { chunks: [], size: 0, next: 0 };
      this.streams.set(p.id, s);
    } else if (!s || p.seq !== s.next) {
      throw new ProtocolError("part_sequence", `part stream ${JSON.stringify(p.id)} got seq ${p.seq}, want seq ${s ? s.next : 0}`);
    }
    if (s.size + p.chunk.length > this.maxMessage) {
      this.streams.delete(p.id);
      throw new ProtocolError("message_too_large", `part stream ${JSON.stringify(p.id)} exceeds ${this.maxMessage} bytes`);
    }
    s.chunks.push(p.chunk);
    s.size += p.chunk.length;
    s.next++;
    if (!p.last) return undefined;
    this.streams.delete(p.id);
    return Buffer.concat(s.chunks, s.size);
  }
}

/** Decodes a whole frame's payload, which counts against the message
 * limit like a reassembled one (a host may lower the limit below a frame). */
export function decodeFrame(payload: Uint8Array, maxMessage: number): Message {
  if (payload.length > maxMessage) {
    throw new ProtocolError("message_too_large", `frame of ${payload.length} bytes exceeds the message limit of ${maxMessage}`);
  }
  return decode(payload);
}

/** Decodes a reassembled payload, which must not be another part. */
export function decodeAssembled(payload: Uint8Array): Message {
  const m = decode(payload);
  if (m.t === "part") throw new ProtocolError("part_in_part", "a part stream reassembled to a part frame");
  return m;
}

/** Decodes a sequence of frame payloads, reassembling part streams. */
export function decodeAll(payloads: Uint8Array[], maxMessage = DEFAULT_MAX_MESSAGE): Message[] {
  const asm = new Assembler(maxMessage);
  const out: Message[] = [];
  for (const p of payloads) {
    const m = decodeFrame(p, maxMessage);
    if (m.t !== "part") {
      out.push(m);
      continue;
    }
    const full = asm.add(m);
    if (full) out.push(decodeAssembled(full));
  }
  return out;
}

/** The messages that are about one invocation, by their id. */
const ABOUT_INVOCATION = new Set(["invoke", "cancel", "result", "late", "log"]);

type OutStream = { id: string; about?: string; payload: Uint8Array; off: number; seq: number; after: Queued[] };
type Queued = { frame: Uint8Array } | { stream: OutStream };

/** Queues frames for a socket that may accept fewer bytes than offered.
 * Whole frames go before the next part frame, and part streams take turns,
 * so a small frame never waits behind a whole large message. Messages about
 * one invocation keep their order: one queued behind an open part stream
 * about the same invocation waits until that stream's last part, so a late
 * frame or a log never overtakes its own large result. */
export class Sender {
  private small: Uint8Array[] = [];
  private streams: OutStream[] = [];
  private latest = new Map<string, OutStream>();
  private current: Uint8Array | null = null;
  private nextStream = 0;
  private flushed: (() => void)[] = [];
  closed = false;

  constructor(
    private write: (b: Uint8Array) => number,
    public maxMessage = DEFAULT_MAX_MESSAGE,
    private maxFrame = MAX_FRAME,
    private chunk = PART_CHUNK,
  ) {}

  /** Queues a message; throws message_too_large without queuing it. */
  send(m: Outgoing) {
    const payload = encode(m);
    if (payload.length > this.maxMessage) {
      throw new ProtocolError("message_too_large", `${m.t} message of ${payload.length} bytes exceeds ${this.maxMessage}`);
    }
    if (this.closed) return;
    const about = ABOUT_INVOCATION.has(m.t) && typeof m.id === "string" ? m.id : undefined;
    const q: Queued =
      payload.length <= this.maxFrame
        ? { frame: frame(payload) }
        : { stream: { id: String(++this.nextStream), about, payload, off: 0, seq: 0, after: [] } };
    const held = about === undefined ? undefined : this.latest.get(about);
    if (held) held.after.push(q);
    else this.enqueue(q);
    if ("stream" in q && about !== undefined) this.latest.set(about, q.stream);
    this.flush();
  }

  /** Writes until the socket stops accepting; call again on drain. */
  flush() {
    while (!this.closed) {
      if (!this.current) this.current = this.next();
      if (!this.current) break;
      const n = this.write(this.current);
      if (n < 0) {
        this.closed = true;
        break;
      }
      if (n < this.current.length) {
        this.current = this.current.subarray(n);
        return;
      }
      this.current = null;
    }
    const cbs = this.flushed;
    this.flushed = [];
    for (const cb of cbs) cb();
  }

  /** Calls cb once everything queued so far is written. */
  whenFlushed(cb: () => void) {
    if (!this.current && this.small.length === 0 && this.streams.length === 0) cb();
    else this.flushed.push(cb);
  }

  private enqueue(q: Queued) {
    if ("frame" in q) this.small.push(q.frame);
    else this.streams.push(q.stream);
  }

  /** Releases what waited behind a finished stream: whole frames up to the
   * next stream, which starts and keeps the rest behind it. */
  private finish(s: OutStream) {
    if (s.about !== undefined && this.latest.get(s.about) === s) this.latest.delete(s.about);
    for (let i = 0; i < s.after.length; i++) {
      const q = s.after[i];
      this.enqueue(q);
      if ("stream" in q) {
        q.stream.after.push(...s.after.slice(i + 1));
        return;
      }
    }
  }

  private next(): Uint8Array | null {
    if (this.small.length === 1) return this.small.shift()!;
    if (this.small.length > 1) {
      const all = Buffer.concat(this.small);
      this.small = [];
      return all;
    }
    const s = this.streams.shift();
    if (!s) return null;
    const end = Math.min(s.off + this.chunk, s.payload.length);
    const f = frame(encode({ t: "part", id: s.id, seq: s.seq, last: end === s.payload.length, chunk: toBase64(s.payload.subarray(s.off, end)) }));
    s.off = end;
    s.seq++;
    if (s.off < s.payload.length) this.streams.push(s);
    else this.finish(s);
    return f;
  }
}
