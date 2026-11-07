// The native boundary's build (deviation D5; WR-40, WR-45): what the
// harness runs for a `nativets` or `//native` step.
//
// Windmill runs those steps in a fresh V8 isolate per job, as the ES module
// `file:///eval.ts` with an environment preamble in front of its text. The
// Bun harness runs them in a fresh realm per invocation instead (a node:vm
// context), where an ES module cannot be evaluated: Bun 1.3.10's
// vm.SourceTextModule crashes the process on top-level await. So the build
// turns each module into the source of one async function, its native
// form, which the harness compiles in the invocation's realm:
//
//   (async (P) => {"use strict";<preamble>PROLOGUE
//   BODY
//   EPILOGUE
//   })
//
// An arrow, so that the module's scope has no `arguments` binding, as an
// ES module has none;
// - P is a parameter name that the module's text never spells;
// - the harness inserts the preamble (process, BASE_URL and, for nativets,
//   one const per environment variable) at `preamble_at`, so its names are
//   bindings of the module's own scope and a top-level declaration of one of
//   them fails to compile, as WR-45 says;
// - PROLOGUE binds the static imports from P.imports, which the harness
//   resolves and links first (WR-40);
// - BODY is the module's text with its import declarations and `export`
//   keywords taken out, line for line, `import.meta` read from P.meta,
//   `import(…)` sent to P.import, and `this` outside every function and
//   class body (the module's own, which the arrow would otherwise take from
//   the script it is compiled as) written as `(void 0)`;
// - EPILOGUE returns the module namespace: a getter per export, so live
//   bindings stay live, and the names of `export *` re-exports.
//
// Top-level await, strict mode, an undefined `this` and module-scoped
// declarations carry over unchanged.

import { parse } from "@babel/parser";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { rustLines } from "./text.ts";

type Node = { type: string; start: number; end: number; [key: string]: any };

/** A static import a native module links before it runs. */
export type NativeLink = { import: number; name: string };

/** What the harness needs besides the module's text, recorded as the
 * `native` part of the script's metadata. */
export type NativeForm = {
  // "nativets", "bunnative" or, for the embedded client, "client".
  kind: string;
  param: string;
  preamble_at: number;
  // Static import specifiers, in order, which the harness resolves against
  // file:///eval.ts.
  imports: string[];
  // Named imports and re-exports that must exist in the imported module.
  links: NativeLink[];
  // For a nativets script, its transpile's line map (lineMap).
  line_map?: number[];
};

/** A module in its native form: the function's source and its record. */
export type NativeModule = { text: string; form: NativeForm; exports: string[] };

const PARAM = "__windmill_lite_native";

// A parameter name that text does not spell, so no binding of the module
// can capture it (as the env rewrite picks its binding, ADR 0010).
function paramFor(text: string): string {
  for (let n = 1; n <= 20; n++) {
    const name = n === 1 ? PARAM : `${PARAM}_${n}`;
    if (!text.includes(name)) return name;
  }
  throw new Error(`every candidate name for ${PARAM} occurs in the module's text`);
}

// Only the line breaks of a range: what a removed declaration leaves, so
// that every later line keeps its number.
function lineBreaks(s: string): string {
  return s.replace(/[^\r\n\u2028\u2029]+/g, "");
}

function walk(n: unknown, visit: (node: Node) => void): void {
  if (Array.isArray(n)) {
    for (const c of n) walk(c, visit);
    return;
  }
  if (n === null || typeof n !== "object") return;
  const node = n as Node;
  if (typeof node.type === "string") visit(node);
  for (const [k, v] of Object.entries(node)) {
    if (k === "loc" || k === "extra" || k.endsWith("Comments")) continue;
    if (v !== null && typeof v === "object") walk(v, visit);
  }
}

function bindingNames(pattern: Node | null, out: string[]): void {
  if (!pattern) return;
  switch (pattern.type) {
    case "Identifier":
      out.push(pattern.name);
      break;
    case "ObjectPattern":
      for (const p of pattern.properties as Node[]) bindingNames(p.type === "RestElement" ? p.argument : p.value, out);
      break;
    case "ArrayPattern":
      for (const e of pattern.elements as (Node | null)[]) bindingNames(e, out);
      break;
    case "AssignmentPattern":
      bindingNames(pattern.left, out);
      break;
    case "RestElement":
      bindingNames(pattern.argument, out);
      break;
  }
}

function exportedName(n: Node): string {
  return n.type === "StringLiteral" ? n.value : n.name;
}

const q = (s: string) => JSON.stringify(s);

/** The native form of an ES module's JavaScript text (a nativets script
 * after its transpile, or a //native bundle). It throws the parser's error
 * for text that is not a module. */
export function nativeModule(js: string, kind: string): NativeModule {
  const program = parse(js, { sourceType: "module" }).program;
  const P = paramFor(js);
  const edits: { start: number; end: number; text: string }[] = [];
  const imports: string[] = [];
  const links: NativeLink[] = [];
  const prologue: string[] = [];
  // Each export: its name and the expression its getter returns.
  const getters: { name: string; expr: string }[] = [];
  const stars: number[] = [];
  const importIndex = (spec: string) => {
    imports.push(spec);
    return imports.length - 1;
  };
  const remove = (n: Node) => edits.push({ start: n.start, end: n.end, text: lineBreaks(js.slice(n.start, n.end)) });

  for (const s of program.body as Node[]) {
    switch (s.type) {
      case "ImportDeclaration": {
        const i = importIndex(s.source.value);
        for (const sp of s.specifiers as Node[]) {
          const local = sp.local.name;
          if (sp.type === "ImportNamespaceSpecifier") prologue.push(`${local} = ${P}.imports[${i}]`);
          else {
            const name = sp.type === "ImportDefaultSpecifier" ? "default" : exportedName(sp.imported);
            links.push({ import: i, name });
            prologue.push(`${local} = ${P}.imports[${i}][${q(name)}]`);
          }
        }
        remove(s);
        break;
      }
      case "ExportNamedDeclaration": {
        if (s.declaration) {
          // `export` and nothing else goes: the declaration stays.
          edits.push({ start: s.start, end: s.declaration.start, text: lineBreaks(js.slice(s.start, s.declaration.start)) });
          const d = s.declaration as Node;
          const names: string[] = [];
          if (d.type === "VariableDeclaration") for (const v of d.declarations as Node[]) bindingNames(v.id, names);
          else names.push(d.id.name);
          for (const n of names) getters.push({ name: n, expr: n });
          break;
        }
        if (s.source) {
          const i = importIndex(s.source.value);
          for (const sp of s.specifiers as Node[]) {
            const name = exportedName(sp.exported);
            if (sp.type === "ExportNamespaceSpecifier") {
              getters.push({ name, expr: `${P}.imports[${i}]` });
              continue;
            }
            const from = exportedName(sp.local);
            links.push({ import: i, name: from });
            getters.push({ name, expr: `${P}.imports[${i}][${q(from)}]` });
          }
        } else {
          for (const sp of s.specifiers as Node[]) getters.push({ name: exportedName(sp.exported), expr: sp.local.name });
        }
        remove(s);
        break;
      }
      case "ExportAllDeclaration": {
        const i = importIndex(s.source.value);
        if (s.exported) getters.push({ name: exportedName(s.exported), expr: `${P}.imports[${i}]` });
        else stars.push(i);
        remove(s);
        break;
      }
      case "ExportDefaultDeclaration": {
        const d = s.declaration as Node;
        const named = (d.type === "FunctionDeclaration" || d.type === "ClassDeclaration") && d.id;
        if (named) {
          edits.push({ start: s.start, end: d.start, text: lineBreaks(js.slice(s.start, d.start)) });
          getters.push({ name: "default", expr: d.id.name });
        } else {
          // An expression, or an anonymous declaration, which becomes a
          // const: an anonymous function declaration is then not hoisted.
          const local = `${P}_default`;
          edits.push({ start: s.start, end: d.start, text: `const ${local} = ` });
          edits.push({ start: d.end, end: d.end, text: ";" });
          getters.push({ name: "default", expr: local });
        }
        break;
      }
    }
  }
  walk(program, (n) => {
    if (n.type === "MetaProperty" && n.meta?.name === "import" && n.property?.name === "meta") {
      edits.push({ start: n.start, end: n.end, text: `${P}.meta` });
    } else if (n.type === "ImportExpression") {
      // `import` is the expression's first six characters.
      edits.push({ start: n.start, end: n.start + 6, text: `${P}.import` });
    }
  });
  moduleThis(program, (n) => edits.push({ start: n.start, end: n.end, text: "(void 0)" }));
  edits.sort((a, b) => a.start - b.start || a.end - b.end);
  let body = "";
  let at = 0;
  for (const e of edits) {
    body += js.slice(at, e.start) + e.text;
    at = Math.max(at, e.end);
  }
  body += js.slice(at);

  const head = `(async (${P}) => {"use strict";`;
  const bind = prologue.length > 0 ? `const ${prologue.join(", ")};` : "";
  const ns = getters.map((g) => `get ${q(g.name)}() { return ${g.expr}; }`).join(", ");
  const epilogue = `;return ${P}.namespace({ ${ns} }, [${stars.map((i) => `${P}.imports[${i}]`).join(", ")}]);`;
  return {
    text: `${head}${bind}\n${body}\n${epilogue}\n})`,
    form: { kind, param: P, preamble_at: head.length, imports, links },
    exports: [...new Set(getters.map((g) => g.name))],
  };
}

// The nodes that give `this` a value of their own: functions other than
// arrows, and the members of a class body, whose computed keys are still
// evaluated outside.
const FUNCTIONS = new Set(["FunctionDeclaration", "FunctionExpression", "ObjectMethod"]);
const MEMBERS = new Set(["ClassMethod", "ClassPrivateMethod", "ClassProperty", "ClassPrivateProperty", "ClassAccessorProperty", "StaticBlock"]);

/** Calls found for every `this` of the module's own scope: outside every
 * function but arrows, and outside every class member. */
function moduleThis(n: unknown, found: (node: Node) => void): void {
  if (Array.isArray(n)) {
    for (const c of n) moduleThis(c, found);
    return;
  }
  if (n === null || typeof n !== "object") return;
  const node = n as Node;
  if (node.type === "ThisExpression") {
    found(node);
    return;
  }
  if (FUNCTIONS.has(node.type)) {
    if (node.type === "ObjectMethod" && node.computed) moduleThis(node.key, found);
    return;
  }
  if (MEMBERS.has(node.type)) {
    if (node.computed) moduleThis(node.key, found);
    return;
  }
  for (const [k, v] of Object.entries(node)) {
    if (k === "loc" || k === "extra" || k.endsWith("Comments")) continue;
    if (v !== null && typeof v === "object") moduleThis(v, found);
  }
}

/** TypeScript source with every `this` of the module's own scope written
 * as `(void 0)`; source that does not parse is returned as it is, for the
 * transpiler to report. */
export function withoutModuleThis(source: string): string {
  let program;
  try {
    program = parse(source, { sourceType: "module", plugins: ["typescript", "decorators-legacy", "decoratorAutoAccessors"] }).program;
  } catch {
    return source;
  }
  const at: Node[] = [];
  moduleThis(program, (n) => at.push(n));
  if (at.length === 0) return source;
  at.sort((a, b) => a.start - b.start);
  let out = "";
  let from = 0;
  for (const n of at) {
    out += source.slice(from, n.start) + "(void 0)";
    from = n.end;
  }
  return out + source.slice(from);
}

/** The native form of a CommonJS bundle: its namespace is its exports
 * object's own enumerable names, read when the module has run. The
 * embedded client is one (WR-40). */
export function nativeCommonJS(js: string, kind: string): NativeModule {
  const P = paramFor(js);
  const head = `(async function (${P}) {"use strict";`;
  return {
    text: `${head}var module = { exports: {} }, exports = module.exports;\n${js}\n;return ${P}.commonjs(module.exports);\n})`,
    form: { kind, param: P, preamble_at: head.length, imports: [], links: [] },
    exports: [],
  };
}

/** The header annotations the native runtime reads (WR-45): from the
 * leading run of lines that start with `//`, which a blank line ends,
 * `//useragent <text>` and `//proxy <url>`. */
export function nativeHeader(source: string): { user_agent?: string; proxy?: string } {
  const out: { user_agent?: string; proxy?: string } = {};
  for (const line of rustLines(source)) {
    if (!line.startsWith("//")) break;
    const m = /^\/\/(useragent|proxy) (.*)$/.exec(line);
    if (m === null) continue;
    const value = m[2].trim();
    if (m[1] === "useragent") out.user_agent = value;
    else out.proxy = value;
  }
  return out;
}

const transpiler = new Bun.Transpiler({ loader: "ts" });

/** A nativets script's JavaScript: its TypeScript with the types stripped
 * and nothing bundled (WR-40), or the transpiler's diagnostic, which fails
 * the step when it runs (WR-43). Bun's transpiler writes a module's own
 * `this` as `null`, where Windmill's leaves it, undefined, so the module's
 * `this` is written as `(void 0)` before. */
export function transpile(source: string): { js: string } | { error: string } {
  try {
    return { js: transpiler.transformSync(withoutModuleThis(source)) };
  } catch (e) {
    const msg = e as { message?: string; position?: { line?: number; column?: number } | null };
    const pos = msg.position;
    const at = pos && typeof pos.line === "number" ? ` at file:///eval.ts:${pos.line}:${(pos.column ?? 0) + 1}` : "";
    return { error: `${msg.message ?? String(e)}${at}` };
  }
}

/** A node of a parsed module, for lineMap: its key (its type, and for a
 * name or a literal its value) and where it starts and ends (lines from
 * 1, columns from 0, the end exclusive). */
type Placed = { key: string; sl: number; sc: number; el: number; ec: number };

/** TypeScript's wrappers of an expression, which the transpile replaces by
 * the expression. */
const TS_WRAPPERS = new Set(["TSAsExpression", "TSSatisfiesExpression", "TSNonNullExpression", "TSTypeAssertion", "TSInstantiationExpression"]);

/** The members of a TypeScript node that hold only types. */
const TYPE_MEMBERS = new Set(["typeAnnotation", "returnType", "typeParameters", "typeArguments", "superTypeParameters", "implements", "predicate"]);

function keyOf(n: Node): string {
  switch (n.type) {
    case "Identifier":
      return `${n.type} ${n.name}`;
    case "StringLiteral":
    case "NumericLiteral":
    case "BigIntLiteral":
    case "BooleanLiteral":
      return `${n.type} ${n.value}`;
    case "TemplateElement":
      return `${n.type} ${n.value?.cooked}`;
    default:
      return n.type;
  }
}

/** A module's nodes in source order. With ts, what the transpile leaves out
 * is left out: types, declarations of types alone, ambient (`declare`) and
 * abstract members, type-only imports and exports, enums and namespaces
 * (which it rewrites), and the wrappers of an expression. */
function placed(n: unknown, ts: boolean, out: Placed[]): void {
  if (Array.isArray(n)) {
    for (const c of n) placed(c, ts, out);
    return;
  }
  if (n === null || typeof n !== "object") return;
  const node = n as Node;
  if (typeof node.type !== "string" || node.loc == null) return;
  if (ts) {
    if (TS_WRAPPERS.has(node.type)) return placed(node.expression, ts, out);
    if (node.type === "TSParameterProperty") return placed(node.parameter, ts, out);
    if (node.type.startsWith("TS") || node.declare === true || node.abstract === true || node.importKind === "type" || node.exportKind === "type") return;
  }
  const { start, end } = node.loc;
  out.push({ key: keyOf(node), sl: start.line, sc: start.column, el: end.line, ec: end.column });
  for (const [k, v] of Object.entries(node)) {
    if (k === "loc" || k === "extra" || k.endsWith("Comments") || (ts && TYPE_MEMBERS.has(k))) continue;
    if (v !== null && typeof v === "object") placed(v, ts, out);
  }
}

/** The pairs (i, j) of a longest common subsequence of a and b, in order:
 * Myers' difference algorithm, whose time and memory grow with the number
 * of differences. Undefined when they differ in more than max places. */
function common(a: string[], b: string[], max: number): [number, number][] | undefined {
  const n = a.length;
  const m = b.length;
  const limit = Math.min(n + m, max);
  const off = limit + 1;
  const v = new Int32Array(2 * limit + 3);
  // trace[d] holds v as step d found it, on the diagonals -d-1 to d+1.
  const trace: Int32Array[] = [];
  for (let d = 0; d <= limit; d++) {
    trace.push(v.slice(off - d - 1, off + d + 2));
    for (let k = -d; k <= d; k += 2) {
      let x = k === -d || (k !== d && v[off + k - 1] < v[off + k + 1]) ? v[off + k + 1] : v[off + k - 1] + 1;
      let y = x - k;
      while (x < n && y < m && a[x] === b[y]) {
        x++;
        y++;
      }
      v[off + k] = x;
      if (x < n || y < m) continue;
      const pairs: [number, number][] = [];
      for (let e = d; e > 0; e--) {
        const w = trace[e];
        const at = (diagonal: number) => w[diagonal + e + 1];
        const kk = x - y;
        const prev = kk === -e || (kk !== e && at(kk - 1) < at(kk + 1)) ? kk + 1 : kk - 1;
        const px = at(prev);
        const py = px - prev;
        while (x > px && y > py) pairs.push([--x, --y]);
        x = px;
        y = py;
      }
      while (x > 0 && y > 0) pairs.push([--x, --y]);
      return pairs.reverse();
    }
  }
  return undefined;
}

/** How many differences lineMap's alignment tolerates before it gives up:
 * its memory grows with their square. */
const MAX_DIFFERENCES = 1024;

/** The line map of a nativets script's transpile: what a source map of it
 * gives a line, in which the harness looks positions up to read a stack's
 * source line as the native runtime does (WR-43). The transpiler writes no
 * map, so the build derives one: the nodes of the source, less what the
 * transpile leaves out, are aligned with the nodes of its JavaScript, and
 * each node the two share is mapped where it starts and at its last
 * character to the source's line there.
 *
 * It is a flat list of triples (line, column, source line), lines from 1
 * and columns from 0, in the JavaScript's order, keeping only the first of
 * each run of triples with the same source line: a lookup gives the source
 * line of the last mapping at or before a position, which that keeps.
 * Empty when either text does not parse or they differ too much. */
export function lineMap(source: string, js: string): number[] {
  const from: Placed[] = [];
  const to: Placed[] = [];
  try {
    placed(parse(source, { sourceType: "module", plugins: ["typescript", "decorators-legacy", "decoratorAutoAccessors"] }).program, true, from);
    placed(parse(js, { sourceType: "module" }).program, false, to);
  } catch {
    return [];
  }
  const pairs = common(
    from.map((p) => p.key),
    to.map((p) => p.key),
    MAX_DIFFERENCES,
  );
  if (pairs === undefined) return [];
  const points: [number, number, number][] = [];
  for (const [i, j] of pairs) {
    // The program spans the whole text, so it maps nothing.
    if (j === 0) continue;
    const s = from[i];
    const t = to[j];
    points.push([t.sl, t.sc, s.sl]);
    if (t.ec > 0) points.push([t.el, t.ec - 1, s.el]);
  }
  points.sort((p, q) => p[0] - q[0] || p[1] - q[1]);
  const out: number[] = [];
  for (const p of points) {
    if (out.length === 0 || out[out.length - 1] !== p[2]) out.push(...p);
  }
  return out;
}

/** The package the embedded client is built from: windmill-client, pinned
 * in this helper's package.json at the version Windmill's native runtime
 * embeds (WR-40: its OpenAPI.VERSION is 1.326.0, at v1.862.0 as at
 * v1.767.0). Apache-2.0. */
export const CLIENT_PACKAGE = "windmill-client";
export const CLIENT_VERSION = "1.326.0";

/** The one method of the package that Windmill 1.862.0's embedded bundle
 * does not have (WR-40), with its comment, in the package's generated
 * services module. */
const DROPPED_METHOD = /\n {4}\/\*\*\n {5}\* edit deploy to\n[\s\S]*?\n {4}static editDeployTo\(data\) \{\n[\s\S]*?\n {4}\}(?=\n)/g;

/** The embedded client in its native form: windmill-client bundled for the
 * browser target as CommonJS, which a nativets script imports as
 * `./windmill.ts` (WR-40), without the method that Windmill 1.862.0's
 * bundle lacks, `WorkspaceService.editDeployTo`. It refuses any other
 * version of the package, and a package in which that method is not found
 * exactly once. */
export async function buildNativeClient(): Promise<NativeModule> {
  const entry = Bun.resolveSync(CLIENT_PACKAGE, import.meta.dir);
  let dir = dirname(entry);
  while (!readable(join(dir, "package.json"))) dir = dirname(dir);
  const pkg = JSON.parse(readFileSync(join(dir, "package.json"), "utf8")) as { name?: string; version?: string };
  if (pkg.name !== CLIENT_PACKAGE || pkg.version !== CLIENT_VERSION) {
    throw new Error(`the embedded client needs ${CLIENT_PACKAGE}@${CLIENT_VERSION}, found ${pkg.name}@${pkg.version} at ${dir}`);
  }
  let dropped = 0;
  const r = await Bun.build({
    entrypoints: [entry],
    target: "browser",
    format: "cjs",
    minify: { syntax: true },
    plugins: [
      {
        name: "windmill-lite-embedded-client",
        setup(build) {
          build.onLoad({ filter: /[\\/]windmill-client[\\/]dist[\\/]services\.gen\.js$/ }, (args) => {
            const contents = readFileSync(args.path, "utf8").replace(DROPPED_METHOD, () => {
              dropped++;
              return "";
            });
            return { contents, loader: "js" };
          });
        },
      },
    ],
  });
  if (!r.success || r.outputs.length !== 1) throw new Error(r.logs.map(String).join("\n") || "the client's bundle has no single output");
  if (dropped !== 1) throw new Error(`the embedded client: WorkspaceService.editDeployTo found ${dropped} times in ${CLIENT_PACKAGE}@${CLIENT_VERSION}, not once`);
  return nativeCommonJS(await r.outputs[0].text(), "client");
}

function readable(path: string): boolean {
  try {
    readFileSync(path);
    return true;
  } catch {
    return false;
  }
}
