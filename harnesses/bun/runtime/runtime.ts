// The Bun harness runtime: the harness side of ADR 0004's protocol. It
// greets the engine, runs invocations concurrently on one event loop, each
// inside its own context (context.ts), reports health, turns a cancel into
// an abandoned invocation whose end is reported late, and exits after
// drain. In host mode (the host SDK, ../host) the process is also the host:
// it submits runs, and each invocation of a run re-enters the scope the
// host had when it submitted it (H11).
//
// The step runner applies Windmill's Bun wrapper rules (wrapper.ts,
// milestone C3a): the entrypoint's signature from the bundle, binding and
// Date hydration, the result and error serialisation, the missing-export
// error, process.exit interception (D11) and the attribution of
// process-level errors to the invocation whose context raised them. The
// walking skeleton (W1) added the per-invocation environment, the facade
// fetch, the process-level handlers and host mode. Milestone C3b added
// per-invocation logs, child-process wrapping, the settle-time fence (D9)
// with its report of the work an invocation leaves behind, and fresh mode:
// one invocation per process, which ends with it as Windmill's job process
// does.

import { AsyncLocalStorage } from "node:async_hooks";
import { copyFileSync, mkdirSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { pathToFileURL } from "node:url";
import {
  Assembler,
  decodeAssembled,
  decodeFrame,
  encode,
  FrameReader,
  type Health,
  type Message,
  type Outgoing,
  ProtocolError,
  Raw,
  Sender,
} from "./frame.ts";
import {
  current,
  exitProcess,
  installChildProcesses,
  installEnvView,
  installExit,
  installFetch,
  installLogs,
  installProcessHandlers,
  installProcessScope,
  installTimers,
  type InvocationContext,
  InvocationLog,
  InvocationResources,
  invocations,
  type PlatformAnswer,
  type PlatformCall,
  ProcessExit,
  realStderr,
  realTimers,
  shadow,
} from "./context.ts";
import { bind, errorText, patchBigInt, resultText, type Signature, signatureErrorText, signatureOf } from "./wrapper.ts";

export { TAIL_LENGTH } from "./context.ts";

/** The protocol version this harness speaks. */
export const PROTOCOL_VERSION = 1;
/** The harness's own version; packaging (milestone F4) stamps it. */
export const HARNESS_VERSION = "0.0.0-c1";
/** The code of a warning frame for a process-level error that no
 * invocation's context raised (ADR 0004, the shared-process failure model,
 * item 3). */
export const UNATTRIBUTED = "unattributed_error";
/** The code of the warning that reports what an invocation left behind at
 * its settlement, which the D9 fence then revoked: its left field counts
 * the handles and requests by kind. */
export const LEFT_BEHIND = "left_behind";
/** The most bytes of one log frame; longer batches are split. */
const LOG_FRAME = 64 * 1024;

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
  /** Writes all of b before it returns, waiting for the socket if need
   * be, or throws. A fresh process that has one ends synchronously,
   * through the real process.exit, once its frames are written: the step's
   * code never runs again (ADR 0004, failure model item 8). */
  writeSync?(b: Uint8Array): void;
  /** Closes the harness's end. */
  end(): void;
};

export type Options = {
  bundleDir: string;
  bundleDigest: string;
  transport: Transport;
  /** Ends the process; tests replace it. In host mode it is called instead
   * when the connection to the engine ends. */
  exit: (code: number) => void;
  /** Writes a diagnostic line no invocation owns. */
  diag?: (line: string) => void;
  /** Host mode: this process is the host, and submits runs (ADR 0009). */
  host?: boolean;
  /** Called once the engine's welcome has been accepted. */
  onWelcome?: () => void;
};

/** An invocation from invoke until the frame that settles it is queued:
 * running while its outcome is open, fixed once an intercepted
 * process.exit or an attributed process-level error has reported its
 * outcome with the call still going on, abandoned once the engine
 * canceled it. A fixed or abandoned invocation's end is reported late. */
type Invocation = {
  id: string;
  state: "running" | "fixed" | "abandoned";
  /** When the engine abandoned it (performance.now()). */
  abandonedAt?: number;
  controller: AbortController;
  /** Its registry in its async-context store, settled with it. */
  resources: InvocationResources;
  /** Its log, closed when it settles. */
  log: InvocationLog;
};

/** The module-state mode of the process (ADR 0006): a warm process serves
 * any number of invocations; a fresh one serves one, and ends with it. */
export type Mode = "warm" | "fresh";

/** A run's outcome, as run_done reports it. result is its JSON text. */
export type RunOutcome = { status: string; result?: string };

/** The health of this process: what pings report, plus the age of the
 * oldest abandoned invocation, which the host SDK's health() exposes. */
export type HostHealth = Health & { oldest_abandoned_ms: number };

/** How an invocation ended. An escaped one's tail is the invocation's log
 * when its frame is sent (WR-20), after print, the error the process would
 * have printed on its way out, has been added to the log. */
type Outcome =
  | { outcome: "ok"; body: string }
  | { outcome: "error"; body: string }
  | { outcome: "escaped"; exit_code: number; print?: string }
  | { outcome: "empty" };

/** A platform call waiting for its reply; DISCARD once its caller stopped
 * waiting, so that the reply, which the engine sends anyway, is dropped. */
type PendingCall = { resolve: (a: PlatformAnswer) => void; reject: (e: unknown) => void } | typeof DISCARD;
const DISCARD = Symbol("discard");

export class Runtime {
  private reader = new FrameReader();
  private assembler = new Assembler();
  private sender: Sender;
  private welcomed = false;
  private stopped = false;
  private byeSent = false;
  private mode: Mode = "warm";
  /** A fresh process's one invocation has been invoked. */
  private served = false;
  /** A fresh process is ending: it sends nothing more, and stops once what
   * it queued has been written. */
  private exiting = false;
  private invocations = new Map<string, Invocation>();
  private modules = new Map<string, Promise<Record<string, unknown>>>();
  private metas = new Map<string, Promise<unknown>>();
  /** The working directory the process started in (ADR 0004): codebase
   * modules load from copies there. */
  private workdir = process.cwd();
  private heartbeat: ReturnType<typeof setInterval> | undefined;
  private pingSeq = 0;
  private lagMs = 0;
  private errors = 0;
  private facade: string | undefined;
  private platformSeq = 0;
  private platformCalls = new Map<string, PendingCall>();
  private runs = new Map<string, { resolve: (o: RunOutcome) => void; reject: (e: Error) => void }>();
  private scopes = new Map<string, <R>(fn: () => R) => R>();
  private diag: (line: string) => void;

  constructor(private opts: Options) {
    this.sender = new Sender((b) => opts.transport.write(b));
    // Diagnostics go to the process's own stderr, never to the log of the
    // invocation whose context the runtime happens to run in, even when the
    // host's own diag writes through console or process.stderr.
    const diag = opts.diag ?? ((line: string) => realStderr(line + "\n"));
    this.diag = (line) => invocations.exit(() => diag(line));
    installEnvView();
    installProcessScope();
    installTimers();
    installLogs();
    installChildProcesses();
    installFetch(
      () => this.facade,
      (c, signal) => this.platformCall(c, signal),
    );
    installProcessHandlers((kind, e) => this.onProcessError(kind, e));
    installExit((c, status) => this.onExit(c, status));
  }

  /** Sends hello; the engine answers with welcome. */
  start() {
    this.send({
      t: "hello",
      proto: PROTOCOL_VERSION,
      harness: { kind: "bun", version: HARNESS_VERSION },
      runtime: Bun.version,
      // Host mode always runs warm (ADR 0006): the steps run in the host.
      caps: { fetch: true, native: false, fresh: this.opts.host !== true, host: this.opts.host === true },
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
      case "platform_reply":
        this.onPlatformReply(m);
        break;
      case "run_done":
        this.onRunDone(m);
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
    const modes = this.opts.host ? ["warm"] : ["warm", "fresh"];
    if (m.proto !== PROTOCOL_VERSION || !modes.includes(c.module_state) || c.heartbeat_ms < 1 || c.max_message < 1) {
      this.diag(`windmill-lite harness: unusable welcome: proto ${m.proto}, config ${JSON.stringify(c)}`);
      this.stop(EXIT.rejected);
      return;
    }
    this.welcomed = true;
    this.mode = c.module_state;
    this.facade = c.facade;
    this.sender.maxMessage = c.max_message;
    this.assembler.maxMessage = c.max_message;
    this.startHeartbeat(c.heartbeat_ms);
    this.opts.onWelcome?.();
  }

  /** Pings the engine every interval with this process's health. The
   * engine's watchdog measures silence from the frames it receives, so a
   * harness whose event loop runs keeps it quiet; one that is blocked
   * cannot. Lag is how late the timer fired. The timer is the runtime's
   * own, outside every invocation's fence. */
  private startHeartbeat(intervalMs: number) {
    let last = performance.now();
    this.heartbeat = realTimers.setInterval(() => {
      const now = performance.now();
      this.lagMs = Math.max(0, Math.round(now - last - intervalMs));
      last = now;
      this.send({ t: "ping", id: `h${++this.pingSeq}`, ...this.health() });
    }, intervalMs);
  }

  private health(): Health {
    const { oldest_abandoned_ms: _, ...h } = this.hostHealth();
    return h;
  }

  /** Invocations in flight and abandoned, unattributed process-level
   * errors, event-loop lag, RSS, and how long ago the oldest abandoned
   * invocation was abandoned (0 when none is). */
  hostHealth(): HostHealth {
    let inFlight = 0;
    let abandoned = 0;
    let oldest = 0;
    const now = performance.now();
    for (const inv of this.invocations.values()) {
      if (inv.state === "running") inFlight++;
      if (inv.state !== "abandoned") continue;
      abandoned++;
      oldest = Math.max(oldest, Math.round(now - (inv.abandonedAt ?? now)));
    }
    return { in_flight: inFlight, abandoned, errors: this.errors, lag_ms: this.lagMs, rss: process.memoryUsage.rss(), oldest_abandoned_ms: oldest };
  }

  private invoke(m: Message) {
    if (this.invocations.has(m.id)) throw new ProtocolError("unexpected_message", `invocation ${JSON.stringify(m.id)} is already in flight`);
    if (this.mode === "fresh") {
      if (this.served) throw new ProtocolError("unexpected_message", "a fresh process serves one invocation");
      this.served = true;
    }
    const resources = new InvocationResources();
    const inv: Invocation = { id: m.id, state: "running", controller: new AbortController(), resources, log: undefined! };
    // A log frame goes out once the code that wrote the lines has run on,
    // so lines written together travel together.
    inv.log = new InvocationLog(() => queueMicrotask(() => this.sendLog(inv)));
    this.invocations.set(m.id, inv);
    const env: Record<string, string> = Object.assign(Object.create(null), m.env);
    shadow(env);
    const ctx: InvocationContext = { id: m.id, run: m.run, env, signal: inv.controller.signal, resources, work: resources, log: inv.log };
    // The module loads and the entrypoint runs inside the invocation's
    // context, and in host mode inside the scope the host had when it
    // submitted the run.
    const go = () => invocations.run(ctx, () => this.run(m));
    const scope = this.scopes.get(m.run);
    // Nothing that goes wrong while one invocation ends may end the
    // process: it would take every co-resident invocation with it.
    (scope ? scope(go) : go())
      .then((o) => this.finish(inv, o))
      .catch((e) => {
        this.diag(`windmill-lite harness: invocation ${JSON.stringify(inv.id)}: ${describe(e)}`);
        this.finish(inv, { outcome: "escaped", exit_code: 1 });
      });
  }

  /** Runs one invocation with the wrapper's rules, in the wrapper's order
   * (WR-2). Its module's top-level code, and a failure to load it, come
   * first; then the export is checked, the arguments are bound and
   * hydrated, and the entrypoint is called and awaited, its result
   * serialised. A throw from any of those is caught and shaped (WR-18). */
  private async run(m: Message): Promise<Outcome> {
    let sig: Signature | undefined;
    let meta: unknown;
    try {
      meta = await this.meta(m.script);
      sig = signatureOf(meta, m.entry);
    } catch (e) {
      return escaped(`windmill-lite: cannot read the metadata of script ${JSON.stringify(m.script)}: ${describe(e)}`);
    }
    // Windmill's worker fails the job before its process starts when the
    // entrypoint's signature does not parse (WR-7, WR-37), so the module
    // never loads.
    if (sig?.error !== undefined) return { outcome: "error", body: signatureErrorText(sig.error, stepId()) };
    let mod: Record<string, unknown>;
    try {
      mod = await this.load(m.script, meta);
    } catch (e) {
      // A module that fails to load or evaluate escapes the wrapper
      // (WR-19), and Bun prints its error on the way out, so the error
      // ends the log its tail is taken from. One that called process.exit
      // while it evaluated ends as the exit did, for every invocation of it
      // (D11).
      if (e instanceof ProcessExit) return exited(e.status);
      return escaped(describe(e));
    }
    const fn = mod[m.entry];
    if (typeof fn === "function" && sig === undefined) {
      // A Bun-built module's metadata signs every export of its bundle. A
      // codebase module's cannot always: CommonJS source has no static
      // export list, so a callable export its source does not declare gets
      // an empty parameter list, as Windmill's parser gives a function it
      // does not find in the script's content (WR-6, WR-7).
      if (!isCodebase(meta)) {
        return escaped(`windmill-lite: the bundle records no signature for export ${JSON.stringify(m.entry)} of script ${JSON.stringify(m.script)}`);
      }
      sig = { params: [] };
    }
    let value: unknown;
    try {
      // The export check happens where the wrapper makes it, after the
      // import (ADR 0004, entrypoints), in the wrapper-catch shape.
      if (typeof fn !== "function") throw new Error(`${m.entry} function is missing`);
      const argv = bind(sig!.params!, m.args as Record<string, unknown>);
      patchBigInt();
      value = await fn(...argv);
    } catch (e) {
      if (e instanceof ProcessExit) return exited(e.status);
      return caught(e);
    }
    try {
      return { outcome: "ok", body: resultText(value) };
    } catch (e) {
      return caught(e);
    }
  }

  /** Loads a script's bundled module once per process (warm module state,
   * ADR 0006), on its first invocation, from the file its metadata names
   * (moduleFile). The module evaluates in that invocation's context, so
   * its top-level code reads the invocation's environment (WA-1) and
   * writes to its log, and a throw fails it; but the timers, requests and
   * children its evaluation starts belong to the module, which outlives
   * the invocation (D1), so they are registered in no invocation and never
   * fenced (ADR 0004, loading). */
  private load(script: string, meta: unknown): Promise<Record<string, unknown>> {
    let p = this.modules.get(script);
    if (!p) {
      const c = current();
      const evaluate = () => moduleFile(this.opts.bundleDir, script, meta, this.workdir).then((path) => import(pathToFileURL(path).href));
      p = c === undefined ? evaluate() : invocations.run({ ...c, work: undefined }, evaluate);
      this.modules.set(script, p);
    }
    return p;
  }

  /** Reads a script's metadata (scripts/bun/<id>.meta.json, ADR 0010)
   * once per process: the signature of each of its exports, which the
   * build recorded (ADR 0002). */
  private meta(script: string): Promise<unknown> {
    let p = this.metas.get(script);
    if (!p) {
      p = scriptPath(this.opts.bundleDir, script, ".meta.json").then((path) => Bun.file(path).json());
      this.metas.set(script, p);
    }
    return p;
  }

  /** An intercepted process.exit (D11). In a fresh process the process is
   * the invocation, so it exits, as on Windmill, once the log has gone
   * out. In a running invocation it fixes the outcome: empty for status 0,
   * since no result was written (WR-V2), escaped with the status
   * otherwise. The call goes on until the sentinel ends it or is caught,
   * and its end is reported late. In an invocation whose outcome is
   * already fixed, that was abandoned, or that has settled, it changes
   * nothing and is reported as a late error. */
  private onExit(c: InvocationContext, status: number) {
    const inv = c.resources.settled ? undefined : this.invocations.get(c.id);
    if (this.mode === "fresh") {
      this.exitNow(status);
      return;
    }
    if (inv?.state === "running") {
      this.fix(inv, exited(status));
      return;
    }
    this.lateError(c.id, new ProcessExit(status));
  }

  /** Fresh mode: ends the process with status, as the job's process would
   * end, once what its invocation logged has been written, and sends
   * nothing else: no result, late frame or warning. With a synchronous
   * transport the process ends before this returns, so the step's code,
   * its catch and finally blocks included, never runs again (WR-2); a
   * second call, or one after the connection ended, changes nothing. */
  private exitNow(status: number) {
    if (this.stopped || this.exiting) return;
    for (const inv of this.invocations.values()) {
      this.sendLog(inv, true);
      inv.resources.settle();
      inv.log.close();
    }
    this.end(status);
  }

  /** Ends a fresh process with status once everything queued has been
   * written: at once, through the synchronous transport, when there is one
   * (the harness's); otherwise when the sender has flushed. */
  private end(status: number) {
    this.exiting = true;
    const writeSync = this.opts.transport.writeSync;
    if (writeSync) {
      try {
        this.sender.drainSync((b) => writeSync(b));
      } catch (e) {
        this.diag(`windmill-lite harness: the last frames were not written: ${describe(e)}`);
      }
      exitProcess(status);
    }
    this.sender.whenFlushed(() => this.stop(status));
  }

  /** A process-level error (ADR 0004, the shared-process failure model).
   * One raised in an invocation's context is attributed to it: a running
   * invocation fails as escaped, with exit code 1 (Q-WR-2) and the error's
   * text at the end of its log, while its call may go on; for any other,
   * it is a late error, and its outcome stands (D9). One raised in no
   * invocation's context fails nothing: it is counted in health, written
   * to the diagnostics, and attached as a warning to every invocation in
   * flight. In a fresh process either ends the process with exit code 1,
   * as on Windmill, where nothing handles them. */
  private onProcessError(kind: string, e: unknown) {
    // A handler must never throw: its exception would reach the other
    // handler, and one thrown from there ends the process (Bun exits 7),
    // which item 1 forbids.
    try {
      this.processError(kind, e);
    } catch (err) {
      try {
        this.diag(`windmill-lite harness: could not report a ${kind}: ${describe(err)}`);
      } catch {
        // Nothing is left to report it to.
      }
    }
  }

  private processError(kind: string, e: unknown) {
    const c = current();
    if (this.mode === "fresh") {
      this.diag(`windmill-lite harness: ${kind}: ${describe(e)}`);
      for (const inv of this.invocations.values()) inv.log.lines(describe(e));
      this.exitNow(1);
      return;
    }
    if (c !== undefined) {
      this.diag(`windmill-lite harness: ${kind} in invocation ${JSON.stringify(c.id)}: ${describe(e)}`);
      const inv = c.resources.settled ? undefined : this.invocations.get(c.id);
      if (inv?.state === "running") this.fix(inv, escaped(describe(e)));
      else this.lateError(c.id, e);
      return;
    }
    this.errors++;
    this.diag(`windmill-lite harness: ${kind} that no invocation owns: ${describe(e)}`);
    const message = `${kind}: ${describe(e)}`;
    for (const inv of this.invocations.values()) {
      // What the invocation wrote before the error goes out first.
      this.sendLog(inv);
      this.sendWarning(inv.id, message);
    }
  }

  /** Sends a warning, its message cut to fit max_message: the error's
   * text can be as long as the step made it. A warning that cannot fit at
   * all is only written to the diagnostics. It never throws. */
  private sendWarning(id: string, message: string) {
    const m = fitString({ t: "warning", id, code: UNATTRIBUTED, message: "" }, "message", message, this.sender.maxMessage);
    if (m === undefined) {
      this.diag(`windmill-lite harness: a warning for invocation ${JSON.stringify(id)} does not fit max_message`);
      return;
    }
    try {
      this.send(m);
    } catch (err) {
      this.diag(`windmill-lite harness: ${describe(err)}`);
    }
  }

  /** Sends the lines inv's log holds as log frames, each within
   * LOG_FRAME bytes and max_message; a line too long for one frame is cut,
   * ending in "…". With all, a line still unfinished goes too: the frame
   * that follows ends what the invocation said. It never throws. */
  private sendLog(inv: Invocation, all = false) {
    const lines = inv.log.take(all);
    if (lines.length === 0 || this.closing()) return;
    const limit = Math.min(LOG_FRAME, this.sender.maxMessage);
    const base = encode({ t: "log", id: inv.id, lines: [] }).length;
    let batch: string[] = [];
    let size = base;
    const flush = () => {
      if (batch.length === 0) return;
      try {
        this.sender.send({ t: "log", id: inv.id, lines: batch });
      } catch (e) {
        this.diag(`windmill-lite harness: a log frame of invocation ${JSON.stringify(inv.id)}: ${describe(e)}`);
      }
      batch = [];
      size = base;
    };
    for (let line of lines) {
      let n = Buffer.byteLength(JSON.stringify(line)) + 1;
      if (base + n > limit) {
        line = cutToFit(line, limit - base - 1);
        if (line === "") continue;
        n = Buffer.byteLength(JSON.stringify(line)) + 1;
      }
      if (size + n > limit) flush();
      batch.push(line);
      size += n;
    }
    flush();
  }

  /** The tail of an escaped outcome, and whatever ends the log before it:
   * the error the process would have printed, then every line not yet
   * sent, unfinished ones included, so that the log frames come before
   * the frame that reports the outcome. */
  private endLog(inv: Invocation, o: Outcome): string {
    if (o.outcome === "escaped" && o.print) inv.log.lines(o.print);
    this.sendLog(inv, true);
    return inv.log.tail();
  }

  /** Reports a running invocation's outcome while its call goes on: a
   * result frame with settled false. The call's end is reported late. */
  private fix(inv: Invocation, o: Outcome) {
    if (this.closing()) return;
    inv.state = "fixed";
    const tail = this.endLog(inv, o);
    for (const msg of [
      { t: "result", id: inv.id, ...rawBody(o, tail), settled: false },
      { t: "result", id: inv.id, outcome: "escaped", tail: "", exit_code: "exit_code" in o ? o.exit_code : 1, settled: false },
    ]) {
      try {
        this.sender.send(msg);
        return;
      } catch (e) {
        this.diag(`windmill-lite harness: ${e instanceof ProtocolError ? e.message : describe(e)}`);
      }
    }
    this.stop(EXIT.unreportable);
  }

  /** Reports an error of an invocation whose outcome is already fixed, as
   * a late error that settles nothing (D9). Its body is the error's WR-18
   * object, or none when that cannot be built. */
  private lateError(id: string, e: unknown) {
    let body: Raw | undefined;
    try {
      body = new Raw(errorText(e, stepId()));
    } catch {
      body = undefined;
    }
    const inv = this.invocations.get(id);
    if (inv) this.sendLog(inv, true);
    // One too large for max_message goes without its body.
    for (const b of body === undefined ? [undefined] : [body, undefined]) {
      try {
        this.send({ t: "late", id, kind: "error", body: b, settled: false });
        return;
      } catch (err) {
        this.diag(`windmill-lite harness: ${describe(err)}`);
      }
    }
  }

  /** Abandons a running invocation. A cancel that crossed the result of an
   * invocation whose outcome is already fixed changes nothing. */
  private cancel(m: Message) {
    const inv = this.invocations.get(m.id);
    if (inv?.state !== "running") return;
    inv.state = "abandoned";
    inv.abandonedAt = performance.now();
    inv.controller.abort(m.reason);
  }

  /** Sends a platform frame for a facade fetch and waits for its reply.
   * When signal aborts first, the call rejects with its reason and the
   * reply is dropped when it comes: the engine has the request already. */
  private platformCall(c: PlatformCall, signal: AbortSignal): Promise<PlatformAnswer> {
    if (this.closing()) return Promise.reject(new TypeError("windmill-lite: the engine connection is closed"));
    if (signal.aborted) return Promise.reject(signal.reason);
    // What the invocation wrote before the call goes out first.
    const inv = this.invocations.get(c.inv);
    if (inv) this.sendLog(inv);
    const id = `p${++this.platformSeq}`;
    return new Promise((resolve, reject) => {
      const call: PendingCall = { resolve, reject };
      this.platformCalls.set(id, call);
      try {
        this.send({ t: "platform", id, inv: c.inv, method: c.method, url: c.url, headers: c.headers, body: c.body });
      } catch (e) {
        this.platformCalls.delete(id);
        reject(e as Error);
        return;
      }
      signal.addEventListener(
        "abort",
        () => {
          if (this.platformCalls.get(id) !== call) return;
          this.platformCalls.set(id, DISCARD);
          reject(signal.reason);
        },
        { once: true },
      );
    });
  }

  private onPlatformReply(m: Message) {
    const call = this.platformCalls.get(m.id);
    if (!call) throw new ProtocolError("unexpected_message", `platform_reply for unknown request ${JSON.stringify(m.id)}`);
    this.platformCalls.delete(m.id);
    if (call !== DISCARD) call.resolve({ status: m.status, headers: m.headers, body: m.body });
  }

  /** Host mode: submits a run of the flow or script at path with args, a
   * JSON object's text, and resolves with its outcome. The run's id is a
   * fresh UUID, which the engine uses as the run's id. The scope the
   * caller has now is captured, and every invocation of the run re-enters
   * it (H11). intercept, a JSON object's text, maps module ids to the
   * results that stand in for them (H1's static form). */
  submit(kind: "flow" | "script", path: string, args: string, intercept?: string): { id: string; done: Promise<RunOutcome> } {
    if (!this.opts.host) throw new Error("windmill-lite: runs are submitted only in host mode");
    if (!this.welcomed || this.stopped) throw new Error("windmill-lite: the engine is not connected");
    const id = crypto.randomUUID();
    const scope = AsyncLocalStorage.snapshot();
    const done = new Promise<RunOutcome>((resolve, reject) => {
      this.runs.set(id, { resolve, reject });
      this.scopes.set(id, scope);
      try {
        this.send({ t: "run", id, kind, path, args: new Raw(args), intercept: intercept === undefined ? undefined : new Raw(intercept) });
      } catch (e) {
        this.runs.delete(id);
        this.scopes.delete(id);
        reject(e as Error);
      }
    });
    return { id, done };
  }

  /** Host mode: cancels a run this process submitted. */
  cancelRun(id: string) {
    if (this.runs.has(id)) this.send({ t: "run_cancel", id });
  }

  private onRunDone(m: Message) {
    const run = this.runs.get(m.id);
    if (!this.opts.host || !run) throw new ProtocolError("unexpected_message", `run_done for unknown run ${JSON.stringify(m.id)}`);
    this.runs.delete(m.id);
    this.scopes.delete(m.id);
    run.resolve({ status: m.status, result: m.result === undefined ? undefined : JSON.stringify(m.result) });
  }

  /** Reports how the entrypoint call ended: as the result, or, for an
   * invocation that was abandoned or whose outcome was fixed early, as the
   * late frame that settles it. The invocation stays in flight until one
   * such frame is queued. When the outcome is larger than max_message, a
   * smaller frame that fits takes its place: an escaped result whose tail
   * says why, then one with no tail; for a late end, a late error without
   * a body. If none fits, the connection ends, and the engine settles the
   * invocation with the process. A result above max_message is refused
   * by the protocol's limit, not by Windmill's size limits, which are the
   * engine's at completion (WR-15).
   *
   * Settling it is the D9 fence: what the invocation still has registered
   * is reported first, in a left_behind warning, and then revoked; its log
   * is closed. A fresh process then ends, as the job's process ends right
   * after the wrapper writes its result (WR-2). */
  private finish(inv: Invocation, o: Outcome) {
    if (this.closing() || this.invocations.get(inv.id) !== inv) return;
    const tail = this.endLog(inv, o);
    const left = inv.resources.pending();
    if (Object.keys(left).length > 0) {
      const counts = Object.entries(left)
        .map(([k, n]) => `${n} ${k}`)
        .join(", ");
      try {
        this.send({ t: "warning", id: inv.id, code: LEFT_BEHIND, message: `left at settlement: ${counts}`, left });
      } catch (e) {
        this.diag(`windmill-lite harness: ${describe(e)}`);
      }
    }
    for (const msg of this.terminalFrames(inv, o, tail)) {
      try {
        this.sender.send(msg);
        this.invocations.delete(inv.id);
        inv.resources.settle();
        inv.log.close();
        // A fresh process ends once its result has been written, as the
        // wrapper exits right after writing it (WR-2): a frame the socket
        // did not take at once must not be cut off.
        if (this.mode === "fresh") this.end(EXIT.ok);
        return;
      } catch (e) {
        this.diag(`windmill-lite harness: ${e instanceof ProtocolError ? e.message : describe(e)}`);
      }
    }
    this.diag(`windmill-lite harness: no frame that ends invocation ${JSON.stringify(inv.id)} fits; ending the connection`);
    this.stop(EXIT.unreportable);
  }

  private terminalFrames(inv: Invocation, o: Outcome, tail: string): Outgoing[] {
    if (inv.state !== "running") {
      const kind = o.outcome === "ok" ? "result" : "error";
      const full: Outgoing = { t: "late", id: inv.id, kind, body: "body" in o ? new Raw(o.body) : undefined, settled: true };
      return [full, { t: "late", id: inv.id, kind: "error", settled: true }];
    }
    const escaped = (tail: string): Outgoing => ({ t: "result", id: inv.id, outcome: "escaped", tail, exit_code: 1, settled: true });
    return [
      { t: "result", id: inv.id, ...rawBody(o, tail), settled: true },
      escaped(`result exceeds the protocol's maximum message size of ${this.sender.maxMessage} bytes`),
      escaped(""),
    ];
  }

  /** bye is the last frame: heartbeats stop, everything queued goes out
   * first, and nothing is sent after it. */
  private drain() {
    if (this.heartbeat) realTimers.clearInterval(this.heartbeat);
    this.heartbeat = undefined;
    this.sender.whenFlushed(() => {
      this.send({ t: "bye" });
      this.byeSent = true;
      this.sender.whenFlushed(() => this.stop(EXIT.ok));
    });
  }

  private send(m: Outgoing) {
    if (!this.closing()) this.sender.send(m);
  }

  /** Nothing more is sent: the connection ended, bye went out, or a fresh
   * process is ending. */
  private closing(): boolean {
    return this.stopped || this.byeSent || this.exiting;
  }

  private protocolError(e: unknown) {
    this.diag(`windmill-lite harness: ${e instanceof ProtocolError ? e.message : describe(e)}`);
    this.stop(EXIT.protocol);
  }

  private stop(code: number) {
    if (this.stopped) return;
    this.stopped = true;
    if (this.heartbeat) realTimers.clearInterval(this.heartbeat);
    for (const inv of this.invocations.values()) {
      inv.controller.abort("harness_exit");
      inv.resources.settle();
      inv.log.close();
    }
    const lost = new Error("windmill-lite: the engine connection ended");
    for (const c of this.platformCalls.values()) if (c !== DISCARD) c.reject(lost);
    for (const r of this.runs.values()) r.reject(lost);
    this.platformCalls.clear();
    this.runs.clear();
    this.scopes.clear();
    this.opts.transport.end();
    this.opts.exit(code);
  }
}

/** text, from the start, as long as its JSON string spells in at most
 * limit bytes, ending in "…" when cut; it never splits a surrogate pair. */
function cutToFit(text: string, limit: number): string {
  const size = (t: string) => Buffer.byteLength(JSON.stringify(t));
  if (size(text) <= limit) return text;
  if (size("…") > limit) return "";
  let lo = 0;
  let hi = text.length;
  while (lo < hi) {
    const mid = Math.ceil((lo + hi) / 2);
    let k = mid;
    if (k > 0 && k < text.length && /[\uD800-\uDBFF]/.test(text[k - 1])) k--;
    if (size(text.slice(0, k) + "…") <= limit) lo = mid;
    else hi = mid - 1;
  }
  let k = lo;
  if (k > 0 && k < text.length && /[\uD800-\uDBFF]/.test(text[k - 1])) k--;
  return text.slice(0, k) + "…";
}

/** The message m with its string field set to as much of text, from the
 * start, as keeps the message's canonical payload within limit bytes,
 * ending in "…" when cut; undefined when not even an empty string fits. A
 * cut never splits a surrogate pair. */
export function fitString(m: Outgoing, field: string, text: string, limit: number): Outgoing | undefined {
  const size = (t: string) => encode({ ...m, [field]: t }).length;
  if (size(text) <= limit) return { ...m, [field]: text };
  const cut = (k: number) => {
    if (k > 0 && k < text.length && /[\uD800-\uDBFF]/.test(text[k - 1])) k--;
    return text.slice(0, k) + "…";
  };
  if (size(cut(0)) > limit) return size("") <= limit ? { ...m, [field]: "" } : undefined;
  let lo = 0;
  let hi = text.length;
  while (lo < hi) {
    const mid = Math.ceil((lo + hi) / 2);
    if (size(cut(mid)) <= limit) lo = mid;
    else hi = mid - 1;
  }
  return { ...m, [field]: cut(lo) };
}

function rawBody(o: Outcome, tail: string): Record<string, unknown> {
  switch (o.outcome) {
    case "escaped":
      return { outcome: o.outcome, tail, exit_code: o.exit_code };
    case "empty":
      return { outcome: o.outcome };
    default:
      return { outcome: o.outcome, body: new Raw(o.body) };
  }
}

/** An outcome that escaped the wrapper's error shaping (WR-19), with exit
 * code 1 until Q-WR-2 is recorded. text is what the process would have
 * printed on its way out: it ends the invocation's log, whose last
 * TAIL_LENGTH characters are the tail (WR-20). */
function escaped(text: string, exitCode = 1): Outcome {
  return { outcome: "escaped", exit_code: exitCode, print: text };
}

/** The outcome of process.exit(status): empty for 0, since no result was
 * written (WR-V2); escaped with the status otherwise, its tail the log as
 * it stands, since Bun prints nothing. */
function exited(status: number): Outcome {
  return status === 0 ? { outcome: "empty" } : { outcome: "escaped", exit_code: status };
}

/** The outcome of a caught throw: an error with WR-18's object. The
 * wrapper first prints the exception to stderr, so it reaches the logs.
 * When the object cannot be built or serialised (null or undefined thrown,
 * a throwing getter, a cycle in extra), the failure escapes the error
 * shaping, as in WR-19. */
function caught(e: unknown): Outcome {
  try {
    console.error(e);
  } catch {
    // Printing is for the logs; it never changes the outcome.
  }
  try {
    return { outcome: "error", body: errorText(e, stepId()) };
  } catch (inner) {
    return escaped(describe(inner));
  }
}

/** The step id a wrapper error carries: WM_FLOW_STEP_ID as the invocation
 * sees it, when it is not empty (WR-18). It is read through the
 * environment view, in the invocation's context. */
function stepId(): string | undefined {
  return process.env.WM_FLOW_STEP_ID || undefined;
}

function describe(e: unknown): string {
  try {
    if (e instanceof Error) return e.stack ?? `${e.name}: ${e.message}`;
    return String(e);
  } catch {
    return "(an error that cannot be printed)";
  }
}

/** A file of an inventory id: scripts/bun/<id><ext> in the bundle
 * (ADR 0010), its module (.mjs) or its metadata (.meta.json). An id is one
 * or more `/`-separated segments, none empty, `.` or `..`. */
async function scriptPath(bundleDir: string, script: string, ext: string): Promise<string> {
  return join(bundleDir, "scripts", "bun", ...idSegments(script)) + ext;
}

/** Whether a script's metadata is a codebase script's, which esbuild
 * bundled (milestone C2b). */
function isCodebase(meta: unknown): boolean {
  return (meta as { bundler?: unknown } | null)?.bundler === "esbuild";
}

/** The file a script's module loads from. A module of the Bun build is
 * scripts/bun/<id>.mjs in the bundle. A codebase script's (its metadata's
 * bundler is esbuild, milestone C2b) is scripts/bun/<id>.cjs for CommonJS,
 * which Bun's CommonJS loader runs (D13), or .mjs for ESM; it loads from a
 * copy in the working directory, as Windmill places a codebase bundle as
 * main.js in the job directory beside its assets (WR-50), so its
 * __dirname, __filename and import.meta, and the relative paths its own
 * require resolves at run time, are the working directory's. The copy is
 * named by a digest of the id, with a leading dot. */
export async function moduleFile(bundleDir: string, script: string, meta: unknown, workdir: string): Promise<string> {
  const m = meta as { bundler?: unknown; format?: unknown } | null;
  const ext = m?.format === "cjs" ? ".cjs" : ".mjs";
  const file = await scriptPath(bundleDir, script, ext);
  if (!isCodebase(meta)) return file;
  const copy = join(workdir, ".windmill-lite-" + new Bun.CryptoHasher("sha256").update(script).digest("hex").slice(0, 16) + ext);
  copyFileSync(file, copy);
  return copy;
}

/** Extracts a bundle's codebase assets, assets/ in the bundle (ADR 0010),
 * into the working directory at their paths there, before any module
 * loads: Windmill unpacks them beside main.js in the job directory, which
 * is its working directory (WR-50); here every codebase entry's assets
 * share the process's one working directory (D10). The bundle holds only
 * regular files and directories. */
export function extractAssets(bundleDir: string, workdir: string) {
  const root = join(bundleDir, "assets");
  const walk = (rel: string) => {
    let entries;
    try {
      entries = readdirSync(join(root, rel), { withFileTypes: true });
    } catch (e) {
      if (rel === "" && (e as { code?: string }).code === "ENOENT") return;
      throw e;
    }
    for (const d of entries) {
      const r = rel === "" ? d.name : join(rel, d.name);
      if (d.isDirectory()) {
        walk(r);
      } else if (d.isFile()) {
        mkdirSync(dirname(join(workdir, r)), { recursive: true });
        copyFileSync(join(root, r), join(workdir, r));
      } else {
        throw new Error(`assets/${r} is not a regular file`);
      }
    }
  };
  walk("");
}

/** The segments of an inventory id or a host-provided module's id, which
 * name files below the bundle. */
function idSegments(id: string): string[] {
  const segs = id.split("/");
  if (id === "" || segs.some((s) => s === "" || s === "." || s === ".." || s.includes("\\") || s.includes("\0"))) {
    throw new Error(`invalid script id ${JSON.stringify(id)}`);
  }
  return segs;
}

/** The registry of host-provided modules: a Map from the module's key, as
 * the host contract names it, to its exports, which every shim reads
 * (ADR 0010, D12). */
export const HOST_MODULES = Symbol.for("windmill-lite.hostModules");

/** Registers the bundle's own copies of its host-provided modules, for a
 * harness the engine spawned: manifest.json lists them in registration
 * order (host_modules, a module after those it imports), and each copy is
 * scripts/bun/provided/<id>.mjs. They load before any step does, outside
 * every invocation. In host mode the host registers its own instances
 * instead (the host SDK). */
export async function registerHostModules(bundleDir: string, manifest: unknown) {
  const list = (manifest as { host_modules?: unknown } | null)?.host_modules;
  if (list === undefined || list === null) return;
  if (!Array.isArray(list)) throw new Error("manifest host_modules is not an array");
  const g = globalThis as Record<symbol, unknown>;
  const registry = (g[HOST_MODULES] ??= new Map<string, object>()) as Map<string, object>;
  for (const h of list) {
    if (!h || typeof h.key !== "string" || typeof h.id !== "string") throw new Error(`manifest host_modules entry ${JSON.stringify(h)} needs a key and an id`);
    const path = join(bundleDir, "scripts", "bun", "provided", ...idSegments(h.id)) + ".mjs";
    registry.set(h.key, await import(pathToFileURL(path).href));
  }
}
