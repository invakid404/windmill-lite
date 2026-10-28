// The Bun harness runtime: the harness side of ADR 0004's protocol. It
// greets the engine, runs invocations concurrently on one event loop,
// reports health, turns a cancel into an abandoned invocation whose end is
// reported late, and exits after drain.
//
// This is milestone C1's skeleton. The step runner below only loads a
// bundled module and calls one export with the argument object. Binding by
// signature, Date hydration, Windmill's result and error serialisation,
// process.exit interception, process-level error handlers and per-invocation
// env, log and fetch views come with milestones C3a and C3b.

import { join } from "node:path";
import { pathToFileURL } from "node:url";
import {
  Assembler,
  decodeAssembled,
  decodeFrame,
  FrameReader,
  type Health,
  type Message,
  type Outgoing,
  ProtocolError,
  Raw,
  Sender,
} from "./frame.ts";

/** The protocol version this harness speaks. */
export const PROTOCOL_VERSION = 1;
/** The harness's own version; packaging (milestone F4) stamps it. */
export const HARNESS_VERSION = "0.0.0-c1";

/** Exit codes of the harness process. */
export const EXIT = {
  /** After drain, or when the engine's end of the socket closed. */
  ok: 0,
  /** The engine rejected the harness, or its welcome is unusable. */
  rejected: 2,
  /** The engine violated the protocol. */
  protocol: 3,
  /** No frame that ends an invocation fits max_message, so the engine
   * could never learn how it ended; it settles it with the process. */
  unreportable: 5,
} as const;

export type Transport = {
  /** Writes bytes; returns how many were accepted, or -1 when closed. */
  write(b: Uint8Array): number;
  /** Closes the harness's end. */
  end(): void;
};

export type Options = {
  bundleDir: string;
  bundleDigest: string;
  transport: Transport;
  /** Ends the process; tests replace it. */
  exit: (code: number) => void;
  /** Writes a diagnostic line no invocation owns. */
  diag?: (line: string) => void;
};

type Invocation = {
  id: string;
  abandoned: boolean;
  controller: AbortController;
};

type Outcome =
  | { outcome: "ok"; body: string }
  | { outcome: "error"; body: string }
  | { outcome: "escaped"; tail: string; exit_code: number };

export class Runtime {
  private reader = new FrameReader();
  private assembler = new Assembler();
  private sender: Sender;
  private welcomed = false;
  private stopped = false;
  private byeSent = false;
  private invocations = new Map<string, Invocation>();
  private modules = new Map<string, Promise<Record<string, unknown>>>();
  private heartbeat: ReturnType<typeof setInterval> | undefined;
  private pingSeq = 0;
  private lagMs = 0;
  private diag: (line: string) => void;

  constructor(private opts: Options) {
    this.sender = new Sender((b) => opts.transport.write(b));
    this.diag = opts.diag ?? ((line) => process.stderr.write(line + "\n"));
  }

  /** Sends hello; the engine answers with welcome. */
  start() {
    this.send({
      t: "hello",
      proto: PROTOCOL_VERSION,
      harness: { kind: "bun", version: HARNESS_VERSION },
      runtime: Bun.version,
      caps: { fetch: false, native: false, fresh: false, host: false },
      bundle: this.opts.bundleDigest,
      pid: process.pid,
    });
  }

  /** Bytes from the engine. */
  onData(data: Uint8Array) {
    if (this.stopped) return;
    try {
      for (const payload of this.reader.push(data)) {
        let m = decodeFrame(payload, this.assembler.maxMessage);
        if (m.t === "part") {
          const full = this.assembler.add(m);
          if (!full) continue;
          m = decodeAssembled(full);
        }
        this.handle(m);
      }
    } catch (e) {
      this.protocolError(e);
    }
  }

  /** The socket accepts bytes again. */
  onDrain() {
    this.sender.flush();
  }

  /** The engine's end closed: nobody is left to report to. */
  onClose() {
    if (this.stopped) return;
    try {
      this.reader.end();
    } catch (e) {
      this.diag(`windmill-lite harness: ${(e as Error).message}`);
    }
    this.stop(EXIT.ok);
  }

  private handle(m: Message) {
    if (!this.welcomed) {
      if (m.t !== "welcome") throw new ProtocolError("unexpected_message", `${m.t} before welcome`);
      this.onWelcome(m);
      return;
    }
    switch (m.t) {
      case "invoke":
        this.invoke(m);
        break;
      case "cancel":
        this.cancel(m);
        break;
      case "ping":
        this.send({ t: "pong", id: m.id, ...this.health() });
        break;
      case "pong":
        break;
      case "drain":
        this.drain();
        break;
      default:
        throw new ProtocolError("unexpected_message", `${m.t} from the engine`);
    }
  }

  private onWelcome(m: Message) {
    if (m.reject) {
      this.diag(`windmill-lite harness: rejected: ${m.reject.code}: ${m.reject.message}`);
      this.stop(EXIT.rejected);
      return;
    }
    const c = m.config;
    if (m.proto !== PROTOCOL_VERSION || c.module_state !== "warm" || c.heartbeat_ms < 1 || c.max_message < 1) {
      this.diag(`windmill-lite harness: unusable welcome: proto ${m.proto}, config ${JSON.stringify(c)}`);
      this.stop(EXIT.rejected);
      return;
    }
    this.welcomed = true;
    this.sender.maxMessage = c.max_message;
    this.assembler.maxMessage = c.max_message;
    this.startHeartbeat(c.heartbeat_ms);
  }

  /** Pings the engine every interval with this process's health. The
   * engine's watchdog measures silence from the frames it receives, so a
   * harness whose event loop runs keeps it quiet; one that is blocked
   * cannot. Lag is how late the timer fired. */
  private startHeartbeat(intervalMs: number) {
    let last = performance.now();
    this.heartbeat = setInterval(() => {
      const now = performance.now();
      this.lagMs = Math.max(0, Math.round(now - last - intervalMs));
      last = now;
      this.send({ t: "ping", id: `h${++this.pingSeq}`, ...this.health() });
    }, intervalMs);
  }

  private health(): Health {
    let inFlight = 0;
    let abandoned = 0;
    for (const inv of this.invocations.values()) {
      if (inv.abandoned) abandoned++;
      else inFlight++;
    }
    return { in_flight: inFlight, abandoned, errors: 0, lag_ms: this.lagMs, rss: process.memoryUsage.rss() };
  }

  private invoke(m: Message) {
    if (this.invocations.has(m.id)) throw new ProtocolError("unexpected_message", `invocation ${JSON.stringify(m.id)} is already in flight`);
    const inv: Invocation = { id: m.id, abandoned: false, controller: new AbortController() };
    this.invocations.set(m.id, inv);
    // Nothing that goes wrong while one invocation ends may end the
    // process: it would take every co-resident invocation with it.
    this.run(m)
      .then((o) => this.finish(inv, o))
      .catch((e) => {
        this.diag(`windmill-lite harness: invocation ${JSON.stringify(inv.id)}: ${describe(e)}`);
        this.finish(inv, { outcome: "escaped", tail: "", exit_code: 1 });
      });
  }

  /** The skeleton step runner. */
  private async run(m: Message): Promise<Outcome> {
    let mod: Record<string, unknown>;
    try {
      mod = await this.load(m.script);
    } catch (e) {
      // A module that fails to load or evaluate escapes the wrapper
      // (WR-19); its tail is the error's text until C3a attributes logs.
      return { outcome: "escaped", tail: describe(e), exit_code: 1 };
    }
    const fn = mod[m.entry];
    if (typeof fn !== "function") {
      return caught(new Error(`${m.entry} function is missing`));
    }
    let value: unknown;
    try {
      value = await fn(m.args);
    } catch (e) {
      if (e === null || e === undefined) return { outcome: "escaped", tail: "", exit_code: 1 };
      return caught(e);
    }
    let body: string | undefined;
    try {
      body = JSON.stringify(value ?? null);
    } catch (e) {
      return caught(e);
    }
    if (body === undefined) return caught(new TypeError("the result has no JSON form"));
    return { outcome: "ok", body };
  }

  /** Loads a script's bundled module once per process (warm module state,
   * ADR 0006), on its first invocation. */
  private load(script: string): Promise<Record<string, unknown>> {
    let p = this.modules.get(script);
    if (!p) {
      p = scriptPath(this.opts.bundleDir, script).then((path) => import(pathToFileURL(path).href));
      this.modules.set(script, p);
    }
    return p;
  }

  private cancel(m: Message) {
    const inv = this.invocations.get(m.id);
    if (!inv || inv.abandoned) return;
    inv.abandoned = true;
    inv.controller.abort(m.reason);
  }

  /** Reports how the entrypoint call ended: as the result, or, for an
   * abandoned invocation, as the late frame that settles it. The invocation
   * stays in flight until one such frame is queued. When the outcome is
   * larger than max_message, a smaller frame that fits takes its place: an
   * escaped result whose tail says why (milestone C3a decides the final
   * mapping), then one with no tail; for an abandoned call, a late error
   * without a body. If none fits, the connection ends, and the engine
   * settles the invocation with the process. */
  private finish(inv: Invocation, o: Outcome) {
    if (this.stopped || this.byeSent || this.invocations.get(inv.id) !== inv) return;
    for (const msg of this.terminalFrames(inv, o)) {
      try {
        this.sender.send(msg);
        this.invocations.delete(inv.id);
        return;
      } catch (e) {
        this.diag(`windmill-lite harness: ${e instanceof ProtocolError ? e.message : describe(e)}`);
      }
    }
    this.diag(`windmill-lite harness: no frame that ends invocation ${JSON.stringify(inv.id)} fits; ending the connection`);
    this.stop(EXIT.unreportable);
  }

  private terminalFrames(inv: Invocation, o: Outcome): Outgoing[] {
    if (inv.abandoned) {
      const kind = o.outcome === "ok" ? "result" : "error";
      const full: Outgoing = { t: "late", id: inv.id, kind, body: "body" in o ? new Raw(o.body) : undefined, settled: true };
      return [full, { t: "late", id: inv.id, kind: "error", settled: true }];
    }
    const escaped = (tail: string): Outgoing => ({ t: "result", id: inv.id, outcome: "escaped", tail, exit_code: 1, settled: true });
    return [
      { t: "result", id: inv.id, ...rawBody(o), settled: true },
      escaped(`result exceeds the protocol's maximum message size of ${this.sender.maxMessage} bytes`),
      escaped(""),
    ];
  }

  /** bye is the last frame: heartbeats stop, everything queued goes out
   * first, and nothing is sent after it. */
  private drain() {
    if (this.heartbeat) clearInterval(this.heartbeat);
    this.heartbeat = undefined;
    this.sender.whenFlushed(() => {
      this.send({ t: "bye" });
      this.byeSent = true;
      this.sender.whenFlushed(() => this.stop(EXIT.ok));
    });
  }

  private send(m: Outgoing) {
    if (!this.stopped && !this.byeSent) this.sender.send(m);
  }

  private protocolError(e: unknown) {
    this.diag(`windmill-lite harness: ${e instanceof ProtocolError ? e.message : describe(e)}`);
    this.stop(EXIT.protocol);
  }

  private stop(code: number) {
    if (this.stopped) return;
    this.stopped = true;
    if (this.heartbeat) clearInterval(this.heartbeat);
    for (const inv of this.invocations.values()) inv.controller.abort("harness_exit");
    this.opts.transport.end();
    this.opts.exit(code);
  }
}

function rawBody(o: Outcome): Record<string, unknown> {
  if (o.outcome === "escaped") return { outcome: o.outcome, tail: o.tail, exit_code: o.exit_code };
  return { outcome: o.outcome, body: new Raw(o.body) };
}

/** The outcome of a caught throw: an error with message, name and stack,
 * the first fields of WR-18's shape (step_id and extra are milestone
 * C3a's); fields a value does not have are left out, as JSON.stringify
 * leaves them. When that object cannot be built or serialised (a throwing
 * getter, a BigInt or a cycle in it), the failure escapes the error shaping,
 * as in WR-19. */
function caught(e: unknown): Outcome {
  try {
    const x = e as { message?: unknown; name?: unknown; stack?: unknown };
    const body = JSON.stringify({ message: x.message, name: x.name, stack: x.stack });
    return { outcome: "error", body };
  } catch (inner) {
    return { outcome: "escaped", tail: describe(inner), exit_code: 1 };
  }
}

function describe(e: unknown): string {
  try {
    if (e instanceof Error) return e.stack ?? `${e.name}: ${e.message}`;
    return String(e);
  } catch {
    return "(an error that cannot be printed)";
  }
}

/** The module of an inventory id: scripts/bun/<id>.mjs in the bundle
 * (ADR 0010). An id is one or more `/`-separated segments, none empty,
 * `.` or `..`. */
async function scriptPath(bundleDir: string, script: string): Promise<string> {
  const segs = script.split("/");
  if (script === "" || segs.some((s) => s === "" || s === "." || s === ".." || s.includes("\\") || s.includes("\0"))) {
    throw new Error(`invalid script id ${JSON.stringify(script)}`);
  }
  return join(bundleDir, "scripts", "bun", ...segs) + ".mjs";
}
