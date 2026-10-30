import { parse } from "@babel/parser";

// Shared code (ADR 0013), the Bun build's side: the sources a Bun-built
// script's shared-layout module and its group's closure are built from,
// and what a closure bundle shows that a CommonJS closure cannot hold. The
// Go build (internal/bundle/sharedcode.go) builds codebase groups with the
// same sources and wraps every closure bundle into its factory.

// The specifier a dependency reference imports the closure support module
// by, and the parameter of a closure that dependency code of an ES module
// group reads as import.meta.
export const CLOSURE_SPECIFIER = "windmill-lite:closure";
export const CLOSURE_META = "__wl_meta";

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
// default import the module's namespace.
export function esmReference(key: string, cjs: string, hasDefault: boolean): string {
  let text = `export * from ${JSON.stringify(cjs)};\n`;
  if (hasDefault) {
    text += `import { dep } from ${JSON.stringify(CLOSURE_SPECIFIER)};\nexport default dep(${JSON.stringify(key)}).default;\n`;
  }
  return text;
}

// A closure's entry: the env support module first, when the group has the
// env rewrite, then a table from each dependency module's key to a
// function that requires its file.
export function closureEntry(files: Map<string, string>, envSpecifier: string | undefined): string {
  const lines: string[] = [];
  if (envSpecifier !== undefined) lines.push(`require(${JSON.stringify(envSpecifier)});`);
  lines.push("module.exports = {");
  for (const k of [...files.keys()].sort()) lines.push(`  ${JSON.stringify(k)}: () => require(${JSON.stringify(files.get(k))}),`);
  lines.push("};");
  return lines.join("\n") + "\n";
}

// The modules of a closure bundle whose code keeps an import(), by the
// path the bundler's comment before each module names: a bundler rewrites
// every dynamic import it resolves, so an import expression left in its
// output is one it kept (a specifier it could not resolve at build, or an
// external one), which resolves from the file holding it (ADR 0013,
// decision item 5). The bundle is parsed, so text that only looks like an
// import, in a string or a comment, is not one.
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
    const node = n as { type?: unknown; start?: number; callee?: { type?: unknown } };
    if (node.type === "ImportExpression" || (node.type === "CallExpression" && node.callee?.type === "Import")) {
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

// A closure's id, from what makes its group.
export function closureId(key: string): string {
  return "bun-" + new Bun.CryptoHasher("sha256").update(key).digest("hex").slice(0, 12);
}
