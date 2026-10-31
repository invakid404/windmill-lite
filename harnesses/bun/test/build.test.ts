// Developer checks of the build helper's textual scans and parse. The
// merge gate is the Go side: internal/bundle runs every case of
// conformance/build through the whole helper on the pinned Bun (ADR 0003).

import { describe, expect, test } from "bun:test";
import { builtinModules, isBuiltin } from "node:module";
import { parseAnnotations, targetFor } from "../build/annotations.ts";
import { packageName, rewriteRelativeImports, removePinnedImports, unpinned, workspacePath } from "../build/imports.ts";
import { BUILTIN_SPECIFIERS, builtinModule, keptImports } from "../build/shared.ts";
import { liveBindings, parseModule, signatures, snakeCase } from "../build/signature.ts";
import { detectWorkflowAsCode, wrapperScan } from "../build/wac.ts";
import { rustTrim } from "../build/text.ts";

// The shared cases that the Go loader's HasAnnotation also runs (internal/bundle).
const annotationCases: { spec: string; variant?: string; title: string; source: string; flags: string[] }[] = (
  await Bun.file(new URL("../../../conformance/build/annotations.json", import.meta.url)).json()
).cases;

describe("annotations: conformance/build/annotations.json (WR-31, SY-V3)", () => {
  for (const c of annotationCases) {
    test(`${c.spec} ${c.variant ?? ""} ${c.title}`, () => {
      const a = parseAnnotations(c.source);
      const set = Object.entries(a)
        .filter(([, on]) => on)
        .map(([flag]) => flag)
        .sort();
      expect(set).toEqual(c.flags);
    });
  }
});

describe("annotations (WR-31)", () => {
  test("leading block only, trimmed after //, blank lines skipped", () => {
    const a = parseAnnotations("\n// nobundling\n\n//native\nconst x = 1;\n//sandbox\n");
    expect(a).toEqual({ npm: false, nodejs: false, native: true, nobundling: true, sandbox: false });
    expect(targetFor(a)).toBe("browser");
  });
  test("an indented comment ends the block", () => {
    expect(parseAnnotations("  //nobundling\n").nobundling).toBe(false);
  });
  test("Rust's whitespace, not JavaScript's", () => {
    expect(rustTrim("\u0085x\u0085")).toBe("x");
    expect(rustTrim("﻿x")).toBe("﻿x");
  });
});

describe("workflow-as-code (WR-32, WA-33)", () => {
  test("the wrapper scan", () => {
    expect(wrapperScan('import { workflow } from "windmill-client"\n')).toBe(true);
    expect(wrapperScan('// import { workflow } from "windmill-client"\n')).toBe(false);
    expect(wrapperScan('/* import { workflow } from "windmill-client" */\n')).toBe(false);
    expect(wrapperScan('import {\n  workflow,\n} from "windmill-client";\n')).toBe(true);
    expect(wrapperScan('import { x } from "windmill-client"\nexport const m = workflow(1)\n')).toBe(true);
    expect(wrapperScan('import { x } from "windmill-client"\n')).toBe(false);
  });
  test("the parser's paths", () => {
    const src = "export const main = workflow(async (x: number) => x)";
    expect(detectWorkflowAsCode(src, parseModule(src))).toEqual({ wrapper: false, parsed: true, substring: false });
  });
});

describe("signatures (WR-6 to WR-8)", () => {
  test("names, anonN, Date and unsupported patterns", () => {
    const p = parseModule(
      "type T = Date | null;\nexport function main(a, { b }, d?: T, e: Date | null | undefined) {}\nexport function r(...x) {}\nexport const k = () => 1;",
    );
    expect(signatures(p, ["main", "r", "k"])).toEqual({
      main: {
        params: [
          { name: "a", date: false },
          { name: "anon1", date: false },
          { name: "d", date: true },
          { name: "e", date: false },
        ],
      },
      r: { error: "parameter syntax unsupported: rest parameter" },
      k: { params: [] },
    });
  });
  test("snake case of alias names", () => {
    expect(snakeCase("MyDate")).toBe("my_date");
    expect(snakeCase("HTTPDate")).toBe("http_date");
    expect(snakeCase("ts")).toBe("ts");
  });
  test("live bindings", () => {
    expect(liveBindings(parseModule("export let a = 1, { b } = {};\nexport var c;\nexport const d = 1;"))).toEqual(["a", "b", "c"]);
  });
});

describe("import transforms (WR-26, WR-28)", () => {
  test("pin removal, longest first and everywhere", () => {
    const src = 'import a from "p@1/x";\nimport b from "p@1";\nconst s = "p@1/x p@1";';
    expect(removePinnedImports(src, parseModule(src))).toBe('import a from "p/x";\nimport b from "p";\nconst s = "p/x p";');
    expect(unpinned("@s/p@1.2.3/sub")).toBe("@s/p/sub");
    expect(unpinned("./x@1")).toBeUndefined();
  });
  test("the .ts rewrite reproduces collisions", () => {
    expect(rewriteRelativeImports('import a from "./util";\nimport b from "./util_db";')).toBe(
      'import a from "./util.ts";\nimport b from "./util.ts_db";',
    );
  });
  test("workspace paths", () => {
    expect(workspacePath("../c/z.ts", "f/a/lib")).toBe("f/c/z");
    expect(workspacePath("/f/b/u.ts", "f/a/lib")).toBe("f/b/u");
    expect(workspacePath("./x", "f/a/lib")).toBeUndefined();
    expect(packageName("@s/p/sub")).toBe("@s/p");
    expect(packageName("node:fs")).toBeUndefined();
    expect(packageName("fs/promises")).toBeUndefined();
  });
});

describe("shared code: built-in modules and kept imports", () => {
  test("the built-in specifiers are the runtime's listed names, alone and after node: where its resolver accepts that", () => {
    for (const m of ["async_hooks", "fs", "fs/promises", "node:fs/promises", "stream/web", "bun", "bun:sqlite", "ws", "undici"]) expect(BUILTIN_SPECIFIERS).toContain(m);
    // Every listed name is a built-in specifier alone, and every specifier is one the runtime calls built in.
    for (const m of builtinModules) expect(BUILTIN_SPECIFIERS).toContain(m);
    for (const m of BUILTIN_SPECIFIERS) expect(isBuiltin(m)).toBe(true);
    // Bun lists its own modules bare, and its resolver rejects them after node:.
    for (const m of ["node:bun", "node:bun:ffi", "node:bun:jsc", "node:bun:sqlite", "node:bun:test", "node:bun:wrap", "node:ws", "node:undici"]) {
      expect(() => Bun.resolveSync(m, import.meta.dir)).toThrow(/No such built-in module/);
      expect(BUILTIN_SPECIFIERS).not.toContain(m);
    }
    // Each of Node's listed names is also built in after node:.
    for (const m of builtinModules) if (!m.startsWith("bun") && m !== "ws" && m !== "undici") expect(BUILTIN_SPECIFIERS).toContain(`node:${m}`);
  });
  test("a built-in is one of the specifiers, spelled exactly", () => {
    for (const m of ["async_hooks", "node:async_hooks", "fs/promises", "node:fs/promises", "bun", "bun:sqlite", "ws", "undici"]) expect(builtinModule(m)).toBe(true);
    for (const m of ["node:nope", "bun:nope", "left-pad", "./async_hooks", "/abs/fs", "async_hooks/nope", "node:bun", "node:bun:sqlite", "node:ws", "node:undici", "node:test"]) {
      expect(builtinModule(m)).toBe(false);
    }
  });
  test("an import() of a literal built-in is not kept; a variable, an external package or a node: spelling that names no built-in is", () => {
    const text = [
      "// a.js",
      'var a = () => import("node:async_hooks");',
      "// b.js",
      "var b = (spec) => import(spec);",
      "// c.js",
      'var c = () => import("left-pad");',
      "// d.js",
      "var d = () => [import(`fs/promises`), import(\"bun:sqlite\"), import(\"async_hooks\")];",
      "// e.js",
      'var e = () => "import(spec)"; // import(spec)',
      "// f.js",
      'var f = () => import("node:nope");',
      "// g.js",
      "var g = (x) => import(`node:${x}`);",
      "// h.js",
      'var h = () => import("node:bun:sqlite");',
      "// i.js",
      'var i = () => import("node:ws");',
      "// j.js",
      'var j = () => [import("node:fs/promises"), import("ws")];',
      "",
    ].join("\n");
    expect([...keptImports(text)].sort()).toEqual(["b.js", "c.js", "f.js", "g.js", "h.js", "i.js"]);
  });
});
