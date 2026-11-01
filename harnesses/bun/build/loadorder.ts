// What the Go build's load-order analysis of host-provided modules reads
// (internal/bundle/loadorder.go, ADR 0010 item 1): the imports of every
// module of a listed module's bundled closure, and, for a module that holds
// an import(), the specifiers it imports only from inside functions. A
// listed module that imports another while it loads needs that module
// registered first; an import() inside a function runs only when the
// function is called, so it puts no constraint on the order.

import { parse } from "@babel/parser";
import { dirname, join, normalize } from "node:path";
import type { Syntax } from "./signature.ts";

type Node = { type?: unknown; [k: string]: unknown };

// One import of one module of a bundle (moduleImport in Go): to is the
// imported module, as the bundle names its input, or empty when it is a
// listed module (host) or the bundler's record does not tell.
export type ModuleImport = { from: string; kind: string; specifier: string; to?: string; host?: string };

// The import graph of a bundle (importGraph in Go).
export type ImportGraph = { entry: string; imports: ModuleImport[]; lazy: Record<string, string[] | null> };

// The nodes whose `params` and `body` run only when the function is called:
// functions, arrows, and object and class methods (accessors and
// constructors included). Their other parts (a method's computed key and
// its decorators) run where the function is defined.
const FUNCTIONS = new Set([
  "FunctionDeclaration",
  "FunctionExpression",
  "ArrowFunctionExpression",
  "ObjectMethod",
  "ClassMethod",
  "ClassPrivateMethod",
]);

// The string a specifier node spells: a string literal, or a template
// literal without expressions; undefined for anything else.
function literalOf(n: unknown): string | undefined {
  const x = n as { type?: unknown; value?: unknown; expressions?: unknown[]; quasis?: { value: { cooked?: string } }[] } | null | undefined;
  if (x?.type === "StringLiteral") return x.value as string;
  if (x?.type === "TemplateLiteral" && x.expressions!.length === 0) return x.quasis![0].value.cooked;
  return undefined;
}

// The specifiers a module imports only through import() calls inside
// functions, sorted: every import() of such a specifier has a function, an
// arrow or a method around it, whose body runs when it is called. null when
// the module does not parse, which the Go build reads as importing nothing
// lazily.
//
// An import() evaluated at module top level, outside every function (a
// top-level await import(...), or one in a class's static block or field),
// runs while the module loads. Its specifier is not one of them, even when
// the module also imports it from a function. A function that the module
// calls while it loads is not followed: its import() still counts as lazy.
export function lazyImports(text: string, syntax: Syntax): string[] | null {
  const plugins: ("typescript" | "jsx" | "decorators-legacy" | "decoratorAutoAccessors")[] = ["decorators-legacy", "decoratorAutoAccessors"];
  if (syntax === "ts" || syntax === "tsx") plugins.push("typescript");
  if (syntax === "tsx" || syntax === "jsx") plugins.push("jsx");
  let ast;
  try {
    ast = parse(text, { sourceType: "unambiguous", allowReturnOutsideFunction: true, plugins });
  } catch {
    return null;
  }
  const inside = new Set<string>();
  const outside = new Set<string>();
  const stack: [unknown, boolean][] = [[ast.program, false]];
  while (stack.length > 0) {
    const [n, inFunction] = stack.pop()!;
    if (Array.isArray(n)) {
      for (const x of n) stack.push([x, inFunction]);
      continue;
    }
    if (n === null || typeof n !== "object") continue;
    const node = n as Node;
    let spec: string | undefined;
    if (node.type === "ImportExpression") spec = literalOf(node.source);
    else if (node.type === "CallExpression" && (node.callee as Node | undefined)?.type === "Import") spec = literalOf((node.arguments as unknown[])[0]);
    if (spec !== undefined) (inFunction ? inside : outside).add(spec);
    const fn = typeof node.type === "string" && FUNCTIONS.has(node.type);
    for (const [k, v] of Object.entries(node)) {
      if (k === "loc" || k.endsWith("Comments") || v === null || typeof v !== "object") continue;
      stack.push([v, inFunction || (fn && (k === "params" || k === "body"))]);
    }
  }
  return [...inside].filter((s) => !outside.has(s)).sort();
}

// A metafile input's imports, as Bun records them.
type MetaImport = { path: string; kind: string; original?: string; external?: boolean };

// The imports of every module of a Bun bundle, from its metafile and from
// what the build's plugin resolved. The file each import loads comes from
// one of three places:
//
//   - for a bare specifier Bun resolved, the metafile's path, which names
//     the file, with the specifier as original;
//   - for an import the plugin resolved, whose path is the specifier,
//     resolvedBy: what the plugin resolved, by importer, kind and specifier;
//   - for a relative import the plugin left to Bun, whose path is the
//     specifier too, the input at the specifier's path from its importer,
//     when there is one.
//
// An import the plugin resolved to a listed module is external in the
// bundle the scan makes; hostBy gives its key, by importer and specifier.
// Other external imports (built-in modules, electron) load nothing of the
// bundle and are left out.
export function bundleImports(
  metafile: Record<string, unknown>,
  resolvedBy: Map<string, Map<string, string>>,
  hostBy: Map<string, Map<string, string>>,
): ModuleImport[] {
  const inputs = (metafile.inputs ?? {}) as Record<string, { imports?: MetaImport[] }>;
  const out: ModuleImport[] = [];
  for (const [from, input] of Object.entries(inputs)) {
    for (const imp of input.imports ?? []) {
      const specifier = imp.original ?? imp.path;
      if (imp.external) {
        const host = hostBy.get(from)?.get(specifier);
        if (host !== undefined) out.push({ from, kind: imp.kind, specifier, host });
        continue;
      }
      let to: string | undefined;
      if (imp.original !== undefined) {
        to = imp.path;
      } else {
        const by = resolvedBy.get(from);
        to = by?.get(`${imp.kind}\0${specifier}`);
        if (to === undefined && by !== undefined) {
          for (const [key, p] of by) if (key.slice(key.indexOf("\0") + 1) === specifier) to = p;
        }
        if (to === undefined && specifier.startsWith(".")) {
          const at = normalize(join(dirname(from), specifier));
          if (Object.hasOwn(inputs, at)) to = at;
        }
      }
      out.push({ from, kind: imp.kind, specifier, to: to ?? "" });
    }
  }
  return out;
}
