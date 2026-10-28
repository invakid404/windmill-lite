// Per-invocation identity in a shared process (ADR 0004, WA-31): each
// invocation runs inside an AsyncLocalStorage context that holds its id,
// its run, its environment, its log and its abort signal. This module
// installs what reads that context:
//
// - the environment view, so that process.env, Bun.env and import.meta.env
//   answer from the current invocation first;
// - the fetch view: requests to the platform facade become platform frames
//   attributed to the invocation whose context made them, and every other
//   request it starts is aborted when it settles;
// - the timer fence: the setTimeout, setInterval and setImmediate handles
//   an invocation leaves pending are cleared when it settles (D9);
// - the log view: console and process.stdout/stderr writes go to the
//   invocation's log, and are dropped once it has settled;
// - child processes started without an environment get the invocation's;
// - the process-level error handlers, which never exit;
// - the process.exit interception, which ends the invocation, not the
//   process (D11).
//
// Everything here is installed once per process, before any step module
// loads, because the stock client reads its configuration at import
// (WA-1) and a module evaluates inside its first invocation's context.

import { AsyncLocalStorage } from "node:async_hooks";
import { createRequire, syncBuiltinESMExports } from "node:module";

const require = createRequire(import.meta.url);

export type InvocationContext = {
  id: string;
  run: string;
  /** The invocation's environment: its own copy, so a step's writes stay
   * with it. A null-prototype object, so any variable name is a plain key. */
  env: Record<string, string>;
  /** Variables the step deleted: absent for it, whatever the process has. */
  unset?: Set<string>;
  signal: AbortSignal;
  /** What the invocation holds that its settlement must release, and
   * whether it has settled. */
  resources: InvocationResources;
  /** Where timers, fetch requests and child processes started in this
   * context register: the invocation's own resources, or undefined while
   * a step module evaluates, whose work belongs to the module (D1) and is
   * never fenced (ADR 0004, loading). */
  work: InvocationResources | undefined;
  /** The invocation's log (WR-20, WR-23). */
  log: InvocationLog;
};

/** The registry that work started in the caller's context registers in,
 * if any: none outside every invocation, in a process scope, or while a
 * step module evaluates. */
function workOf(): InvocationResources | undefined {
  return invocations.getStore()?.work;
}

/** The per-invocation registry of what an invocation started that can
 * outlive its entrypoint call, kept in its async-context store, so code
 * running in that context finds it with current() and nothing global is
 * keyed by invocation. The runtime settles it when it queues the frame
 * that settles the invocation (ADR 0004, settlement), or when its
 * connection ends and the invocation settles with the process.
 *
 * The settle-time fence (D9, decisions 2026-10-28) registers here: the
 * timer wrappers each pending setTimeout, setInterval and setImmediate
 * handle, the fetch view each request in flight, the child-process
 * wrappers each child still running, each with a release that clears or
 * aborts it (a child is left running) and is unregistered when the
 * resource ends on its own; the log and platform paths check settled.
 * Work started in a process scope (PROCESS_SCOPE) has no invocation, and
 * work a step module starts while it evaluates belongs to the module, so
 * neither registers here or is ever fenced. What pending() counts just
 * before settlement is the leak report: Bun 1.3.10's
 * process._getActiveHandles() lists nothing, so this registry is the only
 * record of what an invocation left behind. */
export class InvocationResources {
  /** Set once the invocation has settled; it never clears. */
  settled = false;
  private live = new Map<() => void, string>();

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
    this.live.set(key, kind);
    return () => {
      this.live.delete(key);
    };
  }

  /** How many resources of each kind are registered now, by kind in
   * sorted order: what the call would leave behind if it settled now. */
  pending(): Record<string, number> {
    const counts: Record<string, number> = {};
    for (const kind of this.live.values()) counts[kind] = (counts[kind] ?? 0) + 1;
    return Object.fromEntries(Object.entries(counts).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)));
  }

  /** Marks the invocation settled and releases what it still held, once.
   * Returns how many resources of each kind were still registered: what
   * the call left behind (D9). A release that throws is skipped; the
   * others still run. */
  settle(): Record<string, number> {
    if (this.settled) return {};
    const left = this.pending();
    this.settled = true;
    const live = this.live;
    this.live = new Map();
    for (const release of live.keys()) {
      try {
        release();
      } catch {
        // One failed release must not keep the others.
      }
    }
    return left;
  }
}

/** How many characters of an invocation's log its WR-20 tail keeps. */
export const TAIL_LENGTH = 600;

/** One invocation's log: what it wrote through console, process.stdout
 * and process.stderr, merged line by line with empty lines dropped
 * (WR-23). Lines wait in a batch until the runtime takes them for a log
 * frame; the last TAIL_LENGTH characters stay for the tail of an escaped
 * outcome (WR-20). Once closed, at the invocation's settlement, every
 * write is dropped (the D9 fence). */
export class InvocationLog {
  closed = false;
  /** The Console that formats this invocation's console calls, made on
   * its first one, so counters, timers and groups are its own. */
  console?: Console;
  private batch: string[] = [];
  private partial: Record<string, string> = {};
  private recent = "";

  /** onLines is called when a write leaves lines in an empty batch, so
   * the runtime can schedule a log frame. */
  constructor(private onLines: () => void = () => {}) {}

  /** Appends text written to a stream ("stdout" or "stderr"). A line ends
   * at a newline; text after the last one waits for the rest of its line,
   * or for take(true). */
  write(stream: string, text: string) {
    if (this.closed || text === "") return;
    const parts = ((this.partial[stream] ?? "") + text).split("\n");
    this.partial[stream] = parts.pop()!;
    this.add(parts);
  }

  /** Appends whole lines, as the runtime does for an error the process
   * would have printed on its way out. */
  lines(text: string) {
    if (!this.closed) this.add(text.split("\n"));
  }

  private add(lines: string[]) {
    const was = this.batch.length;
    for (const line of lines) {
      if (line === "") continue;
      this.batch.push(line);
      this.recent = (this.recent === "" ? line : this.recent + "\n" + line).slice(-4 * TAIL_LENGTH);
    }
    if (was === 0 && this.batch.length > 0) this.onLines();
  }

  /** Takes the lines waiting to be sent; with all, also each stream's
   * unfinished line, as a line of its own. */
  take(all = false): string[] {
    if (all) {
      const rest = Object.values(this.partial).filter((s) => s !== "");
      this.partial = {};
      this.add(rest);
    }
    const out = this.batch;
    this.batch = [];
    return out;
  }

  /** The last TAIL_LENGTH characters (code points) of the log so far. */
  tail(): string {
    const cps = Array.from(this.recent);
    return cps.length > TAIL_LENGTH ? cps.slice(-TAIL_LENGTH).join("") : this.recent;
  }

  /** Drops every later write. */
  close() {
    this.closed = true;
    this.batch = [];
    this.partial = {};
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
 * host-provided module loads. This module publishes it when it is
 * evaluated, so a host that imports the host SDK before the modules that
 * capture the scope gives them the API; the runtime calls it again, which
 * changes nothing. */
export function installProcessScope() {
  const g = globalThis as Record<symbol, unknown>;
  if (g[PROCESS_SCOPE] === undefined) {
    Object.defineProperty(g, PROCESS_SCOPE, { value: Object.freeze(processScope), enumerable: false });
  }
}

installProcessScope();

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
 * Bun.env lists only the process's variables; enumerating process.env
 * lists the invocation's view, and so does a child process started
 * without an environment (installChildProcesses).
 *
 * Writes through the original object itself (Bun.env.X = … in code the
 * build does not rewrite, such as an unbundled dependency) are isolated
 * too: one of a variable the process does not have reaches the
 * prototype's set trap, which keeps it in the invocation's copy, and one
 * of a shadowed variable reaches its accessor. Two remain the process's,
 * because an ordinary object cannot trap them: a delete through the
 * original object, and a write of a variable the process has that no
 * invocation has written or been given yet. */
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
      // A write of a variable the process environment does not hold as
      // its own property walks up to here: inside an invocation it goes to
      // the invocation's copy, outside it becomes the process's.
      set(t, k, v, receiver) {
        const c = invocations.getStore();
        if (c && typeof k === "string" && receiver === real) {
          c.env[k] = String(v);
          c.unset?.delete(k);
          return true;
        }
        return Reflect.set(t, k, v, receiver);
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
let fetchRoute:
  | { origin: () => string | undefined; call: (c: PlatformCall, signal: AbortSignal) => Promise<PlatformAnswer> }
  | undefined;
let errorRoute: ((kind: string, e: unknown) => void) | undefined;
let fetchInstalled = false;

/** The reason a request an invocation left in flight is aborted with when
 * it settles: an AbortError, as for any aborted fetch. */
function settledAbort(): DOMException {
  return new DOMException("windmill-lite: the invocation has settled, so its request was aborted", "AbortError");
}

/** Wraps globalThis.fetch, before any step module loads: the fetch view.
 *
 * - A request to the facade origin becomes a platform call attributed to
 *   the invocation whose context made it. The caller is identified by its
 *   context, never by the token it sends, which the stock client captured
 *   at import (WA-31). One made after its invocation settled is refused
 *   with a TypeError (the D9 fence), as is one outside every invocation.
 * - Any other request passes through, with a signal of the fence's added
 *   to its own when an invocation starts it: it is registered as "fetch"
 *   until its response arrives or it fails, and aborted with an
 *   AbortError if the invocation settles first.
 *
 * origin() is read per call, because the welcome names the facade after
 * this is installed. */
export function installFetch(
  origin: () => string | undefined,
  call: (c: PlatformCall, signal: AbortSignal) => Promise<PlatformAnswer>,
) {
  fetchRoute = { origin, call };
  if (fetchInstalled) return;
  fetchInstalled = true;
  const inner = globalThis.fetch;
  const wrapped = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const route = fetchRoute!;
    const facade = route.origin();
    const inv = invocations.getStore();
    const own = init?.signal ?? (input instanceof Request ? input.signal : undefined);
    if (facade === undefined || originOf(input) !== facade) {
      const work = inv?.work;
      if (work === undefined) return inner(input, init);
      const fence = new AbortController();
      const done = work.track("fetch", () => fence.abort(settledAbort()));
      try {
        return await inner(input, { ...init, signal: own ? AbortSignal.any([own, fence.signal]) : fence.signal });
      } finally {
        done();
      }
    }
    if (inv === undefined) throw new TypeError("windmill-lite: a platform call outside any invocation cannot be attributed");
    if (inv.resources.settled) throw new TypeError("windmill-lite: the invocation has settled, so its platform calls are refused");
    const req = new Request(input, init);
    req.signal.throwIfAborted();
    // Bodies travel as bytes, so binary uploads and downloads stay exact.
    const body = req.method === "GET" || req.method === "HEAD" ? undefined : new Uint8Array(await req.arrayBuffer());
    const fence = new AbortController();
    const done = inv.work?.track("fetch", () => fence.abort(settledAbort()));
    try {
      const signal = AbortSignal.any([req.signal, fence.signal]);
      const a = await route.call({ inv: inv.id, method: req.method, url: req.url, headers: Object.fromEntries(req.headers), body }, signal);
      return new Response(a.body ?? null, { status: a.status, headers: a.headers });
    } finally {
      done?.();
    }
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

type TimerFn = (...args: any[]) => any;

// The timer functions as Bun has them; bound copies lose the properties
// util.promisify reads, so the wrappers copy those from these.
const bunTimers = {
  setTimeout: globalThis.setTimeout,
  setInterval: globalThis.setInterval,
  setImmediate: globalThis.setImmediate,
} as unknown as Record<string, Record<symbol, unknown>>;

/** The timer functions as Bun has them, before the fence wraps them. The
 * runtime's own timers use these, since its code may run in an
 * invocation's context. */
export const realTimers = {
  setTimeout: globalThis.setTimeout.bind(globalThis) as typeof setTimeout,
  clearTimeout: globalThis.clearTimeout.bind(globalThis) as typeof clearTimeout,
  setInterval: globalThis.setInterval.bind(globalThis) as typeof setInterval,
  clearInterval: globalThis.clearInterval.bind(globalThis) as typeof clearInterval,
  setImmediate: globalThis.setImmediate.bind(globalThis) as typeof setImmediate,
  clearImmediate: globalThis.clearImmediate.bind(globalThis) as typeof clearImmediate,
};

let timersInstalled = false;

/** Installs the timer fence (D9, decisions 2026-10-28), before any step
 * module loads: setTimeout, setInterval and setImmediate, on globalThis
 * and on node:timers, register each handle an invocation creates in its
 * context's registry ("timeout", "interval", "immediate"), and the
 * invocation's settlement clears every one still pending. A timeout or an
 * immediate that fires, and any handle cleared, is unregistered first. A
 * handle created after settlement, by a continuation of the invocation's
 * code that was already queued, is cleared at once, so it never fires.
 *
 * Handles created outside every invocation, in a process scope, or by a
 * step module while it evaluates belong to no invocation: they are never
 * registered or cleared. Promise-based waits (Bun.sleep,
 * node:timers/promises) do not go through these functions and are not
 * fenced. node:timers is patched before any step module imports it. On
 * Bun 1.3.10 the first named import of a builtin fixes its named bindings
 * for every later importer, so when a host's own module imported it by
 * name before the runtime was made, named imports keep Bun's functions;
 * the default import and require see these. */
export function installTimers() {
  if (timersInstalled) return;
  timersInstalled = true;
  // The unregister function of each live handle the fence registered, by
  // the handle and by its numeric id, which the clear functions accept too.
  const live = new Map<unknown, () => void>();
  const forget = (h: unknown) => {
    const done = live.get(h);
    if (done) done();
  };
  const remember = (kind: string, h: object, clear: (h: any) => void) => {
    const work = workOf();
    if (work === undefined) return;
    const id = Number(h);
    let gone = false;
    const done = work.track(kind, () => {
      drop();
      clear(h);
    });
    function drop() {
      gone = true;
      live.delete(h);
      if (!Number.isNaN(id)) live.delete(id);
    }
    if (gone) return;
    const end = () => {
      drop();
      done();
    };
    live.set(h, end);
    if (!Number.isNaN(id)) live.set(id, end);
  };
  const wrap = (name: string, real: TimerFn, kind: string, clear: (h: any) => void, once: boolean): TimerFn => {
    const w = function (fn: unknown, ...rest: unknown[]) {
      if (typeof fn !== "function" || workOf() === undefined) return real(fn, ...rest);
      // A timeout or an immediate fires once: it is unregistered first.
      const cb = once
        ? function (this: unknown, ...a: unknown[]) {
            forget(h);
            return (fn as TimerFn).apply(this, a);
          }
        : fn;
      const h: object = real(cb, ...rest);
      remember(kind, h, clear);
      return h;
    };
    // util.promisify(setTimeout) reads this, as on Bun's own functions.
    const custom = bunTimers[name][Symbol.for("nodejs.util.promisify.custom")];
    if (custom !== undefined) Object.defineProperty(w, Symbol.for("nodejs.util.promisify.custom"), { value: custom });
    Object.defineProperty(w, "name", { value: name });
    return w;
  };
  const unwrap = (name: string, real: TimerFn): TimerFn =>
    Object.defineProperty(
      function (h: unknown) {
        forget(h);
        return real(h);
      },
      "name",
      { value: name },
    ) as TimerFn;
  const r = realTimers;
  const fenced = {
    setTimeout: wrap("setTimeout", r.setTimeout, "timeout", r.clearTimeout, true),
    setInterval: wrap("setInterval", r.setInterval, "interval", r.clearInterval, false),
    setImmediate: wrap("setImmediate", r.setImmediate, "immediate", r.clearImmediate, true),
    clearTimeout: unwrap("clearTimeout", r.clearTimeout),
    clearInterval: unwrap("clearInterval", r.clearInterval),
    clearImmediate: unwrap("clearImmediate", r.clearImmediate),
  };
  Object.assign(globalThis, fenced);
  Object.assign(require("node:timers"), fenced);
  syncBuiltinESMExports();
}

/** process.stderr.write and process.stdout.write as Bun has them: output
 * no invocation owns, such as the runtime's diagnostics, goes through
 * these, never through an invocation's log. */
export const realStdout = process.stdout.write.bind(process.stdout) as (s: string) => boolean;
export const realStderr = process.stderr.write.bind(process.stderr) as (s: string) => boolean;

const CONSOLE_METHODS = [
  "log",
  "info",
  "debug",
  "warn",
  "error",
  "trace",
  "dir",
  "dirxml",
  "table",
  "assert",
  "count",
  "countReset",
  "group",
  "groupCollapsed",
  "groupEnd",
  "time",
  "timeEnd",
  "timeLog",
] as const;

let logsInstalled = false;

/** The context whose log a write goes to, if any. Work a step module
 * started while it evaluated is the module's, not the invocation's that
 * loaded it (ADR 0004, loading): once that invocation has settled, what
 * the work writes goes to the process's streams, as from a process scope. */
function logContext(): InvocationContext | undefined {
  const c = invocations.getStore();
  return c === undefined || (c.work === undefined && c.resources.settled) ? undefined : c;
}

/** Installs the log view (ADR 0004, per-invocation logs), before any step
 * module loads: inside an invocation, console's methods and
 * process.stdout.write and process.stderr.write write to the invocation's
 * log instead of the process's streams. A console call is formatted by a
 * Console of the invocation's own, Node's, without colours. Once the
 * invocation has settled its writes are dropped (the D9 fence). Outside
 * every invocation, in a process scope included, and from a module's own
 * work after its loading invocation settled (logContext), both write to
 * the process's streams as before. Bun.stdout, a raw file descriptor and
 * a child's inherited stdio are not attributed. */
export function installLogs() {
  if (logsInstalled) return;
  logsInstalled = true;
  const { Console } = require("node:console") as typeof import("node:console");
  const { Writable } = require("node:stream") as typeof import("node:stream");
  const sink = (log: InvocationLog, stream: string) =>
    new Writable({
      decodeStrings: false,
      write(chunk, _enc, cb) {
        log.write(stream, typeof chunk === "string" ? chunk : String(chunk));
        cb();
      },
    });
  const con = console as unknown as Record<string, TimerFn>;
  for (const m of CONSOLE_METHODS) {
    const real = con[m];
    if (typeof real !== "function") continue;
    con[m] = Object.defineProperty(
      function (this: unknown, ...args: unknown[]) {
        const c = logContext();
        if (c === undefined) return real.apply(console, args);
        if (c.log.closed) return;
        c.log.console ??= new Console({ stdout: sink(c.log, "stdout"), stderr: sink(c.log, "stderr"), colorMode: false });
        return (c.log.console as unknown as Record<string, TimerFn>)[m](...args);
      },
      "name",
      { value: m },
    ) as TimerFn;
  }
  for (const [name, stream] of [
    ["stdout", process.stdout],
    ["stderr", process.stderr],
  ] as const) {
    const real = stream.write;
    stream.write = function (this: unknown, chunk: unknown, encoding?: unknown, cb?: unknown) {
      const c = logContext();
      if (c === undefined) return (real as TimerFn).apply(stream, [chunk, encoding, cb].slice(0, arguments.length));
      const text =
        typeof chunk === "string" ? chunk : Buffer.from(chunk as Uint8Array).toString(typeof encoding === "string" ? (encoding as BufferEncoding) : "utf8");
      c.log.write(name, text);
      const done = typeof encoding === "function" ? encoding : cb;
      if (typeof done === "function") queueMicrotask(() => (done as TimerFn)());
      return true;
    } as typeof stream.write;
  }
}

/** A plain copy of the environment the current invocation sees, through
 * the process.env view: what a child process it starts inherits. */
function envSnapshot(): Record<string, string> {
  const env: Record<string, string> = {};
  for (const k of Object.keys(process.env)) {
    const v = process.env[k];
    if (v !== undefined) env[k] = v;
  }
  return env;
}

/** The arguments of a node:child_process call with its options given an
 * env, when called inside an invocation without one. The options are the
 * argument after the command, or after the argument list for the
 * functions that take one (hasArgs), read as Node reads them: an array,
 * null or undefined there is the argument list, an object the options. A
 * callback in their place moves along to make room. */
function withEnv(args: unknown[], hasArgs: boolean): unknown[] {
  if (invocations.getStore() === undefined) return args;
  const a = [...args];
  const at = hasArgs && a.length > 1 && (Array.isArray(a[1]) || a[1] === null || a[1] === undefined) ? 2 : 1;
  const o = a[at];
  if (o === undefined || o === null) a[at] = { env: envSnapshot() };
  else if (typeof o === "function") a.splice(at, 0, { env: envSnapshot() });
  else if (typeof o === "object" && (o as { env?: unknown }).env === undefined) a[at] = { ...o, env: envSnapshot() };
  return a;
}

/** Bun.spawn's arguments with an env, as withEnv: Bun.spawn(cmd, options)
 * or Bun.spawn({cmd, ...options}). */
function withBunEnv(args: unknown[]): unknown[] {
  if (invocations.getStore() === undefined) return args;
  const a = [...args];
  const at = Array.isArray(a[0]) || typeof a[0] === "string" ? 1 : 0;
  const o = a[at];
  if (o === undefined || o === null) a[at] = { env: envSnapshot() };
  else if (typeof o === "object" && (o as { env?: unknown }).env === undefined) a[at] = { ...o, env: envSnapshot() };
  return a;
}

let childrenInstalled = false;

/** Wraps child_process's spawn, exec, execFile and fork, their sync forms,
 * and Bun.spawn and Bun.spawnSync (ADR 0004, failure model item 7), before
 * any step module loads: a child started inside an invocation without an
 * explicit environment gets the invocation's view, not the process
 * environment, which Bun would otherwise pass (spawnSync, execSync and
 * Bun.spawn read neither process.env nor its view). A child that
 * outlives its invocation's settlement is counted as left behind
 * ("child"), and left running: nothing in the reference kills it. */
export function installChildProcesses() {
  if (childrenInstalled) return;
  childrenInstalled = true;
  const cp = require("node:child_process") as Record<string, TimerFn>;
  // A child that started (it has a pid) is registered until it exits.
  const track = (pid: unknown, ended: (done: () => void) => void) => {
    const done = pid === undefined ? undefined : workOf()?.track("child", () => {});
    if (done) ended(done);
  };
  for (const [name, hasArgs] of [
    ["spawn", true],
    ["exec", false],
    ["execFile", true],
    ["fork", true],
  ] as const) {
    const real = cp[name];
    const wrapped = Object.defineProperty(
      function (this: unknown, ...args: unknown[]) {
        const child = real.apply(this, withEnv(args, hasArgs)) as { pid?: number; once(e: string, f: () => void): void };
        track(child.pid, (done) => {
          child.once("exit", done);
          child.once("close", done);
        });
        return child;
      },
      "name",
      { value: real.name },
    ) as TimerFn;
    // util.promisify(exec) resolves {stdout, stderr} through this, as on
    // Node's own functions; the env view reaches it too.
    const custom = (real as unknown as Record<symbol, TimerFn | undefined>)[Symbol.for("nodejs.util.promisify.custom")];
    if (custom !== undefined) {
      Object.defineProperty(wrapped, Symbol.for("nodejs.util.promisify.custom"), {
        value: function (this: unknown, ...args: unknown[]) {
          return custom.apply(this, withEnv(args, hasArgs));
        },
      });
    }
    cp[name] = wrapped;
  }
  for (const [name, hasArgs] of [
    ["spawnSync", true],
    ["execSync", false],
    ["execFileSync", true],
  ] as const) {
    const real = cp[name];
    cp[name] = Object.defineProperty(
      function (this: unknown, ...args: unknown[]) {
        return real.apply(this, withEnv(args, hasArgs));
      },
      "name",
      { value: real.name },
    ) as TimerFn;
  }
  syncBuiltinESMExports();
  const spawn = Bun.spawn;
  const spawnSync = Bun.spawnSync;
  (Bun as { spawn: unknown }).spawn = function (...args: unknown[]) {
    const child = (spawn as TimerFn)(...withBunEnv(args)) as { pid?: number; exited: Promise<unknown> };
    track(child.pid, (done) => child.exited.then(done, done));
    return child;
  };
  (Bun as { spawnSync: unknown }).spawnSync = function (...args: unknown[]) {
    return (spawnSync as TimerFn)(...withBunEnv(args));
  };
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
