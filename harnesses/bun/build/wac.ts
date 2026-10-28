// Workflow-as-code detection. windmill-lite refuses workflow-as-code
// scripts at load (ADR 0001 §3), so the build records every way Windmill
// would treat a script as one, for the default entrypoint `main`:
//
//   - `wrapper`: the worker's line scan that selects the workflow-as-code
//     wrapper when the job has no entrypoint override (WR-32, WR-39);
//   - `parsed`: the parser's classification by an exported
//     `main = workflow(async …)` declaration (WA-33, path 1);
//   - `substring`: the parser's fallback when no declaration gives `main`'s
//     parameters (WA-33, path 2).

import type { Parsed } from "./signature.ts";
import { functionDeclarations } from "./signature.ts";
import { rustLines, rustTrim } from "./text.ts";

type Node = { type: string; [key: string]: any };

export type WorkflowAsCode = { wrapper: boolean; parsed: boolean; substring: boolean };

// The wrapper scan (WR-32): it fires when the source has both an import of
// `windmill-client` and the substring `workflow`, either inside that import
// or on a line holding both `export` and `workflow(`. Lines are trimmed and
// those starting with `//` are skipped; block comments and strings are not
// (WR-39). An import is a line starting with `import` or `from` that names
// `windmill-client`, or a multi-line `import {` … `}` block that names it.
export function wrapperScan(source: string): boolean {
  let imported = false;
  let workflow = false;
  let block: string | undefined;
  for (const raw of rustLines(source)) {
    const line = rustTrim(raw);
    if (line.startsWith("//")) continue;
    if (block !== undefined) {
      block += "\n" + line;
      if (line.includes("}")) {
        if (block.includes("windmill-client")) {
          imported = true;
          if (block.includes("workflow")) workflow = true;
        }
        block = undefined;
      }
      continue;
    }
    if ((line.startsWith("import") || line.startsWith("from")) && line.includes("windmill-client")) {
      imported = true;
      if (line.includes("workflow")) workflow = true;
    } else if (line.startsWith("import") && line.includes("{") && !line.includes("}")) {
      block = line;
    }
    if (line.includes("export") && line.includes("workflow(")) workflow = true;
  }
  return imported && workflow;
}

function unparen(n: Node | undefined): Node | undefined {
  while (n?.type === "ParenthesizedExpression") n = n.expression;
  return n;
}

// WA-33 path 1: an exported top-level variable declaration of `main` whose
// initialiser calls the bare identifier `workflow` with an async arrow or
// async function expression, possibly parenthesised, as first argument.
function parsedWorkflow(p: Parsed): boolean {
  for (const s of p.program) {
    if (s.type !== "ExportNamedDeclaration" || s.declaration?.type !== "VariableDeclaration") continue;
    for (const d of s.declaration.declarations as Node[]) {
      if (d.id?.type !== "Identifier" || d.id.name !== "main") continue;
      const init = unparen(d.init);
      if (init?.type !== "CallExpression" || init.callee?.type !== "Identifier" || init.callee.name !== "workflow") {
        continue;
      }
      const first = unparen(init.arguments?.[0]);
      if ((first?.type === "ArrowFunctionExpression" || first?.type === "FunctionExpression") && first.async) {
        return true;
      }
    }
  }
  return false;
}

export function detectWorkflowAsCode(source: string, p: Parsed): WorkflowAsCode {
  const hasMain = functionDeclarations(p).has("main");
  const parsed = !hasMain && parsedWorkflow(p);
  const substring =
    !hasMain &&
    !parsed &&
    source.includes("workflow(") &&
    source.includes("task(") &&
    source.includes("windmill-client");
  return { wrapper: wrapperScan(source), parsed, substring };
}
