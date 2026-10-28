// Per-invocation identity in a shared process (ADR 0004, WA-31): each
// invocation runs inside an AsyncLocalStorage context that holds its id,
// its run, its environment and its abort signal. This module installs what
// reads that context:
//
// - the environment view, so that process.env, Bun.env and import.meta.env
//   answer from the current invocation first;
// - the fetch wrapper, which turns requests to the platform facade into
//   platform frames attributed to the invocation whose context made them;
// - the process-level error handlers, which never exit;
// - the process.exit interception, which ends the invocation, not the
//   process (D11).
//
// Everything here is installed once per process, before any step module
// loads, because the stock client reads its configuration at import
// (WA-1) and a module evaluates inside its first invocation's context.

import { AsyncLocalStorage } from "node:async_hooks";

export type InvocationContext = {
  id: string;
  run: string;
  /** The invocation's environment: its own copy, so a step's writes stay
   * with it. A null-prototype object, so any variable name is a plain key. */
  env: Record<string, string>;
  /** Variables the step deleted: absent for it, whatever the process has. */
  unset?: Set<string>;
  signal: AbortSignal;
  /** What the invocation holds that its settlement must release. */
  resources: InvocationResources;
};

/** The per-invocation registry of what an invocation started that can
 * outlive its entrypoint call, kept in its async-context store, so code
 * running in that context finds it with current() and nothing global is
 * keyed by invocation. The runtime settles it when it queues the frame
 * that settles the invocation (ADR 0004, settlement), or when its
 * connection ends and the invocation settles with the process.
 *
 * Milestone C3b's settle-time fence (decisions, 2026-10-28) registers here:
 * the timer wrappers each pending setTimeout, setInterval and setImmediate
 * handle, the fetch view each request in flight, each with a release that
 * clears or aborts it and is unregistered when the resource ends on its
 * own; its log and platform paths check settled. Work started in a
 * process scope (PROCESS_SCOPE) has no invocation, so it registers nowhere
 * and is never fenced. What settle() returns is the leak count: Bun 1.3.10's
 * process._getActiveHandles() lists nothing, so this registry is the only
 * record of what an invocation left behind. */
export class InvocationResources {
  /** Set once the invocation has settled; it never clears. */
  settled = false;
  private pending = new Map<() => void, string>();

  /** Registers a resource of a kind ("timeout", "fetch" and the like) and
   * the release that revokes it. The returned function unregisters it,
   * for a resource that ended by itself. After settlement the release runs
   * at once, since nothing may outlive it. */
  track(kind: string, release: () => void): () => void {
    if (this.settled) {
      release();
      return () => {};
    }
    // A function of its own, so one release can be tracked twice.
    const key = () => release();
    this.pending.set(key, kind);
    return () => {
      this.pending.delete(key);
    };
  }

  /** Marks the invocation settled and releases what it still held, once.
   * Returns how many resources of each kind were still pending: what the
   * call left behind (D9), which C3b reports. A release that throws is
   * skipped; the others still run. */
  settle(): Record<string, number> {
    const left: Record<string, number> = {};
    if (this.settled) return left;
    this.settled = true;
    const pending = this.pending;
    this.pending = new Map();
    for (const [release, kind] of pending) {
      left[kind] = (left[kind] ?? 0) + 1;
      try {
        release();
      } catch {
        // One failed release must not keep the others.
      }
    }
    return left;
  }
}

export const invocations = new AsyncLocalStorage<InvocationContext>();

/** The invocation whose context the caller runs in, if any. */
export function current(): InvocationContext | undefined {
  return invocations.getStore();
}

/** The key, on globalThis, of the process-scope API: what a pooled client
 * or a host-provided module uses to keep work it starts for the whole
 * process out of the invocation that happens to trigger it. A step module
 * evaluates inside its first invocation's context, and a pool's first
 * client is often created inside a step, so without it the pool's
 * maintenance timers would be that invocation's, and C3b's settle-time
 * fence would clear them. Code that the harness does not bundle reaches it
 * through the symbol, as shims reach the host-provided modules:
 *
 *   const inProcess = globalThis[Symbol.for("windmill-lite.processScope")]?.capture() ?? ((f) => f());
 *   export function getClient() { return inProcess(() => pool.connect()); }
 *
 * The runtime publishes it before the bundle's host-provided modules load,
 * so a capture at load finds it. Loading outside every invocation is not
 * enough on its own: a function a module exports runs in its caller's
 * context, so a factory a step calls later must re-enter the captured
 * scope, as getClient does, for what it starts to belong to no invocation.
 */
export const PROCESS_SCOPE = Symbol.for("windmill-lite.processScope");

/** The process-scope API. */
export type ProcessScope = {
  /** Captures the caller's async context, with every store it holds but
   * the invocation's, and returns a function that runs a callback in it.
   * Timers, promises and handles the callback starts belong to no
   * invocation: they are never attributed to one, never fenced, and an
   * error they throw is unattributed (ADR 0004, the shared-process failure
   * model, item 3). Captured at load, it keeps a module's own context. */
  capture(): <R>(fn: (...args: any[]) => R, ...args: any[]) => R;
};

const processScope: ProcessScope = {
  capture() {
    return invocations.exit(() => AsyncLocalStorage.snapshot());
  },
};

/** Publishes the process-scope API on globalThis, before any step or
 * host-provided module loads. */
export function installProcessScope() {
  const g = globalThis as Record<symbol, unknown>;
  if (g[PROCESS_SCOPE] === undefined) {
    Object.defineProperty(g, PROCESS_SCOPE, { value: Object.freeze(processScope), enumerable: false });
  }
}

/** ABSENT: the current invocation deleted the variable. */
const ABSENT = Symbol("absent");

/** The current invocation's say on variable k: its value, ABSENT, or
 * undefined when it has none and the process environment answers. */
function ownVar(k: string | symbol): string | typeof ABSENT | undefined {
  const c = invocations.getStore();
  if (c === undefined || typeof k !== "string") return undefined;
  if (Object.hasOwn(c.env, k)) return c.env[k];
  return c.unset?.has(k) ? ABSENT : undefined;
}

/** A read through the view: the invocation's value, else the process's. */
function read(k: string | symbol, fallback: () => unknown): unknown {
  const v = ownVar(k);
  if (v === ABSENT) return undefined;
  return v !== undefined ? v : fallback();
}

// process.env, Bun.env and import.meta.env start out as one object.
const real = process.env as Record<string, string | undefined>;
let envInstalled = false;
const shadowed = new Set<string>();

/** Installs the environment view. Three paths read the environment, and
 * each is made to see the current invocation's variables first:
 *
 * 1. process.env is replaced by a Proxy over the process environment.
 *    Bun 1.3.10 lets it be assigned, but Bun.env and import.meta.env keep
 *    pointing at the original object.
 * 2. That original object accepts a new prototype: a read of a variable
 *    the process does not have falls through to it, and it answers from
 *    the current invocation. This reaches code that reads Bun.env or
 *    import.meta.env at run time, bundled or not.
 * 3. A variable the process has as its own property would shadow that
 *    prototype, so shadow() turns each one an invocation sets into an
 *    accessor that answers from the current invocation first.
 *
 * Outside any invocation every path reads the process environment, so a
 * host's own code sees no change. Writes and deletes through process.env
 * inside an invocation go to its own copy and never reach the process
 * environment; the first write of a variable the process has shadows it,
 * so Bun.env and import.meta.env read the written value too. Enumerating
 * Bun.env lists only the process's variables.
 *
 * Writes and deletes through the original object itself (Bun.env.X = …
 * in code the build does not rewrite, such as an unbundled dependency) are
 * isolated only for variables already shadowed: a variable the process
 * does not have becomes a plain property of the process environment,
 * which every invocation and the host then read. Isolating those is
 * milestone C3b's, with the rest of the per-invocation environment. */
export function installEnvView() {
  if (envInstalled) return;
  envInstalled = true;
  const view = new Proxy(real, {
    get(t, k) {
      return read(k, () => Reflect.get(t, k));
    },
    has(t, k) {
      const v = ownVar(k);
      return v === ABSENT ? false : v !== undefined || Reflect.has(t, k);
    },
    set(t, k, v) {
      const c = invocations.getStore();
      if (c && typeof k === "string") {
        c.env[k] = String(v);
        c.unset?.delete(k);
        // Bun.env reads a variable the process has as its own property:
        // make it an accessor, so it answers with the written value there
        // too (W1 review, finding 1).
        shadow({ [k]: "" });
        return true;
      }
      const ok = Reflect.set(t, k, v);
      // A variable the host writes while invocations that set it are in
      // flight must keep answering them from their own copies.
      if (ok && typeof k === "string") shadow({ [k]: "" });
      return ok;
    },
    deleteProperty(t, k) {
      const c = invocations.getStore();
      if (c && typeof k === "string") {
        delete c.env[k];
        (c.unset ??= new Set()).add(k);
        // Bun.env reads a variable the process has as its own property:
        // make it an accessor, so it is gone for this invocation there too.
        shadow({ [k]: "" });
        return true;
      }
      return Reflect.deleteProperty(t, k);
    },
    ownKeys(t) {
      const keys = new Set(Reflect.ownKeys(t));
      const c = invocations.getStore();
      if (c) {
        for (const k of c.unset ?? []) keys.delete(k);
        for (const k of Object.keys(c.env)) keys.add(k);
      }
      return [...keys];
    },
    getOwnPropertyDescriptor(t, k) {
      const v = ownVar(k);
      if (v === ABSENT) return undefined;
      if (v !== undefined) return { value: v, writable: true, enumerable: true, configurable: true };
      return Reflect.getOwnPropertyDescriptor(t, k);
    },
  });
  (process as { env: unknown }).env = view;
  const proto = Object.getPrototypeOf(real);
  Object.setPrototypeOf(
    real,
    new Proxy(proto ?? Object.prototype, {
      get(t, k, receiver) {
        return read(k, () => Reflect.get(t, k, receiver));
      },
      has(t, k) {
        const v = ownVar(k);
        return v === ABSENT ? false : v !== undefined || Reflect.has(t, k);
      },
    }),
  );
}

/** Turns each variable of env that the process environment holds as its
 * own data property into an accessor that answers from the current
 * invocation first (path 3 of installEnvView). */
export function shadow(env: Record<string, string>) {
  for (const k of Object.keys(env)) {
    if (shadowed.has(k)) continue;
    const d = Object.getOwnPropertyDescriptor(real, k);
    if (d === undefined || !("value" in d) || !d.configurable) continue;
    let base = d.value;
    Object.defineProperty(real, k, {
      get() {
        return read(k, () => base);
      },
      set(v) {
        const c = invocations.getStore();
        if (c) {
          c.env[k] = String(v);
          c.unset?.delete(k);
        } else base = String(v);
      },
      enumerable: true,
      configurable: true,
    });
    shadowed.add(k);
  }
}

export type PlatformCall = {
  inv: string;
  method: string;
  url: string;
  headers: Record<string, string>;
  body?: Uint8Array;
};

export type PlatformAnswer = { status: number; headers: Record<string, string>; body?: Uint8Array };

// The process-wide hooks are installed once and route to the runtime that
// installed them last, so a host that reconnects counts its errors and
// sends its facade calls on its current connection.
let fetchRoute: { origin: () => string | undefined; call: (c: PlatformCall) => Promise<PlatformAnswer> } | undefined;
let errorRoute: ((kind: string, e: unknown) => void) | undefined;
let fetchInstalled = false;

/** Wraps globalThis.fetch, before any step module loads: a request to the
 * facade origin becomes a platform call attributed to the invocation whose
 * context made it; every other request passes through unchanged. The
 * caller is identified by its context, never by the token it sends, which
 * the stock client captured at import (WA-31). origin() is read per call,
 * because the welcome names the facade after this is installed. */
export function installFetch(origin: () => string | undefined, call: (c: PlatformCall) => Promise<PlatformAnswer>) {
  fetchRoute = { origin, call };
  if (fetchInstalled) return;
  fetchInstalled = true;
  const inner = globalThis.fetch;
  const wrapped = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const route = fetchRoute!;
    const facade = route.origin();
    if (facade === undefined || originOf(input) !== facade) return inner(input, init);
    const inv = invocations.getStore();
    if (inv === undefined) throw new TypeError("windmill-lite: a platform call outside any invocation cannot be attributed");
    const req = new Request(input, init);
    req.signal.throwIfAborted();
    // Bodies travel as bytes, so binary uploads and downloads stay exact.
    const body = req.method === "GET" || req.method === "HEAD" ? undefined : new Uint8Array(await req.arrayBuffer());
    const a = await route.call({ inv: inv.id, method: req.method, url: req.url, headers: Object.fromEntries(req.headers), body });
    return new Response(a.body ?? null, { status: a.status, headers: a.headers });
  };
  globalThis.fetch = Object.assign(wrapped, { preconnect: inner.preconnect }) as typeof fetch;
}

function originOf(input: RequestInfo | URL): string | undefined {
  try {
    if (input instanceof Request) return new URL(input.url).origin;
    return new URL(String(input)).origin;
  } catch {
    return undefined;
  }
}

let handlersInstalled = false;

/** Installs uncaughtException and unhandledRejection handlers that never
 * exit, before any step module loads (ADR 0004, the shared-process failure
 * model, item 1): without them one detached rejection ends the process and
 * every invocation in it. Each error goes to onError, called inside the
 * handler, where current() names the invocation whose context raised it,
 * if any: on Bun 1.3.10 a throw from a timer keeps its context, and an
 * unhandled rejection or a throw from a microtask has none (item 2). A
 * ProcessExit thrown out of a callback never reaches onError: its call
 * already fixed the outcome or was reported. */
export function installProcessHandlers(onError: (kind: string, e: unknown) => void) {
  errorRoute = onError;
  if (handlersInstalled) return;
  handlersInstalled = true;
  const handle = (kind: string) => (e: unknown) => {
    if (e instanceof ProcessExit) return;
    errorRoute!(kind, e);
  };
  process.on("uncaughtException", handle("uncaughtException"));
  process.on("unhandledRejection", handle("unhandledRejection"));
}

/** The sentinel an intercepted process.exit throws (D11): the exit fixed
 * the invocation's outcome, and the step's code goes on only if it catches
 * this. status is the exit status the call asked for. */
export class ProcessExit extends Error {
  constructor(readonly status: number) {
    super(`process.exit(${status})`);
    this.name = "ProcessExit";
  }
}

/** process.exit as Bun has it, which ends the process. The runtime ends
 * the process through it, since its own code may run inside an
 * invocation's context. */
export const exitProcess: (code?: number) => never = process.exit.bind(process);

let exitRoute: ((c: InvocationContext, status: number) => void) | undefined;
let exitInstalled = false;

/** Replaces process.exit, before any step module loads (ADR 0004, the
 * shared-process failure model, item 6): called inside an invocation, it
 * reports the exit status to onExit, which fixes the invocation's outcome,
 * and throws a ProcessExit, so finally blocks run and a catch can swallow
 * it (D11). The code is checked as Bun checks it, so an invalid one throws
 * Bun's own error instead, which the step sees as from the real call.
 * Outside every invocation it is the real process.exit. */
export function installExit(onExit: (c: InvocationContext, status: number) => void) {
  exitRoute = onExit;
  if (exitInstalled) return;
  exitInstalled = true;
  process.exit = function exit(code?: number | string | null): never {
    const c = invocations.getStore();
    if (c === undefined) return exitProcess(code as number);
    const status = exitStatus(code);
    exitRoute!(c, status);
    throw new ProcessExit(status);
  } as typeof process.exit;
}

/** The exit status process.exit(code) gives on Bun 1.3.10, which checks a
 * code before it exits:
 *
 * - no code (undefined or null) is process.exitCode, or 0;
 * - a number must be an integer within ±(2^53 − 1);
 * - a string other than "" is read as Number reads it (so " " is 0 and
 *   " 0x1F " 31) and must then be such an integer;
 * - anything else is refused.
 *
 * A valid code exits with it modulo 256. Bun refuses an invalid one with
 * its own RangeError or TypeError (code ERR_OUT_OF_RANGE or
 * ERR_INVALID_ARG_TYPE) and does not exit; assigning such a code to
 * process.exitCode, which Bun checks the same way, throws that very error
 * here and leaves process.exitCode as it was. */
export function exitStatus(code: unknown): number {
  if (code === undefined || code === null) code = process.exitCode ?? 0;
  const n = typeof code === "number" ? code : typeof code === "string" && code !== "" ? Number(code) : NaN;
  if (Number.isSafeInteger(n)) return ((n % 256) + 256) % 256;
  const before = process.exitCode;
  process.exitCode = code as number;
  // Not reached on Bun 1.3.10, which refuses every such code. Should a
  // later Bun accept one, the step's exit is not changed by the probe.
  if (before !== undefined) process.exitCode = before;
  throw new TypeError(`windmill-lite: process.exit(${String(code)}) was accepted as an exit code`);
}
