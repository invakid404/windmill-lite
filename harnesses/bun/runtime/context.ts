// Per-invocation identity in a shared process (ADR 0004, WA-31): each
// invocation runs inside an AsyncLocalStorage context that holds its id,
// its run, its environment and its abort signal. This module installs what
// reads that context:
//
// - the environment view, so that process.env, Bun.env and import.meta.env
//   answer from the current invocation first;
// - the fetch wrapper, which turns requests to the platform facade into
//   platform frames attributed to the invocation whose context made them;
// - the process-level error handlers, which never exit.
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
};

export const invocations = new AsyncLocalStorage<InvocationContext>();

/** The invocation whose context the caller runs in, if any. */
export function current(): InvocationContext | undefined {
  return invocations.getStore();
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
 * host's own code sees no change. Writes and deletes inside an invocation
 * go to its own copy and never reach the process environment. Enumerating
 * Bun.env lists only the process's variables. */
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
 * every invocation in it. Each error goes to onError. Attributing an error
 * to the invocation whose context raised it (item 2) is milestone C3a's;
 * until then every one is counted as unattributed. */
export function installProcessHandlers(onError: (kind: string, e: unknown) => void) {
  errorRoute = onError;
  if (handlersInstalled) return;
  handlersInstalled = true;
  process.on("uncaughtException", (e) => errorRoute!("uncaughtException", e));
  process.on("unhandledRejection", (e) => errorRoute!("unhandledRejection", e));
}
