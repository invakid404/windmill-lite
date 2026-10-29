// The native boundary mode (deviation D5): how the Bun harness runs a
// `nativets` or `//native` step, whose rules are those of Windmill's
// in-worker V8 runtime (WR-40 to WR-45), not of its Bun wrapper.
//
// Every invocation gets a fresh realm, a node:vm context, as every native
// job gets a fresh isolate (WR-41): module state and globals never survive
// it, whatever the process's module-state mode. Its global object holds
// the ECMAScript built-ins and the web APIs the native runtime installs
// (WR-45), taken from the harness's own realm, and nothing of Bun's; its
// timers and fetches are the invocation's, released when it settles, as
// the isolate is dropped when the job ends. The web APIs are given to the
// realm through a boundary that fails closed (below): every promise and
// object they hand over is tracked as the invocation's, or counted and
// reported when it settles. The script runs in its native
// form (harnesses/bun/build/native.ts): one async function that the
// harness compiles with the environment preamble in front of its text,
// then calls with the static imports it resolved and linked.
//
// The boundary's rules follow: arguments bound by name, parsed in the
// realm, never hydrated (WR-41); the result is JSON.stringify(value ??
// null) in the realm, with no replacer (WR-42); a thrown value becomes
// {message, stack, name}, and anything that fails before the module runs
// an `execution error:` (WR-43). The process-level errors of a native
// invocation are attributed to it through its realm as well as its async
// context, so an unhandled rejection fails it (WR-43); runtime.ts routes
// them here.

import { AsyncLocalStorage } from "node:async_hooks";
import { join } from "node:path";
import { types } from "node:util";
import vm from "node:vm";
import { current, type InvocationResources, invocations, realTimers } from "./context.ts";
import { type Param, signatureErrorText, signatureOf } from "./wrapper.ts";

/** The URL a native step's module is loaded as, and the embedded client's,
 * against which import specifiers resolve (WR-40). */
export const SCRIPT_URL = "file:///eval.ts";
export const CLIENT_URL = "file:///windmill.ts";
/** fetch's User-Agent when the script sets none and has no `//useragent`
 * header (WR-45). */
export const DEFAULT_USER_AGENT = "windmill/beta";

/** The record of a module's native form (the build's NativeForm), with the
 * header annotations and, for a nativets script that did not transpile,
 * the transpiler's diagnostic. */
export type NativeForm = {
  kind: "nativets" | "bunnative" | "client";
  param: string;
  preamble_at: number;
  imports: string[];
  links: { import: number; name: string }[];
  user_agent?: string;
  proxy?: string;
  transpile_error?: string;
};

/** How a native invocation ended: the result's JSON text, or the error
 * object's, which the engine stores as {"error": …} (WR-21). */
export type NativeResult = { ok: string } | { error: string };

/** The invocation a native job belongs to. */
export type NativeInvocation = {
  id: string;
  script: string;
  entry: string;
  args: Record<string, unknown>;
  env: Record<string, string>;
  resources: InvocationResources;
};

/** The web APIs the native runtime installs (WR-45), by name. Bun 1.3.10
 * has no FileReader, so a native step finds none. */
export const WEB_GLOBALS = [
  "Request", "Response", "Headers", "FormData",
  "URL", "URLSearchParams", "URLPattern",
  "atob", "btoa", "TextEncoder", "TextDecoder", "TextEncoderStream", "TextDecoderStream",
  "Blob", "File", "FileReader",
  "crypto", "Crypto", "CryptoKey", "SubtleCrypto",
  "Event", "EventTarget", "CustomEvent", "MessageEvent", "CloseEvent", "ErrorEvent", "reportError",
  "AbortController", "AbortSignal", "DOMException",
  "ReadableStream", "ReadableStreamDefaultReader", "ReadableStreamBYOBReader", "ReadableStreamDefaultController",
  "ReadableByteStreamController", "ReadableStreamBYOBRequest", "WritableStream", "WritableStreamDefaultWriter",
  "WritableStreamDefaultController", "TransformStream", "TransformStreamDefaultController",
  "ByteLengthQueuingStrategy", "CountQueuingStrategy", "CompressionStream", "DecompressionStream",
  "MessageChannel", "MessagePort", "structuredClone",
  "performance", "Performance", "PerformanceEntry", "PerformanceMark", "PerformanceMeasure",
];

// The native boundary fails closed (ADR 0001, D5): every object and promise
// a web API hands to a native invocation is either tracked, the moment the
// boundary hands it over, or counted, and the count is reported when the
// invocation settles. The harness realm's prototypes cannot be changed (Bun
// 1.3.10 makes most of their methods read-only, and Bun steps share them),
// so the boundary sits between the realm and them:
//
// - Every web API class above is given to the realm as a realm-facing
//   subclass (Native.realmClass), made once per process. Its constructor
//   is the only way the realm makes one, and each of its members, its
//   class's own and inherited ones, string- and symbol-named, and its
//   static ones, calls the original and passes on what it returns or
//   throws (Native.toRealm). The function globals (atob, btoa,
//   reportError, structuredClone) do the same, and crypto, crypto.subtle
//   and performance, which every caller shares, are forwarders: objects of
//   their realm-facing classes whose members apply to them.
// - What the harness calls back is wrapped where the realm hands it over:
//   a callback argument of a method (FormData.forEach), the callbacks of a
//   stream's underlying source, sink, transformer and strategy
//   (STREAM_CALLBACKS), and every event listener and on… handler of every
//   event target, which runs in the async context that registered it and
//   gets its event, and its this, passed on.
// - toRealm decides what each value is, once, in this order: a primitive,
//   or an object or function of the realm itself, is the script's own; a
//   promise of the harness realm is tracked for the invocation; an
//   instance of a web API class takes its realm-facing class's prototype;
//   an array, Map, Set or plain object has its contents passed on, all
//   through; an iterator is given a forwarder whose results are passed on;
//   buffers, views, dates, regular expressions, errors and boxed
//   primitives are data, with nothing to track. Anything else of the
//   harness realm, an object of a class outside this list or a function,
//   is untracked: it is handed over as it is, and counted
//   (Native.untracked).

/** The dictionaries a stream constructor reads callbacks from, by
 * constructor: for each argument, the callbacks it may hold. */
const STREAM_CALLBACKS: Record<string, string[][]> = {
  ReadableStream: [["start", "pull", "cancel"], ["size"]],
  WritableStream: [["start", "write", "close", "abort"], ["size"]],
  TransformStream: [["start", "transform", "flush", "cancel"], ["size"], ["size"]],
};

/** The methods whose promise gives JSON data, in which nothing is left to
 * track, so a continuation gets it without a walk. */
const DATA_METHODS = new Set(["json"]);

/** The EventTarget methods that take a listener. */
const LISTENER_METHODS = new Set(["addEventListener", "removeEventListener"]);

/** Statuses whose response has no body. */
const NULL_BODY = new Set([101, 103, 204, 205, 304]);

/** A failure to load a native module: its static imports, its link or its
 * compilation. It fails the step as an `execution error:` (WR-43). */
class LoadFailure extends Error {}

/** The functions every native realm runs before any of its script's code:
 * the argument copy, which builds each argument in the realm from its
 * parsed value (WR-41: each is JSON.parse'd there) with the realm's
 * intrinsics taken before the script can change them, and the result's
 * serialisation (WR-42), which reads the realm's JSON when it runs, as the
 * native runtime does. */
const REALM_SCRIPT = new vm.Script(`(function () {
  const keys = Object.keys, define = Object.defineProperty, isArray = Array.isArray;
  function copy(v) {
    if (v === null || typeof v !== "object") return v;
    if (isArray(v)) {
      const a = [];
      for (let i = 0; i < v.length; i++) define(a, i, { value: copy(v[i]), writable: true, enumerable: true, configurable: true });
      return a;
    }
    const o = {};
    for (const k of keys(v)) define(o, k, { value: copy(v[k]), writable: true, enumerable: true, configurable: true });
    return o;
  }
  return { copy, stringify: (v) => JSON.stringify(v ?? null), errorProto: Error.prototype, promiseProto: Promise.prototype };
})`, { filename: "windmill-lite:native" });

/** An identifier the preamble can declare as a const. A variable name that
 * is not one fails every nativets step's load on Windmill too (WR-45). */
const IDENTIFIER = /^[\p{ID_Start}$_][\p{ID_Continue}$‌‍]*$/u;

/** The Bun harness's native mode: one per runtime. */
export class Native {
  private texts = new Map<string, Promise<string>>();
  private compiled = new Map<string, { script: vm.Script; lines: string[] }>();
  private clientForm: Promise<NativeForm> | undefined;
  private realms = new WeakMap<object, NativeJob>();
  private byContext = new WeakMap<object, NativeJob>();
  private web: Record<string, unknown> = {};
  /** Each realm-facing class's prototype, by the prototype of its class. */
  private realmProtos = new Map<object, object>();
  /** The realm-facing classes' prototypes. */
  private facing = new Set<object>();
  /** The forwarder of each object every caller shares. */
  private forwarders = new Map<object, object>();
  /** What the boundary itself made and hands to the realm: its classes,
   * wrappers and forwarders. */
  private mine = new WeakSet<object>();
  /** The promises given to a native invocation, and those derived from
   * them, with the job they belong to. */
  private tracked = new WeakMap<object, NativeJob>();
  private trackedProto: object | undefined;
  /** The tracked promises whose value is JSON data (DATA_METHODS). */
  private dataPromises = new WeakSet<object>();
  /** The arrays, maps, sets and plain objects of the harness realm whose
   * contents were passed on already. */
  private walked = new WeakSet<object>();
  /** The realm-facing File's prototype: Bun 1.3.10's File shares Blob's
   * prototype, so a File the harness made is told apart by instanceof. */
  private fileProto: object | undefined;
  /** Each event target's listener wrappers, by the listener. */
  private listeners = new WeakMap<object, WeakMap<object, (e: unknown) => unknown>>();
  /** Each event target's on… handlers, as the realm gave them. */
  private handlers = new WeakMap<object, Map<string, unknown>>();
  /** A fetched response's own url, redirected and type, which a response
   * the harness constructs around its body cannot carry; kept through
   * every clone. */
  private responseMeta = new WeakMap<object, { url: string; redirected: boolean; type: string }>();
  /** Bun's timers, before the fence wraps them: a realm registers its
   * handles itself (NativeJob.timer). */
  private timers = { ...realTimers, queueMicrotask };
  private fetch: typeof fetch = fetch;
  private console: Console = console;

  constructor(
    private opts: {
      bundleDir: string;
      /** The platform facade's origin: BASE_URL and BASE_INTERNAL_URL. */
      baseURL: () => string | undefined;
      /** An error of a native job's code outside its entrypoint's call: a
       * throw from a microtask it queued. */
      onError: (job: NativeJob, e: unknown) => void;
      /** Something the boundary handed over untracked (Native.untracked)
       * that no unsettled job can count: in no native invocation's
       * context (job undefined), or after its job settled. */
      onUntracked: (job: NativeJob | undefined, kind: string) => void;
    },
  ) {}

  /** Takes the harness realm's web APIs, fetch included: called once the
   * runtime has wrapped fetch for the facade (ADR 0004), before any step
   * module loads, so no step's change to them reaches a native realm. */
  capture() {
    const g = globalThis as Record<string, unknown>;
    for (const name of WEB_GLOBALS) if (g[name] !== undefined) this.web[name] = g[name];
    this.fetch = globalThis.fetch;
    this.console = console;
    this.trackedProto ??= this.trackedPromise().prototype;
    // Each class gets its own, even one whose prototype another shares
    // (Bun 1.3.10's File has Blob's); an instance takes the first's.
    const made: [object, object][] = [];
    for (const name of WEB_GLOBALS) {
      const v = this.web[name];
      if (typeof v !== "function") continue;
      if (/^[A-Z]/.test(name)) {
        const R = this.realmClass(name, v as new (...a: any[]) => object);
        made.push([(v as { prototype: object }).prototype, R.prototype]);
        this.web[name] = R;
      } else {
        const d = this.member(name, { value: v, writable: true, enumerable: false, configurable: true }, globalThis);
        this.web[name] = d.value;
      }
    }
    // A realm-facing class inherits from its class's parent's, as V8's
    // CustomEvent from Event and AbortSignal from EventTarget, so
    // instanceof holds in the realm. A File is a Blob, as in V8, though
    // Bun's File has no Blob parent of its own.
    for (const [base, proto] of made) {
      const parent = this.realmProtos.get(Object.getPrototypeOf(base));
      if (parent !== undefined) Object.setPrototypeOf(proto, parent);
    }
    this.fileProto = (this.web.File as { prototype: object }).prototype;
    Object.setPrototypeOf(this.fileProto, (this.web.Blob as { prototype: object }).prototype);
    this.responseClass(this.web.Response as { prototype: object });
    const crypto = globalThis.crypto;
    const subtle = this.forwarder(crypto.subtle, this.web.SubtleCrypto as { prototype: object });
    this.web.crypto = this.forwarder(crypto, this.web.Crypto as { prototype: object }, { subtle });
    this.web.performance = this.forwarder(globalThis.performance, this.web.Performance as { prototype: object });
  }

  /** The class of the promises a native invocation is given: a promise of
   * the harness realm whose then records the invocation the promise
   * belongs to on the promise it derives, which by species is one of
   * these too, and passes its value or reason on with toRealm. catch,
   * finally and await all go through then. */
  private trackedPromise() {
    const native = this;
    return class TrackedPromise<T> extends Promise<T> {
      then<A = T, B = never>(onFulfilled?: ((v: T) => A | PromiseLike<A>) | null, onRejected?: ((e: unknown) => B | PromiseLike<B>) | null): Promise<A | B> {
        const job = native.tracked.get(this) ?? native.currentJob();
        const pass = native.dataPromises.has(this) ? (v: T) => v : (v: T) => native.toRealm(v, job) as T;
        const ok = typeof onFulfilled === "function" ? (v: T) => onFulfilled(pass(v)) : onFulfilled;
        const fail = typeof onRejected === "function" ? (e: unknown) => onRejected(native.toRealm(e, job)) : onRejected;
        const p = super.then(ok, fail);
        if (job !== undefined) native.tracked.set(p, job);
        return p;
      }
    };
  }

  /** The realm-facing subclass of a web API class: each member its
   * prototype has, its own and inherited ones up to Object.prototype,
   * string- and symbol-named, and each of its own static ones, as
   * Native.member makes it. A stream class's constructor wraps the
   * callbacks of its dictionaries (STREAM_CALLBACKS). */
  private realmClass(name: string, C: new (...a: any[]) => object) {
    const native = this;
    const dicts = STREAM_CALLBACKS[name];
    const R =
      dicts === undefined
        ? { [name]: class extends C {} }[name]
        : {
            [name]: class extends C {
              constructor(...a: any[]) {
                super(...a.map((x, i) => (i < dicts.length ? native.callbacks(x, dicts[i]) : x)));
              }
            },
          }[name];
    const seen = new Set<PropertyKey>(["constructor"]);
    for (let proto: object | null = C.prototype; proto !== null && proto !== Object.prototype; proto = Object.getPrototypeOf(proto)) {
      for (const k of Reflect.ownKeys(proto)) {
        if (seen.has(k)) continue;
        seen.add(k);
        Object.defineProperty(R.prototype, k, this.member(k, Object.getOwnPropertyDescriptor(proto, k)!));
      }
    }
    for (const k of Reflect.ownKeys(C)) {
      if (k === "prototype" || k === "length" || k === "name") continue;
      Object.defineProperty(R, k, this.member(k, Object.getOwnPropertyDescriptor(C, k)!, C));
    }
    if (!this.realmProtos.has(C.prototype)) this.realmProtos.set(C.prototype, R.prototype);
    this.facing.add(R.prototype);
    this.mine.add(R);
    return R;
  }

  /** A web API member as the realm gets it, on a realm-facing class, where
   * it applies to this, or applied to self (a static member's class, a
   * forwarder's shared object, globalThis for a function global):
   *
   * - a method passes on what it returns or throws (toRealm), and calls
   *   back a function argument through Native.callback, or, for
   *   addEventListener and removeEventListener, a listener through its
   *   wrapper (Native.listener);
   * - an on… accessor is an event handler (Native.handler);
   * - any other getter passes on what it gives, and a setter what it
   *   throws;
   * - a data property is copied as it is, so a class keeps its constants
   *   and its toStringTag whatever its prototype inherits from. */
  private member(k: PropertyKey, d: PropertyDescriptor, self?: object): PropertyDescriptor {
    const native = this;
    if (typeof d.value === "function") {
      const m = d.value as (...a: unknown[]) => unknown;
      const register = typeof k === "string" && LISTENER_METHODS.has(k);
      const data = typeof k === "string" && DATA_METHODS.has(k);
      const value = {
        [k](this: unknown, ...a: unknown[]) {
          const target = self ?? this;
          const args = register ? native.listenerArgs(target, a) : a.map((x) => (typeof x === "function" ? native.callback(x as (...b: unknown[]) => unknown) : x));
          const r = native.call(m, target, args);
          if (data && r instanceof Promise) native.dataPromises.add(r);
          return r;
        },
      }[k as string];
      this.mine.add(value);
      return { ...d, value, configurable: true };
    }
    if (d.get === undefined && d.set === undefined) return { ...d, configurable: true };
    if (typeof k === "string" && k.startsWith("on") && d.set !== undefined) return this.handler(k, d, self);
    const { get, set } = d;
    const out: PropertyDescriptor = { enumerable: d.enumerable, configurable: true };
    if (get !== undefined) out.get = { get(this: unknown) { return native.call(get, self ?? this, []); } }.get;
    if (set !== undefined) out.set = { set(this: unknown, v: unknown) { native.call(set, self ?? this, [v]); } }.set;
    for (const f of [out.get, out.set]) if (f !== undefined) this.mine.add(f);
    return out;
  }

  /** Calls a web API function and passes on what it returns, or what it
   * throws. */
  private call(fn: (...a: unknown[]) => unknown, self: unknown, args: unknown[]): unknown {
    let r: unknown;
    try {
      r = Reflect.apply(fn, self, args);
    } catch (e) {
      throw this.toRealm(e);
    }
    return this.toRealm(r);
  }

  /** An on… event handler accessor: the handler is set as a listener
   * would be added (Native.listener), and the getter reads back the
   * handler the realm gave. */
  private handler(k: string, d: PropertyDescriptor, self?: object): PropertyDescriptor {
    const native = this;
    const set = d.set!;
    const handlers = this.handlers;
    const out: PropertyDescriptor = {
      enumerable: d.enumerable,
      configurable: true,
      get: {
        get(this: object) {
          return handlers.get(self ?? this)?.get(k) ?? null;
        },
      }.get,
      set: {
        set(this: object, h: unknown) {
          const target = self ?? this;
          let own = handlers.get(target);
          if (own === undefined) handlers.set(target, (own = new Map()));
          own.set(k, typeof h === "function" ? h : null);
          native.call(set as (...a: unknown[]) => unknown, target, [typeof h === "function" ? native.listener(target, h) : h]);
        },
      }.set,
    };
    this.mine.add(out.get!);
    this.mine.add(out.set!);
    return out;
  }

  /** addEventListener's or removeEventListener's arguments, with the
   * listener, a function or an object with handleEvent, replaced by its
   * wrapper for the target. */
  private listenerArgs(target: unknown, a: unknown[]): unknown[] {
    const [type, l, ...rest] = a;
    if (l === null || (typeof l !== "function" && typeof l !== "object")) return a;
    if (target === null || (typeof target !== "object" && typeof target !== "function")) return a;
    return [type, this.listener(target, l), ...rest];
  }

  /** A listener's wrapper for an event target, one per target and
   * listener, so removing a listener by identity removes it: it runs in
   * the async context that first registered it, so what it does is that
   * invocation's, and passes on its event and its this (toRealm). An
   * object's handleEvent is read when the event arrives. */
  private listener(target: object, l: object): (e: unknown) => unknown {
    let byTarget = this.listeners.get(target);
    if (byTarget === undefined) this.listeners.set(target, (byTarget = new WeakMap()));
    let w = byTarget.get(l);
    if (w === undefined) {
      const native = this;
      const run = AsyncLocalStorage.snapshot();
      w = function (this: unknown, e: unknown) {
        return run(() => {
          const event = native.toRealm(e);
          if (typeof l === "function") return Reflect.apply(l, native.toRealm(this), [event]);
          return Reflect.apply((l as { handleEvent: (e: unknown) => unknown }).handleEvent, l, [event]);
        });
      };
      this.mine.add(w);
      byTarget.set(l, w);
    }
    return w;
  }

  /** A stream constructor's dictionary argument, with each of the
   * callbacks it names wrapped: an object that inherits from the
   * dictionary, with an own property for each of them that calls the
   * dictionary's, on the dictionary, with its arguments passed on
   * (toRealm). Each is read once, when the stream is constructed, as the
   * stream reads it. */
  private callbacks(dict: unknown, names: string[]): unknown {
    if (dict === null || typeof dict !== "object") return dict;
    const native = this;
    let out: object | undefined;
    for (const k of names) {
      const fn = (dict as Record<string, unknown>)[k];
      if (typeof fn !== "function") continue;
      out ??= Object.create(dict) as object;
      const value = {
        [k](...a: unknown[]) {
          return Reflect.apply(fn, dict, a.map((x) => native.toRealm(x)));
        },
      }[k];
      Object.defineProperty(out, k, { value, writable: true, enumerable: true, configurable: true });
    }
    return out ?? dict;
  }

  /** The realm-facing Response's url, redirected and type read a fetched
   * response's own, and its clone carries them on (Native.respond). */
  private responseClass(R: { prototype: object }) {
    const meta = this.responseMeta;
    for (const k of ["url", "redirected", "type"] as const) {
      const d = Object.getOwnPropertyDescriptor(R.prototype, k);
      if (d?.get === undefined) continue;
      const get = d.get;
      Object.defineProperty(R.prototype, k, {
        ...d,
        get(this: object) {
          const m = meta.get(this);
          return m !== undefined ? m[k] : get.call(this);
        },
      });
    }
    const d = Object.getOwnPropertyDescriptor(R.prototype, "clone")!;
    const clone = d.value as (this: object) => object;
    Object.defineProperty(R.prototype, "clone", {
      ...d,
      value: function clone_(this: object) {
        const c = clone.call(this);
        const m = meta.get(this);
        if (m !== undefined) meta.set(c, m);
        return c;
      },
    });
    Object.defineProperty(R.prototype.clone, "name", { value: "clone" });
  }

  /** An object of a realm-facing class that stands for an object every
   * caller shares, whose prototype is never changed: each member it and
   * its prototypes have, as Native.member makes it, applied to it; its
   * own data properties read it when they are read; props are the
   * forwarder's own. */
  private forwarder(obj: object, R: { prototype: object }, props: Record<string, unknown> = {}) {
    const out = Object.create(R.prototype);
    const seen = new Set<PropertyKey>(["constructor", ...Object.keys(props)]);
    for (let p: object | null = obj; p !== null && p !== Object.prototype; p = Object.getPrototypeOf(p)) {
      for (const k of Reflect.ownKeys(p)) {
        if (seen.has(k)) continue;
        seen.add(k);
        const d = Object.getOwnPropertyDescriptor(p, k)!;
        const own = p === obj && typeof d.value !== "function" && d.get === undefined && d.set === undefined;
        const get = () => this.toRealm((obj as Record<PropertyKey, unknown>)[k]);
        Object.defineProperty(out, k, own ? { get, enumerable: d.enumerable, configurable: true } : this.member(k, d, obj));
      }
    }
    for (const [k, v] of Object.entries(props)) Object.defineProperty(out, k, { value: v, enumerable: true, configurable: true });
    this.mine.add(out);
    this.forwarders.set(obj, out);
    return out;
  }

  /** What a web API hands to the realm, as the invocation job (by default
   * the one whose context the caller runs in) gets it: tracked, or
   * counted as untracked. See the boundary's rules above. */
  toRealm(v: unknown, job: NativeJob | undefined = this.currentJob()): unknown {
    if (v === null || (typeof v !== "object" && typeof v !== "function")) return v;
    if (this.mine.has(v) || types.isProxy(v)) return v;
    const forwarder = this.forwarders.get(v);
    if (forwarder !== undefined) return forwarder;
    if (typeof v === "function") {
      if (v instanceof Function) this.untracked(job, `function ${v.name || "(anonymous)"}`);
      return v;
    }
    const proto = Object.getPrototypeOf(v);
    // The realm's own, handed back.
    if (proto !== null && !(v instanceof Object)) return v;
    if (v instanceof Promise) return this.promise(v, job);
    for (let p = proto; p !== null; p = Object.getPrototypeOf(p)) {
      if (this.facing.has(p)) return v;
      const R = this.realmProtos.get(p);
      if (R === undefined) continue;
      // A subclass the harness made of a web API class is not one.
      if (p !== proto) break;
      Object.setPrototypeOf(v, v instanceof File ? this.fileProto! : R);
      return v;
    }
    if (this.walked.has(v)) return v;
    if (Array.isArray(v)) {
      this.walked.add(v);
      for (let i = 0; i < v.length; i++) {
        const x = this.toRealm(v[i], job);
        if (x !== v[i]) Reflect.set(v, i, x);
      }
      return v;
    }
    if (v instanceof Map) {
      this.walked.add(v);
      const entries = [...v].map(([k, x]) => [this.toRealm(k, job), this.toRealm(x, job)] as const);
      v.clear();
      for (const [k, x] of entries) v.set(k, x);
      return v;
    }
    if (v instanceof Set) {
      this.walked.add(v);
      const values = [...v].map((x) => this.toRealm(x, job));
      v.clear();
      for (const x of values) v.add(x);
      return v;
    }
    const it = v as AsyncIterator<unknown> & Iterator<unknown> & { [Symbol.asyncIterator]?: unknown; [Symbol.iterator]?: unknown };
    if (typeof it.next === "function" && typeof it[Symbol.asyncIterator] === "function") return this.iterator(it);
    if (typeof it.next === "function" && typeof it[Symbol.iterator] === "function") return this.syncIterator(it);
    if (proto === Object.prototype || proto === null) {
      this.walked.add(v);
      for (const k of Object.keys(v)) {
        const o = v as Record<string, unknown>;
        const x = this.toRealm(o[k], job);
        if (x !== o[k]) Reflect.set(o, k, x);
      }
      return v;
    }
    if (isData(v)) return v;
    this.untracked(job, proto?.constructor?.name || Object.prototype.toString.call(v));
    return v;
  }

  /** A promise of the harness realm, tracked for the job: as the same
   * object, so what the web API's own code does with it (marking it
   * handled, as a stream's closed and Bun's own iteration do) is
   * unchanged, with TrackedPromise's prototype when it has
   * Promise.prototype. One of another class is tracked itself, but its
   * continuations cannot be, so it is counted too. With no job, it is
   * counted. */
  private promise(p: Promise<unknown>, job: NativeJob | undefined): Promise<unknown> {
    if (this.tracked.has(p)) return p;
    if (job === undefined) {
      this.untracked(undefined, "Promise");
      return p;
    }
    const proto = Object.getPrototypeOf(p);
    if (proto === Promise.prototype) Object.setPrototypeOf(p, this.trackedProto!);
    else if (proto !== this.trackedProto) this.untracked(job, `${proto?.constructor?.name ?? "Promise"} (its continuations)`);
    this.tracked.set(p, job);
    return p;
  }

  /** Counts something handed to a native invocation that the boundary does
   * not track, on its job, which reports the count when it settles; or,
   * when no unsettled job can count it, reports it through onUntracked. */
  private untracked(job: NativeJob | undefined, kind: string) {
    if (job !== undefined && !job.inv.resources.settled) job.count(kind);
    else this.opts.onUntracked(job, kind);
  }

  /** A callback a web API method calls: its this and its arguments passed
   * on with toRealm. */
  private callback(fn: (...a: unknown[]) => unknown) {
    const native = this;
    return function (this: unknown, ...a: unknown[]) {
      return Reflect.apply(fn, native.toRealm(this), a.map((x) => native.toRealm(x)));
    };
  }

  /** A sync iterator whose results are passed on. */
  private syncIterator(it: Iterator<unknown>): IterableIterator<unknown> {
    const out: IterableIterator<unknown> = {
      next: (...a: [] | [unknown]) => this.toRealm(it.next(...a)) as IteratorResult<unknown>,
      [Symbol.iterator]() {
        return out;
      },
    };
    if (typeof it.return === "function") out.return = (x?: unknown) => this.toRealm(it.return!(x)) as IteratorResult<unknown>;
    if (typeof it.throw === "function") out.throw = (e?: unknown) => this.toRealm(it.throw!(e)) as IteratorResult<unknown>;
    this.mine.add(out);
    return out;
  }

  /** An async iterator whose next, return and throw pass on their
   * promises, so a continuation gets each result passed on. */
  private iterator(it: AsyncIterator<unknown>): AsyncIterableIterator<unknown> {
    const out: AsyncIterableIterator<unknown> = {
      next: (...a: [] | [unknown]) => this.toRealm(it.next(...a)) as Promise<IteratorResult<unknown>>,
      [Symbol.asyncIterator]() {
        return out;
      },
    };
    if (typeof it.return === "function") out.return = (x?: unknown) => this.toRealm(it.return!(x)) as Promise<IteratorResult<unknown>>;
    if (typeof it.throw === "function") out.throw = (e?: unknown) => this.toRealm(it.throw!(e)) as Promise<IteratorResult<unknown>>;
    this.mine.add(out);
    return out;
  }

  /** A fetch's response as the realm gets it: of the realm-facing Response,
   * with a body that reports its end, and the fetched response's url,
   * redirected and type, which its clones keep. The request stays the
   * invocation's until its body has been read to its end, has failed or
   * was cancelled, or the invocation settles, whatever reads it (a clone
   * included); done is called once, at that end. */
  respond(res: Response, done: () => void): Response {
    const src = res.body;
    if (src === null || NULL_BODY.has(res.status)) {
      done();
      return this.toRealm(res) as Response;
    }
    const reader = src.getReader();
    let ended = false;
    const end = () => {
      if (!ended) {
        ended = true;
        done();
      }
    };
    const body = new ReadableStream({
      async pull(c) {
        try {
          const r = await reader.read();
          if (r.done) {
            end();
            c.close();
          } else c.enqueue(r.value);
        } catch (e) {
          end();
          c.error(e);
        }
      },
      cancel(reason) {
        end();
        return reader.cancel(reason);
      },
    });
    const R = this.web.Response as typeof Response;
    const out = new R(body, { status: res.status, statusText: res.statusText, headers: res.headers });
    this.responseMeta.set(out, { url: res.url, redirected: res.redirected, type: res.type });
    return out;
  }

  private currentJob(): NativeJob | undefined {
    const c = current();
    return c === undefined ? undefined : this.byContext.get(c);
  }

  /** The native job a promise belongs to, for an unhandled rejection,
   * which carries no async context (ADR 0004, failure model item 2): the
   * one a web API's promise was given to, or the one whose realm made it. */
  jobOfPromise(promise: unknown): NativeJob | undefined {
    const tracked = promise !== null && typeof promise === "object" ? this.tracked.get(promise) : undefined;
    if (tracked !== undefined) return tracked;
    let p: unknown = promise;
    try {
      for (let i = 0; i < 8 && p !== null && (typeof p === "object" || typeof p === "function"); i++) {
        p = Object.getPrototypeOf(p);
        const job = p === null ? undefined : this.realms.get(p as object);
        if (job !== undefined) return job;
      }
    } catch {
      // A proxy that refuses: not a promise of a native realm.
    }
    return undefined;
  }

  /** The native job of an invocation's context object, if it has one. */
  jobOfContext(c: object): NativeJob | undefined {
    return this.byContext.get(c);
  }

  /** Runs one native invocation: the checks the worker makes before the
   * runtime starts, then the module's load, the entrypoint's call and the
   * result's serialisation. A failure of the harness itself throws. */
  async run(inv: NativeInvocation, meta: unknown, context: object, stepId: string | undefined): Promise<NativeResult> {
    const form = (meta as { native: NativeForm }).native;
    // nativets always calls main (WR-41).
    const entry = form.kind === "nativets" ? "main" : inv.entry;
    if (form.transpile_error !== undefined) return { error: signatureErrorText(`Error: ${form.transpile_error}`, stepId) };
    const sig = signatureOf(meta, entry);
    if (sig?.error !== undefined) return { error: signatureErrorText(sig.error, stepId) };
    // A proxy is an Enterprise Edition feature (WR-45).
    if (form.proxy !== undefined) return { error: signatureErrorText("Proxy is an EE feature", stepId) };
    const job = this.open(inv, form, context);
    let ns: Record<string, unknown>;
    try {
      ns = await job.load(join("scripts", "bun", ...inv.script.split("/")) + ".native.js", form, SCRIPT_URL);
    } catch (e) {
      if (e instanceof LoadFailure) return { error: signatureErrorText(`Error: failed to load module: ${e.message}`, stepId) };
      return { error: job.errorText(e) };
    }
    try {
      const fn = ns[entry];
      // The call goes through the module namespace (WR-41).
      if (typeof fn !== "function") throw new job.g.TypeError(`${entry} is not a function`);
      const value = await fn.apply(ns, job.bind(sig?.params ?? [], inv.args));
      return { ok: job.stringify(value) };
    } catch (e) {
      return { error: job.errorText(e) };
    }
  }

  /** A new realm for an invocation, which context (its async-context
   * store) names from then on, as the realm of its promises does. */
  open(inv: NativeInvocation, form: NativeForm, context: object): NativeJob {
    const job = new NativeJob(this, inv, form);
    this.realms.set(job.promiseProto, job);
    this.byContext.set(context, job);
    return job;
  }

  /** A bundle file's text, read once per process. */
  text(rel: string): Promise<string> {
    let p = this.texts.get(rel);
    if (!p) {
      p = Bun.file(join(this.opts.bundleDir, rel)).text();
      this.texts.set(rel, p);
    }
    return p;
  }

  /** The embedded client's record, read once per process. */
  client(): Promise<NativeForm> {
    this.clientForm ??= Bun.file(join(this.opts.bundleDir, "scripts", "native", "windmill.meta.json"))
      .json()
      .then((m: { native: NativeForm }) => m.native);
    return this.clientForm;
  }

  /** A module's function with its preamble, compiled once per process for
   * each set of preamble names, and its lines as compiled, which a stack's
   * first frame points into (WR-43). */
  compile(rel: string, text: string, form: NativeForm, preamble: string, url: string) {
    const key = `${rel}\0${preamble}`;
    let c = this.compiled.get(key);
    if (c === undefined) {
      const source = text.slice(0, form.preamble_at) + preamble + text.slice(form.preamble_at);
      let script: vm.Script;
      try {
        script = new vm.Script(source, { filename: url });
      } catch (e) {
        throw new LoadFailure(describe(e));
      }
      c = { script, lines: source.split(/\r\n|[\n\r\u2028\u2029]/) };
      this.compiled.set(key, c);
    }
    return c;
  }

  get baseURL() {
    return this.opts.baseURL();
  }

  get webGlobals() {
    return this.web;
  }

  get hooks() {
    return { timers: this.timers, fetch: this.fetch, console: this.console, onError: this.opts.onError };
  }
}

/** One native invocation's realm. */
export class NativeJob {
  readonly ctx: vm.Context;
  /** The realm's global object. */
  readonly g: Record<string, any>;
  readonly promiseProto: object;
  private realm: { copy: (v: unknown) => unknown; stringify: (v: unknown) => string | undefined; errorProto: object };
  private timers = new Map<number, () => void>();
  private timerSeq = 0;
  private modules = new Map<string, Promise<Record<string, unknown>>>();
  /** The lines of the script's module as compiled. */
  private lines: string[] = [];
  /** What the boundary handed this invocation untracked, by kind
   * (Native.untracked). */
  private untrackedKinds = new Map<string, number>();

  constructor(
    private native: Native,
    readonly inv: NativeInvocation,
    private form: NativeForm,
  ) {
    this.ctx = vm.createContext();
    this.g = vm.runInContext("globalThis", this.ctx);
    this.realm = REALM_SCRIPT.runInContext(this.ctx)();
    this.promiseProto = (this.realm as unknown as { promiseProto: object }).promiseProto;
    this.install();
  }

  /** Counts one thing the boundary handed this invocation untracked. */
  count(kind: string) {
    this.untrackedKinds.set(kind, (this.untrackedKinds.get(kind) ?? 0) + 1);
  }

  /** What the boundary handed this invocation untracked, by kind in
   * sorted order. */
  get untracked(): Record<string, number> {
    return Object.fromEntries([...this.untrackedKinds].sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)));
  }

  /** The text of the warning that reports what the boundary handed this
   * invocation untracked, when it settles, or undefined when it handed
   * over nothing untracked. */
  untrackedText(): string | undefined {
    if (this.untrackedKinds.size === 0) return undefined;
    const kinds = Object.entries(this.untracked)
      .map(([k, n]) => `${n} ${k}`)
      .join(", ");
    return `untracked by the native boundary (ADR 0001, D5): ${kinds}`;
  }

  /** The realm's globals (WR-45): the web APIs, console, the invocation's
   * timers and fetch, globalThis as an event target, and the Deno object
   * with only its core. Nothing of Bun's: no Bun, Buffer, require, module,
   * process or setImmediate. */
  private install() {
    const g = this.g;
    const define = (name: string, value: unknown) =>
      Object.defineProperty(g, name, { value, writable: true, enumerable: false, configurable: true });
    for (const [name, value] of Object.entries(this.native.webGlobals)) define(name, value);
    // V8 has no ShadowRealm.
    delete g.ShadowRealm;
    const { timers, console, onError } = this.native.hooks;
    define("console", console);
    define("fetch", this.fetch());
    define("setTimeout", this.timer(false));
    define("setInterval", this.timer(true));
    const clear = (id: unknown) => this.timers.get(Number(id))?.();
    define("clearTimeout", clear);
    define("clearInterval", clear);
    define("queueMicrotask", (cb: unknown) => {
      if (typeof cb !== "function") throw new g.TypeError("queueMicrotask needs a function");
      // A throw from a microtask carries no async context: it is this
      // job's, so it goes to it directly.
      timers.queueMicrotask(() => {
        try {
          cb();
        } catch (e) {
          onError(this, e);
        }
      });
    });
    const events = new (this.native.webGlobals.EventTarget as typeof EventTarget)();
    for (const k of ["addEventListener", "removeEventListener", "dispatchEvent"] as const) define(k, events[k].bind(events));
    define("Deno", Object.freeze({ core: Object.freeze({}) }));
  }

  /** setTimeout or setInterval of the realm: numeric ids, as the web API
   * has them; each handle the invocation's until it fires or is cleared,
   * and cleared when the invocation settles. A callback runs in the
   * invocation's async context, so a throw from it is attributed. */
  private timer(repeat: boolean) {
    const { timers } = this.native.hooks;
    const g = this.g;
    return (cb: unknown, ms?: unknown, ...args: unknown[]) => {
      // A string is code, as the web API has it.
      const fn = typeof cb === "function" ? cb : () => (0, g.eval)(String(cb));
      const id = ++this.timerSeq;
      let untrack = () => {};
      const fire = () => {
        if (!repeat) {
          this.timers.delete(id);
          untrack();
        }
        Reflect.apply(fn, g, args);
      };
      const handle = repeat ? timers.setInterval(fire, Number(ms) || 0) : timers.setTimeout(fire, Number(ms) || 0);
      const cancel = () => (repeat ? timers.clearInterval(handle) : timers.clearTimeout(handle));
      untrack = this.inv.resources.track(repeat ? "interval" : "timeout", cancel);
      this.timers.set(id, () => {
        cancel();
        untrack();
        this.timers.delete(id);
      });
      return id;
    };
  }

  /** fetch of the realm (WR-45): the harness's fetch view, so a request to
   * the facade becomes a platform call; with the User-Agent the script's
   * header names, or windmill/beta, unless the request sets one; settled as
   * a promise of the realm, with a response of the realm (Native.respond).
   * The request is the invocation's until its body ends, however the realm
   * reads it, and aborted if the invocation settles first: the realm
   * registers it itself, so the view runs with no work registry and
   * registers nothing. */
  private fetch() {
    const { fetch } = this.native.hooks;
    const g = this.g;
    const ua = this.form.user_agent ?? DEFAULT_USER_AGENT;
    return (input: unknown, init?: unknown) =>
      new g.Promise((resolve: (v: unknown) => void, reject: (e: unknown) => void) => {
        try {
          const req = new Request(input as RequestInfo, init as RequestInit);
          if (!req.headers.has("user-agent")) req.headers.set("user-agent", ua);
          const abort = new AbortController();
          const untrack = this.inv.resources.track("fetch", () => abort.abort());
          const send = () => fetch(new Request(req, { signal: AbortSignal.any([req.signal, abort.signal]) }));
          const c = current();
          (c === undefined ? send() : invocations.run({ ...c, work: undefined }, send)).then(
            (res) => resolve(this.native.respond(res, untrack)),
            (e) => {
              untrack();
              reject(this.native.toRealm(e));
            },
          );
        } catch (e) {
          reject(this.native.toRealm(e));
        }
      });
  }

  /** The realm's process.env and the preamble that declares it (WR-45):
   * `process`, BASE_URL and BASE_INTERNAL_URL in both kinds, and for
   * nativets and the client every variable as a const of its name. The
   * values are the invocation's job variables and the facade's origin as
   * both base URLs; nothing else (no PATH, HOME or TZ). */
  private preamble(form: NativeForm, P: string): { text: string; env: Record<string, string> } {
    const env = new this.g.Object() as Record<string, string>;
    const base = this.native.baseURL ?? this.inv.env.BASE_INTERNAL_URL ?? this.inv.env.BASE_URL;
    if (base !== undefined) {
      env.BASE_URL = base;
      env.BASE_INTERNAL_URL = base;
    }
    for (const [k, v] of Object.entries(this.inv.env)) {
      if (k !== "BASE_URL" && k !== "BASE_INTERNAL_URL") Object.defineProperty(env, k, { value: v, writable: true, enumerable: true, configurable: true });
    }
    const names = form.kind === "bunnative" ? ["BASE_URL", "BASE_INTERNAL_URL"] : Object.keys(env);
    for (const n of names) {
      if (!IDENTIFIER.test(n)) throw new LoadFailure(`SyntaxError: the environment variable ${JSON.stringify(n)} is not a valid identifier`);
    }
    const decls = [`process = { env: ${P}.env }`, ...names.map((n) => `${n} = ${P}.env[${JSON.stringify(n)}]`)];
    return { text: `const ${decls.join(", ")};`, env };
  }

  /** Loads a module in this realm: resolves and links its static imports
   * (WR-40), compiles it with its preamble, and evaluates it. A failure to
   * resolve, read or link an import, or to compile, is a LoadFailure; a
   * throw while it evaluates is the script's own exception. */
  async load(rel: string, form: NativeForm, url: string): Promise<Record<string, unknown>> {
    const imports: Record<string, unknown>[] = [];
    for (const [i, spec] of form.imports.entries()) {
      const target = resolve(spec);
      if (typeof target !== "string") throw new LoadFailure(target.error);
      imports[i] = await this.module(target);
    }
    for (const l of form.links) {
      if (!(l.name in imports[l.import])) {
        throw new LoadFailure(`SyntaxError: The requested module '${form.imports[l.import]}' does not provide an export named '${l.name}'`);
      }
    }
    const text = await this.native.text(rel);
    const { text: preamble, env } = this.preamble(form, form.param);
    const c = this.native.compile(rel, text, form, preamble, url);
    if (url === SCRIPT_URL) this.lines = c.lines;
    // Running the script only evaluates the function expression, so what
    // it throws is the compilation's failure: JavaScriptCore reports a
    // redeclaration of a preamble name here, not when the script is made.
    let fn: (p: unknown) => Promise<Record<string, unknown>>;
    try {
      fn = c.script.runInContext(this.ctx);
    } catch (e) {
      throw new LoadFailure(describe(e));
    }
    const meta = new this.g.Object();
    meta.url = url;
    return await fn({
      env,
      imports,
      meta,
      import: (spec: unknown) => this.dynamicImport(spec),
      namespace,
      commonjs,
    });
  }

  /** The module at a resolved URL: the embedded client, for a nativets
   * script (WR-40), loaded once per job; anything else cannot be loaded,
   * since windmill-lite reads no file of the host it runs on. */
  private module(url: string): Promise<Record<string, unknown>> {
    let p = this.modules.get(url);
    if (p === undefined) {
      if (url === CLIENT_URL && this.form.kind === "nativets") {
        p = this.native.client().then((form) => this.load(join("scripts", "native", "windmill.js"), form, CLIENT_URL));
      } else {
        p = Promise.reject(new LoadFailure(`Failed to load ${url}`));
      }
      // Rejections are read by whoever awaits; a later import sees the
      // same outcome.
      p.catch(() => {});
      this.modules.set(url, p);
    }
    return p;
  }

  /** import(…) of the realm: a promise of the realm that resolves to the
   * module's namespace, or rejects with an error the script can catch
   * (WR-40). */
  private dynamicImport(spec: unknown) {
    const g = this.g;
    return new g.Promise((ok: (v: unknown) => void, fail: (e: unknown) => void) => {
      let target;
      try {
        target = resolve(String(spec));
      } catch (e) {
        fail(e);
        return;
      }
      if (typeof target !== "string") {
        fail(new g.TypeError(target.error));
        return;
      }
      this.module(target).then(ok, (e) => fail(e instanceof LoadFailure ? new g.Error(e.message) : e));
    });
  }

  /** The arguments of an entrypoint with these parameters (WR-41): each
   * present one parsed in the realm, a missing one undefined, so its
   * default applies; nothing hydrated; with no parameters, none, even with
   * an entrypoint override. */
  bind(params: readonly Param[], args: Record<string, unknown>): unknown[] {
    return params.map((p) => (Object.hasOwn(args, p.name) ? this.realm.copy(args[p.name]) : undefined));
  }

  /** The result's JSON text (WR-42): JSON.stringify(value ?? null) in the
   * realm, with no replacer, so undefined fields are left out and a BigInt
   * fails with V8's TypeError; a top-level value with no JSON text is
   * null. */
  stringify(value: unknown): string {
    try {
      return this.realm.stringify(value) ?? "null";
    } catch (e) {
      if (isA(e, this.g.TypeError) && (e as Error).message === "JSON.stringify cannot serialize BigInt.") {
        rewrite(e as Error, "Do not know how to serialize a BigInt");
      }
      throw e;
    }
  }

  /** The error object of a JavaScript exception that reached the end of
   * the run (WR-43): exactly message, stack and name. For an Error (of this
   * realm, or of the harness's web APIs), name and message are decoded
   * together, each a string or null, both null when either is anything
   * else; stack is the error's stack, if a string, after the source line
   * its first frame points at when that frame is in the script's module.
   * Any other value gives three nulls. */
  errorText(e: unknown): string {
    if (!this.isError(e)) return JSON.stringify({ message: null, stack: null, name: null });
    const x = e as Record<string, unknown>;
    let name: string | null = null;
    let message: string | null = null;
    try {
      const n = optionalString(x.name);
      const m = optionalString(x.message);
      if (n !== FAILED && m !== FAILED) [name, message] = [n, m];
    } catch {
      // A getter that throws decodes as neither.
    }
    let stack = "";
    try {
      if (typeof x.stack === "string") stack = x.stack;
    } catch {
      // Dropped, as a stack that is not a string.
    }
    // JavaScriptCore ends its stack-overflow message with a period, which
    // V8's has not (WR-44).
    if (name === "RangeError" && message === OVERFLOW + ".") {
      message = OVERFLOW;
      stack = stack.replace(`RangeError: ${OVERFLOW}.`, `RangeError: ${OVERFLOW}`);
    }
    const s = this.sourceLine(stack) + stack;
    return JSON.stringify({ message, stack: s === "" ? null : s, name });
  }

  private isError(e: unknown): boolean {
    if (e === null || (typeof e !== "object" && typeof e !== "function")) return false;
    const protos = [this.realm.errorProto, Error.prototype];
    try {
      for (let p = Object.getPrototypeOf(e); p !== null; p = Object.getPrototypeOf(p)) if (protos.includes(p)) return true;
    } catch {
      // A proxy that refuses is not an Error.
    }
    return false;
  }

  /** The text of the script's line that a stack's first frame points at,
   * and a newline, when that frame is in the script's module. */
  private sourceLine(stack: string): string {
    const frame = stack.split("\n").find((l) => /^\s+at /.test(l));
    const m = frame === undefined ? null : /(?:\(|at )file:\/\/\/eval\.ts:(\d+):\d+\)?$/.exec(frame);
    const line = m === null ? undefined : this.lines[Number(m[1]) - 1];
    return line === undefined ? "" : line + "\n";
  }
}

const OVERFLOW = "Maximum call stack size exceeded";

/** Whether an object of the harness realm is data, with nothing in it to
 * track: a buffer or a view of one, a date, a regular expression, an error
 * (DOMException, a web API class, is handled before), or a boxed
 * primitive. */
function isData(v: object): boolean {
  return (
    v instanceof ArrayBuffer ||
    v instanceof SharedArrayBuffer ||
    ArrayBuffer.isView(v) ||
    v instanceof Date ||
    v instanceof RegExp ||
    v instanceof Error ||
    v instanceof Boolean ||
    v instanceof Number ||
    v instanceof String
  );
}
const FAILED = Symbol("failed");

/** A name or message as an optional string: a string or a String object is
 * its text, null and undefined are null, and anything else fails. */
function optionalString(v: unknown): string | null | typeof FAILED {
  if (typeof v === "string") return v;
  if (v === null || v === undefined) return null;
  if (typeof v !== "object") return FAILED;
  try {
    return String.prototype.valueOf.call(v);
  } catch {
    return FAILED;
  }
}

function isA(e: unknown, ctor: { prototype: object }): boolean {
  try {
    return e !== null && typeof e === "object" && ctor.prototype.isPrototypeOf(e);
  } catch {
    return false;
  }
}

/** Replaces an error's message, and the message in its stack's first line. */
function rewrite(e: Error, message: string) {
  const old = e.message;
  e.message = message;
  if (typeof e.stack === "string") e.stack = e.stack.replace(old, message);
}

/** An import specifier resolved against the script's URL, as the native
 * runtime resolves it (WR-40): a URL, or a path that starts with /, ./
 * or ../; anything else is a bare specifier, which cannot be loaded. */
function resolve(spec: string): string | { error: string } {
  if (spec.startsWith("/") || spec.startsWith("./") || spec.startsWith("../")) return new URL(spec, SCRIPT_URL).href;
  try {
    return new URL(spec).href;
  } catch {
    return { error: `Relative import path "${spec}" not prefixed with / or ./ or ../` };
  }
}

/** A module namespace: the module's getters, then the names of every
 * `export *` module it does not export itself, except default; sorted, as
 * a namespace's keys are, and frozen. */
function namespace(getters: object, stars: Record<string, unknown>[]): Record<string, unknown> {
  const own = Object.getOwnPropertyDescriptors(getters);
  const all: Record<string, PropertyDescriptor> = { ...own };
  for (const s of stars) {
    for (const k of Object.keys(s)) {
      if (k !== "default" && !Object.hasOwn(all, k)) all[k] = { get: () => s[k], enumerable: true, configurable: true };
    }
  }
  const ns = Object.create(null);
  for (const k of Object.keys(all).sort()) Object.defineProperty(ns, k, all[k]);
  Object.defineProperty(ns, Symbol.toStringTag, { value: "Module" });
  return Object.freeze(ns);
}

/** The namespace of a CommonJS module: its exports object's own names
 * besides __esModule, read when they are. */
function commonjs(exports: Record<string, unknown>): Record<string, unknown> {
  const getters: Record<string, PropertyDescriptor> = {};
  for (const k of Object.keys(exports)) {
    if (k !== "__esModule") getters[k] = { get: () => exports[k], enumerable: true, configurable: true };
  }
  return namespace(Object.defineProperties({}, getters), []);
}

function describe(e: unknown): string {
  try {
    if (e instanceof Error || (e !== null && typeof e === "object" && "message" in e)) {
      const x = e as Error;
      return `${x.name}: ${x.message}`;
    }
    return String(e);
  } catch {
    return "(an error that cannot be printed)";
  }
}
