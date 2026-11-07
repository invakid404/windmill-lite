// Developer checks of the native boundary mode (deviation D5): the build's
// native form (build/native.ts) and the realm a native invocation runs in
// (runtime/native.ts). The gate is the step suite (conformance/steps/bun,
// WR-40 to WR-45), which runs native steps through the real harness; these
// pin the parts it cannot reach, such as the fetch user agent (WR-45/g).

import { heapStats } from "bun:jsc";
import { afterAll, describe, expect, test } from "bun:test";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import vm from "node:vm";
import { buildNativeClient, lineMap, nativeHeader, nativeModule, transpile } from "../build/native.ts";
import { type InvocationContext, InvocationResources, invocations } from "../runtime/context.ts";
import { DEFAULT_USER_AGENT, Native, type NativeForm, NativeJob, STALLED, WEB_GLOBALS } from "../runtime/native.ts";

// Runs a module's native form in a fresh context with the imports given,
// with an empty preamble, and returns its namespace.
async function evaluate(js: string, imports: Record<string, unknown>[] = []) {
  const m = nativeModule(js, "nativets");
  const ctx = vm.createContext();
  const fn = new vm.Script(m.text).runInContext(ctx);
  const namespace = (getters: object, stars: Record<string, unknown>[]) => {
    const ns: Record<string, unknown> = Object.create(null);
    Object.defineProperties(ns, Object.getOwnPropertyDescriptors(getters));
    for (const s of stars) for (const k of Object.keys(s)) if (!(k in ns)) ns[k] = s[k];
    return ns;
  };
  const ns = await fn({ imports, meta: { url: "file:///eval.ts" }, import: async (s: string) => ({ spec: s }), namespace });
  return { m, ns };
}

describe("the native form", () => {
  test("exports become getters, live bindings stay live, and lines keep their numbers", async () => {
    const js = "export let n = 1;\nexport function bump() { n++; }\nexport { n as alias };\n";
    const { m, ns } = await evaluate(js);
    expect(m.exports).toEqual(["n", "bump", "alias"]);
    (ns.bump as () => void)();
    expect([ns.n, ns.alias]).toEqual([2, 2]);
    // The body starts on the second line and keeps the module's lines.
    const lines = m.text.split("\n");
    expect(lines[2]).toBe("function bump() { n++; }");
    expect(m.form.preamble_at).toBe('(async (__windmill_lite_native) => {"use strict";'.length);
  });

  test("imports bind from the imports the harness passes, and links name what must exist", async () => {
    const js = 'import * as all from "./windmill.ts";\nimport def, { a as b } from "/x.ts";\nexport function main() { return [typeof all, def, b]; }\n';
    const { m, ns } = await evaluate(js, [{ k: 1 }, { default: "D", a: "A" }]);
    expect(m.form.imports).toEqual(["./windmill.ts", "/x.ts"]);
    expect(m.form.links).toEqual([
      { import: 1, name: "default" },
      { import: 1, name: "a" },
    ]);
    expect((ns.main as () => unknown)()).toEqual(["object", "D", "A"]);
  });

  test("re-exports, export default, top-level await, import.meta and import()", async () => {
    const js = [
      'export * from "./windmill.ts";',
      'export { a as renamed } from "./windmill.ts";',
      "const x = await Promise.resolve(41);",
      "export default x + 1",
      "export const url = import.meta.url;",
      'export const dyn = () => import("./other.ts");',
    ].join("\n");
    const { m, ns } = await evaluate(js, [{ a: "A", b: "B" }, { a: "A" }]);
    expect(m.form.imports).toEqual(["./windmill.ts", "./windmill.ts"]);
    expect([ns.default, ns.renamed, ns.b, ns.url]).toEqual([42, "A", "B", "file:///eval.ts"]);
    expect(await (ns.dyn as () => Promise<unknown>)()).toEqual({ spec: "./other.ts" });
  });

  test("the parameter is a name the module does not spell", () => {
    const m = nativeModule("const __windmill_lite_native = 1;\nexport { __windmill_lite_native as v };\n", "nativets");
    expect(m.form.param).toBe("__windmill_lite_native_2");
  });

  test("module code is strict, and this is undefined at the top level", async () => {
    const { ns } = await evaluate("export const top = this === undefined;\nexport function f() { return (function () { return this; })(); }\n");
    expect(ns.top).toBe(true);
    expect((ns.f as () => unknown)()).toBe(undefined);
  });

  test("the module's scope has no arguments; an arrow inherits none, a function has its own (c3n review, finding 3)", async () => {
    const js = [
      "const top = typeof arguments;",
      "const arrow = () => typeof arguments;",
      "function ordinary() { return [typeof arguments, arguments.length]; }",
      "export const read = () => { try { return arguments[0]; } catch (e) { return e.name; } };",
      "export function main() { return [top, arrow(), ordinary(1, 2)]; }",
    ].join("\n");
    const { ns } = await evaluate(js);
    expect((ns.main as () => unknown)()).toEqual(["undefined", "undefined", ["object", 2]]);
    expect((ns.read as () => unknown)()).toBe("ReferenceError");
  });

  test("this is the module's undefined outside functions and class members, and theirs inside", async () => {
    const js = [
      "const arrow = () => this;",
      "export const member = () => { try { return this.x; } catch (e) { return e.name; } };",
      "class C { field = this; static s = this; m() { return this; } [String(this)]() { return 1; } }",
      "const o = { m() { return this; } };",
      "export function main() { const c = new C(); return [arrow(), c.field === c, C.s === C, c.m() === c, o.m() === o, typeof c.undefined]; }",
    ].join("\n");
    const { ns } = await evaluate(js);
    expect((ns.main as () => unknown)()).toEqual([undefined, true, true, true, true, "function"]);
    expect((ns.member as () => unknown)()).toBe("TypeError");
  });
});

test("nativets keeps the module's this undefined through the transpile, which would write null", () => {
  const out = transpile("const t: unknown = this;\nexport function f(this: Window) { return this; }\nexport { t };");
  expect("js" in out && out.js).toContain("const t = undefined;");
  expect("js" in out && out.js).toContain("return this;");
});

test("nativets transpiles TypeScript, and reports a syntax error with its position (WR-40, WR-43)", () => {
  expect(transpile("type T = number;\nexport function main(x: T) { return x; }")).toEqual({
    js: "export function main(x) {\n  return x;\n}\n",
  });
  const bad = transpile("export function main( {");
  expect("error" in bad && bad.error).toContain("at file:///eval.ts:1:");
});

test("header annotations come from the leading // lines, and a blank line ends them (WR-45)", () => {
  expect(nativeHeader("//native\n//useragent probe/1\n//proxy http://h:1\nexport {}")).toEqual({ user_agent: "probe/1", proxy: "http://h:1" });
  expect(nativeHeader("//native\n\n//useragent probe/1\n")).toEqual({});
  expect(nativeHeader("const x = 1;\n//useragent probe/1\n")).toEqual({});
});

describe("a native realm", () => {
  const seen: string[] = [];
  const server = Bun.serve({
    port: 0,
    fetch(req) {
      seen.push(req.headers.get("user-agent") ?? "");
      return new Response(req.headers.get("user-agent"));
    },
  });
  afterAll(() => server.stop(true));

  // What the boundary reported through onUntracked: handed over in no
  // native invocation's context, or after its job settled.
  const unowned: string[] = [];
  const onUntracked = (j: NativeJob | undefined, kind: string) => unowned.push(`${j === undefined ? "no job" : "settled"}: ${kind}`);

  const job = (form: Partial<NativeForm> = {}, env: Record<string, string> = {}) => {
    const native = new Native({ bundleDir: "/nonexistent", baseURL: () => undefined, onError: () => {}, onUntracked });
    native.capture();
    const resources = new InvocationResources();
    const inv = { id: "i", script: "s", entry: "main", args: {}, env, resources };
    const full: NativeForm = { kind: "nativets", param: "P", preamble_at: 0, imports: [], links: [], ...form };
    return { job: new NativeJob(native, inv, full), resources };
  };

  test("fetch sends windmill/beta, or the //useragent header's text, unless the request sets one (WR-45/g)", async () => {
    const url = `http://127.0.0.1:${server.port}/`;
    const plain = job().job.g;
    const named = job({ user_agent: "probe/1" }).job.g;
    expect(await (await plain.fetch(url)).text()).toBe(DEFAULT_USER_AGENT);
    expect(await (await named.fetch(url)).text()).toBe("probe/1");
    expect(await (await plain.fetch(url, { headers: { "User-Agent": "own/2" } })).text()).toBe("own/2");
    // fetch settles as a promise of the realm, so a rejection nobody
    // handles is attributed to its invocation.
    const p = plain.fetch(url);
    expect(p instanceof plain.Promise).toBe(true);
    expect(p instanceof Promise).toBe(false);
    await p;
  });

  test("the realm has the native globals and nothing of Bun's (WR-45)", () => {
    const g = job().job.g;
    for (const name of ["fetch", "URL", "URLPattern", "crypto", "setTimeout", "queueMicrotask", "structuredClone", "console", "Deno"]) {
      expect(typeof g[name]).not.toBe("undefined");
    }
    for (const name of ["Bun", "Buffer", "require", "process", "module", "setImmediate", "ShadowRealm", "WebSocket", "navigator"]) {
      expect(typeof g[name]).toBe("undefined");
    }
  });

  test("settling the invocation clears its timers and aborts its fetches, as dropping the isolate does (WR-41)", async () => {
    const { job: j, resources } = job();
    let fired = false;
    j.g.setTimeout(() => {
      fired = true;
    }, 20);
    const pending = j.g.fetch(`http://127.0.0.1:${server.port}/slow`).catch((e: Error) => e.name);
    const left = resources.settle();
    expect(left.timeout).toBe(1);
    expect(left.fetch).toBe(1);
    expect(await pending).toBe("AbortError");
    await Bun.sleep(40);
    expect(fired).toBe(false);
  });

  test("a fetch stays the invocation's until its body ends, and settling first aborts it (c3n review, finding 2)", async () => {
    let open: ReadableStreamDefaultController<Uint8Array> | undefined;
    const enc = new TextEncoder();
    const streaming = Bun.serve({
      port: 0,
      fetch(req) {
        if (new URL(req.url).pathname === "/whole") return new Response("all of it");
        return new Response(new ReadableStream({ start: (c) => { open = c; c.enqueue(enc.encode("prefix")); } }));
      },
    });
    const base = `http://127.0.0.1:${streaming.port}`;
    try {
      // A body read to its end releases the request.
      const a = job();
      const whole = await a.job.g.fetch(`${base}/whole`);
      expect(whole instanceof a.job.g.Response).toBe(true);
      expect([whole.url, whole.status, whole.ok]).toEqual([`${base}/whole`, 200, true]);
      expect(await whole.text()).toBe("all of it");
      expect(a.resources.settle()).toEqual({});
      // So does a clone's branch read to its end.
      const b = job();
      const cloned = (await b.job.g.fetch(`${base}/whole`)).clone();
      expect(await cloned.text()).toBe("all of it");
      expect(b.resources.settle()).toEqual({});
      // A body never read is still the invocation's when it settles.
      const c = job();
      await c.job.g.fetch(`${base}/whole`);
      expect(c.resources.settle()).toEqual({ fetch: 1 });
      // Headers in, body still open: settling aborts it, and nothing sent
      // afterwards arrives.
      const d = job();
      const streamed = await d.job.g.fetch(`${base}/open`);
      await Bun.sleep(10);
      const body = streamed.text().then((v: string) => ({ body: v }), (e: Error) => ({ error: e.name }));
      expect(d.resources.settle()).toEqual({ fetch: 1 });
      expect(await body).toEqual({ error: "AbortError" });
      // The abort may have reached the server and closed its stream.
      try {
        open?.enqueue(enc.encode(" after settlement"));
        open?.close();
      } catch {
        // Already cancelled by the client's abort.
      }
      expect(await body).toEqual({ error: "AbortError" });
    } finally {
      streaming.stop(true);
    }
  });

  // A native job registered for an async-context store, as Native.run
  // registers one, and the store to run its code in.
  const registered = () => {
    const native = new Native({ bundleDir: "/nonexistent", baseURL: () => undefined, onError: () => {}, onUntracked });
    native.capture();
    const resources = new InvocationResources();
    const ctx: InvocationContext = { id: "i", run: "r", env: {}, signal: new AbortController().signal, resources };
    const j = native.open({ id: "i", script: "s", entry: "main", args: {}, env: {}, resources }, { kind: "nativets", param: "P", preamble_at: 0, imports: [], links: [] }, ctx);
    return { native, j, ctx, resources };
  };

  test("every web API class is realm-facing: each member it has, inherited and symbol-named ones included, is wrapped, and it inherits as in V8 (c3n review r3, finding 1)", () => {
    const { j } = registered();
    const classes = WEB_GLOBALS.filter((n) => /^[A-Z]/.test(n) && typeof (globalThis as Record<string, unknown>)[n] === "function");
    expect(classes.length).toBeGreaterThan(40);
    for (const name of classes) {
      const R = j.g[name];
      const C = (globalThis as Record<string, any>)[name];
      expect([name, Object.getPrototypeOf(R) === C]).toEqual([name, true]);
      const missing: string[] = [];
      for (let proto = C.prototype; proto !== null && proto !== Object.prototype; proto = Object.getPrototypeOf(proto)) {
        for (const k of Reflect.ownKeys(proto)) {
          const d = Object.getOwnPropertyDescriptor(proto, k)!;
          if (k === "constructor" || (typeof d.value !== "function" && d.get === undefined)) continue;
          if (!Object.hasOwn(R.prototype, k)) missing.push(`${name}.${String(k)}`);
        }
      }
      expect(missing).toEqual([]);
    }
    // Each inherits from its parent's realm-facing class, and keeps its
    // own toStringTag.
    const g = j.g;
    const inherits = [
      [g.CustomEvent, g.Event], [g.MessageEvent, g.Event], [g.ErrorEvent, g.Event], [g.CloseEvent, g.Event],
      [g.AbortSignal, g.EventTarget], [g.MessagePort, g.EventTarget], [g.File, g.Blob],
    ].map(([a, b]) => [a.name, b.name, b.prototype.isPrototypeOf(a.prototype)]);
    expect(inherits.filter(([, , ok]) => !ok)).toEqual([]);
    expect(Object.prototype.toString.call(new g.CustomEvent("x"))).toBe("[object CustomEvent]");
    expect(new g.AbortController().signal instanceof g.EventTarget).toBe(true);
    // crypto, crypto.subtle and performance forward to the shared objects.
    expect([g.crypto instanceof g.Crypto, g.crypto.subtle instanceof g.SubtleCrypto, g.performance instanceof g.Performance]).toEqual([true, true, true]);
    expect([typeof g.performance.now(), g.performance.timeOrigin]).toEqual(["number", performance.timeOrigin]);
  });

  test("what Bun 1.4.3 adds to or reshapes in the web APIs reaches the realm as the rest does, and FileReader is still missing", async () => {
    const { native, j, ctx } = registered();
    const g = j.g;
    expect(g.FileReader).toBeUndefined();
    await invocations.run(ctx, async () => {
      // crypto.subtle is an accessor of Crypto's prototype, which throws
      // off its receiver; the forwarder applies it to the shared crypto.
      expect(Object.hasOwn(crypto, "subtle")).toBe(false);
      expect(typeof Object.getOwnPropertyDescriptor(g.Crypto.prototype, "subtle")?.get).toBe("function");
      expect(g.crypto.subtle instanceof g.SubtleCrypto).toBe(true);
      // A static that makes a stream gives the realm's stream.
      const from = g.ReadableStream.from(["a", "b"]);
      expect(from instanceof g.ReadableStream).toBe(true);
      const reader = from.getReader();
      expect(reader instanceof g.ReadableStreamDefaultReader).toBe(true);
      expect([await reader.read(), await reader.read()]).toEqual([
        { value: "a", done: false },
        { value: "b", done: false },
      ]);
      // A sink's controller has the signal a writer's abort aborts, as the
      // realm's AbortSignal.
      let signal: AbortSignal | undefined;
      const writer = new g.WritableStream({ start: (ctl: WritableStreamDefaultController) => (signal = ctl.signal) }).getWriter();
      await writer.abort("stop");
      expect([signal instanceof g.AbortSignal, signal?.aborted, signal?.reason]).toEqual([true, true, "stop"]);
      // A web API object inspects as Bun inspects it: its class's
      // inspect.custom, now on the prototype, reaches it.
      const inspect = Symbol.for("nodejs.util.inspect.custom");
      expect(typeof g.URL.prototype[inspect]).toBe("function");
      expect(Bun.inspect(new g.URL("https://a.example/x?y=1"))).toBe(Bun.inspect(new URL("https://a.example/x?y=1")));
      expect(native.jobOfPromise(reader.cancel()) === j).toBe(true);
    });
  });

  test("what a web API returns, and every continuation of it, is the calling invocation's (c3n review, finding 1)", async () => {
    const { native, j, ctx } = registered();
    const g = j.g;
    await invocations.run(ctx, async () => {
      const owned = (p: unknown) => native.jobOfPromise(p) === j;
      const file = new g.File(["file text"], "f.txt");
      const form = new g.FormData();
      form.append("f", file);
      const writable = new g.WritableStream({ write() {} });
      const writer = writable.getWriter();
      const reader = new g.Response("streamed").body.getReader();
      const iterator = new g.ReadableStream({ start: (c: ReadableStreamDefaultController) => { c.enqueue("x"); c.close(); } })[Symbol.asyncIterator]();
      const promises: Record<string, unknown> = {
        "Response.json": new g.Response('{"a":1}').json(),
        "File.text (inherited from Blob)": file.text(),
        "File.arrayBuffer": file.arrayBuffer(),
        "FormData File.text": (form.get("f") as Blob).text(),
        "Request.text": new g.Request("http://x.invalid/", { method: "POST", body: "b" }).text(),
        "ReadableStream.text": new g.ReadableStream({ start: (c: ReadableStreamDefaultController) => { c.enqueue("s"); c.close(); } }).text(),
        "reader.read": reader.read(),
        "async iterator next": iterator.next(),
        "writer.write": writer.write("w"),
        "crypto.subtle.digest": g.crypto.subtle.digest("SHA-256", new Uint8Array()),
      };
      for (const [what, p] of Object.entries(promises)) {
        expect([what, owned(p)]).toEqual([what, true]);
        // A continuation, by then, catch or finally, is the job's too.
        const q = (p as Promise<unknown>).then((x) => x);
        expect([what, owned(q), owned(q.catch(() => {})), owned(q.finally(() => {}))]).toEqual([what, true, true, true]);
        await q;
      }
      expect(await promises["Response.json"]).toEqual({ a: 1 });
      expect(await promises["File.text (inherited from Blob)"]).toBe("file text");
      // A value a promise gives is the realm's objects too.
      const blob = await new g.Response("x").blob();
      expect(blob instanceof g.Blob).toBe(true);
      expect(owned(blob.text())).toBe(true);
    });
    // The harness realm's own objects keep their prototypes.
    expect(Object.getPrototypeOf(crypto) === Crypto.prototype && Object.getPrototypeOf(new Response("y")) === Response.prototype).toBe(true);
    const outside = new Response("y").text();
    expect(Object.getPrototypeOf(outside) === Promise.prototype).toBe(true);
    await outside;
  });

  test("objects a web API hands over by iterators, callbacks, structuredClone and messages are the invocation's too (c3n review r3, finding 1)", async () => {
    const { native, j, ctx } = registered();
    const g = j.g;
    await invocations.run(ctx, async () => {
      const owned = (p: unknown) => native.jobOfPromise(p) === j;
      const form = new g.FormData();
      form.append("file", new g.Blob(["abc"]), "a.txt");
      let viaForEach: unknown;
      form.forEach((v: unknown) => {
        viaForEach = v;
      });
      const handed: Record<string, Blob> = {
        "FormData.get": form.get("file") as Blob,
        "FormData.values().next().value": form.values().next().value as Blob,
        "[...FormData][0][1]": [...form][0][1] as Blob,
        "FormData.entries() for-of": (() => {
          for (const [, v] of form.entries()) return v as Blob;
          throw new Error("empty");
        })(),
        "FormData.forEach callback": viaForEach as Blob,
        "structuredClone, nested": g.structuredClone({ nested: [new g.Blob(["n"])], map: new Map([["k", new g.Blob(["m"])]]) }).nested[0],
        "structuredClone, in a Map": g.structuredClone({ map: new Map([["k", new g.Blob(["m"])]]) }).map.get("k"),
      };
      for (const [what, b] of Object.entries(handed)) {
        expect([what, b instanceof g.Blob]).toEqual([what, true]);
        const p = b.text();
        const q = p.then(() => "continued");
        expect([what, owned(p), owned(q)]).toEqual([what, true, true]);
        await q;
      }
      // A stream reader's read() result has its value passed on too.
      const chunk = (await new g.Response(new g.Blob(["s"])).body.getReader().read()) as { value: unknown };
      expect(chunk.value instanceof Uint8Array).toBe(true);
      // A FormData file is the realm's File, as V8's is a File.
      expect([handed["FormData.get"] instanceof g.File, handed["[...FormData][0][1]"] instanceof g.File]).toEqual([true, true]);

      // A message's data, in the listener, which runs as the invocation
      // that registered it.
      const mc = new g.MessageChannel();
      const inListener = await new Promise<{ blob: boolean; owned: boolean; port: boolean }>((res) => {
        const h = (e: MessageEvent) => {
          const p = e.data.b.text();
          res({ blob: e.data.b instanceof g.Blob, owned: owned(p), port: e.ports.length === 1 && e.ports[0] instanceof g.MessagePort });
        };
        mc.port2.onmessage = h;
        expect(mc.port2.onmessage).toBe(h);
        const extra = new g.MessageChannel();
        mc.port1.postMessage({ b: new g.Blob(["m"]) }, [extra.port1]);
        extra.port2.close();
      });
      expect(inListener).toEqual({ blob: true, owned: true, port: true });
      // A listener removed by identity is removed.
      let calls = 0;
      const listener = () => {
        calls++;
      };
      mc.port2.onmessage = null;
      mc.port2.addEventListener("message", listener);
      mc.port2.removeEventListener("message", listener);
      mc.port1.postMessage("ignored");
      await Bun.sleep(20);
      expect(calls).toBe(0);
      mc.port1.close();
      mc.port2.close();
    });
  });

  test("events, event handlers and stream callbacks get the realm's objects, and a listener runs as the invocation that registered it (c3n review r3, finding 1)", async () => {
    const { native, j, ctx } = registered();
    const g = j.g;
    const owned = (p: unknown) => native.jobOfPromise(p) === j;
    const seen: Record<string, unknown> = {};
    const c = await invocations.run(ctx, () => {
      const c = new g.AbortController();
      const listener = function (this: unknown, e: Event) {
        seen.listener = [e instanceof g.Event, e.target instanceof g.AbortSignal, this === c.signal, owned(new g.Blob(["l"]).text())];
      };
      c.signal.addEventListener("abort", listener);
      c.signal.addEventListener("abort", { handleEvent: (e: Event) => (seen.handleEvent = e instanceof g.Event) });
      const removed = () => (seen.removed = true);
      c.signal.addEventListener("abort", removed);
      c.signal.removeEventListener("abort", removed);
      const h = (e: Event) => (seen.handler = [e instanceof g.Event, owned(new g.Blob(["h"]).text())]);
      c.signal.onabort = h;
      expect(c.signal.onabort).toBe(h);
      return c;
    });
    // Aborted from outside any invocation: the listeners still run as the
    // one that registered them.
    c.abort();
    expect(seen).toEqual({ listener: [true, true, true, true], handleEvent: true, handler: [true, true] });
    await invocations.run(ctx, async () => {
      // A web API's exception and rejection are the realm's DOMException,
      // as the value a getter gives is.
      let thrown: unknown;
      try {
        g.AbortSignal.abort().throwIfAborted();
      } catch (e) {
        thrown = e;
      }
      expect(thrown instanceof g.DOMException).toBe(true);
      const errored = new g.ReadableStream({ start: (ctl: ReadableStreamDefaultController) => ctl.error(new DOMException("d", "AbortError")) });
      expect(await errored.getReader().read().catch((e: unknown) => e instanceof g.DOMException)).toBe(true);
      const rejected = await g.fetch(`http://127.0.0.1:${server.port}/`, { signal: g.AbortSignal.abort() }).catch((e: unknown) => e);
      expect(rejected instanceof g.DOMException).toBe(true);
      expect(c.signal.reason instanceof g.DOMException).toBe(true);

      // A stream's callbacks get its controllers as the realm's, on the
      // dictionary that holds them, inherited ones included.
      const got: Record<string, unknown> = {};
      class Source {
        start(this: unknown, ctl: unknown) {
          got.readable = [ctl instanceof g.ReadableStreamDefaultController, this instanceof Source];
          (ctl as ReadableStreamDefaultController).enqueue("r");
          (ctl as ReadableStreamDefaultController).close();
        }
      }
      expect(await new g.Response(new g.ReadableStream(new Source())).text()).toBe("r");
      const sink = {
        write(this: unknown, _chunk: unknown, ctl: unknown) {
          got.writable = [ctl instanceof g.WritableStreamDefaultController, this === sink];
        },
      };
      const writer = new g.WritableStream(sink).getWriter();
      await writer.write("w");
      const ts = new g.TransformStream({
        transform(chunk: unknown, ctl: TransformStreamDefaultController) {
          got.transform = ctl instanceof g.TransformStreamDefaultController;
          ctl.enqueue(chunk);
        },
      });
      ts.writable.getWriter().write("t");
      await ts.readable.getReader().read();
      // A BYOB request is answered with at least one byte: web streams
      // refuse respond(0) on a stream that is not closed.
      const bytes = new g.ReadableStream({
        type: "bytes",
        autoAllocateChunkSize: 4,
        pull(ctl: ReadableByteStreamController) {
          got.bytes = [ctl instanceof g.ReadableByteStreamController, ctl.byobRequest instanceof g.ReadableStreamBYOBRequest];
          (ctl.byobRequest!.view as Uint8Array)[0] = 98;
          ctl.byobRequest!.respond(1);
          ctl.close();
        },
      });
      const chunk = await bytes.getReader().read();
      expect([...(chunk.value as Uint8Array)]).toEqual([98]);
      expect(got).toEqual({ readable: [true, true], writable: [true, true], transform: true, bytes: [true, true] });
    });
  });

  test("what the boundary does not track is counted, on the invocation it was handed to, or reported when no unsettled invocation can count it (c3n review r3, finding 1)", async () => {
    const { native, j, ctx, resources } = registered();
    const g = j.g;
    unowned.length = 0;
    await invocations.run(ctx, async () => {
      // Data, the realm's own and the web API classes are not counted; a
      // container is passed on all through.
      const given = native.toRealm({ list: [new Blob(["x"])], bytes: new Uint8Array(2), when: new Date(), err: new TypeError("t"), own: new g.Object() }) as { list: Blob[] };
      expect(given.list[0] instanceof g.Blob).toBe(true);
      expect(j.untracked).toEqual({});
      expect(j.untrackedText()).toBeUndefined();
      // Bun's performance.timing is a PerformanceTiming, which is no web
      // API of the native runtime; a harness function or WeakMap is none
      // either.
      expect(typeof g.performance.timing.toJSON).toBe("function");
      g.performance.toJSON();
      native.toRealm(new WeakMap());
      native.toRealm(function harnessFunction() {});
      expect(j.untracked).toEqual({ PerformanceTiming: 2, WeakMap: 1, "function harnessFunction": 1 });
      expect(Object.keys(j.untracked)).toEqual(["PerformanceTiming", "WeakMap", "function harnessFunction"]);
      expect(j.untrackedText()).toBe("untracked by the native boundary (ADR 0001, D5): 2 PerformanceTiming, 1 WeakMap, 1 function harnessFunction");
    });
    expect(unowned).toEqual([]);
    // A web API's promise given in no native invocation's context cannot
    // be tracked.
    const outside = new j.g.Blob(["o"]).text();
    await outside;
    expect(unowned).toEqual(["no job: Promise"]);
    // Nor can anything handed over after the invocation settled.
    resources.settle();
    await invocations.run(ctx, async () => g.performance.timing);
    expect(unowned).toEqual(["no job: Promise", "settled: PerformanceTiming"]);
    expect(j.untracked).toEqual({ PerformanceTiming: 2, WeakMap: 1, "function harnessFunction": 1 });
  });

  test("a pending entrypoint is given up as unable to settle only once nothing it holds could settle it (WR-43)", async () => {
    // What outcome gives within ms: the entrypoint's value, STALLED, or
    // "waiting" while it is still checking.
    const within = (p: Promise<unknown>, ms: number) => Promise.race([p, Bun.sleep(ms).then(() => "waiting")]);
    const never = (j: NativeJob) => new j.g.Promise(() => {});

    // Nothing pending: given up after two checks.
    {
      const { j, ctx } = registered();
      expect(await invocations.run(ctx, () => within(j.outcome(never(j)), 200))).toBe(STALLED);
      // A value, or a promise that settles, is the entrypoint's outcome.
      expect(await invocations.run(ctx, () => j.outcome(7))).toBe(7);
      const late = new j.g.Promise((r: (v: unknown) => void) => j.g.setTimeout(() => r("late"), 30));
      expect(await invocations.run(ctx, () => j.outcome(late))).toBe("late");
    }
    // A timer, then a web API promise, each holds it until it ends.
    {
      const { native, j, ctx } = registered();
      let give: (v: unknown) => void = () => {};
      const out = invocations.run(ctx, () => {
        j.g.setTimeout(() => {}, 60);
        return j.outcome(never(j));
      });
      expect(await within(out, 40)).toBe("waiting");
      invocations.run(ctx, () => native.toRealm(new Promise((r) => (give = r))));
      expect(await within(out, 80)).toBe("waiting");
      give(1);
      expect(await within(out, 200)).toBe(STALLED);
    }
    // An event listener, a built-in the engine settles by itself, or
    // anything handed over untracked holds it for good.
    const holds: ((native: Native, j: NativeJob) => void)[] = [
      (_, j) => {
        new j.g.MessageChannel().port1.onmessage = () => {};
      },
      (_, j) => {
        j.g.Atomics.waitAsync(new j.g.Int32Array(new j.g.SharedArrayBuffer(4)), 0, 0, 10);
      },
      (native) => {
        native.toRealm(new WeakMap());
      },
    ];
    for (const hold of holds) {
      const { native, j, ctx } = registered();
      const out = invocations.run(ctx, () => {
        hold(native, j);
        return j.outcome(never(j));
      });
      expect(await within(out, 100)).toBe("waiting");
    }
    // So does a web API promise handed to no invocation since it began,
    // which could be its own work.
    {
      const { j, ctx } = registered();
      unowned.length = 0;
      const out = invocations.run(ctx, () => j.outcome(never(j)));
      await new j.g.Blob(["o"]).text();
      expect(unowned).toEqual(["no job: Promise"]);
      expect(await within(out, 100)).toBe("waiting");
    }
    // Once the invocation settles otherwise, checking stops.
    {
      const { j, ctx, resources } = registered();
      const out = invocations.run(ctx, () => j.outcome(never(j)));
      resources.settle();
      expect(await within(out, 100)).toBe("waiting");
    }
  });

  test("a stream error the script catches is no unhandled rejection, as with Bun's own stream (c3n review r2, finding 4)", async () => {
    const { native, j, ctx } = registered();
    const seen: unknown[] = [];
    const record = (_e: unknown, p: unknown) => seen.push(native.jobOfPromise(p) === j ? "job" : "other");
    process.on("unhandledRejection", record);
    try {
      const iterate = async (C: typeof ReadableStream) => {
        let caught = false;
        try {
          for await (const _ of new C({ start: (c: ReadableStreamDefaultController) => c.error(new Error("handled")) })) {
            // nothing arrives
          }
        } catch {
          caught = true;
        }
        await Bun.sleep(30);
        return caught;
      };
      expect(await iterate(ReadableStream)).toBe(true);
      expect(await invocations.run(ctx, () => iterate(j.g.ReadableStream))).toBe(true);
      expect(seen).toEqual([]);
      // A next() nobody awaits is the job's, so its rejection would be
      // attributed (bun test fails a test on any unhandled rejection, so
      // the step cases of WR-43 check that end to end).
      await invocations.run(ctx, async () => {
        const next = new j.g.ReadableStream({ start: (c: ReadableStreamDefaultController) => c.error(new Error("detached")) })[Symbol.asyncIterator]().next();
        expect(native.jobOfPromise(next)).toBe(j);
        await next.catch(() => {});
      });
      expect(seen).toEqual([]);
    } finally {
      process.off("unhandledRejection", record);
    }
  });

  test("a fetched response's url, redirected and type survive clone, and clones of clones (c3n review r2, finding 5)", async () => {
    const redirecting = Bun.serve({
      port: 0,
      fetch(req) {
        return new URL(req.url).pathname === "/start" ? Response.redirect(`http://127.0.0.1:${redirecting.port}/ok`, 302) : new Response("arrived");
      },
    });
    try {
      const url = `http://127.0.0.1:${redirecting.port}/start`;
      const direct = await fetch(url);
      const want = { url: direct.url, redirected: direct.redirected, type: direct.type };
      expect(want.redirected).toBe(true);
      const { job: j, resources } = job();
      const res = await j.g.fetch(url);
      const clone = res.clone();
      const cloneOfClone = clone.clone();
      for (const r of [res, clone, cloneOfClone]) {
        expect({ url: r.url, redirected: r.redirected, type: r.type }).toEqual(want);
        expect(r instanceof j.g.Response).toBe(true);
      }
      expect(Object.keys(res)).toEqual([]);
      expect(await clone.text()).toBe("arrived");
      expect(await cloneOfClone.text()).toBe("arrived");
      expect(await res.text()).toBe("arrived");
      // The body was read to its end through the clones: the request is
      // released.
      expect(resources.settle()).toEqual({});
      await direct.text();
    } finally {
      redirecting.stop(true);
    }
  });

  test("errors take WR-43's shape", () => {
    const { job: j } = job();
    const shape = (v: unknown) => JSON.parse(j.errorText(v));
    expect(shape("s")).toEqual({ message: null, stack: null, name: null });
    expect(shape({ message: "m", name: "N" })).toEqual({ message: null, stack: null, name: null });
    const e = new j.g.TypeError("boom");
    e.code = 7;
    expect(Object.keys(shape(e))).toEqual(["message", "stack", "name"]);
    expect(shape(e)).toMatchObject({ message: "boom", name: "TypeError" });
    // An Error of the harness's realm, as a web API throws, is one too.
    expect(shape(new DOMException("d", "AbortError"))).toMatchObject({ message: "d", name: "AbortError" });
    const odd = new j.g.Error("x");
    odd.message = 7;
    expect(shape(odd)).toMatchObject({ message: null, name: null });
  });

  test("errors end with the job's step context keys, whatever was thrown (WR-43, FX-12)", () => {
    const env = { WM_FLOW_STEP_ID: "a", WM_JOB_ID: "j", WM_FLOW_PATH: "f/x/branchall-0", WM_ROOT_JOB_ID: "r" };
    const { job: j } = job({}, env);
    const keys = ["message", "stack", "name", "step_id", "job_id", "flow_path"];
    const context = { step_id: "a", job_id: "j", flow_path: "f/x/branchall-0" };
    const e = new j.g.TypeError("boom");
    e.code = 7;
    expect(Object.keys(JSON.parse(j.errorText(e)))).toEqual(keys);
    expect(JSON.parse(j.errorText(e))).toMatchObject({ message: "boom", name: "TypeError", ...context });
    expect(JSON.parse(j.errorText("s"))).toEqual({ message: null, stack: null, name: null, ...context });
    expect(Object.keys(JSON.parse(j.errorText("s")))).toEqual(keys);
    // Each key only when its variable is not empty: a script run on its
    // own carries job_id alone.
    const { job: alone } = job({}, { WM_FLOW_STEP_ID: "", WM_JOB_ID: "j", WM_FLOW_PATH: "" });
    expect(JSON.parse(alone.errorText("s"))).toEqual({ message: null, stack: null, name: null, job_id: "j" });
  });
});

describe("the errors the harness generates", () => {
  // The worker's errors before the module loads (a transpile error, a
  // signature that did not parse, //proxy on CE) and a load failure are
  // written by the harness in place of the worker's, so run marks them
  // generated and the result frame names the harness as their origin (WR-43,
  // WR-45, ADR 0004, D14); a script's own exception is not. The load
  // failure's mark is checked end to end in cmd/wltest
  // (TestNativeGeneratedErrorsAreGenerated).
  const form: NativeForm = { kind: "nativets", param: "P", preamble_at: 0, imports: [], links: [] };
  const run = async (bundleDir: string, meta: unknown) => {
    const native = new Native({ bundleDir, baseURL: () => "http://127.0.0.1:9", onError: () => {}, onUntracked: () => {} });
    native.capture();
    const resources = new InvocationResources();
    const env = { WM_FLOW_STEP_ID: "a", WM_JOB_ID: "j", WM_FLOW_PATH: "f/x" };
    const ctx: InvocationContext = { id: "i", run: "r", env, signal: new AbortController().signal, resources };
    const inv = { id: "i", script: "s", entry: "main", args: {}, env, resources };
    return invocations.run(ctx, () => native.run(inv, meta, ctx, "a"));
  };

  // The worker builds these itself, so they carry step_id alone, not
  // job_id or flow_path (FX-12).
  test("the worker's errors before the module loads are generated", async () => {
    const generated = (message: string) => ({ error: JSON.stringify({ message: `execution error:\n${message}`, name: "ExecutionErr", step_id: "a" }), generated: true });
    expect(await run("/nonexistent", { native: { ...form, transpile_error: "Expected identifier" } })).toEqual(generated("Error: Expected identifier"));
    expect(await run("/nonexistent", { native: form, signatures: { main: { error: "parameter syntax unsupported: a rest parameter" } } })).toEqual(
      generated("parameter syntax unsupported: a rest parameter"),
    );
    expect(await run("/nonexistent", { native: { ...form, proxy: "http://127.0.0.1:3128" } })).toEqual(generated("Proxy is an EE feature"));
  });

  test("a script's own exception is not", async () => {
    const dir = mkdtempSync(join(tmpdir(), "wl-native-"));
    try {
      const m = nativeModule('export function main() { throw new Error("mine"); }\n', "nativets");
      mkdirSync(join(dir, "scripts", "bun"), { recursive: true });
      writeFileSync(join(dir, "scripts", "bun", "s.native.js"), m.text);
      const r = (await run(dir, { native: m.form, signatures: { main: { params: [] } } })) as { error: string; generated?: true };
      expect(r.generated).toBeUndefined();
      expect(JSON.parse(r.error)).toMatchObject({ message: "mine", name: "Error", step_id: "a", job_id: "j", flow_path: "f/x" });
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });
});

describe("the preamble and the module's compiled variants", () => {
  // One Native, as one process has, across invocations that each get a
  // fresh realm, with the environment each invocation gives. A nativets
  // preamble declares the variables the module's text spells, every
  // variable when it spells eval, and always the names whose declaration
  // fails; a module keeps at most 16 compiled variants, and the realm's eval
  // compiles any other with the same outcome.
  const dir = mkdtempSync(join(tmpdir(), "wl-native-preamble-"));
  afterAll(() => rmSync(dir, { recursive: true, force: true }));
  const native = new Native({ bundleDir: dir, baseURL: () => "http://127.0.0.1:9", onError: () => {}, onUntracked: () => {} });
  native.capture();
  const write = (script: string, js: string) => {
    const m = nativeModule(js, "nativets");
    mkdirSync(join(dir, "scripts", "bun"), { recursive: true });
    writeFileSync(join(dir, "scripts", "bun", `${script}.native.js`), m.text);
    return { native: m.form, signatures: { main: { params: [] } } };
  };
  const run = (script: string, meta: unknown, env: Record<string, string>) => {
    const resources = new InvocationResources();
    const ctx: InvocationContext = { id: "i", run: "r", env, signal: new AbortController().signal, resources };
    const inv = { id: "i", script, entry: "main", args: {}, env, resources };
    return invocations.run(ctx, () => native.run(inv, meta, ctx, "a")) as Promise<{ ok?: string; error?: string; generated?: true }>;
  };
  const scripts = () => {
    for (let i = 0; i < 3; i++) Bun.gc(true);
    return heapStats().objectTypeCounts.Script ?? 0;
  };

  test("a variable the text does not spell keeps its value in process.env; one it spells is a const", async () => {
    const meta = write("spells", 'export function main() { return [SPELLED, process.env["UN" + "SPELLED"], Object.keys(process.env).sort()]; }\n');
    expect(await run("spells", meta, { SPELLED: "s", UNSPELLED: "u" })).toEqual({
      ok: JSON.stringify(["s", "u", ["BASE_INTERNAL_URL", "BASE_URL", "SPELLED", "UNSPELLED"]]),
    });
  });

  test("a direct eval reads every variable, whatever name it builds", async () => {
    const meta = write("evals", 'export function main() { return eval("UN" + "SPELLED"); }\n');
    expect(await run("evals", meta, { UNSPELLED: "u" })).toEqual({ ok: JSON.stringify("u") });
  });

  test("a name spelled with escapes is a const too", async () => {
    const meta = write("escaped", "export function main() { return [W\\u0041, W\\u{42}]; }\n");
    expect(await run("escaped", meta, { WA: "a", WB: "b" })).toEqual({ ok: JSON.stringify(["a", "b"]) });
  });

  test("a name whose const fails, or that the module declares, fails the load whether it is spelled or not", async () => {
    // eval and arguments are not reserved words, so they get a const, which
    // strict code refuses (WR-45).
    const plain = write("plain", "export function main() { return 1; }\n");
    for (const name of ["eval", "arguments"]) {
      const r = await run("plain", plain, { [name]: "x" });
      expect(r.generated).toBe(true);
      expect(JSON.parse(r.error!).message).toBe(`execution error:\nError: failed to load module: SyntaxError: Cannot declare a variable named ${name} in strict mode.`);
    }
    const declares = write("declares", "const MINE = 2;\nexport function main() { return MINE; }\n");
    const r = await run("declares", declares, { MINE: "1" });
    expect(JSON.parse(r.error!).message).toBe("execution error:\nError: failed to load module: SyntaxError: Cannot declare a const variable twice: 'MINE'.");
    expect(await run("declares", declares, { OTHER: "1" })).toEqual({ ok: "2" });
  });

  test("a name that cannot be a binding gets no const and is a process.env entry alone, so the module loads (WR-45, v1.862.0)", async () => {
    // A reserved word of strict mode, a name that is not an ASCII
    // identifier, and the preamble's own process: each was a failed load
    // up to v1.767.0.
    const env = { while: "w", await: "a", "WL-DASHED": "d", "\u00c4": "u", process: "p" };
    const reads = write("reads", 'export function main() { return [process.env["while"], process.env["await"], process.env["WL-DASHED"], process.env["\\u00c4"], process.env.process, typeof process]; }\n');
    expect(await run("reads", reads, env)).toEqual({ ok: JSON.stringify(["w", "a", "d", "u", "p", "object"]) });
    const bare = write("bare", "export function main() { try { return \u00c4; } catch (e) { return e.name; } }\n");
    expect(await run("bare", bare, env)).toEqual({ ok: JSON.stringify("ReferenceError") });
  });

  test("a variable named arguments fails the load though the module never spells it, compiled or past the bound", async () => {
    // A text that spells eval declares every variable, so each invocation
    // here, with a name of its own, has a preamble of its own: the first 16
    // compile, and the rest go through the realm's eval.
    const meta = write("noarguments", 'export function main() { return eval("1"); }\n');
    const failed = "execution error:\nError: failed to load module: SyntaxError: Cannot declare a variable named arguments in strict mode.";
    const before = scripts();
    for (let i = 0; i < 24; i++) {
      const r = await run("noarguments", meta, { arguments: "x", [`N${i}`]: "y" });
      expect(r.generated).toBe(true);
      expect(JSON.parse(r.error!).message).toBe(failed);
    }
    expect(await run("noarguments", meta, { N0: "y" })).toEqual({ ok: "1" });
    expect(scripts() - before).toBeLessThanOrEqual(16);
  });

  test("a name the text does not spell makes no new variant, and past 16 the realm's eval gives the same outcome and keeps no script", async () => {
    const quiet = write("quiet", "export function main() { return 1; }\n");
    const loud = write("loud", 'export function main() {\n  const v = eval("V");\n  throw new Error("boom " + v);\n}\n');
    // A sourceURL comment of the module's own text names its frames, on
    // every variant.
    const named = write("named", 'export function main() {\n  throw new Error(eval("V"));\n}\n//# sourceURL=own.js\n');
    // Each invocation adds a name; every call is made from the same line, so
    // the stacks' frames below the module are the same too.
    const outcomes: { error?: string }[] = [];
    const ownURL: { error?: string }[] = [];
    let before = 0;
    for (let i = 0; i <= 40; i++) {
      if (i === 1) before = scripts();
      expect(await run("quiet", quiet, { V: "v", [`N${i}`]: "x" })).toEqual({ ok: "1" });
      outcomes.push(await run("loud", loud, { V: "v", [`N${i}`]: "x" }));
      ownURL.push(await run("named", named, { V: "v", [`N${i}`]: "x" }));
    }
    // Every variant throws from the module's third line, line 4 of its
    // native form, with the same stack.
    const [first] = outcomes;
    expect(JSON.parse(first.error!)).toMatchObject({ message: "boom v", name: "Error" });
    expect(JSON.parse(first.error!).stack).toContain("at main (file:///eval.ts:4:");
    for (const o of outcomes) expect(o).toEqual(first);
    expect(JSON.parse(ownURL[0].error!).stack).toContain("at main (own.js:3:");
    for (const o of ownURL) expect(o).toEqual(ownURL[0]);
    // quiet kept its one variant; loud and named added at most 15 each to
    // the one they had.
    expect(scripts() - before).toBeLessThanOrEqual(30);
  });
});

describe("the transpile's line map (WR-43)", () => {
  const triples = (source: string) => {
    const t = transpile(source);
    if (!("js" in t)) throw new Error(t.error);
    const m = lineMap(source, t.js);
    const out: number[][] = [];
    for (let i = 0; i < m.length; i += 3) out.push(m.slice(i, i + 3));
    return out;
  };

  test("WR-43/c's script keeps its lines, and its last mapping is on line 3", () => {
    expect(triples('export function main() {\n  throw new Error("x");\n}\n')).toEqual([
      [1, 0, 1],
      [2, 2, 2],
      [3, 0, 3],
    ]);
  });

  test("lines the transpile drops (a comment, a type, a blank line) and a call it joins onto one line map to the source's lines", () => {
    // The JavaScript: export function main(a) { / const v = f(a, a); /
    // return v; / } / function f(x, y) { / return x; / }.
    const source = "// a comment\ntype T = number;\n\nexport function main(a: T) {\n  const v = f(\n    a,\n    a as number,\n  );\n  return v!;\n}\nfunction f(x: any, y: any) { return x; }\n";
    expect(triples(source)).toEqual([
      [1, 0, 4],
      [2, 2, 5],
      [2, 14, 6],
      [2, 17, 7],
      [2, 18, 8],
      [3, 2, 9],
      [4, 0, 10],
      [5, 0, 11],
    ]);
  });

  test("text that does not parse has no map", () => {
    expect(lineMap("export function (", "")).toEqual([]);
  });
});

describe("a stack's source line (WR-43)", () => {
  // Scripts built as the build helper builds them: a nativets script is
  // transpiled and keeps its transpile's line map; a //native one is the
  // bundle's text, which has none. The facade's origin is both base URLs.
  const dir = mkdtempSync(join(tmpdir(), "wl-native-stack-"));
  afterAll(() => rmSync(dir, { recursive: true, force: true }));
  const native = new Native({ bundleDir: dir, baseURL: () => "http://127.0.0.1:9", onError: () => {}, onUntracked: () => {} });
  native.capture();
  const write = (script: string, source: string, kind = "nativets") => {
    let js = source;
    let map: number[] | undefined;
    if (kind === "nativets") {
      const t = transpile(source);
      if (!("js" in t)) throw new Error(t.error);
      js = t.js;
      map = lineMap(source, js);
    }
    const m = nativeModule(js, kind);
    mkdirSync(join(dir, "scripts", "bun"), { recursive: true });
    writeFileSync(join(dir, "scripts", "bun", `${script}.native.js`), m.text);
    return { native: { ...m.form, line_map: map }, signatures: { main: { params: [] } } };
  };
  // The first two lines of the stack of the error main throws.
  const firstLines = async (script: string, meta: unknown, env: Record<string, string>) => {
    const resources = new InvocationResources();
    const ctx: InvocationContext = { id: "i", run: "r", env, signal: new AbortController().signal, resources };
    const inv = { id: "i", script, entry: "main", args: {}, env, resources };
    const r = (await invocations.run(ctx, () => native.run(inv, meta, ctx, "a"))) as { error?: string };
    return (JSON.parse(r.error!).stack as string).split("\n").slice(0, 2);
  };
  // A script of n lines: main, which throws on its second line, then a
  // const on each further line.
  const throwing = (n: number) =>
    ["export function main() {", '  throw new Error("x");', "}", ...Array.from({ length: n - 3 }, (_, i) => `const v${i + 4} = ${i + 4};`)].join("\n") + "\n";
  // The 22 reserved variables of a flow step (WR-22); on the oracle a flow
  // step has 23 variables (WR-45).
  const reserved: Record<string, string> = {};
  for (const name of [
    "WM_WORKSPACE", "WM_ROOT_WORKSPACE", "WM_EMAIL", "WM_USERNAME", "WM_PERMISSIONED_AS", "WM_TOKEN", "WM_BASE_URL",
    "WM_JOB_ID", "WM_JOB_PATH", "WM_FLOW_JOB_ID", "WM_ROOT_FLOW_JOB_ID", "WM_ROOT_JOB_ID", "WM_FLOW_PATH",
    "WM_FLOW_STEP_ID", "WM_SCHEDULE_PATH", "WM_SCHEDULED_FOR", "WM_STATE_PATH", "WM_OBJECT_PATH", "WM_END_USER_EMAIL",
    "WM_WORKER_GROUP", "WM_RUNNABLE_ID", "WM_TESTED_RUNNABLE",
  ]) {
    reserved[name] = name.toLowerCase();
  }

  test("as nativets, a frame past the code's last mapping reads the line numbered as the code's last mapped line, in the preamble: WR-43/c's script reads BASE_INTERNAL_URL's", async () => {
    const meta = write("c", 'export function main() {\n  throw new Error("x");\n}\n');
    for (const env of [{}, reserved]) {
      expect(await firstLines("c", meta, env)).toEqual(["const BASE_INTERNAL_URL = 'http://127.0.0.1:9';", "Error: x"]);
    }
  });

  test("as bunnative, whose bundle has no source map, the frame keeps its own line", async () => {
    const meta = write("cb", 'export function main() {\n  throw new Error("x");\n}\n', "bunnative");
    expect(await firstLines("cb", meta, reserved)).toEqual(['  throw new Error("x");', "Error: x"]);
  });

  test("the code follows 3n + 4 preamble lines: with an oracle flow step's 23 variables it starts on line 74, the line of WR-45/c's frame", async () => {
    // A script of 73 lines that throws on its second. With 23 variables
    // the frame, at 73 + 2, is past the code's last mapping, on its line
    // 73, so the line read is the preamble's 73rd, the empty line before
    // the code. With 22, at 70 + 2, it is in the code: the line read is
    // the code's 72 - 70 = 2.
    const meta = write("n73", throwing(73));
    expect(await firstLines("n73", meta, { ...reserved, CUSTOM: "c" })).toEqual(["", "Error: x"]);
    expect(await firstLines("n73", meta, reserved)).toEqual(['  throw new Error("x");', "Error: x"]);
  });

  test("past the fourth, a preamble line holds a variable, its const then its process.env entry, quoted with backslash, quote, LF and CR escaped; a name with no const has its entry alone", async () => {
    // V's lines are 5 and 6, then an empty line, while's entry on line 8,
    // and the empty line before the code on line 9.
    const env = { V: "a'b\\c\nd\re", while: "w" };
    const line = async (n: number) => (await firstLines(`l${n}`, write(`l${n}`, throwing(n)), env))[0];
    expect(await line(5)).toBe("const V = 'a\\'b\\\\c\\nd\\re';");
    expect(await line(6)).toBe("process.env['V'] = 'a\\'b\\\\c\\nd\\re';");
    expect(await line(7)).toBe("");
    expect(await line(8)).toBe("process.env['while'] = 'w';");
  });

  test("a frame in the code is looked up in the line map: the lines the transpile drops move the line read", async () => {
    // The JavaScript drops the two type lines, so its line g holds the
    // source's g + 2, and main throws on its line 2. With one variable the
    // preamble has 7 lines: the frame, at 7 + 2, maps to the source's line
    // 11, and the line read is the code's 11 - 7 = 4, not its line 2.
    const meta = write("types", "type A = number;\ntype B = string;\n" + throwing(20));
    expect(await firstLines("types", meta, { V: "v" })).toEqual(["const v4 = 4;", "Error: x"]);
  });
});

test("the embedded client is windmill-client 1.326.0 without WorkspaceService.editDeployTo, the one method Windmill 1.862.0's bundle has fewer (WR-40)", async () => {
  const c = await buildNativeClient();
  const text = `${c.text.slice(0, c.form.preamble_at)}const process = { env: {} };${c.text.slice(c.form.preamble_at)}`;
  const ns = await new vm.Script(text).runInContext(vm.createContext())({ commonjs: (e: unknown) => e });
  expect(ns.OpenAPI.VERSION).toBe("1.326.0");
  expect(typeof ns.WorkspaceService.editDeployTo).toBe("undefined");
  expect(typeof ns.WorkspaceService.editAutoInvite).toBe("function");
});
