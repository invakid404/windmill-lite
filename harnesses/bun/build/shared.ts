import { parse } from "@babel/parser";
import { builtinModules } from "node:module";
import { isAbsolute } from "node:path";
import type { Syntax } from "./signature.ts";

// Shared code (ADR 0013), the Bun build's side: the sources a Bun-built
// script's shared-layout module and its group's closure are built from,
// and what a closure bundle shows that a CommonJS closure cannot hold. The
// Go build (internal/bundle/sharedcode.go) builds codebase groups with the
// same sources and wraps every closure bundle into its factory.

// The specifier a dependency reference imports the closure support module
// by, and the base of the name of a closure's parameter that dependency
// code of an ES module group reads as import.meta: the build takes the
// first of CLOSURE_META, CLOSURE_META_2, … that no input of the closure
// spells, so that no binding of a dependency captures it.
export const CLOSURE_SPECIFIER = "windmill-lite:closure";
export const CLOSURE_META = "__wl_meta";

// The global property through which a closure's entry hands the registry
// its table of dependency modules while the closure runs, so that the
// module an instance runs with stays the step module's (the Go build's
// ClosureTable).
export const CLOSURE_TABLE = `Symbol.for("windmill-lite.closure-table")`;

// The prefix of the specifier an ES module reference re-exports its
// CommonJS reference by, which the shared-layout build resolves to the
// reference's file (the Go build's cjsSpecifier).
export const CJS_SPECIFIER = "windmill-lite:dep-cjs/";

// The support module of a shared-layout module of closure id: dep(key)
// returns the exports of the dependency module key in the module's own
// instance of the closure, made at its first call with the module's
// scope. A Bun-built module is an ES module, which reaches its require,
// file and import.meta through import.meta.
export function closureSupport(id: string): string {
  return `let instance;
export function dep(key) {
  if (instance === undefined) {
    const registry = globalThis[Symbol.for("windmill-lite.closures")];
    if (registry === undefined) throw new Error("windmill-lite: shared code needs the closure registry of the harness runtime");
    instance = registry.instance(${JSON.stringify(id)}, { require: import.meta.require, filename: import.meta.path, dirname: import.meta.dir, meta: import.meta });
  }
  return instance(key);
}
`;
}

// A shared-layout module's reference to its dependency module key: a
// CommonJS module whose exports are that module's in the instance, so the
// script imports it with the bundler's CommonJS interop.
export function depReference(key: string): string {
  return `module.exports = require(${JSON.stringify(CLOSURE_SPECIFIER)}).dep(${JSON.stringify(key)});\n`;
}

// A shared-layout module's reference to an ES module dependency key: an ES
// module that re-exports its named exports from the CommonJS reference cjs,
// live through its getters, and, when it has one (hasDefault), its default
// export, read when the reference initialises. A bundler gives an ES module
// importer Node's interop with a CommonJS module, whose default import is
// the whole exports object, so a CommonJS reference alone would hand a
// default import the module's namespace. With the module's export names all
// known (names), it lists them, so its namespace object has the module's
// names, in the order the bundler gives a namespace, and not the CommonJS
// interop marker (__esModule) of the reference it reads them from;
// otherwise it re-exports them all (export *), and the build keeps a script
// whose entry takes the namespace object on its per-script bundle (the Go
// build's esmReference).
export function esmReference(key: string, cjs: string, hasDefault: boolean, names?: { names: string[]; complete: boolean }): string {
  // The CommonJS reference initialises the module, whatever names an
  // importer reads, as the per-script bundle does for an import of it.
  let text = `import ${JSON.stringify(cjs)};\nexport * from ${JSON.stringify(cjs)};\n`;
  if (names?.complete) {
    const list = names.names.filter((n) => n !== "default").map((n) => (/^[A-Za-z_$][\w$]*$/.test(n) ? n : JSON.stringify(n)));
    text = `import ${JSON.stringify(cjs)};\n`;
    if (list.length > 0) text += `export { ${list.join(", ")} } from ${JSON.stringify(cjs)};\n`;
  }
  if (hasDefault) {
    text += `import { dep } from ${JSON.stringify(CLOSURE_SPECIFIER)};\nexport default dep(${JSON.stringify(key)}).default;\n`;
  }
  return text;
}

// A closure's entry: the env support module first, when the group has the
// env rewrite, then a table from each dependency module's key to a
// function that requires its file, handed over through CLOSURE_TABLE.
export function closureEntry(files: Map<string, string>, envSpecifier: string | undefined): string {
  const lines: string[] = [];
  if (envSpecifier !== undefined) lines.push(`require(${JSON.stringify(envSpecifier)});`);
  lines.push(`globalThis[${CLOSURE_TABLE}] = {`);
  for (const k of [...files.keys()].sort()) lines.push(`  ${JSON.stringify(k)}: () => require(${JSON.stringify(files.get(k))}),`);
  lines.push("};");
  return lines.join("\n") + "\n";
}

// Whether the runtime's resolver resolves a specifier to a module it has
// built in: Bun.resolveSync returns a built-in's name, such as node:fs or
// bun:sqlite, never a file path. It throws for a node: specifier that names
// no built-in, and returns a file path for a package.
function resolvesToBuiltin(spec: string): boolean {
  try {
    return !isAbsolute(Bun.resolveSync(spec, import.meta.dir));
  } catch {
    return false;
  }
}

// The specifiers that name a module the runtime has built in. Each is a
// name that node:module's builtinModules lists on the Bun that runs the
// build, alone or after node:, kept only when the runtime's resolver
// resolves it to a built-in module (resolvesToBuiltin). The build requires
// that Bun to be the pinned Bun 1.3.10 unless its caller allows another.
//
//   - Bun 1.3.10 lists Node's modules without the node: prefix, subpaths
//     such as fs/promises among them, and its own: bun, bun:ffi, bun:jsc,
//     bun:sqlite, bun:test and bun:wrap, and ws and undici, which it
//     implements itself whatever node_modules holds.
//   - Every listed name resolves alone. After node:, Node's names resolve
//     and Bun's own do not: node:bun, node:bun:sqlite, node:ws and
//     node:undici name no built-in module.
//   - A name the list leaves out names none, alone or after node:,
//     although Bun loads node:test.
//
// The Go build asks for these specifiers with the kept op, so both builds
// accept the same spellings.
export const BUILTIN_SPECIFIERS: readonly string[] = [...new Set(builtinModules.flatMap((m) => [m, `node:${m}`]))]
  .filter(resolvesToBuiltin)
  .sort();

const BUILTINS = new Set(BUILTIN_SPECIFIERS);

// Whether a specifier names a module the runtime has built in: one of
// BUILTIN_SPECIFIERS. The runtime resolves such a specifier to the same
// module from every file, so importing one from a closure instead of the
// step's module changes nothing. Any other specifier, node: ones included,
// keeps the fallbacks that apply to an external module.
export function builtinModule(spec: string): boolean {
  return BUILTINS.has(spec);
}

// The string a specifier node of the Babel AST spells: a string literal, or
// a template literal without expressions; undefined for anything else.
function literalOf(n: unknown): string | undefined {
  const x = n as { type?: unknown; value?: unknown; expressions?: unknown[]; quasis?: { value: { cooked?: string } }[] } | null | undefined;
  if (x?.type === "StringLiteral") return x.value as string;
  if (x?.type === "TemplateLiteral" && x.expressions!.length === 0) return x.quasis![0].value.cooked;
  return undefined;
}

// The modules of a closure bundle whose code keeps an import(), by the
// path the bundler's comment before each module names: a bundler rewrites
// every dynamic import it resolves, so an import expression left in its
// output is one it kept (a specifier it could not resolve at build, or an
// external one), which resolves from the file holding it. An import()
// whose specifier is a literal naming a module the runtime has built in
// (builtinModule) does not count: it loads that module from any file. The
// bundle is parsed, so text that only looks like an import, in a string or
// a comment, is not one.
export function keptImports(text: string): Set<string> {
  const ast = parse(text, { sourceType: "script", allowReturnOutsideFunction: true });
  const marks = (ast.comments ?? [])
    .filter((c) => c.type === "CommentLine" && /^ \S/.test(c.value) && (c.start === 0 || text[c.start! - 1] === "\n"))
    .map((c) => ({ start: c.start!, path: c.value.slice(1) }));
  const out = new Set<string>();
  const stack: unknown[] = [ast.program];
  while (stack.length > 0) {
    const n = stack.pop();
    if (Array.isArray(n)) {
      for (const x of n) stack.push(x);
      continue;
    }
    if (n === null || typeof n !== "object") continue;
    const node = n as { type?: unknown; start?: number; callee?: { type?: unknown }; source?: unknown; arguments?: unknown[] };
    const dynamic = node.type === "ImportExpression" || (node.type === "CallExpression" && node.callee?.type === "Import");
    const spec = node.type === "ImportExpression" ? literalOf(node.source) : literalOf(node.arguments?.[0]);
    if (dynamic && !(spec !== undefined && builtinModule(spec))) {
      let path = "";
      for (const m of marks) {
        if (m.start > (node.start ?? 0)) break;
        path = m.path;
      }
      out.add(path);
    }
    for (const [k, v] of Object.entries(node)) {
      if (k !== "loc" && !k.endsWith("Comments") && v !== null && typeof v === "object") stack.push(v);
    }
  }
  return out;
}

// The specifiers whose namespace object a module takes: a namespace import
// (`import * as ns`), a namespace re-export (`export * as ns from`), or an
// import() of a literal specifier, which a bundler resolves to the
// module's namespace object. An import of types only takes none. A script
// whose entry and another module of its bundle take the namespace object
// of one ES module dependency keeps its per-script bundle: in the shared
// layout the entry's would be its reference's and the dependency's the
// closure's, two objects where the per-script bundle has one (ADR 0013,
// decision item 5). null when the module does not parse, which the build
// reads as taking every namespace it imports.
export function namespaceImports(text: string, syntax: Syntax): string[] | null {
  const plugins: ("typescript" | "jsx" | "decorators-legacy" | "decoratorAutoAccessors")[] = ["decorators-legacy", "decoratorAutoAccessors"];
  if (syntax === "ts" || syntax === "tsx") plugins.push("typescript");
  if (syntax === "tsx" || syntax === "jsx") plugins.push("jsx");
  let ast;
  try {
    ast = parse(text, { sourceType: "unambiguous", allowReturnOutsideFunction: true, plugins });
  } catch {
    return null;
  }
  type Node = { type?: unknown; [k: string]: unknown };
  const out = new Set<string>();
  const stack: unknown[] = [ast.program];
  while (stack.length > 0) {
    const n = stack.pop();
    if (Array.isArray(n)) {
      for (const x of n) stack.push(x);
      continue;
    }
    if (n === null || typeof n !== "object") continue;
    const node = n as Node;
    const specifiers = (node.specifiers ?? []) as Node[];
    let spec: string | undefined;
    if (node.type === "ImportDeclaration" && node.importKind !== "type" && specifiers.some((s) => s.type === "ImportNamespaceSpecifier")) {
      spec = literalOf(node.source);
    } else if (node.type === "ExportNamedDeclaration" && node.exportKind !== "type" && specifiers.some((s) => s.type === "ExportNamespaceSpecifier")) {
      spec = literalOf(node.source);
    } else if (node.type === "ExportAllDeclaration" && node.exported != null && node.exportKind !== "type") {
      spec = literalOf(node.source);
    } else if (node.type === "ImportExpression") {
      spec = literalOf(node.source);
    } else if (node.type === "CallExpression" && (node.callee as Node | undefined)?.type === "Import") {
      spec = literalOf((node.arguments as unknown[])[0]);
    }
    if (spec !== undefined) out.add(spec);
    for (const [k, v] of Object.entries(node)) {
      if (k !== "loc" && !k.endsWith("Comments") && v !== null && typeof v === "object") stack.push(v);
    }
  }
  return [...out].sort();
}

// The syntax a module is parsed with, by its extension.
export function syntaxOf(path: string): Syntax {
  if (/\.[mc]?ts$/.test(path)) return "ts";
  if (path.endsWith(".tsx")) return "tsx";
  if (path.endsWith(".jsx")) return "jsx";
  return "js";
}

// A closure's id, from what makes its group.
export function closureId(key: string): string {
  return "bun-" + new Bun.CryptoHasher("sha256").update(key).digest("hex").slice(0, 12);
}
