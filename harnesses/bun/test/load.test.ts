// Developer checks of loadModule (runtime/runtime.ts), which evaluates a
// step module in its loading invocation's async context: Bun 1.4.3 runs a
// module that import() loads outside every context. The gates are the step
// cases of a module's load (WR-2, D9/module-evaluation-*, D11/top-level*)
// and the stock client's facade tests (internal/platform/facade).

import { AsyncLocalStorage } from "node:async_hooks";
import { afterAll, expect, test } from "bun:test";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { loadModule } from "../runtime/runtime.ts";

type Probe = { store: AsyncLocalStorage<string>; seen: Record<string, unknown>; evaluated: Record<string, number> };
const probe: Probe = { store: new AsyncLocalStorage<string>(), seen: {}, evaluated: {} };
(globalThis as Record<symbol, unknown>)[Symbol.for("windmill-lite.load-test")] = probe;

// Each module records what it saw under its own name, and counts its
// evaluations.
const PRELUDE = `const t = globalThis[Symbol.for("windmill-lite.load-test")];`;
const at = (name: string) => `t.evaluated[${JSON.stringify(name)}] = (t.evaluated[${JSON.stringify(name)}] ?? 0) + 1;`;

const dir = mkdtempSync(join(tmpdir(), "wl-load-"));
afterAll(() => rmSync(dir, { recursive: true, force: true }));
function file(name: string, source: string): string {
  const path = join(dir, name);
  mkdirSync(join(path, ".."), { recursive: true });
  writeFileSync(path, source);
  return path;
}

test("an ES module, its static imports and a CommonJS module evaluate in the caller's context", async () => {
  file("ctx/dep.mjs", `${PRELUDE} t.seen.dep = t.store.getStore(); export const v = 1;`);
  const esm = file("ctx/main.mjs", `${PRELUDE} import { v } from "./dep.mjs"; t.seen.esm = t.store.getStore(); export function main() { return v; }`);
  const cjs = file("ctx/main.cjs", `${PRELUDE} t.seen.cjs = t.store.getStore(); exports.main = () => 2;`);
  await probe.store.run("A", () => loadModule(esm));
  await probe.store.run("B", () => loadModule(cjs));
  expect([probe.seen.esm, probe.seen.dep, probe.seen.cjs]).toEqual(["A", "A", "B"]);
  expect(probe.store.getStore()).toBeUndefined();
});

test("a top-level await keeps the context, and so does the work the module starts", async () => {
  const path = file(
    "tla/main.mjs",
    `${PRELUDE} ${at("tla")}
     t.seen.before = t.store.getStore();
     await Bun.sleep(5);
     t.seen.after = t.store.getStore();
     setTimeout(() => { t.seen.timer = t.store.getStore(); }, 0);
     Promise.resolve().then(() => { t.seen.then = t.store.getStore(); });
     export function main() { return "ok"; }`,
  );
  const ns = await probe.store.run("C", () => loadModule(path));
  expect((ns.main as () => string)()).toBe("ok");
  await Bun.sleep(20);
  expect([probe.seen.before, probe.seen.after, probe.seen.timer, probe.seen.then]).toEqual(["C", "C", "C", "C"]);
  expect(probe.evaluated.tla).toBe(1);
});

test("the namespace is import()'s, and the module evaluates once", async () => {
  const esm = file("ns/main.mjs", `${PRELUDE} ${at("ns-esm")} export default function main() {} export const a = 1;`);
  const cjs = file("ns/main.cjs", `${PRELUDE} ${at("ns-cjs")} exports.main = () => 1; exports.n = 2;`);
  const fn = file("ns/fn.cjs", `${PRELUDE} ${at("ns-fn")} module.exports = function main() {};`);
  const a = await loadModule(esm);
  expect(a).toBe(await import(pathToFileURL(esm).href));
  expect(Object.keys(a).sort()).toEqual(["a", "default"]);
  const b = await loadModule(cjs);
  expect(Object.keys(b).sort()).toEqual(["default", "main", "n"]);
  expect((b.default as { main: unknown }).main).toBe(b.main);
  const c = await loadModule(fn);
  expect(typeof c.default).toBe("function");
  expect(c.main).toBeUndefined();
  expect([probe.evaluated["ns-esm"], probe.evaluated["ns-cjs"], probe.evaluated["ns-fn"]]).toEqual([1, 1, 1]);
});

test("a failure is the module's own error, its stack starting at the module, and is not evaluated twice", async () => {
  const fail = async (path: string) => {
    try {
      await probe.store.run("D", () => loadModule(path));
    } catch (e) {
      return e as Error;
    }
    throw new Error(`${path} loaded`);
  };
  const esm = await fail(file("fail/main.mjs", `${PRELUDE} ${at("fail-esm")} throw new RangeError("esm at load");`));
  const tla = await fail(file("fail/tla.mjs", `${PRELUDE} ${at("fail-tla")} await Bun.sleep(5); throw new TypeError("after a top-level await");`));
  const cjs = await fail(file("fail/main.cjs", `${PRELUDE} ${at("fail-cjs")} throw new EvalError("cjs at load");`));
  expect([esm.name, esm.message, tla.name, tla.message, cjs.name, cjs.message]).toEqual([
    "RangeError",
    "esm at load",
    "TypeError",
    "after a top-level await",
    "EvalError",
    "cjs at load",
  ]);
  const firstFrames = [esm, tla, cjs].map((e) => String(e.stack).split("\n")[1]);
  expect(firstFrames.map((f, i) => f.includes(["fail/main.mjs", "fail/tla.mjs", "fail/main.cjs"][i]))).toEqual([true, true, true]);
  expect([probe.evaluated["fail-esm"], probe.evaluated["fail-tla"], probe.evaluated["fail-cjs"]]).toEqual([1, 1, 1]);
});

// The same file loaded by import() alone, in a directory of its own, and by
// loadModule: what each resolves to, and how often an exported then ran.
test("an exported then and a CommonJS module's thenable exports are resolved as import() alone resolves them", async () => {
  const sources: Record<string, string> = {
    "then.mjs": "let calls = 0; export function then(resolve) { calls++; resolve({ main: () => calls }); } export function main() { return calls; }",
    "then.cjs": "let calls = 0; module.exports = { then(resolve) { calls++; resolve({ main: () => calls }); } };",
    "pending.cjs": "module.exports = new Promise(() => {});",
  };
  // What a load gives: main's answer if it has one, else its keys and the
  // type of its default export; or that it never settled.
  const outcome = (p: Promise<Record<string, unknown>>) =>
    Promise.race([
      p.then((ns) => (typeof ns.main === "function" ? (ns.main as () => unknown)() : `${Object.keys(ns).sort()} ${typeof ns.default}`)),
      Bun.sleep(500).then(() => "never settled"),
    ]);
  for (const [name, source] of Object.entries(sources)) {
    const alone = await outcome(import(pathToFileURL(file(`import-only/${name}`, source)).href));
    const loaded = await outcome(loadModule(file(`loaded/${name}`, source)));
    expect(`${name}: ${loaded}`).toBe(`${name}: ${alone}`);
  }
});
