// Leading-comment annotations of a TypeScript script (WR-31, WR-39): a
// textual scan of the leading block of `//` lines, not a parse.

import { rustLines, rustTrim } from "./text.ts";

export type Annotations = {
  npm: boolean;
  nodejs: boolean;
  native: boolean;
  nobundling: boolean;
  sandbox: boolean;
};

const FLAGS = ["npm", "nodejs", "native", "nobundling", "sandbox"] as const;

export function parseAnnotations(source: string): Annotations {
  const found: Annotations = { npm: false, nodejs: false, native: false, nobundling: false, sandbox: false };
  for (const line of rustLines(source)) {
    // A blank line is skipped. Leading whitespace before `//` is not
    // trimmed, so an indented comment ends the block.
    if (rustTrim(line) === "") continue;
    if (!line.startsWith("//")) break;
    const key = rustTrim(line.slice(2));
    if ((FLAGS as readonly string[]).includes(key)) found[key as keyof Annotations] = true;
  }
  return found;
}

// The bundler target the annotations select, in the order Windmill checks
// them (WR-40): `//nodejs` first, then `//native`, otherwise Bun.
export function targetFor(a: Annotations): "node" | "browser" | "bun" {
  if (a.nodejs) return "node";
  if (a.native) return "browser";
  return "bun";
}
