// Developer checks of the build helper's textual scans and parse. The
// merge gate is the Go side: internal/bundle runs every case of
// conformance/build through the whole helper on the pinned Bun (ADR 0003).

import { describe, expect, test } from "bun:test";
import { parseAnnotations, targetFor } from "../build/annotations.ts";
import { packageName, rewriteRelativeImports, removePinnedImports, unpinned, workspacePath } from "../build/imports.ts";
import { liveBindings, parseModule, signatures, snakeCase } from "../build/signature.ts";
import { detectWorkflowAsCode, wrapperScan } from "../build/wac.ts";
import { rustTrim } from "../build/text.ts";

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
