// Bundling with Windmill's settings (WR-27): Bun.build with the target the
// annotations select, ESM output, syntax-only minification and only
// `electron` external, after the import transforms of WR-26 and WR-28, with
// workspace imports resolved from the sync folder (WR-28, WR-29) and the
// env rewrite and host-provided-module shims of ADR 0010.
//
// Sources are materialised under a dependency set's directory, so bare
// imports resolve from its node_modules and the paths a bundle names are
// relative to it: entries at `entry/<id>.ts` (an id may have `/`-separated
// segments), workspace scripts at
// `src/<Windmill path>.ts`, shims at `host/<id>.mjs`, a host-provided copy's
// entry at `host/<id>.entry.mjs`, the env support module at
// `wl/env-<binding>.mjs`.

import type { BunPlugin } from "bun";
import { mkdirSync, readFileSync, realpathSync, writeFileSync } from "node:fs";
import { dirname, isAbsolute, join, relative, sep } from "node:path";
import { parseAnnotations, targetFor } from "./annotations.ts";
import { packageName, removePinnedImports, rewriteRelativeImports, workspacePath } from "./imports.ts";
import type { EntryResult, HostModuleRequest, HostModuleResult, Problem, Request } from "./protocol.ts";
import { importSpecifiers, liveBindings, parseModule, signatures } from "./signature.ts";
import { detectWorkflowAsCode } from "./wac.ts";

export class BuildProblem extends Error {
  constructor(
    readonly kind: string,
    message: string,
  ) {
    super(message);
  }
  problem(): Problem {
    return { kind: this.kind, message: this.message };
  }
}

// An id names files: one or more `/`-separated segments, none empty, `.`
// or `..`, and none holding a backslash or NUL (the Go build's rule).
function validId(id: string): boolean {
  return id.split("/").every((s) => s !== "" && s !== "." && s !== ".." && !s.includes("\\") && !s.includes("\0"));
}
const SAFE_KEY = /^[A-Za-z0-9_-]+$/;

function own(m: Record<string, string>, key: string): string | undefined {
  return Object.hasOwn(m, key) ? m[key] : undefined;
}

function inside(rel: string): boolean {
  return rel !== "" && !isAbsolute(rel) && !rel.split(/[\\/]/).includes("..");
}

const exportScanner = new Bun.Transpiler({ loader: "js" });

type Bundled = {
  text: string;
  metafile: Record<string, unknown>;
  closure: string[];
  hostModules: string[];
};

// The specifier a Bun-target entry imports the env support module by,
// before its own imports, so that the binding exists when any of its
// modules runs (ADR 0010 step 5).
const ENV_SPECIFIER = "windmill-lite:env";

// The text of an entry that imports the env support module first, after a
// hashbang line if it has one.
function withEnvImport(text: string): string {
  const line = `import ${JSON.stringify(ENV_SPECIFIER)};\n`;
  if (!text.startsWith("#!")) return line + text;
  const nl = text.indexOf("\n");
  return nl < 0 ? text + "\n" + line : text.slice(0, nl + 1) + line + text.slice(nl + 1);
}

// How imports of host-provided modules resolve in one bundle: to the shim
// (build), or left external so the scan sees only the module's own exports.
type HostMapping = { shims: boolean; self?: string };

export class Builder {
  private depDirs = new Map<string, string>();
  private transformed = new Map<string, string>();
  private hostByPackage = new Map<string, HostModuleRequest>();
  private hostByPath = new Map<string, HostModuleRequest>();

  constructor(private readonly req: Request) {
    for (const hm of req.host_modules) {
      if (!validId(hm.id)) throw new BuildProblem("invalid_request", `host module id ${JSON.stringify(hm.id)}`);
      if (hm.package !== undefined) this.hostByPackage.set(hm.package, hm);
      else if (hm.path !== undefined && hm.file !== undefined) this.hostByPath.set(hm.path, hm);
      else throw new BuildProblem("invalid_request", `host module ${hm.id} names neither a package nor a file`);
    }
    for (const e of req.entries) {
      if (!validId(e.id)) throw new BuildProblem("invalid_request", `entry id ${JSON.stringify(e.id)}`);
    }
    for (const k of Object.keys(req.deps)) {
      if (!SAFE_KEY.test(k)) throw new BuildProblem("invalid_request", `dependency set ${JSON.stringify(k)}`);
    }
  }

  private readFolder(file: string): string {
    if (!inside(file)) throw new BuildProblem("invalid_request", `folder file ${JSON.stringify(file)} leaves the folder`);
    try {
      return readFileSync(join(this.req.folder, file), "utf8");
    } catch (e) {
      throw new BuildProblem("invalid_request", `cannot read ${file}: ${(e as Error).message}`);
    }
  }

  // The source of a workspace script after the import transforms.
  private workspaceSource(path: string): string {
    let text = this.transformed.get(path);
    if (text === undefined) {
      const file = own(this.req.scripts, path);
      if (file === undefined) throw new BuildProblem("bundle_failed", `no script at ${path}`);
      text = transformSource(this.readFolder(file), path);
      this.transformed.set(path, text);
    }
    return text;
  }

  // The directory of a dependency set, installed from its lock (WR-25) the
  // first time a build needs it. Without a set, an empty directory.
  depDir(key: string | undefined): string {
    const name = key ?? "_none";
    const cached = this.depDirs.get(name);
    if (cached !== undefined) return cached;
    // The real path, since the bundler names importers by theirs.
    const made = join(this.req.work, "deps", name);
    mkdirSync(made, { recursive: true });
    const dir = realpathSync(made);
    if (key !== undefined) {
      const lock = own(this.req.deps, key);
      if (lock === undefined) throw new BuildProblem("invalid_request", `unknown dependency set ${key}`);
      install(dir, lock);
    }
    this.depDirs.set(name, dir);
    return dir;
  }

  private materialise(dir: string, rel: string, text: string): string {
    const abs = join(dir, rel);
    mkdirSync(dirname(abs), { recursive: true });
    writeFileSync(abs, text);
    return abs;
  }

  private shimCopy(dir: string, hm: HostModuleRequest): string {
    if (hm.shim === undefined) throw new BuildProblem("invalid_request", `host module ${hm.id} has no shim`);
    return this.materialise(dir, join("host", hm.id + ".mjs"), readFileSync(hm.shim, "utf8"));
  }

  // A bundle of one entry. seeds names the workspace modules among its
  // files (absolute path to Windmill path), whose imports resolve from the
  // folder. With env, the entry imports the env support module, and the
  // env rewrite applies (Bun-target bundles only): its binding is the Go
  // build's name, or the name with a suffix when an input of the bundle
  // spells it, so that no binding of a script can capture the rewrite.
  async bundle(
    dir: string,
    entry: string,
    seeds: Map<string, string>,
    target: "bun" | "browser" | "node",
    hosts: HostMapping,
    env: boolean,
  ): Promise<Bundled> {
    if (!env) return (await this.bundleOnce(dir, entry, seeds, target, hosts, undefined)).bundled;
    const base = this.req.env_binding;
    for (let n = 1; n <= 20; n++) {
      const name = n === 1 ? base : `${base}_${n}`;
      const { bundled, inputs } = await this.bundleOnce(dir, entry, seeds, target, hosts, name);
      if (!inputs.some((text) => spells(text, name))) return bundled;
    }
    throw new BuildProblem("bundle_failed", `every candidate name for ${base} occurs in the bundle's sources`);
  }

  private async bundleOnce(
    dir: string,
    entry: string,
    seeds: Map<string, string>,
    target: "bun" | "browser" | "node",
    hosts: HostMapping,
    binding: string | undefined,
  ): Promise<{ bundled: Bundled; inputs: string[] }> {
    const modules = new Map(seeds);
    const base = this.req.env_binding;
    const envFile =
      binding === undefined
        ? undefined
        : this.materialise(dir, join("wl", `env-${binding}.mjs`), this.req.env_support.split(base).join(binding));
    const define =
      binding === undefined
        ? undefined
        : Object.fromEntries(Object.entries(this.req.define).map(([k, v]) => [k, v.split(base).join(binding)]));
    const closure = new Set<string>();
    const used = new Set<string>();
    const host = (hm: HostModuleRequest) => {
      used.add(hm.key);
      return hosts.shims ? { path: this.shimCopy(dir, hm) } : { path: hm.key, external: true };
    };
    const plugin: BunPlugin = {
      name: "windmill-lite",
      setup: (b) => {
        b.onResolve({ filter: /.*/ }, (args) => {
          if (args.kind === "entry-point-build" || args.kind === "entry-point-run") return undefined;
          const spec = args.path;
          if (spec === ENV_SPECIFIER) {
            if (envFile === undefined) throw new Error(`Could not resolve: "${spec}"`);
            return { path: envFile };
          }
          const pkg = this.hostByPackage.get(spec);
          if (pkg !== undefined && pkg.key !== hosts.self) return host(pkg);
          const importer = modules.get(args.importer);
          if (importer === undefined) return undefined;
          if (!spec.startsWith(".") && !spec.startsWith("/")) return undefined;
          // Windmill intercepts only `.ts` specifiers (WR-28); Bun resolves
          // anything else against the job directory, where nothing but the
          // entry exists.
          const path = workspacePath(spec, importer);
          if (path === undefined || path.split("/").pop()!.startsWith("_wm_")) {
            throw new Error(`Could not resolve: "${spec}"`);
          }
          const hm = this.hostByPath.get(path);
          if (hm !== undefined && hm.key !== hosts.self) return host(hm);
          if (own(this.req.scripts, path) === undefined) {
            throw new Error(`Could not resolve: "${spec}" from ${importer}: no script at ${path}`);
          }
          const abs = this.materialise(dir, join("src", path + ".ts"), this.workspaceSource(path));
          modules.set(abs, path);
          closure.add(path);
          return { path: abs };
        });
      },
    };
    const cwd = process.cwd();
    process.chdir(dir);
    let result: Awaited<ReturnType<typeof Bun.build>>;
    try {
      result = await Bun.build({
        entrypoints: [entry],
        target,
        format: "esm",
        minify: { syntax: true, identifiers: false, whitespace: false },
        external: ["electron"],
        ...(define === undefined ? {} : { define }),
        metafile: true,
        plugins: [plugin],
      });
    } catch (e) {
      throw new BuildProblem("bundle_failed", buildMessage(e));
    } finally {
      process.chdir(cwd);
    }
    if (!result.success) throw new BuildProblem("bundle_failed", result.logs.map(String).join("\n"));
    if (result.outputs.length !== 1) {
      throw new BuildProblem(
        "bundle_failed",
        `the bundle has ${result.outputs.length} output files; only a single module is supported`,
      );
    }
    const metafile = normaliseMetafile(result.metafile, dir);
    // Everything a bundle inlines comes from the folder or the lock: an
    // input outside the dependency set's directory was found in a
    // node_modules above it, which no lock installed.
    const outside = Object.keys((metafile.inputs ?? {}) as object).filter((p) => p.startsWith("..") || isAbsolute(p));
    if (outside.length > 0) {
      throw new BuildProblem(
        "bundle_failed",
        `the bundle resolved files outside its dependency set: ${outside.join(", ")}`,
      );
    }
    const inputs: string[] = [];
    if (binding !== undefined) {
      for (const key of Object.keys((metafile.inputs ?? {}) as object)) {
        const abs = join(dir, key);
        if (abs !== envFile) inputs.push(readFileSync(abs, "utf8"));
      }
    }
    return {
      bundled: {
        text: await result.outputs[0].text(),
        metafile,
        closure: [...closure].sort(),
        hostModules: [...used].sort(),
      },
      inputs,
    };
  }

  // The file a script's import of a package resolves to: the bundler's
  // resolution (its conditions for target bun), read from the metafile of
  // a bundle that imports it.
  private async resolvePackage(dir: string, hm: HostModuleRequest): Promise<string> {
    const probe = this.materialise(dir, join("host", hm.id + ".resolve.mjs"), `import * as m from ${JSON.stringify(hm.package)};\nexport { m };\n`);
    const cwd = process.cwd();
    process.chdir(dir);
    try {
      const r = await Bun.build({ entrypoints: [probe], target: "bun", format: "esm", metafile: true });
      const inputs = (r.metafile?.inputs ?? {}) as Record<string, { imports?: { path: string; original?: string }[] }>;
      for (const input of Object.values(inputs)) {
        for (const imp of input.imports ?? []) {
          if (imp.original === hm.package) return isAbsolute(imp.path) ? imp.path : join(dir, imp.path);
        }
      }
    } catch (e) {
      throw new BuildProblem("bundle_failed", `cannot resolve host-provided module ${hm.package}: ${buildMessage(e)}`);
    } finally {
      process.chdir(cwd);
    }
    throw new BuildProblem("bundle_failed", `cannot resolve host-provided module ${hm.package}`);
  }

  // A host-provided module: its entry file, its source and the import base
  // of a folder module.
  private async hostEntry(
    hm: HostModuleRequest,
  ): Promise<{ dir: string; entry: string; source: string; seeds: Map<string, string> }> {
    if (hm.package !== undefined) {
      const dir = this.depDir(hm.deps);
      const entry = await this.resolvePackage(dir, hm);
      return { dir, entry, source: readFileSync(entry, "utf8"), seeds: new Map() };
    }
    const dir = this.depDir(hm.deps);
    const source = this.readFolder(hm.file!);
    const entry = this.materialise(dir, join("src", hm.path! + ".ts"), this.workspaceSource(hm.path!));
    return { dir, entry, source, seeds: new Map([[entry, hm.path!]]) };
  }

  // Scan: the exports of each host-provided module, from its bundled copy
  // with other host-provided modules left external, after the live-binding
  // check (ADR 0010).
  async scanHostModule(hm: HostModuleRequest): Promise<HostModuleResult> {
    const { dir, entry, source, seeds } = await this.hostEntry(hm);
    const parsed = parse(source);
    const live = liveBindings(parsed);
    if (live.length > 0) {
      throw new BuildProblem(
        "host_module_live_binding",
        `host-provided module ${hm.key} exports a binding it can reassign (export let or export var): ${live.join(", ")}`,
      );
    }
    const b = await this.bundle(dir, entry, seeds, "bun", { shims: false, self: hm.key }, false);
    // A shim takes exports by name from the scan. A CommonJS module's names
    // are its exports object's properties, known only when it runs, and
    // Bun lists them differently as an entry and as a dependency, so it
    // cannot be shared through a shim (ADR 0001 §5a).
    const inputs = (b.metafile.inputs ?? {}) as Record<string, { format?: string }>;
    if (inputs[relative(dir, entry)]?.format === "cjs") {
      throw new BuildProblem(
        "host_module_commonjs",
        `host-provided module ${hm.key} is CommonJS; a host-provided module must be an ES module`,
      );
    }
    return {
      exports: exportScanner.scan(b.text).exports,
      host_modules: b.hostModules,
      packages: directPackages(parsed),
    };
  }

  // Build: the bundled copy that a harness registers when no host does,
  // with other host-provided modules resolved to their shims.
  async buildHostModule(hm: HostModuleRequest): Promise<HostModuleResult> {
    const { dir, entry, source, seeds } = await this.hostEntry(hm);
    // The copy's entry re-exports the scanned names and imports the env
    // support module first. An id with segments puts it in a subdirectory.
    const rel = join("host", hm.id + ".entry.mjs");
    const target = JSON.stringify("./" + relative(dirname(join(dir, rel)), entry));
    const wrapper = this.materialise(
      dir,
      rel,
      withEnvImport(
        `export * from ${target};\n` +
          ((hm.exports ?? []).includes("default") ? `export { default } from ${target};\n` : ""),
      ),
    );
    const b = await this.bundle(dir, wrapper, seeds, "bun", { shims: true, self: hm.key }, true);
    const exports = exportScanner.scan(b.text).exports;
    this.writeOut(join("scripts", "bun", "provided", hm.id + ".mjs"), b.text);
    return { exports, host_modules: b.hostModules, packages: directPackages(parse(source)), imports: b.closure };
  }

  async buildEntry(e: EntryRequest): Promise<EntryResult> {
    const source = e.file !== undefined ? this.readFolder(e.file) : (e.content ?? "");
    const meta: Record<string, unknown> = { path: e.path };
    let parsed;
    try {
      parsed = parse(source);
    } catch (err) {
      return { meta, error: (err as BuildProblem).problem() };
    }
    const annotations = parseAnnotations(source);
    const target = targetFor(annotations);
    Object.assign(meta, {
      target,
      annotations,
      workflow_as_code: detectWorkflowAsCode(source, parsed),
      packages: directPackages(parsed),
    });
    try {
      if (annotations.npm) throw new BuildProblem("unsupported_feature", "unsupported: npm");
      const dir = this.depDir(e.deps);
      // The env rewrite is for the shared Bun process (WA-31); native and
      // Node-target scripts do not run there with Bun's globals, and their
      // environment is the native boundary's (C3n).
      const env = target === "bun";
      const text = transformSource(source, e.path);
      const entry = this.materialise(dir, join("entry", e.id + ".ts"), env ? withEnvImport(text) : text);
      const b = await this.bundle(dir, entry, new Map([[entry, e.path]]), target, { shims: true }, env);
      const exports = exportScanner.scan(b.text).exports;
      this.writeOut(join("scripts", "bun", e.id + ".mjs"), b.text);
      Object.assign(meta, {
        exports,
        signatures: signatures(parsed, exports),
        imports: b.closure,
        host_modules: b.hostModules,
        metafile: b.metafile,
      });
      return { meta };
    } catch (err) {
      if (err instanceof BuildProblem) return { meta, error: err.problem() };
      throw err;
    }
  }

  private writeOut(rel: string, text: string): void {
    const abs = join(this.req.out, rel);
    mkdirSync(dirname(abs), { recursive: true });
    writeFileSync(abs, text);
  }
}

type EntryRequest = Request["entries"][number];

function parse(source: string) {
  try {
    return parseModule(source);
  } catch (e) {
    throw new BuildProblem("parse_failed", (e as Error).message);
  }
}

// The import transforms of a workspace script or entry: pinned-import
// removal (WR-26), then the `.ts` rewrite (WR-28).
function transformSource(source: string, path: string): string {
  let parsed;
  try {
    parsed = parseModule(source);
  } catch (e) {
    throw new BuildProblem("parse_failed", `${path}: ${(e as Error).message}`);
  }
  try {
    return rewriteRelativeImports(removePinnedImports(source, parsed));
  } catch (e) {
    throw new BuildProblem("bundle_failed", `${path}: ${buildMessage(e)}`);
  }
}

// The npm packages a module imports by name, for the shared-dependency
// warning of ADR 0010.
function directPackages(p: ReturnType<typeof parseModule>): string[] {
  const names = new Set<string>();
  for (const spec of importSpecifiers(p, { values: true })) {
    const name = packageName(spec);
    if (name !== undefined) names.add(name);
  }
  return [...names].sort();
}

function buildMessage(e: unknown): string {
  if (e instanceof AggregateError) return e.errors.map((x) => String(x?.message ?? x)).join("\n");
  return String((e as Error)?.message ?? e);
}

// The separators of a Bun lock (WR-25).
const TEXT_LOCK = /\r?\n\/\/bun\.lock\r?\n/;
const BINARY_LOCK = /\r?\n\/\/bun\.lockb\r?\n/;

function install(dir: string, lock: string): void {
  const binary = BINARY_LOCK.exec(lock);
  const text = binary === null ? TEXT_LOCK.exec(lock) : null;
  const sep = binary ?? text;
  if (sep === null) {
    throw new BuildProblem(
      "invalid_lock",
      `Invalid requirements, expected to find //bun.lock split pattern in reqs. Found: |${lock}|`,
    );
  }
  const packageJson = lock.slice(0, sep.index);
  const rest = lock.slice(sep.index + sep[0].length);
  writeFileSync(join(dir, "package.json"), packageJson);
  if (rest === "<empty>") return;
  if (binary !== null) writeFileSync(join(dir, "bun.lockb"), Buffer.from(rest.trim(), "base64"));
  else writeFileSync(join(dir, "bun.lock"), rest);
  const proc = Bun.spawnSync([process.execPath, "install", "--frozen-lockfile"], {
    cwd: dir,
    stdin: "ignore",
    stdout: "pipe",
    stderr: "pipe",
  });
  if (proc.exitCode !== 0) {
    const out = (proc.stdout.toString() + proc.stderr.toString()).trim();
    throw new BuildProblem("install_failed", `bun install exited with ${proc.exitCode}: ${out.slice(-2000)}`);
  }
}

// The bundler's metafile with every path relative to the dependency set's
// directory, so that a bundle does not depend on where it was built.
function normaliseMetafile(m: unknown, dir: string): Record<string, unknown> {
  const roots = [...new Set([dir, realpathSync(dir)])].map((r) => r + sep);
  const rel = (p: unknown) => {
    if (typeof p !== "string") return p;
    for (const r of roots) if (p.startsWith(r)) return p.slice(r.length);
    return p;
  };
  const walk = (v: unknown, key?: string): unknown => {
    if (Array.isArray(v)) return v.map((x) => walk(x));
    if (v !== null && typeof v === "object") {
      const o: Record<string, unknown> = {};
      for (const [k, x] of Object.entries(v)) o[String(rel(k))] = walk(x, k);
      return o;
    }
    return key === "path" || key === "entryPoint" ? rel(v) : v;
  };
  return walk(m ?? {}) as Record<string, unknown>;
}

// Whether text spells the identifier name, as written or through Unicode
// escapes (`\u0075`, `\u{75}`), which an identifier may use for any of its
// characters (c2a review 2, finding 6). Escapes are decoded everywhere, in
// strings and comments too: a false match only moves the build to another
// name.
export function spells(text: string, name: string): boolean {
  if (text.includes(name)) return true;
  if (!text.includes("\\u")) return false;
  const decoded = text.replace(/\\u\{([0-9a-fA-F]+)\}|\\u([0-9a-fA-F]{4})/g, (m, braced: string | undefined, four: string | undefined) => {
    const cp = parseInt(braced ?? four!, 16);
    return cp <= 0x10ffff ? String.fromCodePoint(cp) : m;
  });
  return decoded.includes(name);
}
