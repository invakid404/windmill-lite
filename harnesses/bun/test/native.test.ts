// Developer checks of the native boundary mode (deviation D5): the build's
// native form (build/native.ts) and the realm a native invocation runs in
// (runtime/native.ts). The gate is the step suite (conformance/steps/bun,
// WR-40 to WR-45), which runs native steps through the real harness; these
// pin the parts it cannot reach, such as the fetch user agent (WR-45/g).

import { afterAll, describe, expect, test } from "bun:test";
import vm from "node:vm";
import { nativeHeader, nativeModule, transpile } from "../build/native.ts";
import { InvocationResources } from "../runtime/context.ts";
import { DEFAULT_USER_AGENT, Native, type NativeForm, NativeJob } from "../runtime/native.ts";

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
    expect(m.form.preamble_at).toBe('(async function (__windmill_lite_native) {"use strict";'.length);
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

  const job = (form: Partial<NativeForm> = {}) => {
    const native = new Native({ bundleDir: "/nonexistent", baseURL: () => undefined, onError: () => {} });
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
