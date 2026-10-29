// The request and response of the build helper. The Go build
// (internal/bundle) writes a request file, runs the helper on the pinned
// Bun, and reads the response file; the types there mirror these.

export type Problem = { kind: string; message: string };

export type HostModuleRequest = {
  id: string;
  key: string;
  // A module of the folder: its Windmill path and its file.
  path?: string;
  file?: string;
  // A package module: the specifier and the dependency set it installs from.
  package?: string;
  deps?: string;
  // A module outside the folder, a file of a codebase entry's
  // relative_path, by its absolute path (parse only): the helper checks its
  // source, and the Go build bundles it with esbuild (milestone F3h).
  source?: string;
  // The generated shim, in the bundle tree, and the scan's export names
  // (build only).
  shim?: string;
  exports?: string[];
};

export type EntryRequest = {
  id: string;
  // The import base (WR-29): the script's Windmill path, or the inline
  // step's base.
  path: string;
  // A path script's file, relative to the folder, or an inline script's text.
  file?: string;
  content?: string;
  deps?: string;
  // The language a deploy records ("bun", "bunnative" or "nativets"): a
  // nativets script is transpiled, not bundled (WR-40).
  language?: string;
  // parse only: the export names of a codebase script's bundle, which the
  // Go build reads from esbuild; absent, the source's own are scanned.
  exports?: string[];
  // native only: a file under work that holds esbuild's bundle of a
  // codebase script with the //native header (WR-50).
  bundle?: string;
};

export type Request = {
  version: 1;
  // scan: the host-provided modules' exports; build: every module; parse:
  // only what the source of each entry records, for codebase scripts,
  // which esbuild bundles in Go (C2b).
  op: "scan" | "build" | "parse" | "native";
  require_bun?: string;
  folder: string;
  work: string;
  out: string;
  // The folder's scripts that imports resolve against: Windmill path → file.
  scripts: Record<string, string>;
  // Dependency sets: key → lock text in the format of WR-25.
  deps: Record<string, string>;
  // The env rewrite owned by the Go build (ADR 0010 step 5): the define
  // table, the support module's source, and the binding name both use.
  define: Record<string, string>;
  env_support: string;
  env_binding: string;
  host_modules: HostModuleRequest[];
  entries: EntryRequest[];
};

export type HostModuleResult = {
  error?: Problem;
  exports?: string[];
  host_modules?: string[];
  packages?: string[];
  // The workspace scripts the bundled copy inlined, by Windmill path (build
  // only).
  imports?: string[];
};

export type EntryResult = {
  error?: Problem;
  meta: Record<string, unknown>;
};

export type Response = {
  version: 1;
  bun_version: string;
  error?: Problem;
  host_modules: Record<string, HostModuleResult>;
  entries: Record<string, EntryResult>;
};
