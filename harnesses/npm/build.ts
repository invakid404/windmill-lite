// Builds the npm package windmill-lite into dist/. npm runs it as the
// package's prepack script, so `npm pack` and `npm publish` build first.
//
// - dist/bun/host/index.js: the Bun host SDK and the harness runtime it
//   imports, bundled into one ES module by Bun's bundler, as the host-mode
//   tests bundle them with `bun build --compile`. The build stamps the
//   package's version into it: WL_RELEASE is "v" + the version in
//   package.json, so the SDK's harness version is the version of the
//   engine it pairs with (runtime/version.ts, scripts/release.py).
// - dist/bun/host/index.d.ts and dist/bun/runtime/*.d.ts: their type
//   declarations, which TypeScript emits (npm/tsconfig.json). They keep
//   the sources' relative imports, which end in .ts; TypeScript resolves
//   each to its .d.ts file.
//
// It runs on Bun 1.3.10, the harness's pinned Bun, and refuses any other:
// Bun's bundler transpiles the runtime, so another Bun could ship other
// code. It needs the package's devDependencies (bun install
// --frozen-lockfile in harnesses/).

import { existsSync, readdirSync, readFileSync, rmSync } from "node:fs";
import { join, relative } from "node:path";

const PINNED_BUN = "1.3.10";

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
const built = await Bun.build({
  entrypoints: [join(root, "bun", "host", "index.ts")],
  outdir: join(dist, "bun", "host"),
  target: "bun",
  format: "esm",
  define: { WL_RELEASE: JSON.stringify(release) },
});
if (!built.success) {
  for (const log of built.logs) console.error(log);
  fail("bun build failed");
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

const files = readdirSync(dist, { recursive: true, withFileTypes: true })
  .filter((e) => e.isFile())
  .map((e) => relative(root, join(e.parentPath, e.name)))
  .sort();
console.log(`npm/build.ts: built windmill-lite ${version} (harness version ${release}), ${files.length} files in dist/:\n  ${files.join("\n  ")}`);
