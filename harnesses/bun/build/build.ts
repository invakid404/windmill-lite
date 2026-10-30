// Bundling with Windmill's settings (WR-27): Bun.build with the target the
// annotations select, ESM output, syntax-only minification and only
// `electron` external, after the import transforms of WR-26 and WR-28, with
// workspace imports resolved from the sync folder (WR-28, WR-29) and the
// env rewrite and host-provided-module shims of ADR 0010. A native step
// (`nativets`, or `//native`) is built into its native form instead
// (native.ts, deviation D5).
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
import { builtinModules } from "node:module";
import { dirname, isAbsolute, join, relative, sep } from "node:path";
import { parseAnnotations, targetFor } from "./annotations.ts";
import { packageName, removePinnedImports, rewriteRelativeImports, workspacePath } from "./imports.ts";
import type { EntryResult, HostModuleRequest, HostModuleResult, Problem, Request } from "./protocol.ts";
import { buildNativeClient, nativeHeader, nativeModule, type NativeModule, transpile } from "./native.ts";
import { CJS_SPECIFIER, CLOSURE_META, CLOSURE_SPECIFIER, closureEntry, closureId, closureSupport, depReference, esmReference, keptImports } from "./shared.ts";
import { importSpecifiers, liveBindings, parseModule, signatures, type Syntax } from "./signature.ts";
import { detectWorkflowAsCode } from "./wac.ts";

export class BuildProblem extends Error {
  constructor(
    readonly kind: string,
    message: string,
    // The files, relative to the dependency set's directory, that the
    // bundler's messages name.
    readonly files: string[] = [],
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
const sourceScanner = new Bun.Transpiler({ loader: "ts" });

type Bundled = {
  text: string;
  metafile: Record<string, unknown>;
  closure: string[];
  hostModules: string[];
  // The env rewrite's binding the bundle uses, when it has the rewrite.
  binding?: string;
  // What the build's plugin resolved each of the entry's imports to, by
  // kind and specifier, relative to the dependency set's directory: the
  // metafile names a specifier the plugin resolved by itself, not by the
  // file it became.
  resolved: Map<string, string>;
};

// What a bundle of the shared layout changes (ADR 0013): the output format,
// defines on top of the env rewrite's, and a plugin ahead of the build's.
type BundleOptions = { format?: "esm" | "cjs"; define?: Record<string, string>; plugin?: BunPlugin };

// A metafile input's imports, as Bun records them.
type MetaImport = { path: string; kind: string; original?: string; external?: boolean };
type MetaInputs = Record<string, { imports?: MetaImport[]; format?: string }>;

// A built script that shared code covers: where its entry was
// materialised, its dependency set and target, the binding of its env
// rewrite, and its per-script bundle's metafile.
type SharedEntry = {
  id: string;
  path: string;
  dir: string;
  entry: string;
  deps?: string;
  target: "bun" | "node";
  binding?: string;
  metafile: Record<string, unknown>;
  resolved: Map<string, string>;
};

// A script's record under shared code: its group's closure, or the reason
// it keeps its per-script bundle.
type SharedRecord = { closure?: string; fallback?: string };

// The specifier a Bun-target entry imports the env support module by,
// before its own imports, so that the binding exists when any of its
// modules runs (ADR 0010 step 5).
const ENV_SPECIFIER = "windmill-lite:env";

// Which specifiers the plugin's onResolve callback sees: relative and
// absolute ones, the env support module and listed packages, the only ones
// it resolves. Bun 1.3.10 drops a module that a package marked
// sideEffects:false re-exports (`export { x } from "./x.js"`) when an
// onResolve filter matches the import of that package, even one whose
// callback resolves nothing, so the bundle references a binding it never
// defines (milestone F3h); every other package import is left to Bun
// without the callback.
function resolveFilter(packages: Iterable<string>): RegExp {
  const exact = [ENV_SPECIFIER, ...packages].map((s) => s.replace(/[.*+?^${}()|[\]\\/]/g, "\\$&") + "$");
  return new RegExp(`^(?:\\.|/|${exact.join("|")})`);
}

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
// A native bundle inlines them as any other import: it runs in a realm of
// its own, which the process-wide registry does not reach (D5).
type HostMapping = { shims: boolean; self?: string; inline?: boolean };

export class Builder {
  private depDirs = new Map<string, string>();
  private transformed = new Map<string, string>();
  private hostByPackage = new Map<string, HostModuleRequest>();
  private hostByPath = new Map<string, HostModuleRequest>();
  private client: Promise<NativeModule> | undefined;
  private sharedEntries: SharedEntry[] = [];

  constructor(private readonly req: Request) {
    for (const hm of req.host_modules) {
      if (!validId(hm.id)) throw new BuildProblem("invalid_request", `host module id ${JSON.stringify(hm.id)}`);
      if (hm.package !== undefined) this.hostByPackage.set(hm.package, hm);
      else if (hm.path !== undefined && hm.file !== undefined) this.hostByPath.set(hm.path, hm);
      else if (hm.source !== undefined && isAbsolute(hm.source) && req.op === "parse") continue;
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
    opts: BundleOptions = {},
  ): Promise<Bundled> {
    if (!env) return (await this.bundleOnce(dir, entry, seeds, target, hosts, undefined, opts)).bundled;
    const base = this.req.env_binding;
    for (let n = 1; n <= 20; n++) {
      const name = n === 1 ? base : `${base}_${n}`;
      const { bundled, inputs } = await this.bundleOnce(dir, entry, seeds, target, hosts, name, opts);
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
    opts: BundleOptions = {},
  ): Promise<{ bundled: Bundled; inputs: string[] }> {
    const modules = new Map(seeds);
    const base = this.req.env_binding;
    const envFile =
      binding === undefined
        ? undefined
        : this.materialise(dir, join("wl", `env-${binding}.mjs`), this.req.env_support.split(base).join(binding));
    const envDefine =
      binding === undefined
        ? undefined
        : Object.fromEntries(Object.entries(this.req.define).map(([k, v]) => [k, v.split(base).join(binding)]));
    const define = opts.define === undefined ? envDefine : { ...opts.define, ...envDefine };
    const closure = new Set<string>();
    const used = new Set<string>();
    const resolved = new Map<string, string>();
    // Records what the entry's import resolved to.
    const answer = <R extends { path: string; external?: boolean } | undefined>(args: { importer: string; kind: string; path: string }, r: R): R => {
      if (r !== undefined && !r.external && args.importer === entry) resolved.set(`${args.kind}\0${args.path}`, relative(dir, r.path));
      return r;
    };
    const host = (hm: HostModuleRequest) => {
      if (hosts.inline) return undefined;
      used.add(hm.key);
      return hosts.shims ? { path: this.shimCopy(dir, hm) } : { path: hm.key, external: true };
    };
    const plugin: BunPlugin = {
      name: "windmill-lite",
      setup: (b) => {
        b.onResolve({ filter: resolveFilter(this.hostByPackage.keys()) }, (args) => {
          if (args.kind === "entry-point-build" || args.kind === "entry-point-run") return undefined;
          const spec = args.path;
          if (spec === ENV_SPECIFIER) {
            if (envFile === undefined) throw new Error(`Could not resolve: "${spec}"`);
            return answer(args, { path: envFile });
          }
          const pkg = this.hostByPackage.get(spec);
          if (pkg !== undefined && pkg.key !== hosts.self) return answer(args, host(pkg));
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
          if (hm !== undefined && hm.key !== hosts.self && !hosts.inline) return answer(args, host(hm));
          if (own(this.req.scripts, path) === undefined) {
            throw new Error(`Could not resolve: "${spec}" from ${importer}: no script at ${path}`);
          }
          const abs = this.materialise(dir, join("src", path + ".ts"), this.workspaceSource(path));
          modules.set(abs, path);
          closure.add(path);
          return answer(args, { path: abs });
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
        format: opts.format ?? "esm",
        minify: { syntax: true, identifiers: false, whitespace: false },
        external: ["electron"],
        ...(define === undefined ? {} : { define }),
        metafile: true,
        plugins: opts.plugin === undefined ? [plugin] : [opts.plugin, plugin],
      });
    } catch (e) {
      throw new BuildProblem("bundle_failed", buildMessage(e));
    } finally {
      process.chdir(cwd);
    }
    if (!result.success) {
      const files = result.logs.flatMap((l) => {
        const f = (l as { position?: { file?: string } | null }).position?.file;
        return f ? [isAbsolute(f) ? relative(dir, f) : f] : [];
      });
      throw new BuildProblem("bundle_failed", result.logs.map(String).join("\n"), files);
    }
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
        binding,
        resolved,
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

  // Parse: what the source of a host-provided module outside the folder
  // must not do, which only its syntax shows (the Go build bundles it with
  // esbuild, as the codebase scripts that import it, milestone F3h): export
  // a binding it can reassign, which a shim cannot follow (ADR 0010). The
  // source is read with the syntax of the loader esbuild bundles it with
  // (TypeScript, TSX, JavaScript or JSX); a file of any other loader has
  // no exports a source could reassign.
  parseHostSource(hm: HostModuleRequest): HostModuleResult {
    let source: string;
    try {
      source = readFileSync(hm.source!, "utf8");
    } catch (e) {
      throw new BuildProblem("invalid_request", `cannot read ${hm.key}: ${(e as Error).message}`);
    }
    const syntax = hm.loader ?? "ts";
    if (syntax !== "ts" && syntax !== "tsx" && syntax !== "js" && syntax !== "jsx") return { packages: [] };
    const parsed = parse(source, syntax);
    const live = liveBindings(parsed);
    if (live.length > 0) {
      throw new BuildProblem(
        "host_module_live_binding",
        `host-provided module ${hm.key} exports a binding it can reassign (export let or export var): ${live.join(", ")}`,
      );
    }
    return { packages: directPackages(parsed) };
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
    if (e.language === "nativets") return this.buildNativeTS(e, source, meta);
    let parsed;
    try {
      parsed = parse(source);
    } catch (err) {
      return { meta, error: (err as BuildProblem).problem() };
    }
    const annotations = parseAnnotations(source);
    const target = targetFor(annotations);
    // The header, not the stored language, selects the native runtime for
    // a Bun-family script (WR-40).
    const native = annotations.native;
    Object.assign(meta, {
      target,
      boundary: native ? "native" : "bun",
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
      const b = await this.bundle(dir, entry, new Map([[entry, e.path]]), target, { shims: !native, inline: native }, env);
      if (native) {
        const nm = this.native(b.text, "bunnative");
        this.writeOut(join("scripts", "bun", e.id + ".native.js"), nm.text);
        Object.assign(meta, {
          exports: nm.exports,
          signatures: signatures(parsed, nm.exports),
          imports: b.closure,
          host_modules: b.hostModules,
          metafile: b.metafile,
          native: { ...nm.form, ...nativeHeader(source) },
        });
        return { meta };
      }
      const exports = exportScanner.scan(b.text).exports;
      this.writeOut(join("scripts", "bun", e.id + ".mjs"), b.text);
      if (this.req.shared_code && (target === "bun" || target === "node")) {
        this.sharedEntries.push({ id: e.id, path: e.path, dir, entry, deps: e.deps, target, binding: b.binding, metafile: b.metafile, resolved: b.resolved });
      }
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

  // A nativets script (WR-40): its TypeScript transpiled, not bundled, in
  // its native form. Its imports are resolved when it runs, so it inlines
  // nothing and installs nothing. Text the transpiler rejects is not a
  // build problem: the step fails when it runs, as on Windmill (WR-43).
  private async buildNativeTS(e: EntryRequest, source: string, meta: Record<string, unknown>): Promise<EntryResult> {
    const annotations = parseAnnotations(source);
    Object.assign(meta, {
      target: "none",
      boundary: "native",
      annotations,
      workflow_as_code: { wrapper: false, parsed: false, substring: false },
      exports: [],
      signatures: {},
      imports: [],
      packages: [],
      host_modules: [],
    });
    const header = nativeHeader(source);
    const t = transpile(source);
    if ("error" in t) {
      meta.native = { kind: "nativets", imports: [], links: [], ...header, transpile_error: t.error };
      return { meta };
    }
    let parsed;
    try {
      parsed = parse(source);
    } catch (err) {
      return { meta, error: (err as BuildProblem).problem() };
    }
    meta.workflow_as_code = detectWorkflowAsCode(source, parsed);
    try {
      const nm = this.native(t.js, "nativets");
      await this.nativeClient();
      this.writeOut(join("scripts", "bun", e.id + ".native.js"), nm.text);
      Object.assign(meta, { exports: nm.exports, signatures: signatures(parsed, nm.exports), native: { ...nm.form, ...header } });
      return { meta };
    } catch (err) {
      if (err instanceof BuildProblem) return { meta, error: err.problem() };
      throw err;
    }
  }

  // A codebase script with the //native header (WR-50): esbuild's bundle of
  // it, which the Go build wrote to e.bundle, in its native form. The
  // native runtime evaluates the bundle as an ES module whatever its
  // format, so a CommonJS one fails on its first use of module, exports or
  // require, as on Windmill. The header annotations are its source's.
  nativeEntry(e: EntryRequest): EntryResult {
    const meta: Record<string, unknown> = { path: e.path };
    try {
      if (e.bundle === undefined || !inside(relative(this.req.work, e.bundle))) {
        throw new BuildProblem("invalid_request", `the bundle of ${e.id} is not a file under the work directory`);
      }
      const source = e.file !== undefined ? this.readFolder(e.file) : (e.content ?? "");
      const nm = this.native(readFileSync(e.bundle, "utf8"), "bunnative");
      this.writeOut(join("scripts", "bun", e.id + ".native.js"), nm.text);
      Object.assign(meta, { exports: nm.exports, native: { ...nm.form, ...nativeHeader(source) } });
      return { meta };
    } catch (err) {
      if (err instanceof BuildProblem) return { meta, error: err.problem() };
      throw err;
    }
  }

  private native(js: string, kind: string): NativeModule {
    try {
      return nativeModule(js, kind);
    } catch (e) {
      throw new BuildProblem("bundle_failed", `the native form: ${buildMessage(e)}`);
    }
  }

  // The embedded client a nativets script imports as ./windmill.ts
  // (WR-40), written once per bundle as scripts/native/windmill.js, outside
  // the scripts' own tree, with the record of its native form.
  private nativeClient(): Promise<NativeModule> {
    this.client ??= buildNativeClient().then(
      (c) => {
        this.writeOut(join("scripts", "native", "windmill.js"), c.text);
        this.writeOut(join("scripts", "native", "windmill.meta.json"), JSON.stringify({ native: c.form }, null, 2) + "\n");
        return c;
      },
      (e) => {
        throw new BuildProblem("bundle_failed", `the embedded client (./windmill.ts): ${buildMessage(e)}`);
      },
    );
    return this.client;
  }

  // What a codebase script's source records, which esbuild bundles in Go
  // (C2b): its annotations and bundler target, the workflow-as-code
  // markers, the packages it imports, and a signature for each export of
  // its bundle (e.exports, which the Go build reads from esbuild, or the
  // source's own exports without it). As for any Bun script, a function the
  // source declares gets its parameters, as Windmill parses the script's
  // content (WR-6, WR-7; SY-8 sends the source with the bundle), and an
  // export it does not declare, such as one `export *` brings in, gets none.
  // Nothing is installed, bundled or written.
  parseEntry(e: EntryRequest): EntryResult {
    const source = e.file !== undefined ? this.readFolder(e.file) : (e.content ?? "");
    const meta: Record<string, unknown> = { path: e.path };
    let parsed;
    try {
      parsed = parse(source);
    } catch (err) {
      return { meta, error: (err as BuildProblem).problem() };
    }
    const annotations = parseAnnotations(source);
    let exports: string[];
    try {
      exports = e.exports ?? sourceScanner.scan(source).exports;
    } catch (err) {
      return { meta, error: { kind: "parse_failed", message: buildMessage(err) } };
    }
    Object.assign(meta, {
      target: targetFor(annotations),
      annotations,
      workflow_as_code: detectWorkflowAsCode(source, parsed),
      packages: directPackages(parsed),
      exports,
      signatures: signatures(parsed, exports),
    });
    return { meta };
  }

  // Shared code (ADR 0013): groups the built scripts by target and
  // dependency set, the build groups of the Bun build, and for each builds
  // its scripts' shared-layout modules and its closure, which a script
  // keeps its per-script bundle without when a module it reaches cannot be
  // held by a CommonJS closure (top-level await, a static import of an
  // external module, a kept import()). The closure bundles go to
  // <work>/closures/<id>.cjs, which the Go build wraps into factories.
  async buildShared(): Promise<{ closures: string[]; shared: Record<string, SharedRecord> }> {
    const out = { closures: [] as string[], shared: {} as Record<string, SharedRecord> };
    const groups = new Map<string, SharedEntry[]>();
    for (const s of this.sharedEntries) {
      const key = `${s.target}\0${s.deps ?? ""}`;
      groups.set(key, [...(groups.get(key) ?? []), s]);
    }
    for (const [key, entries] of groups) {
      const id = closureId(key);
      const { dir, target } = entries[0];
      const support = this.materialise(dir, join("wl", `closure-${id}.mjs`), closureSupport(id));
      const modules = new Map<string, string>();
      const refs = new Map<string, Map<string, string>>();
      for (const s of entries) {
        const r = await this.sharedModule(s, support);
        modules.set(s.id, r.text);
        refs.set(s.id, r.refs);
      }
      const reaches = (s: SharedEntry, m: string) => Object.hasOwn((s.metafile.inputs ?? {}) as object, m);
      let live = entries;
      while (live.length > 0) {
        const files = new Map<string, string>();
        for (const s of live) for (const [k, f] of refs.get(s.id)!) files.set(k, f);
        const bad = new Map<string, string>();
        let text: string | undefined;
        try {
          const b = await this.closureBundle(dir, id, files, target);
          text = b.text;
          for (const m of keptImports(text)) bad.set(m, "dynamic_import");
          for (const [m, input] of Object.entries((b.metafile.inputs ?? {}) as MetaInputs)) {
            for (const imp of input.imports ?? []) {
              // Bun lists a module it pruned from a re-export as an
              // external relative path; an external module is named bare.
              const bare = !imp.path.startsWith(".") && !imp.path.startsWith("/");
              if (!imp.external || !bare || builtinModule(imp.path)) continue;
              if (imp.kind === "dynamic-import") bad.set(m, "dynamic_import");
              else if ((imp.kind === "import-statement" || imp.kind === "export-from") && !bad.has(m)) bad.set(m, "external_import");
            }
          }
        } catch (e) {
          if (!(e instanceof BuildProblem) || e.files.length === 0 || !/top-level await/i.test(e.message)) throw e;
          for (const f of e.files) bad.set(f, "top_level_await");
        }
        const kept = live.filter((s) => {
          for (const [m, reason] of bad) {
            if (reaches(s, m)) {
              out.shared[s.id] = { fallback: reason };
              return false;
            }
          }
          return true;
        });
        if (kept.length < live.length) {
          live = kept;
          continue;
        }
        if (text === undefined) throw new BuildProblem("bundle_failed", `the closure ${id} fails to build on modules no script reaches`);
        mkdirSync(join(this.req.work, "closures"), { recursive: true });
        writeFileSync(join(this.req.work, "closures", id + ".cjs"), text);
        for (const s of live) {
          this.writeOut(join("scripts", "bun", s.id + ".shared.mjs"), modules.get(s.id)!);
          out.shared[s.id] = { closure: id };
        }
        out.closures.push(id);
        break;
      }
    }
    return out;
  }

  // A script's shared-layout module: the build of its entry with each of
  // the entry's imports of a module of its per-script bundle resolved to a
  // reference to that module in the script's instance of closure support's
  // group, keyed by its path in the dependency set, and an import of a
  // module the per-script bundle pruned to an empty module, as the bundle
  // drops it. The env support module and host-provided modules' shims stay
  // as the per-script bundle has them. It returns the module's text and
  // the files of the modules it references, by key.
  private async sharedModule(s: SharedEntry, support: string): Promise<{ text: string; refs: Map<string, string> }> {
    const inputs = (s.metafile.inputs ?? {}) as MetaInputs;
    const included = new Set<string>();
    for (const o of Object.values((s.metafile.outputs ?? {}) as Record<string, { inputs?: object }>)) {
      for (const k of Object.keys(o.inputs ?? {})) included.add(k);
    }
    // The entry's imports Bun resolved by itself name their file, with the
    // specifier as original; those the build's plugin resolved, it
    // recorded.
    const resolved = new Map(s.resolved);
    for (const imp of inputs[relative(s.dir, s.entry)]?.imports ?? []) {
      if (imp.original !== undefined && !imp.external) resolved.set(`${imp.kind}\0${imp.original}`, imp.path);
    }
    const refs = new Map<string, string>();
    const empty = this.materialise(s.dir, join("wl", "dep", "empty.cjs"), "module.exports = {};\n");
    // Every file this build writes is reached by the path the plugin
    // returns, never through Bun's resolver: Bun 1.3.10 keeps directory
    // listings across builds in one process, and on a file system with
    // coarse timestamps a file written in the same tick as an earlier read
    // of its directory leaves the listing unchanged, so a relative import of
    // it can fail to resolve.
    const cjsFiles = new Map<string, string>();
    const plugin: BunPlugin = {
      name: "windmill-lite-shared",
      setup: (b) => {
        b.onResolve({ filter: new RegExp(`^${CLOSURE_SPECIFIER}$`) }, () => ({ path: support }));
        b.onResolve({ filter: new RegExp(`^${CJS_SPECIFIER}`) }, (args) => {
          const f = cjsFiles.get(args.path);
          if (f === undefined) throw new Error(`the shared layout of ${s.id}: no CommonJS reference ${JSON.stringify(args.path)}`);
          return { path: f };
        });
        b.onResolve({ filter: /.*/ }, (args) => {
          if (args.importer !== s.entry || args.path === ENV_SPECIFIER) return undefined;
          const p = resolved.get(`${args.kind}\0${args.path}`);
          if (p === undefined) {
            throw new Error(`the shared layout of ${s.id}: the per-script bundle did not resolve its import ${JSON.stringify(args.path)} (${args.kind})`);
          }
          if (p.startsWith("host/") || p.startsWith("wl/")) return undefined;
          if (!included.has(p)) return { path: empty };
          refs.set(p, join(s.dir, p));
          const name = new Bun.CryptoHasher("sha256").update(p).digest("hex").slice(0, 16);
          const cjs = this.materialise(s.dir, join("wl", "dep", name + ".cjs"), depReference(p));
          if (inputs[p]?.format !== "esm") return { path: cjs };
          cjsFiles.set(CJS_SPECIFIER + name, cjs);
          return { path: this.materialise(s.dir, join("wl", "dep", name + ".mjs"), esmReference(p, CJS_SPECIFIER + name, hasDefault(join(s.dir, p)))) };
        });
      },
    };
    const b = await this.bundleOnce(s.dir, s.entry, new Map([[s.entry, s.path]]), s.target, { shims: true }, s.binding, { plugin });
    return { text: b.bundled.text, refs };
  }

  // A group's closure bundle: CommonJS, from an entry that requires each
  // dependency module's file, with the group's target, the env rewrite for
  // a Bun-target group (its support module first) and import.meta read
  // from the instance, as dependency code of an ES module bundle reads its
  // module's own. Workspace modules among the files resolve their imports
  // from the folder, as in the per-script bundles.
  private closureBundle(dir: string, id: string, files: Map<string, string>, target: "bun" | "node"): Promise<Bundled> {
    const env = target === "bun";
    const entry = this.materialise(dir, join("wl", `closure-entry-${id}.cjs`), closureEntry(files, env ? ENV_SPECIFIER : undefined));
    const seeds = new Map<string, string>();
    for (const [k, f] of files) if (k.startsWith("src/") && k.endsWith(".ts")) seeds.set(f, k.slice(4, -3));
    // The entry requires each file by its absolute path, which the plugin
    // returns as it is, rather than Bun's resolver finding it in a directory
    // listing it may have kept from before the file was written (above,
    // sharedModule).
    const listed = new Set(files.values());
    const plugin: BunPlugin = {
      name: "windmill-lite-closure",
      setup: (b) => {
        b.onResolve({ filter: /^\// }, (args) => (args.importer === entry && listed.has(args.path) ? { path: args.path } : undefined));
      },
    };
    return this.bundle(dir, entry, seeds, target, { shims: true }, env, { format: "cjs", define: { "import.meta": CLOSURE_META }, plugin });
  }

  private writeOut(rel: string, text: string): void {
    const abs = join(this.req.out, rel);
    mkdirSync(dirname(abs), { recursive: true });
    writeFileSync(abs, text);
  }
}

type EntryRequest = Request["entries"][number];

function parse(source: string, syntax?: Syntax) {
  try {
    return parseModule(source, syntax);
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
// Whether an import names a module the runtime has built in, Node's or
// Bun's own, which a require loads as an import does.
function builtinModule(spec: string): boolean {
  return spec.startsWith("node:") || spec === "bun" || spec.startsWith("bun:") || builtinModules.includes(spec);
}

// Whether a module's own source has a default export, by Bun's scan.
function hasDefault(file: string): boolean {
  const scanner = /\.[jt]sx$/.test(file) ? new Bun.Transpiler({ loader: "tsx" }) : sourceScanner;
  try {
    return scanner.scan(readFileSync(file, "utf8")).exports.includes("default");
  } catch {
    return true;
  }
}

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
