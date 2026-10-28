// The Bun host SDK, in the minimal form of the walking skeleton (milestone
// W1 of ADR 0012; F3a completes it): for a host that is itself a Bun
// program and whose per-run state lives in AsyncLocalStorage (ADR 0009).
// The host process is also the Bun harness. connect() starts the engine
// binary as a child, with one end of a socketpair as its file descriptor 3,
// and runs the harness runtime in this process; run() submits runs, whose
// steps execute here, inside the scope the caller had when it submitted
// them (H11).
//
// A host shares modules with step code by registering its own instances of
// them, the host-provided modules (ADR 0010, D12), before any step loads.
// A step's bundle reaches them through a shim that takes each export from
// the registry.

import { spawn } from "node:child_process";
import { accessSync, constants, realpathSync } from "node:fs";
import type { Socket } from "node:net";
import { join } from "node:path";
import { type HostHealth, Runtime } from "../runtime/runtime.ts";

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
  /** The bundle directory, which the engine and this process both load. */
  bundle: string;
  /** A writable directory that must already be this process's working
   * directory (ADR 0004, the shared-process failure model, item 5). */
  workdir: string;
  /** The host's own instances of the bundle's host-provided modules, by
   * module path. */
  hostModules?: Record<string, object>;
  /** The engine's watchdog: it kills this process when no frame has come
   * from it for killAfter milliseconds (default 30000; 0 disables it). */
  watchdog?: { killAfter: number };
  /** Called when the connection to the engine ends other than by close().
   * The default writes a line to stderr and exits with code 1, so that the
   * host's supervisor restarts it: without its engine a host can run
   * nothing. */
  onEngineExit?: (status: { code: number | null; signal: string | null }) => void;
  /** Writes a diagnostic line; the default is stderr. */
  diag?: (line: string) => void;
};

export type RunOptions = {
  /** Static stand-ins for modules of the flow, by module id: H1's no-code
   * form (ADR 0008). */
  intercept?: Record<string, unknown>;
  /** Cancels the run when it aborts. */
  signal?: AbortSignal;
};

/** A run's outcome: its status and its final result. */
export type RunResult = { status: string; result?: unknown };

export type Health = HostHealth & {
  /** Runs submitted and not yet done. */
  runs: number;
  draining: boolean;
};

export type Host = {
  /** Runs the flow at path with args, inside the caller's current scope. */
  run(path: string, args?: Record<string, unknown>, opts?: RunOptions): Promise<RunResult>;
  /** In-flight and abandoned invocations, the age of the oldest abandoned
   * one, process-level errors no invocation owned, and the runs in flight
   * (ADR 0004 host mode item 2). */
  health(): Health;
  /** Refuses new runs (HostError "draining") and waits for the runs in
   * flight, up to timeoutMs; the host may then exit (host mode item 3). */
  drain(timeoutMs: number): Promise<void>;
  /** Ends the connection; resolves once the engine has exited. The host
   * process lives on. */
  close(): Promise<void>;
  /** The engine's process id. */
  readonly enginePid: number;
};

/** Checks the working directory: the SDK never changes it, so the host
 * must have made the directory it passes its own (ADR 0004). */
function checkWorkdir(dir: string) {
  try {
    accessSync(dir, constants.W_OK);
  } catch (e) {
    throw new HostError("workdir_not_writable", `${dir}: ${(e as Error).message}`);
  }
  if (realpathSync(dir) !== realpathSync(process.cwd())) {
    throw new HostError("workdir_mismatch", `${dir} is not the working directory ${process.cwd()}`);
  }
}

function register(modules: Record<string, object> | undefined) {
  const g = globalThis as Record<symbol, unknown>;
  const registry = (g[HOST_MODULES] ??= new Map<string, object>()) as Map<string, object>;
  for (const [path, mod] of Object.entries(modules ?? {})) registry.set(path, mod);
}

export async function connect(o: ConnectOptions): Promise<Host> {
  checkWorkdir(o.workdir);
  // Before any step module loads: a step that imports a host-provided
  // module takes it from the registry when it loads.
  register(o.hostModules);
  const diag = o.diag ?? ((line: string) => process.stderr.write(line + "\n"));
  let manifest: Uint8Array;
  try {
    manifest = await Bun.file(join(o.bundle, "manifest.json")).bytes();
  } catch (e) {
    throw new HostError("bundle", `cannot read the bundle manifest: ${(e as Error).message}`);
  }
  const digest = "sha256:" + new Bun.CryptoHasher("sha256").update(manifest).digest("hex");
  const killAfter = o.watchdog?.killAfter ?? 30000;
  // The extra "pipe" is a socketpair on Bun 1.3.10: the child's fd 3.
  const child = spawn(o.engine, [...(o.engineArgs ?? []), "host", "--bundle", o.bundle, "--kill-after", `${killAfter}ms`], {
    stdio: ["ignore", "inherit", "inherit", "pipe"],
  });
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
  let inflight = 0;
  let draining = false;
  let closing = false;
  let welcomed = false;
  const exited = new Promise<{ code: number | null; signal: string | null }>((resolve) =>
    child.on("exit", (code, signal) => resolve({ code, signal })),
  );
  const runtime = new Runtime({
    bundleDir: o.bundle,
    bundleDigest: digest,
    host: true,
    diag,
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
    exit: (code) => {
      if (!welcomed) {
        settle.reject(new HostError("rejected", `the engine refused this host (harness exit code ${code}); see its stderr`));
        return;
      }
      exited.then((status) => {
        if (closing) return;
        if (o.onEngineExit) return o.onEngineExit(status);
        diag(`windmill-lite: the engine exited (${status.signal ?? `code ${status.code}`}); exiting so that the supervisor restarts the host`);
        process.exit(1);
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
        submitted = runtime.submit("flow", path, JSON.stringify(args), opts.intercept && JSON.stringify(opts.intercept));
      } catch (e) {
        return Promise.reject(e);
      }
      const { id, done } = submitted;
      inflight++;
      const cancel = () => runtime.cancelRun(id);
      if (opts.signal?.aborted) cancel();
      else opts.signal?.addEventListener("abort", cancel, { once: true });
      return done
        .then((o): RunResult => ({ status: o.status, result: o.result === undefined ? undefined : JSON.parse(o.result) }))
        .finally(() => {
          inflight--;
          opts.signal?.removeEventListener("abort", cancel);
        });
    },
    health: () => ({ ...runtime.hostHealth(), runs: inflight, draining }),
    async drain(timeoutMs) {
      draining = true;
      const deadline = performance.now() + timeoutMs;
      while (inflight > 0 && performance.now() < deadline) await Bun.sleep(Math.min(20, Math.max(1, deadline - performance.now())));
    },
    async close() {
      closing = true;
      sock.end();
      await exited;
    },
  };
}
