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
// the isolate is dropped when the job ends. The script runs in its native
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

import { join } from "node:path";
import vm from "node:vm";
import type { InvocationResources } from "./context.ts";
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
const WEB_GLOBALS = [
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
const IDENTIFIER = /^[\p{ID_Start}$_][\p{ID_Continue}$\u200C\u200D]*$/u;

/** The Bun harness's native mode: one per runtime. */
export class Native {
  private texts = new Map<string, Promise<string>>();
  private compiled = new Map<string, { script: vm.Script; lines: string[] }>();
  private clientForm: Promise<NativeForm> | undefined;
  private realms = new WeakMap<object, NativeJob>();
  private byContext = new WeakMap<object, NativeJob>();
  private web: Record<string, unknown> = {};
  private timers = { setTimeout, clearTimeout, setInterval, clearInterval, queueMicrotask };
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
    this.timers = { setTimeout, clearTimeout, setInterval, clearInterval, queueMicrotask };
  }

  /** The native job whose realm made promise, for an unhandled rejection,
   * which carries no async context (ADR 0004, failure model item 2). */
  jobOfPromise(promise: unknown): NativeJob | undefined {
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
    const job = new NativeJob(this, inv, form);
    this.realms.set(job.promiseProto, job);
    this.byContext.set(context, job);
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

  /** fetch of the realm (WR-45): the harness's own, so a request to the
   * facade becomes a platform call; with the User-Agent the script's
   * header names, or windmill/beta, unless the request sets one; aborted
   * when the invocation settles; settled as a promise of the realm. */
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
          fetch(new Request(req, { signal: AbortSignal.any([req.signal, abort.signal]) }))
            .then(resolve, reject)
            .finally(untrack);
        } catch (e) {
          reject(e);
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
