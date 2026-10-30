// The Bun host SDK (ADR 0009, host mode; milestones W1 and F3a of
// ADR 0012): for a host that is itself a Bun program and whose per-run
// state lives in AsyncLocalStorage. The host process is also the Bun
// harness. connect() checks the host's working directory, registers the
// host's instances of the bundle's host-provided modules, extracts the
// bundle's codebase assets, starts the engine binary as a child, with one
// end of a socketpair as its file descriptor 3, and runs the harness
// runtime in this process; run() submits runs, whose steps execute here,
// inside the scope the caller had when it submitted them (H11).
//
// A host shares modules with step code by registering its own instances of
// them, the host-provided modules (ADR 0010, D12), before any step loads.
// A step's bundle reaches them through a shim that takes each export from
// the registry.

import { accessSync, constants, realpathSync, statSync, writeSync } from "node:fs";
import { createRequire } from "node:module";
import type { Socket } from "node:net";
import { join, resolve } from "node:path";
import { exitProcess, realStderr } from "../runtime/context.ts";
import type { Plugin, Selector } from "../runtime/plugin.ts";
import { extractAssets, type HostHealth, RunError, Runtime } from "../runtime/runtime.ts";

export type { AroundCall, Call, Outcome, PlatformCall, Plugin, Selector, Site, StepOutcome, StepRecord, Trace, Warning } from "../runtime/plugin.ts";

/** The process-wide registry of host-provided modules: module path to the
 * module's namespace (ADR 0010). */
export const HOST_MODULES = Symbol.for("windmill-lite.hostModules");

export class HostError extends Error {
  constructor(
    readonly code: string,
    message: string,
  ) {
    super(`windmill-lite: ${code}: ${message}`);
  }
}

export type ConnectOptions = {
  /** The windmill-lite engine binary, and arguments to put before its
   * `host` command. */
  engine: string;
  engineArgs?: string[];
  /** Flags to put after the `host` command's own, such as `-variables`
   * and `-resources` (cmd/windmill-lite's README). */
  engineFlags?: string[];
  /** The bundle directory, which the engine and this process both load. */
  bundle: string;
  /** A writable directory that must already be this process's working
   * directory (ADR 0004, the shared-process failure model, item 5). The
   * bundle's codebase assets are extracted into it, and the engine writes
   * each step's args.json there. connect() refuses with a HostError
   * workdir_not_writable or workdir_mismatch otherwise, and so does the
   * engine, which checks it again. */
  workdir: string;
  /** The host's own instances of the bundle's host-provided modules, by
   * module path. */
  hostModules?: Record<string, object>;
  /** The host's plugin (ADR 0008, milestone F2c): TypeScript hooks for the
   * host's runs (H1, H2, H6, H8, H9, H12, H13), around wrappers, and the
   * providers of the bundle's host-provided sites, which the engine calls
   * over host_call, each in the scope of the run it serves (H11). The
   * engine checks its declaration in the handshake and refuses the host
   * (plugin_mismatch) when a selector is malformed or ambiguous, or a
   * provider the host contract names is missing. */
  plugin?: Plugin;
  /** The engine's watchdog: it kills this process when no frame has come
   * from it for killAfter milliseconds (default 30000; 0 disables it). */
  watchdog?: { killAfter: number };
  /** Recycle triggers (none by default): health() reports the first that
   * trips, and onRecycle is called once with it ("rss", "modules" or
   * "retired"). A host that sees one drains and exits, and its supervisor
   * starts a new generation. rss trips when the resident set is at least
   * rss bytes on samples consecutive heartbeats (default 2); modules when
   * more than that many script modules are loaded, since a module stays
   * loaded for the life of the process (ADR 0006); retired when the host's
   * pooled-connection layer reports more than max entries retired but not
   * released: entries it superseded, whose leases an abandoned invocation
   * may still hold, so that only the process's exit frees them (ADR 0004
   * host mode item 5). count is asked at every heartbeat. */
  recycle?: { rss?: number; samples?: number; modules?: number; retired?: { max: number; count: () => number } };
  onRecycle?: (reason: string) => void;
  /** Called when the connection to the engine ends other than by close().
   * The default writes a line to stderr and exits with code 1, so that the
   * host's supervisor restarts it: without its engine a host can run
   * nothing. */
  onEngineExit?: (status: { code: number | null; signal: string | null }) => void;
  /** Writes a diagnostic line; the default is stderr. */
  diag?: (line: string) => void;
};

export type RunOptions = {
  /** What path names: a flow (the default) or a script, which runs as a
   * script job (WA-4). */
  kind?: "flow" | "script";
  /** Static stand-ins for modules of the flow, by module id: H1's no-code
   * form (ADR 0008). */
  intercept?: Record<string, unknown>;
  /** The flow run's write-capable selectors (H3), ahead of the plugin's:
   * the plugin's beforeFirstWrite, when it has one, is asked before the
   * first of them launches, and the run then may have mutated. */
  writeCapable?: Selector[];
  /** Error names that latch the flow run instead of failing a step (H7),
   * with the plugin's. */
  fatalErrorNames?: string[];
  /** The run's hard deadline, in milliseconds from its submission (H4): the
   * run ends canceled when it passes, and steps it left running are
   * abandoned. */
  deadlineMs?: number;
  /** The flow run's pre-write deadline, in milliseconds from its
   * submission (H4): it ends the run canceled if it passes before the
   * first write is approved. */
  preWriteDeadlineMs?: number;
  /** The flow run's clock (H10): "record" returns every sleep and retry
   * delay at once, and the run's deviations then list H10. */
  clock?: "real" | "record";
  /** Cancels the run when it aborts. */
  signal?: AbortSignal;
  /** Called once the run has settled, after its outcome: every invocation
   * it started has settled, so no step code of it can run any more, which
   * may be never (ADR 0004 host mode item 5, onRunSettled). Resources that
   * an abandoned step may still use are released here, never on a timer. */
  onSettled?: () => void;
};

/** A run's outcome: its job id, its status, its final result, whether it
 * may have mutated (ADR 0008 H3), and its warnings. */
export type RunResult = {
  id: string;
  status: string;
  result?: unknown;
  mayHaveMutated: boolean;
  warnings: { code: string; message: string }[];
  /** The error a latched run latched (H5), such as provider_declined. */
  latched?: string;
  /** The flow run's deviations that depend on a run-time setting, as its
   * trace lists them (H7, H10). */
  deviations: string[];
};

export type Health = HostHealth & {
  /** Runs submitted and not yet done. */
  runs: number;
  draining: boolean;
  /** The recycle trigger that tripped, or null. */
  recycle: string | null;
};

export type Host = {
  /** Runs the flow (or script) at path with args, inside the caller's
   * current scope. It rejects with a HostError when the run does not
   * start: "draining", or "refused" (no such target, or arguments that
   * are not an object). */
  run(path: string, args?: Record<string, unknown>, opts?: RunOptions): Promise<RunResult>;
  /** In-flight and abandoned invocations, the age of the oldest abandoned
   * one, process-level errors no invocation owned, event-loop lag, RSS,
   * the script modules loaded, the runs in flight and the recycle trigger
   * that tripped (ADR 0004 host mode item 2). */
  health(): Health;
  /** Refuses new runs (HostError "draining", here and in the engine),
   * and waits up to timeoutMs for the runs in flight, until the engine
   * says bye; the host may then exit (host mode item 3). Runs still in
   * flight at the deadline end with the host. */
  drain(timeoutMs: number): Promise<void>;
  /** Ends the connection; resolves once the engine has exited. The host
   * process lives on. */
  close(): Promise<void>;
  /** The engine's process id. */
  readonly enginePid: number;
};

/** Checks the working directory (ADR 0004, the shared-process failure
 * model, item 5): the SDK never changes it, so the host must have made the
 * directory it passes its own. It must be a directory this process can
 * write, else workdir_not_writable, and the working directory once both
 * are resolved without symbolic links, else workdir_mismatch. The engine
 * checks the same (harness.CheckWorkdir), on the cases of
 * conformance/protocol/host. It returns the directory's absolute path. */
export function checkWorkdir(dir: string): string {
  const abs = resolve(dir);
  try {
    if (!statSync(abs).isDirectory()) throw new Error("not a directory");
    accessSync(abs, constants.W_OK);
  } catch (e) {
    throw new HostError("workdir_not_writable", `${dir}: ${(e as Error).message}`);
  }
  if (realpathSync(abs) !== realpathSync(process.cwd())) {
    throw new HostError("workdir_mismatch", `${dir} is not the working directory ${process.cwd()}`);
  }
  return abs;
}

/** Registers the host's instances of host-provided modules, by the keys
 * the host contract gives them, and says which keys the bundle does not
 * list (they are never read) and which listed ones the host left out (a
 * step that imports one fails at load with "host-provided module not
 * registered", ADR 0010). */
function register(modules: Record<string, object> | undefined, listed: string[], diag: (line: string) => void) {
  const g = globalThis as Record<symbol, unknown>;
  const registry = (g[HOST_MODULES] ??= new Map<string, object>()) as Map<string, object>;
  for (const [key, mod] of Object.entries(modules ?? {})) {
    if (!listed.includes(key)) diag(`windmill-lite: the bundle lists no host-provided module ${JSON.stringify(key)}; no step reads the host's instance`);
    registry.set(key, mod);
  }
  for (const key of listed) {
    if (!registry.has(key)) diag(`windmill-lite: the host registers no instance of the host-provided module ${JSON.stringify(key)}; a step that imports it fails at load`);
  }
}

export async function connect(o: ConnectOptions): Promise<Host> {
  const workdir = checkWorkdir(o.workdir);
  // The host's stderr as Bun has it: a step's context must never take a
  // diagnostic into the step's log.
  const diag = o.diag ?? ((line: string) => realStderr(line + "\n"));
  let manifest: Uint8Array;
  let listed: string[];
  try {
    manifest = await Bun.file(join(o.bundle, "manifest.json")).bytes();
    const hm = (JSON.parse(new TextDecoder().decode(manifest)) as { host_modules?: { key?: unknown }[] | null }).host_modules ?? [];
    listed = hm.map((h) => String(h?.key));
  } catch (e) {
    throw new HostError("bundle", `cannot read the bundle manifest: ${(e as Error).message}`);
  }
  // Before any step module loads: a step that imports a host-provided
  // module takes it from the registry when it loads.
  register(o.hostModules, listed, diag);
  // The bundle's codebase assets go into the working directory before any
  // step loads, as a spawned harness extracts them into its own (WR-50,
  // D10).
  try {
    extractAssets(o.bundle, workdir);
  } catch (e) {
    throw new HostError("assets", `cannot extract the bundle's assets: ${(e as Error).message}`);
  }
  const digest = "sha256:" + new Bun.CryptoHasher("sha256").update(manifest).digest("hex");
  const killAfter = o.watchdog?.killAfter ?? 30000;
  // The extra "pipe" is a socketpair on Bun 1.3.10: the child's fd 3.
  // Through require, not a named import: a named import of a builtin fixes
  // its ESM bindings for every later importer, so a step's named import of
  // node:child_process would miss the runtime's wrappers (ADR 0004, item 7).
  const { spawn } = createRequire(import.meta.url)("node:child_process") as typeof import("node:child_process");
  // The second one, the child's fd 4, carries only the child frames that
  // announce the steps' child groups (WR-55), written synchronously: the
  // socket may hold bytes it has not written yet, which such a write would
  // overtake, and the step may block the event loop right after.
  const child = spawn(
    o.engine,
    [...(o.engineArgs ?? []), "host", "--bundle", o.bundle, "--workdir", workdir, "--kill-after", `${killAfter}ms`, ...(o.engineFlags ?? [])],
    { stdio: ["ignore", "inherit", "inherit", "pipe", "pipe"] },
  );
  let settle: { resolve: () => void; reject: (e: Error) => void };
  const ready = new Promise<void>((resolve, reject) => (settle = { resolve, reject }));
  const started = new Promise<Error | undefined>((resolve) => {
    child.once("spawn", () => resolve(undefined));
    child.once("error", (e) => resolve(e));
  });
  const failed = await started;
  const sock = child.stdio[3] as Socket | null;
  if (failed || !sock) {
    throw new HostError("engine_start", `${o.engine}: ${failed?.message ?? "no socket for file descriptor 3"}`);
  }
  child.on("error", (e) => diag(`windmill-lite: engine process: ${e.message}`));
  // The child channel's own descriptor, which Bun 1.3.10's socket keeps in
  // its handle; the socket itself is never read or written, and holds the
  // host's event loop open no more than the engine's socket does.
  const chan = child.stdio[4] as (Socket & { _handle?: { fd?: unknown } }) | null;
  chan?.unref();
  const chanFd = chan?._handle?.fd;
  if (typeof chanFd !== "number") diag("windmill-lite: no child channel to the engine; child groups are announced on its socket");
  const children =
    typeof chanFd === "number"
      ? (b: Uint8Array) => {
          // Waits for a full channel, as a spawned harness waits for its
          // socket; the engine reads it on a goroutine of its own.
          const deadline = Date.now() + 30_000;
          for (let off = 0; off < b.length; ) {
            try {
              off += writeSync(chanFd, b, off, b.length - off);
            } catch (e) {
              if ((e as { code?: string }).code !== "EAGAIN" || Date.now() > deadline) throw e;
              Bun.sleepSync(1);
            }
          }
        }
      : undefined;
  let inflight = 0;
  // Called each time no run is in flight.
  const idle: (() => void)[] = [];
  let draining = false;
  let drained: Promise<void> | undefined;
  let closing = false;
  let welcomed = false;
  let refused: HostError | undefined;
  let recycle: string | null = null;
  let rssSamples = 0;
  const trip = (reason: string, detail: string) => {
    if (recycle !== null) return;
    recycle = reason;
    diag(`windmill-lite: recycle trigger ${reason}: ${detail}`);
    o.onRecycle?.(reason);
  };
  const triggers = o.recycle;
  const exited = new Promise<{ code: number | null; signal: string | null }>((resolve) =>
    child.on("exit", (code, signal) => resolve({ code, signal })),
  );
  const runtime = new Runtime({
    bundleDir: o.bundle,
    bundleDigest: digest,
    host: true,
    children,
    diag,
    plugin: o.plugin,
    transport: {
      // Accepting nothing while the socket's buffer is full keeps the
      // sender's order: a small frame waits behind at most one part frame
      // (ADR 0004), not behind every part already handed to the socket.
      write: (b) => {
        if (sock.destroyed) return -1;
        if (sock.writableNeedDrain) return 0;
        sock.write(b);
        return b.length;
      },
      end: () => sock.end(),
    },
    onWelcome: () => {
      welcomed = true;
      settle.resolve();
    },
    onReject: (code, message) => {
      refused = new HostError(code, message);
    },
    onHeartbeat: triggers
      ? (h) => {
          if (triggers.rss !== undefined) {
            rssSamples = h.rss >= triggers.rss ? rssSamples + 1 : 0;
            if (rssSamples >= (triggers.samples ?? 2)) trip("rss", `${h.rss} bytes resident on ${rssSamples} heartbeats, the trigger is ${triggers.rss}`);
          }
          if (triggers.modules !== undefined && h.modules > triggers.modules) trip("modules", `${h.modules} script modules loaded, more than ${triggers.modules}`);
          if (triggers.retired !== undefined) {
            let n: number;
            try {
              n = triggers.retired.count();
            } catch (e) {
              diag(`windmill-lite: the retired-entry count failed: ${e instanceof Error ? e.message : String(e)}`);
              return;
            }
            if (n > triggers.retired.max) trip("retired", `${n} pooled entries retired and not released, more than ${triggers.retired.max}`);
          }
        }
      : undefined,
    exit: (code) => {
      if (!welcomed) {
        settle.reject(refused ?? new HostError("rejected", `the engine refused this host (harness exit code ${code}); see its stderr`));
        return;
      }
      exited.then((status) => {
        if (closing) return;
        if (o.onEngineExit) return o.onEngineExit(status);
        diag(`windmill-lite: the engine exited (${status.signal ?? `code ${status.code}`}); exiting so that the supervisor restarts the host`);
        // The real exit: this may run in an invocation's context, where
        // process.exit is intercepted (D11).
        exitProcess(1);
      });
    },
  });
  sock.on("data", (d: Buffer) => runtime.onData(new Uint8Array(d.buffer, d.byteOffset, d.length)));
  sock.on("drain", () => runtime.onDrain());
  sock.on("close", () => runtime.onClose());
  sock.on("error", (e) => diag(`windmill-lite: engine socket: ${e.message}`));
  runtime.start();
  await ready;

  return {
    enginePid: child.pid ?? -1,
    run(path, args = {}, opts = {}) {
      if (draining) return Promise.reject(new HostError("draining", "the host is draining and takes no new runs"));
      let submitted;
      try {
        // Submitted synchronously, so the scope captured is the caller's.
        submitted = runtime.submit(
          opts.kind ?? "flow",
          path,
          JSON.stringify(args),
          {
            intercept: opts.intercept && JSON.stringify(opts.intercept),
            write_capable: opts.writeCapable,
            fatal_error_names: opts.fatalErrorNames,
            deadline_ms: opts.deadlineMs,
            pre_write_deadline_ms: opts.preWriteDeadlineMs,
            clock: opts.clock,
          },
          opts.onSettled,
        );
      } catch (e) {
        return Promise.reject(e);
      }
      const { id, done } = submitted;
      inflight++;
      const cancel = () => runtime.cancelRun(id);
      if (opts.signal?.aborted) cancel();
      else opts.signal?.addEventListener("abort", cancel, { once: true });
      return done
        .then(
          (o): RunResult => ({
            id,
            status: o.status,
            result: o.result === undefined ? undefined : JSON.parse(o.result),
            mayHaveMutated: o.may_have_mutated,
            warnings: o.warnings,
            latched: o.latched,
            deviations: o.deviations,
          }),
          (e) => {
            throw e instanceof RunError ? new HostError(e.code, e.detail) : e;
          },
        )
        .finally(() => {
          inflight--;
          opts.signal?.removeEventListener("abort", cancel);
          if (inflight === 0) for (const cb of idle.splice(0)) cb();
        });
    },
    health: () => ({ ...runtime.hostHealth(), runs: inflight, draining, recycle }),
    drain(timeoutMs) {
      draining = true;
      // The engine refuses the runs that reach it from now on, and says
      // bye once those in flight have ended.
      drained ??= new Promise<void>((resolve) => {
        try {
          runtime.drainEngine(resolve);
        } catch (e) {
          diag(`windmill-lite: drain: ${(e as Error).message}`);
          resolve();
        }
      });
      // Done once the engine said bye, after the outcome of every run it
      // took, and every caller has its run's outcome; or at the deadline.
      const settledIn = new Promise<void>((resolve) => (inflight === 0 ? resolve() : idle.push(resolve)));
      let timer: ReturnType<typeof setTimeout> | undefined;
      const deadline = new Promise<void>((resolve) => (timer = setTimeout(resolve, Math.max(0, timeoutMs))));
      return Promise.race([Promise.all([drained, settledIn]), deadline]).then(
        () => clearTimeout(timer),
        () => clearTimeout(timer),
      );
    },
    async close() {
      closing = true;
      sock.end();
      await exited;
    },
  };
}
