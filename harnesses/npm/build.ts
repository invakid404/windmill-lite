// Builds the npm package windmill-lite into dist/. npm runs it as the
// package's prepack script, so `npm pack` and `npm publish` build first.
//
// - dist/bun/host/index.js and dist/bun/host/testing.js: the package's two
//   entries, windmill-lite/bun (the Bun host SDK and the harness runtime it
//   imports) and windmill-lite/bun/testing (the SDK's test entry), bundled
//   by Bun's bundler as the host-mode tests bundle the SDK with `bun build
//   --compile`. The build stamps the package's version into the SDK:
//   WL_RELEASE is "v" + the version in package.json, so the SDK's harness
//   version is the version of the engine it pairs with
//   (runtime/version.ts, scripts/release.py).
// - dist/bun/runtime/context.js: the one runtime module that both entries
//   import, which holds the invocation context, the timer fence and the
//   process scope. The bundler's code splitting puts it in a module of its
//   own, so a process that loads both entries has one copy of that state
//   (ADR 0009, "The test entry"). The build checks that it is the only
//   shared module.
// - dist/bun/host/index.d.ts, dist/bun/host/testing.d.ts and
//   dist/bun/runtime/*.d.ts: their type declarations, which TypeScript
//   emits (npm/tsconfig.json), keeping only those that the two entries'
//   declarations reach. They keep the sources' relative imports, which end
//   in .ts; TypeScript resolves each to its .d.ts file.
//
// It runs on Bun 1.4.3, the harness's pinned Bun, and refuses any other:
// Bun's bundler transpiles the runtime, so another Bun could ship other
// code. It needs the package's devDependencies (bun install
// --frozen-lockfile in harnesses/).

import { existsSync, readdirSync, readFileSync, rmSync } from "node:fs";
import { dirname, join, relative } from "node:path";

const PINNED_BUN = "1.4.3";

const root = join(import.meta.dir, "..");
const dist = join(root, "dist");

function fail(message: string): never {
  console.error(`npm/build.ts: ${message}`);
  process.exit(1);
}

if (Bun.version !== PINNED_BUN) {
  fail(`runs on Bun ${PINNED_BUN}, not ${Bun.version}; point WL_BUN at a Bun ${PINNED_BUN} executable`);
}
const tsc = join(root, "node_modules", "typescript", "lib", "tsc.js");
if (!existsSync(tsc)) {
  fail("no TypeScript; run bun install --frozen-lockfile in harnesses/ first");
}

// The form scripts/release.py checks: MAJOR.MINOR.PATCH with an optional
// pre-release, whose numeric identifiers have no leading zeros.
const PRE = "(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)";
const VERSION_FORM = new RegExp(`^(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)(-${PRE}(\\.${PRE})*)?$`);
const { version } = JSON.parse(readFileSync(join(root, "package.json"), "utf8")) as { version?: unknown };
if (typeof version !== "string" || !VERSION_FORM.test(version)) {
  fail(`package.json's version ${JSON.stringify(version)} is not MAJOR.MINOR.PATCH with an optional pre-release`);
}
const release = `v${version}`;

rmSync(dist, { recursive: true, force: true });

const js = join(dist, "bun", "host", "index.js");
const testingJs = join(dist, "bun", "host", "testing.js");
const sharedJs = join(dist, "bun", "runtime", "context.js");
const built = await Bun.build({
  entrypoints: [join(root, "bun", "host", "index.ts"), join(root, "bun", "host", "testing.ts")],
  root: join(root, "bun"),
  outdir: join(dist, "bun"),
  target: "bun",
  format: "esm",
  splitting: true,
  naming: { entry: "[dir]/[name].[ext]", chunk: "runtime/context.[ext]" },
  define: { WL_RELEASE: JSON.stringify(release) },
});
if (!built.success) {
  for (const log of built.logs) console.error(log);
  fail("bun build failed");
}
// One runtime state: the two entries share exactly one module, the code of
// runtime/context.ts, and import it rather than holding copies of it. A
// change that made them share more would put more into that module, which
// must stay loadable without Bun, as the test entry is on Node.
const outputs = built.outputs.map((o) => `${o.kind} ${relative(dist, o.path)}`).sort();
const wantOutputs = ["chunk bun/runtime/context.js", "entry-point bun/host/index.js", "entry-point bun/host/testing.js"];
if (JSON.stringify(outputs) !== JSON.stringify(wantOutputs)) {
  fail(`bun build wrote ${JSON.stringify(outputs)}, want ${JSON.stringify(wantOutputs)}`);
}
// Bun's bundler marks where each source module starts with a comment of its
// path, such as "// bun/runtime/context.ts".
const modules = (file: string) => [...readFileSync(file, "utf8").matchAll(/^\/\/ (?:.*\/)?(bun\/[\w/]+\.ts)$/gm)].map((m) => m[1]);
if (JSON.stringify(modules(sharedJs)) !== JSON.stringify(["bun/runtime/context.ts"])) {
  fail(`${relative(root, sharedJs)} holds ${JSON.stringify(modules(sharedJs))}, want only bun/runtime/context.ts`);
}
for (const entry of [js, testingJs]) {
  if (modules(entry).includes("bun/runtime/context.ts") || !readFileSync(entry, "utf8").includes(`from "../runtime/context.js"`)) {
    fail(`${relative(root, entry)} does not import the shared ../runtime/context.js`);
  }
}
// The define replaced WL_RELEASE, so the harness version the hello names
// is the release, never the repository's "dev", which pairs with any
// engine.
if (!readFileSync(js, "utf8").includes(`HARNESS_VERSION = ${JSON.stringify(release)}`)) {
  fail(`${js} does not set HARNESS_VERSION to ${JSON.stringify(release)}`);
}

const declarations = Bun.spawnSync([process.execPath, tsc, "-p", join(root, "npm", "tsconfig.json")], {
  stdout: "inherit",
  stderr: "inherit",
});
if (declarations.exitCode !== 0) {
  fail(`tsc exited with ${declarations.exitCode}`);
}

// tsc emits a declaration file for every source file that the entries
// import, but the package ships only those that the declarations of ./bun
// and ./bun/testing reach: the bundled JavaScript needs none of them, and a
// runtime file whose types no entry exports is no part of what a host
// imports.
const SPECIFIER = /(?:from\s+|import\()\s*"(\.{1,2}\/[^"]+)"/g;
const reached = new Set<string>();
const pending = [join(dist, "bun", "host", "index.d.ts"), join(dist, "bun", "host", "testing.d.ts")];
for (let file = pending.pop(); file !== undefined; file = pending.pop()) {
  if (reached.has(file)) continue;
  if (!existsSync(file)) fail(`${relative(root, file)} is imported but tsc did not emit it`);
  reached.add(file);
  for (const [, spec] of readFileSync(file, "utf8").matchAll(SPECIFIER)) {
    pending.push(join(dirname(file), spec!.replace(/\.ts$/, ".d.ts")));
  }
}
for (const e of readdirSync(dist, { recursive: true, withFileTypes: true })) {
  const file = join(e.parentPath, e.name);
  if (e.isFile() && file.endsWith(".d.ts") && !reached.has(file)) rmSync(file);
}

const files = readdirSync(dist, { recursive: true, withFileTypes: true })
  .filter((e) => e.isFile())
  .map((e) => relative(root, join(e.parentPath, e.name)))
  .sort();
console.log(`npm/build.ts: built windmill-lite ${version} (harness version ${release}), ${files.length} files in dist/:\n  ${files.join("\n  ")}`);
