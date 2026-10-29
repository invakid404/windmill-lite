// Developer checks of the native boundary mode (deviation D5): the build's
// native form (build/native.ts) and the realm a native invocation runs in
// (runtime/native.ts). The gate is the step suite (conformance/steps/bun,
// WR-40 to WR-45), which runs native steps through the real harness; these
// pin the parts it cannot reach, such as the fetch user agent (WR-45/g).

import { afterAll, describe, expect, test } from "bun:test";
import vm from "node:vm";
import { nativeHeader, nativeModule, transpile } from "../build/native.ts";
import { type InvocationContext, InvocationResources, invocations } from "../runtime/context.ts";
import { DEFAULT_USER_AGENT, Native, type NativeForm, NativeJob, WEB_GLOBALS } from "../runtime/native.ts";

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

  const job = (form: Partial<NativeForm> = {}) => {
    const native = new Native({ bundleDir: "/nonexistent", baseURL: () => undefined, onError: () => {}, onUntracked });
    native.capture();
    const resources = new InvocationResources();
    const inv = { id: "i", script: "s", entry: "main", args: {}, env: {}, resources };
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
      const bytes = new g.ReadableStream({
        type: "bytes",
        autoAllocateChunkSize: 4,
        pull(ctl: ReadableByteStreamController) {
          got.bytes = [ctl instanceof g.ReadableByteStreamController, ctl.byobRequest instanceof g.ReadableStreamBYOBRequest];
          ctl.byobRequest!.respond(0);
          ctl.close();
        },
      });
      await bytes.getReader().read();
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
      expect(j.untrackedText()).toBe(
        "the native boundary handed this invocation objects it does not track (ADR 0001, D5): PerformanceTiming (2), WeakMap (1), function harnessFunction (1)",
      );
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
});
