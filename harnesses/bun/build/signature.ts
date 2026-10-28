// The TypeScript signature parse (WR-6 to WR-8, WR-V3, WR-39): an AST of
// the module, walked over its top-level declarations only, so comments and
// strings never change a signature.
//
// An entrypoint's parameters come from the top-level function declaration
// of that name, exported or not; any other export (an arrow function, a
// `const` holding a function, a re-export) has an empty parameter list,
// which is how Windmill's parser sees it (WR-7, WR-V1). A parameter
// hydrates to `Date` exactly when its annotation resolves to `Date` (WR-8).

import { parse } from "@babel/parser";

type Node = { type: string; [key: string]: any };

export type Param = { name: string; date: boolean };
export type Signature = { params: Param[] } | { error: string };

export type Parsed = { program: Node[] };

// The parser accepts what Bun bundles: TypeScript with its decorators
// (class, member and parameter ones) and auto-accessors. Babel 8 has
// dropped the deprecated `import … assert {}` form, so that fails to parse.
export function parseModule(source: string): Parsed {
  const file = parse(source, {
    sourceType: "module",
    plugins: ["typescript", "decorators-legacy", "decoratorAutoAccessors"],
  });
  return { program: file.program.body as Node[] };
}

// The top-level declarations of a statement, looking through `export` but
// not through `export default` (a default export has no entrypoint name).
function declarationOf(statement: Node): Node | undefined {
  if (statement.type === "ExportNamedDeclaration") return statement.declaration ?? undefined;
  return statement;
}

// Every top-level function declaration by name, the first one of a name
// winning (an overload signature comes before its implementation).
export function functionDeclarations(p: Parsed): Map<string, Node> {
  const out = new Map<string, Node>();
  for (const s of p.program) {
    const d = declarationOf(s);
    if (d === undefined) continue;
    if ((d.type === "FunctionDeclaration" || d.type === "TSDeclareFunction") && d.id) {
      if (!out.has(d.id.name)) out.set(d.id.name, d);
    }
  }
  return out;
}

// Snake case, as alias names are compared (WR-8): a boundary before an
// upper-case letter that follows a lower-case letter or a digit, or that
// starts a word after an acronym; `-`, `_` and spaces separate words.
const isUpper = (c: string | undefined) => c !== undefined && /\p{Lu}/u.test(c);
const isLower = (c: string | undefined) => c !== undefined && /\p{Ll}/u.test(c);
const isDigit = (c: string | undefined) => c !== undefined && /\p{Nd}/u.test(c);

export function snakeCase(name: string): string {
  const words: string[] = [];
  let word = "";
  const flush = () => {
    if (word !== "") words.push(word.toLowerCase());
    word = "";
  };
  const chars = [...name];
  for (let i = 0; i < chars.length; i++) {
    const c = chars[i];
    if (c === "_" || c === "-" || c === " ") {
      flush();
      continue;
    }
    const prev = chars[i - 1];
    const next = chars[i + 1];
    if (isUpper(c) && word !== "" && (isLower(prev) || isDigit(prev) || (isUpper(prev) && isLower(next)))) flush();
    word += c;
  }
  flush();
  return words.join("_");
}

// Local top-level type aliases, exported or not, keyed by their snake-case
// name; a later alias of the same key replaces an earlier one.
function typeAliases(p: Parsed): Map<string, Node> {
  const out = new Map<string, Node>();
  for (const s of p.program) {
    const d = declarationOf(s);
    if (d?.type === "TSTypeAliasDeclaration") out.set(snakeCase(d.id.name), d.typeAnnotation);
  }
  return out;
}

function isNullish(t: Node): boolean {
  return t.type === "TSUndefinedKeyword" || t.type === "TSNullKeyword";
}

// Whether a type annotation resolves to `Date` (WR-8, WR-V3): the reference
// `Date` itself, before any local symbol; a local alias that resolves to it;
// parentheses; and a two-member union of one of those with `undefined` or
// `null`. Anything else, including longer unions, arrays, interfaces and
// imported names, does not.
function resolvesToDate(t: Node | undefined, aliases: Map<string, Node>, seen: Set<string>): boolean {
  if (t === undefined || t === null) return false;
  switch (t.type) {
    case "TSParenthesizedType":
      return resolvesToDate(t.typeAnnotation, aliases, seen);
    case "TSTypeReference": {
      if (t.typeName?.type !== "Identifier") return false;
      const name: string = t.typeName.name;
      if (name === "Date") return true;
      const key = snakeCase(name);
      const target = aliases.get(key);
      if (target === undefined || seen.has(key)) return false;
      return resolvesToDate(target, aliases, new Set([...seen, key]));
    }
    case "TSUnionType": {
      if (t.types.length !== 2) return false;
      const [a, b] = t.types as Node[];
      if (isNullish(a)) return resolvesToDate(b, aliases, seen);
      if (isNullish(b)) return resolvesToDate(a, aliases, seen);
      return false;
    }
    default:
      return false;
  }
}

function annotation(n: Node): Node | undefined {
  return n.typeAnnotation?.typeAnnotation;
}

// The parameters of one function declaration, or the parse failure that
// fails a job whose entrypoint it is (WR-7): destructured objects are named
// `anon1`, `anon2`, … in order; a rest parameter or any other pattern is
// unsupported. A TypeScript `this` parameter is erased by the compiler and
// is not a parameter at run time.
function paramsOf(fn: Node, aliases: Map<string, Node>): Signature {
  const params: Param[] = [];
  let anon = 0;
  for (const [i, p] of (fn.params as Node[]).entries()) {
    if (i === 0 && p.type === "Identifier" && p.name === "this") continue;
    const target = p.type === "AssignmentPattern" ? (p.left as Node) : p;
    if (target.type === "Identifier") {
      params.push({ name: target.name, date: resolvesToDate(annotation(target), aliases, new Set()) });
    } else if (target.type === "ObjectPattern") {
      anon++;
      params.push({ name: `anon${anon}`, date: resolvesToDate(annotation(target), aliases, new Set()) });
    } else {
      return { error: `parameter syntax unsupported: ${describe(target)}` };
    }
  }
  return { params };
}

function describe(p: Node): string {
  switch (p.type) {
    case "RestElement":
      return "rest parameter";
    case "ArrayPattern":
      return "array destructuring";
    default:
      return p.type;
  }
}

// The signature recorded for each export name (ADR 0002): the parameters of
// the top-level function declaration of that name, or none.
export function signatures(p: Parsed, exports: string[]): Record<string, Signature> {
  const fns = functionDeclarations(p);
  const aliases = typeAliases(p);
  const out: Record<string, Signature> = {};
  for (const name of exports) {
    const fn = fns.get(name);
    out[name] = fn === undefined ? { params: [] } : paramsOf(fn, aliases);
  }
  return out;
}

// Import specifiers as a parser sees them, for pinned-import removal
// (WR-26): static imports (type-only ones included, unless `values` asks
// for the imports that remain at run time), re-exports, and dynamic imports
// of a string literal, in source order.
export function importSpecifiers(p: Parsed, opts: { values?: boolean } = {}): string[] {
  const out: string[] = [];
  const visit = (n: unknown): void => {
    if (Array.isArray(n)) {
      for (const c of n) visit(c);
      return;
    }
    if (n === null || typeof n !== "object") return;
    const node = n as Node;
    if (typeof node.type === "string") {
      if (
        (node.type === "ImportDeclaration" ||
          node.type === "ExportNamedDeclaration" ||
          node.type === "ExportAllDeclaration") &&
        node.source?.type === "StringLiteral"
      ) {
        if (!(opts.values && (node.importKind === "type" || node.exportKind === "type"))) out.push(node.source.value);
      } else if (node.type === "ImportExpression" && node.source?.type === "StringLiteral") {
        out.push(node.source.value);
      } else if (
        node.type === "CallExpression" &&
        node.callee?.type === "Import" &&
        node.arguments?.[0]?.type === "StringLiteral"
      ) {
        out.push(node.arguments[0].value);
      }
    }
    for (const [k, v] of Object.entries(node)) {
      if (k === "loc" || k === "start" || k === "end" || k === "extra" || k.endsWith("Comments")) continue;
      if (v !== null && typeof v === "object") visit(v);
    }
  };
  visit(p.program);
  return out;
}

// The names a module binds with `export let` or `export var` at top level:
// a shim cannot follow such a binding (ADR 0010, `host_module_live_binding`).
export function liveBindings(p: Parsed): string[] {
  const out: string[] = [];
  for (const s of p.program) {
    if (s.type !== "ExportNamedDeclaration") continue;
    const d = s.declaration;
    if (d?.type !== "VariableDeclaration" || d.declare) continue;
    if (d.kind !== "let" && d.kind !== "var") continue;
    for (const decl of d.declarations as Node[]) collectNames(decl.id, out);
  }
  return out;
}

function collectNames(pattern: Node | null, out: string[]): void {
  if (!pattern) return;
  switch (pattern.type) {
    case "Identifier":
      out.push(pattern.name);
      break;
    case "ObjectPattern":
      for (const prop of pattern.properties as Node[]) {
        collectNames(prop.type === "RestElement" ? prop.argument : prop.value, out);
      }
      break;
    case "ArrayPattern":
      for (const el of pattern.elements as (Node | null)[]) collectNames(el, out);
      break;
    case "AssignmentPattern":
      collectNames(pattern.left, out);
      break;
    case "RestElement":
      collectNames(pattern.argument, out);
      break;
  }
}
