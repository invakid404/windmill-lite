// Entry point of the Bun build helper (ADR 0010):
//
//   bun harnesses/bun/build/main.ts <request.json> <response.json>
//
// The Go build runs it twice per bundle. `scan` reads the exports of each
// host-provided module so that Go can generate its shim; `build` bundles
// the host-provided copies and every Bun script, writing the modules into
// the bundle directory and returning what the parse recorded. `parse`
// returns only what each entry's source records, for codebase scripts,
// which esbuild bundles in Go (C2b), and checks the source of each
// host-provided module outside the folder (F3h). A request
// that cannot be served at all fails with exit code 2 and an `error` in the
// response; a problem with one script or module is reported against it.

import { writeFileSync } from "node:fs";
import { Builder, BuildProblem } from "./build.ts";
import type { Request, Response } from "./protocol.ts";

const [requestPath, responsePath] = process.argv.slice(2);
if (requestPath === undefined || responsePath === undefined) {
  process.stderr.write("usage: main.ts <request.json> <response.json>\n");
  process.exit(2);
}

const response: Response = { version: 1, bun_version: Bun.version, host_modules: {}, entries: {} };

function finish(code: number): never {
  writeFileSync(responsePath, JSON.stringify(response));
  process.exit(code);
}

let req: Request;
try {
  req = await Bun.file(requestPath).json();
} catch (e) {
  response.error = { kind: "invalid_request", message: `cannot read the request: ${(e as Error).message}` };
  finish(2);
}
if (req.version !== 1 || (req.op !== "scan" && req.op !== "build" && req.op !== "parse" && req.op !== "native")) {
  response.error = { kind: "invalid_request", message: `unsupported request version ${req.version} or op ${req.op}` };
  finish(2);
}
// The build runs on the pinned Bun only, unless the caller allows another
// (ADR 0010 step 2).
if (req.require_bun !== undefined && req.require_bun !== "" && Bun.version !== req.require_bun) {
  response.error = {
    kind: "runtime_mismatch",
    message: `the build needs Bun ${req.require_bun}, this is Bun ${Bun.version}`,
  };
  finish(2);
}

let builder: Builder;
try {
  builder = new Builder(req);
} catch (e) {
  if (!(e instanceof BuildProblem)) throw e;
  response.error = e.problem();
  finish(2);
}

const problemOf = (e: unknown) => {
  if (e instanceof BuildProblem) return e.problem();
  return { kind: "internal", message: String((e as Error)?.stack ?? e) };
};

for (const hm of req.op === "native" ? [] : req.host_modules) {
  try {
    if (req.op === "parse") {
      // Only a module outside the folder is parsed (milestone F3h).
      if (hm.source !== undefined) response.host_modules[hm.id] = builder.parseHostSource(hm);
      continue;
    }
    response.host_modules[hm.id] =
      req.op === "scan" ? await builder.scanHostModule(hm) : await builder.buildHostModule(hm);
  } catch (e) {
    response.host_modules[hm.id] = { error: problemOf(e) };
  }
}
if (req.op !== "scan") {
  for (const e of req.entries) {
    try {
      response.entries[e.id] =
        req.op === "build" ? await builder.buildEntry(e) : req.op === "native" ? builder.nativeEntry(e) : builder.parseEntry(e);
    } catch (err) {
      response.entries[e.id] = { meta: { path: e.path }, error: problemOf(err) };
    }
  }
}
finish(0);
