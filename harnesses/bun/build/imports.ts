// The import transforms Windmill applies before bundling a Bun script and
// every workspace script it imports. Both are whole-text replacements
// (WR-V4): they also change matching text in strings and comments.
//
//   1. Pinned-import removal (WR-26): each specifier `pkg@version[/sub]` or
//      `@scope/pkg@version[/sub]` that the parser reports is replaced,
//      longest first, everywhere in the text by `pkg[/sub]`.
//   2. The `.ts` rewrite (WR-28): each static import that Bun's scanner
//      (`Bun.Transpiler().scan`, which also reads a hashbang line and
//      auto-accessors) reports, starting with `.`, `/u/` or `/f/` and not
//      ending in `.ts`,
//      gets `.ts` appended to every occurrence of its text, in scanner
//      order and without deduplication, so overlapping or repeated
//      specifiers collide exactly as on Windmill (WR-38, ADR 0001 §7).

import { builtinModules } from "node:module";
import { importSpecifiers, type Parsed } from "./signature.ts";

const PINNED = /^(npm:)?((?:@[^/@:]+\/)?[^/@:.][^/@:]*)@([^/]+)(\/.*)?$/;

// The unpinned form of a specifier, or undefined when it is not pinned. An
// `npm:` prefix stays (WR-26).
export function unpinned(spec: string): string | undefined {
  const m = PINNED.exec(spec);
  if (m === null) return undefined;
  return (m[1] ?? "") + m[2] + (m[4] ?? "");
}

export function removePinnedImports(source: string, p: Parsed): string {
  const pinned = [...new Set(importSpecifiers(p).filter((s) => unpinned(s) !== undefined))];
  pinned.sort((a, b) => b.length - a.length);
  let out = source;
  for (const spec of pinned) out = out.split(spec).join(unpinned(spec)!);
  return out;
}

const scanner = new Bun.Transpiler({ loader: "ts" });

export function rewriteRelativeImports(source: string): string {
  let out = source;
  for (const imp of scanner.scan(source).imports) {
    // The scan lists an empty entry for a decorated class.
    if (imp == null || imp.kind !== "import-statement") continue;
    const spec = imp.path;
    if (!(spec.startsWith(".") || spec.startsWith("/u/") || spec.startsWith("/f/"))) continue;
    if (spec.endsWith(".ts")) continue;
    out = out.split(spec).join(spec + ".ts");
  }
  return out;
}

// The Windmill path a `.ts` specifier names (WR-28): a relative specifier
// against the importer's path taken as a file, an absolute one without its
// leading `/`; dot segments normalised (never above the root) and the `.ts`
// removed. Undefined for a specifier the resolver does not intercept.
export function workspacePath(spec: string, importer: string): string | undefined {
  if (!spec.endsWith(".ts")) return undefined;
  let joined: string;
  if (spec.startsWith("/")) joined = spec.slice(1);
  else if (spec.startsWith(".")) joined = importer.split("/").slice(0, -1).concat(spec).join("/");
  else return undefined;
  const parts: string[] = [];
  for (const seg of joined.split("/")) {
    if (seg === "" || seg === ".") continue;
    if (seg === "..") parts.pop();
    else parts.push(seg);
  }
  const path = parts.join("/");
  return path.slice(0, -".ts".length);
}

const BUILTINS = new Set(builtinModules);

// The npm package a bare specifier names, or undefined for a relative,
// absolute, URL or built-in specifier.
export function packageName(spec: string): string | undefined {
  if (spec.startsWith(".") || spec.startsWith("/") || /^[a-z][a-z0-9+.-]*:/i.test(spec)) return undefined;
  if (spec === "bun" || BUILTINS.has(spec) || BUILTINS.has(spec.split("/")[0])) return undefined;
  const parts = spec.split("/");
  const name = spec.startsWith("@") ? parts.slice(0, 2).join("/") : parts[0];
  return unpinned(name) ?? name;
}
