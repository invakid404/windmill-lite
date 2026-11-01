// What the Go build's load-order analysis of host-provided modules reads
// (internal/bundle/loadorder.go, ADR 0010 item 1): every import of a listed
// module in a listed module's bundle, and, for a module that holds an
// import() of one, the specifiers it imports only from inside functions. A
// listed module that imports another while it loads needs that module
// registered first; an import() of it inside a function runs only when the
// function is called, so it puts no constraint on the order.

import { parse } from "@babel/parser";
import type { Syntax } from "./signature.ts";

type Node = { type?: unknown; [k: string]: unknown };

// One import of a listed module in a bundle (hostImport in Go): the module
// that holds it, the bundler's kind of import, the specifier as that module
// spells it, and the listed module's key.
export type HostImport = { from: string; kind: string; specifier: string; host: string };

// What the Go build reads of a bundle (importGraph in Go).
export type ImportGraph = { imports: HostImport[]; lazy: Record<string, string[] | null> };

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
//
// A top-level import() whose specifier is not a literal, such as
// import("./" + "a.ts") or a template literal with a substitution, can name
// any module once a bundler folds its specifier. The parse cannot tell which,
// so a module that holds one imports nothing lazily.
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
  let opaque = false;
  const stack: [unknown, boolean][] = [[ast.program, false]];
  while (stack.length > 0) {
    const [n, inFunction] = stack.pop()!;
    if (Array.isArray(n)) {
      for (const x of n) stack.push([x, inFunction]);
      continue;
    }
    if (n === null || typeof n !== "object") continue;
    const node = n as Node;
    const dynamic = node.type === "ImportExpression" || (node.type === "CallExpression" && (node.callee as Node | undefined)?.type === "Import");
    if (dynamic) {
      const spec = node.type === "ImportExpression" ? literalOf(node.source) : literalOf((node.arguments as unknown[])[0]);
      if (spec !== undefined) (inFunction ? inside : outside).add(spec);
      else if (!inFunction) opaque = true;
    }
    const fn = typeof node.type === "string" && FUNCTIONS.has(node.type);
    for (const [k, v] of Object.entries(node)) {
      if (k === "loc" || k.endsWith("Comments") || v === null || typeof v !== "object") continue;
      stack.push([v, inFunction || (fn && (k === "params" || k === "body"))]);
    }
  }
  if (opaque) return [];
  return [...inside].filter((s) => !outside.has(s)).sort();
}

// A metafile input's imports, as Bun records them.
type MetaImport = { path: string; kind: string; original?: string; external?: boolean };

// The imports of listed modules in a Bun bundle that the scan made, with
// every listed module left external: the metafile gives each external
// import's module, kind and specifier, and hostBy gives the key of the
// listed module that the build's plugin resolved it to, by importer and
// specifier. Other external imports (built-in modules, electron) are not of
// a listed module.
export function bundleImports(metafile: Record<string, unknown>, hostBy: Map<string, Map<string, string>>): HostImport[] {
  const inputs = (metafile.inputs ?? {}) as Record<string, { imports?: MetaImport[] }>;
  const out: HostImport[] = [];
  for (const [from, input] of Object.entries(inputs)) {
    for (const imp of input.imports ?? []) {
      if (!imp.external) continue;
      const specifier = imp.original ?? imp.path;
      const host = hostBy.get(from)?.get(specifier);
      if (host !== undefined) out.push({ from, kind: imp.kind, specifier, host });
    }
  }
  return out;
}
