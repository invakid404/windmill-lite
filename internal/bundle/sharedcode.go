package bundle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/invakid404/windmill-lite/internal/harness"
)

// Shared code (ADR 0013). A bundle built with it keeps every script's
// per-script bundle, which fresh mode runs and the CLI parity check
// compares, and adds, per build group, one closure: a CommonJS file with
// every dependency module the group's scripts reach, each behind the
// bundler's lazy wrapper, which exports a factory of instances. Each
// script gets a shared-layout module, its own code with every import of a
// dependency module resolved to the instance its module makes of the
// closure, so a step's dependencies keep module state of their own while
// their code is compiled once per process. The harness runtime loads the
// closures (ClosureRegistry).

const (
	// depNamespace holds the shared-layout module's references to its
	// dependency modules, by key; supportNamespace its closure support
	// module, which makes the instance.
	depNamespace     = "windmill-lite-dep"
	supportNamespace = "windmill-lite-shared"
	// closureSpecifier is the specifier a reference imports the support
	// module by; closureEntry the closure build's entry.
	closureSpecifier = "windmill-lite:closure"
	closureEntry     = "windmill-lite:closure-entry"
	// ClosureRegistry is the process-wide registry of closures the harness
	// runtime installs: instance(id, scope) loads closure id once per
	// process and returns an accessor of a fresh instance of it, made with
	// the step module's scope (its module, require, filename, dirname and,
	// for an ES module, import.meta).
	ClosureRegistry = `Symbol.for("windmill-lite.closures")`
	// closureMeta is the parameter of a closure that an ES module group's
	// dependency code reads as import.meta.
	closureMeta = "__wl_meta"
)

// depGuard marks the resolution the dependency plugin asks esbuild for, so
// that its own callback leaves it alone.
type depGuard struct{}

// closureSupport is the source of a shared-layout module's support module
// for closure id: dep(key) returns the exports of the dependency module
// key in the module's own instance of the closure, made at its first call
// with the module's scope. An ES module reaches its require, file and
// import.meta through import.meta, as Bun gives them.
func closureSupport(id string, esm bool) string {
	scope := "{ require, filename: __filename, dirname: __dirname }"
	if esm {
		scope = "{ require: import.meta.require, filename: import.meta.path, dirname: import.meta.dir, meta: import.meta }"
	}
	return fmt.Sprintf(`let instance;
export function dep(key) {
  if (instance === undefined) {
    const registry = globalThis[%s];
    if (registry === undefined) throw new Error("windmill-lite: shared code needs the closure registry of the harness runtime");
    instance = registry.instance(%q, %s);
  }
  return instance(key);
}
`, ClosureRegistry, id, scope)
}

// depReference is the source of a shared-layout module's reference to its
// dependency module key: a CommonJS module whose exports are that module's
// exports in the instance, so the script imports it as it would the module
// itself, with the bundler's CommonJS interop.
func depReference(key string) string {
	k, _ := json.Marshal(key)
	return fmt.Sprintf("module.exports = require(%q).dep(%s);\n", closureSpecifier, k)
}

// esmReference is the source of a shared-layout module's reference to an ES
// module dependency key: an ES module that re-exports its named exports
// from the CommonJS reference (cjsSpecifier + key), live through its
// getters, and, when it has one, its default export, read when the
// reference initialises. A bundler that gives an ES module importer Node's
// interop with a CommonJS module hands a default import the whole exports
// object, so a CommonJS reference alone would hand a default import the
// module's namespace. The Bun helper's esmReference writes the same.
func esmReference(key string, hasDefault bool) string {
	k, _ := json.Marshal(key)
	cjs, _ := json.Marshal(cjsSpecifier + key)
	text := fmt.Sprintf("export * from %s;\n", cjs)
	if hasDefault {
		text += fmt.Sprintf("import { dep } from %q;\nexport default dep(%s).default;\n", closureSpecifier, k)
	}
	return text
}

// cjsSpecifier prefixes the specifier an ES module reference re-exports its
// dependency's CommonJS reference by.
const cjsSpecifier = "windmill-lite:dep-cjs/"

// closureEntrySource is the source of a closure's entry: a table from each
// dependency module's key to a function that requires its file, so each
// module initialises in an instance when a script first reaches it.
func closureEntrySource(files map[string]string) string {
	var b strings.Builder
	b.WriteString("module.exports = {\n")
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		key, _ := json.Marshal(k)
		file, _ := json.Marshal(files[k])
		fmt.Fprintf(&b, "  %s: () => require(%s),\n", key, file)
	}
	b.WriteString("};\n")
	return b.String()
}

// wrapClosure makes a closure file of a CommonJS bundle of a closure's
// entry: its module.exports is a factory that runs the bundle once per
// call, with a module of its own and the step module's require, file
// names and, for an ES module group, import.meta (closureMeta), and
// returns the instance's table. A strict group's closure is strict, as its
// per-script bundles are. A Bun CommonJS bundle is already a function of
// (exports, require, module, __filename, __dirname), which it calls. The
// file is in Bun's own form of a CommonJS module that a bundler wrote
// (the `// @bun @bun-cjs` pragma and a function of the module's
// variables), which Bun loads without transpiling it again.
func wrapClosure(text []byte, strict bool) []byte {
	var b bytes.Buffer
	b.WriteString("// @bun @bun-cjs\n(function(exports, require, module, __filename, __dirname) {")
	if strict {
		b.WriteString("\"use strict\";")
	}
	b.WriteString("\nmodule.exports = function windmillLiteClosure(module, exports, require, __filename, __dirname, " + closureMeta + ") {\n")
	if rest, ok := bytes.CutPrefix(text, []byte("// @bun @bun-cjs\n")); ok {
		b.Write(bytes.TrimRight(rest, "\n"))
		b.WriteString("(exports, require, module, __filename, __dirname);\n")
	} else {
		b.Write(text)
		if len(text) > 0 && text[len(text)-1] != '\n' {
			b.WriteByte('\n')
		}
	}
	b.WriteString("return module.exports;\n};\n})\n")
	return b.Bytes()
}

// closureID names a group's closure by a digest of what makes the group.
func closureID(prefix, key string) string {
	sum := sha256.Sum256([]byte(key))
	return prefix + "-" + hex.EncodeToString(sum[:6])
}

// sharedModuleFile is the bundle path of a script's shared-layout module.
func sharedModuleFile(id string, format Format) string {
	ext := ".shared.mjs"
	if format == CJS {
		ext = ".shared.cjs"
	}
	return "scripts/bun/" + id + ext
}

// keptImports returns, by closure id, the modules of each closure bundle
// (a file each) whose code keeps an import(), which resolves from the file
// holding it (ADR 0013, decision item 5). The build helper parses the
// bundles, so text that only looks like an import is not one (kept op).
func keptImports(ctx context.Context, bc *BunConfig, files map[string]string) (map[string]map[string]bool, error) {
	req := helperRequest{
		Version: 1, Op: "kept", Folder: bc.Folder, Work: bc.Work, Out: bc.Out,
		Scripts: map[string]string{}, Deps: map[string]string{}, Define: map[string]string{},
		HostModules: []helperHostModule{}, Entries: []helperEntry{},
	}
	if !bc.AllowBunMismatch {
		req.RequireBun = harness.PinnedBun
	}
	for _, id := range slices.Sorted(maps.Keys(files)) {
		req.Closures = append(req.Closures, helperClosure{ID: id, File: files[id]})
	}
	resp, err := runHelper(ctx, bc, &req)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]bool{}
	for id, mods := range resp.Kept {
		out[id] = map[string]bool{}
		for _, m := range mods {
			out[id][m] = true
		}
	}
	return out, nil
}

// metaInputs reads the inputs of a metafile: each input's imports.
type metaInputs map[string]struct {
	Format  string `json:"format"`
	Imports []struct {
		Path     string `json:"path"`
		Kind     string `json:"kind"`
		External bool   `json:"external"`
	} `json:"imports"`
}

func readMetaInputs(metafile []byte) (metaInputs, error) {
	var m struct {
		Inputs metaInputs `json:"inputs"`
	}
	if err := json.Unmarshal(metafile, &m); err != nil {
		return nil, err
	}
	return m.Inputs, nil
}

// codebaseGroup is one build group of codebase scripts under shared code:
// scripts of one settings text, format, target class and strictness.
type codebaseGroup struct {
	id       string
	settings *CodebaseSettings
	strict   bool
	env      bool
	scripts  []CodebaseScript
	// reach holds, by script, the inputs of its per-script bundle, by path
	// relative to the folder.
	reach map[string]metaInputs
	// defaults caches, by file, whether an ES module dependency has a
	// default export.
	mu       sync.Mutex
	defaults map[string]bool
}

// hasDefault reports whether an ES module file has a default export, by its
// export names (moduleExports); one whose names do not read is taken to
// have one.
func (g *codebaseGroup) hasDefault(folder, file string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if d, ok := g.defaults[file]; ok {
		return d
	}
	names, err := moduleExports(folder, file, g.settings)
	d := err != nil || slices.Contains(names, "default")
	if g.defaults == nil {
		g.defaults = map[string]bool{}
	}
	g.defaults[file] = d
	return d
}

// fallback records that a script keeps its per-script bundle, with a
// warning naming the reason.
func fallback(res *CodebaseResult, e CodebaseScript, reason, why string) {
	res.Scripts[e.ID].Shared = &SharedMeta{Fallback: reason}
	res.Warnings = append(res.Warnings, Warning{WarningSharedCodeFallback, fmt.Sprintf("script %s (%s) keeps its per-script bundle: %s", e.ID, e.Path, why)})
}

// objectDefine reports whether a define value is an object or array
// literal, of which esbuild makes one per bundle.
func objectDefine(v string) bool {
	v = strings.TrimSpace(v)
	return strings.HasPrefix(v, "{") || strings.HasPrefix(v, "[")
}

// shareCode builds the shared layout of the built codebase scripts (ADR
// 0013): it groups them, records each script's fallback, and writes each
// group's closure and its scripts' shared-layout modules. It returns the
// ids of the closures it wrote.
func (cfg *CodebaseConfig) shareCode(ctx context.Context, bc *BunConfig, built []CodebaseScript, res *CodebaseResult, shims map[string]string) ([]string, error) {
	groups := map[string]*codebaseGroup{}
	var order []string
	for _, e := range built {
		meta := res.Scripts[e.ID]
		if meta.Boundary == "native" || meta.Annotations.Native {
			// A native script runs in a realm of its own (D5).
			continue
		}
		s, err := ParseCodebaseSettings(e.Settings)
		if err != nil {
			return nil, err
		}
		if len(s.Banner) > 0 {
			fallback(res, e, FallbackBanner, "its codebase entry sets a banner")
			continue
		}
		objects := false
		for _, v := range s.Define {
			objects = objects || objectDefine(v)
		}
		if objects {
			fallback(res, e, FallbackDefineObject, "its codebase entry's define gives an object or array value")
			continue
		}
		inputs, err := readMetaInputs(meta.Metafile)
		if err != nil {
			return nil, fmt.Errorf("bundle: script %s: its metafile: %w", e.ID, err)
		}
		if entryCycle(inputs, e.File) {
			fallback(res, e, FallbackEntryCycle, "a module it imports imports its own entry file")
			continue
		}
		text, err := os.ReadFile(filepath.Join(cfg.Out, "scripts", "bun", filepath.FromSlash(e.ID)+formatExt(s.Format)))
		if err != nil {
			return nil, err
		}
		strict := s.Format != CJS || bytes.HasPrefix(text, []byte(`"use strict";`))
		env := meta.binding != ""
		key := fmt.Sprintf("%s\x00%s\x00env=%t\x00strict=%t", e.Settings, s.Format, env, strict)
		g := groups[key]
		if g == nil {
			g = &codebaseGroup{id: closureID("cb", key), settings: s, strict: strict, env: env, reach: map[string]metaInputs{}}
			groups[key] = g
			order = append(order, key)
		}
		g.scripts = append(g.scripts, e)
		g.reach[e.ID] = inputs
	}
	var ids []string
	for _, key := range order {
		id, err := cfg.buildGroup(ctx, bc, groups[key], res, shims)
		if err != nil {
			return nil, err
		}
		if id != "" {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

func formatExt(f Format) string {
	if f == CJS {
		return ".cjs"
	}
	return ".mjs"
}

// entryCycle reports whether an input other than the entry imports the
// entry's own file.
func entryCycle(inputs metaInputs, entry string) bool {
	for p, in := range inputs {
		if p == entry {
			continue
		}
		for _, imp := range in.Imports {
			if imp.Path == entry {
				return true
			}
		}
	}
	return false
}

// buildGroup builds one group's shared-layout modules and closure, and
// returns the closure's id, or "" when every script fell back.
func (cfg *CodebaseConfig) buildGroup(ctx context.Context, bc *BunConfig, g *codebaseGroup, res *CodebaseResult, shims map[string]string) (string, error) {
	esm := g.settings.Format != CJS
	listed := map[string]bool{}
	for _, m := range cfg.HostModules {
		if m.Package == "" {
			listed[realPath(filepath.Join(cfg.Folder, filepath.FromSlash(m.File)))] = true
		}
	}
	// The entry's inject files, each replaced in a shared-layout module by a
	// proxy that re-exports its names from the module's instance, which the
	// closure's dependency modules share, the injected file being in the
	// closure too: one instance per script, as in its per-script bundle.
	proxies, err := injectProxies(cfg.Folder, g.settings)
	if err != nil {
		return "", err
	}
	// Each script's shared-layout module, and the dependency modules it
	// references.
	modules := map[string][]byte{}
	refs := map[string]map[string]string{} // script → key → file
	for _, e := range g.scripts {
		var mu sync.Mutex
		own := map[string]string{}
		reach := g.reach[e.ID]
		p := depPlugin(cfg.Folder, e.File, g.id, esm, listed, proxies, func(key, file string) (bool, bool) {
			if reach[key].Format != "esm" {
				return false, false
			}
			return true, g.hasDefault(cfg.Folder, file)
		}, func(key, file string) {
			mu.Lock()
			own[key] = file
			mu.Unlock()
		})
		out, _, _, _, prob := cfg.esbuild(e, g.settings, res.Scripts[e.ID].binding, shims, &p, func(o *api.BuildOptions) {
			for i, f := range o.Inject {
				if spec, ok := proxies.byFile[f]; ok {
					o.Inject[i] = spec
				}
			}
		})
		if prob != nil {
			return "", fmt.Errorf("bundle: the shared layout of script %s: %s", e.ID, prob.Message)
		}
		modules[e.ID], refs[e.ID] = out.text, own
	}
	live := slices.Clone(g.scripts)
	for {
		if len(live) == 0 {
			return "", nil
		}
		files := map[string]string{}
		for _, e := range live {
			for k, f := range refs[e.ID] {
				files[k] = f
			}
		}
		text, inputs, failed, err := cfg.buildClosure(g, files, shims)
		if err != nil {
			return "", err
		}
		// What a CommonJS closure cannot hold, by module: top-level await
		// (the build fails on it), a static import of an external module in
		// an ES module group, and an import() the bundle kept.
		bad := map[string]string{} // module → reason
		for m := range failed {
			bad[m] = FallbackTopLevelAwait
		}
		if text != nil {
			raw := filepath.Join(cfg.Work, "closures", g.id+".raw.cjs")
			if err := writeFile(raw, text); err != nil {
				return "", err
			}
			kept, err := keptImports(ctx, bc, map[string]string{g.id: raw})
			if err != nil {
				return "", err
			}
			for m := range kept[g.id] {
				bad[m] = FallbackDynamicImport
			}
		}
		for m, in := range inputs {
			for _, imp := range in.Imports {
				switch {
				case imp.External && imp.Kind == "dynamic-import":
					bad[m] = FallbackDynamicImport
				case imp.External && esm && !builtinModule(imp.Path) && (imp.Kind == "import-statement" || imp.Kind == "re-export"):
					if _, ok := bad[m]; !ok {
						bad[m] = FallbackExternalImport
					}
				}
			}
		}
		var kept []CodebaseScript
		for _, e := range live {
			reason, module := "", ""
			for _, m := range slices.Sorted(maps.Keys(bad)) {
				if _, ok := g.reach[e.ID][m]; ok {
					reason, module = bad[m], m
					break
				}
			}
			if reason == "" {
				kept = append(kept, e)
				continue
			}
			fallback(res, e, reason, fallbackWhy(reason)+" ("+module+")")
		}
		if len(kept) < len(live) {
			live = kept
			continue
		}
		if err := writeFile(filepath.Join(cfg.Out, filepath.FromSlash(ClosureFile(g.id))), wrapClosure(text, g.strict)); err != nil {
			return "", err
		}
		for _, e := range live {
			if err := writeFile(filepath.Join(cfg.Out, filepath.FromSlash(sharedModuleFile(e.ID, g.settings.Format))), modules[e.ID]); err != nil {
				return "", err
			}
			res.Scripts[e.ID].Shared = &SharedMeta{Closure: g.id}
		}
		return g.id, nil
	}
}

// nodeBuiltins are the modules Node 20 has built in (module.builtinModules),
// which the node platform leaves external: a require of one loads it as an
// import does, so a static import of one costs no script its shared layout.
var nodeBuiltins = map[string]bool{}

func init() {
	for _, m := range strings.Fields(`_http_agent _http_client _http_common _http_incoming _http_outgoing
		_http_server _stream_duplex _stream_passthrough _stream_readable _stream_transform _stream_wrap
		_stream_writable _tls_common _tls_wrap assert assert/strict async_hooks buffer child_process cluster
		console constants crypto dgram diagnostics_channel dns dns/promises domain events fs fs/promises http
		http2 https inspector inspector/promises module net os path path/posix path/win32 perf_hooks process
		punycode querystring readline readline/promises repl stream stream/consumers stream/promises
		stream/web string_decoder sys timers timers/promises tls trace_events tty url util util/types v8 vm
		wasi worker_threads zlib`) {
		nodeBuiltins[m] = true
	}
}

// builtinModule reports whether an import names a module the runtime has
// built in: Node's, with or without node:, and Bun's own.
func builtinModule(spec string) bool {
	return strings.HasPrefix(spec, "node:") || spec == "bun" || strings.HasPrefix(spec, "bun:") || nodeBuiltins[spec]
}

// fallbackWhy says why a script keeps its per-script bundle, for the
// warning.
func fallbackWhy(reason string) string {
	switch reason {
	case FallbackTopLevelAwait:
		return "a module it imports uses top-level await, which a CommonJS closure cannot initialise"
	case FallbackExternalImport:
		return "a module it imports imports an external module statically, which the closure would turn into a synchronous require"
	case FallbackDynamicImport:
		return "a module it imports keeps an import() that resolves from the file holding it"
	}
	return reason
}

// buildClosure bundles a group's closure: its entry requires each
// dependency module's file, with the group's settings (the first script's
// options with the entry, banner and format replaced), the env rewrite
// when the group has it, the host-provided modules' shims, and, for an ES
// module group, import.meta read from the instance. It returns the output,
// its inputs, and the modules whose top-level await failed the build.
func (cfg *CodebaseConfig) buildClosure(g *codebaseGroup, files map[string]string, shims map[string]string) ([]byte, metaInputs, map[string]bool, error) {
	o, err := g.settings.CLIOptions(cfg.Folder, g.scripts[0].File)
	if err != nil {
		return nil, nil, nil, err
	}
	o.EntryPoints = []string{closureEntry}
	o.Format = api.FormatCommonJS
	o.Metafile = true
	if o.Define == nil {
		o.Define = map[string]string{}
	}
	if g.settings.Format != CJS {
		o.Define["import.meta"] = closureMeta
	}
	src := closureEntrySource(files)
	entry := api.Plugin{Name: "windmill-lite-closure", Setup: func(b api.PluginBuild) {
		b.OnResolve(api.OnResolveOptions{Filter: `^` + regexp.QuoteMeta(closureEntry) + `$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			return api.OnResolveResult{Path: "entry", Namespace: supportNamespace}, nil
		})
		b.OnLoad(api.OnLoadOptions{Filter: `^entry$`, Namespace: supportNamespace}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			return api.OnLoadResult{Contents: &src, Loader: api.LoaderJS, ResolveDir: cfg.Folder}, nil
		})
	}}
	base := o
	for n := 1; n <= 20; n++ {
		o := base
		o.Define = cloneMap(base.Define)
		o.Inject = slices.Clone(base.Inject)
		support, binding := "", ""
		if g.env {
			binding = EnvBinding
			if n > 1 {
				binding = fmt.Sprintf("%s_%d", EnvBinding, n)
			}
			define, sup, err := EnvRewrite(g.settings.Format)
			if err != nil {
				return nil, nil, nil, err
			}
			for k, v := range define {
				o.Define[k] = strings.ReplaceAll(v, EnvBinding, binding)
			}
			support = strings.ReplaceAll(sup, EnvBinding, binding)
			o.Inject = append([]string{envSpecifier}, o.Inject...)
		}
		o.Plugins = []api.Plugin{entry, cfg.plugin(support, binding, shims, "", nil)}
		r := api.Build(o)
		if len(r.Errors) > 0 {
			// A module with top-level await fails a CommonJS build, and so
			// does each require of it, which names the importer: both are
			// modules a script that reaches them cannot share.
			failed := map[string]bool{}
			for _, e := range r.Errors {
				if !strings.Contains(strings.ToLower(e.Text), "top-level await") || e.Location == nil {
					return nil, nil, nil, fmt.Errorf("bundle: the closure %s: %s", g.id, esbuildMessages(r.Errors))
				}
				failed[e.Location.File] = true
			}
			return nil, nil, failed, nil
		}
		inputs, err := readMetaInputs([]byte(r.Metafile))
		if err != nil {
			return nil, nil, nil, err
		}
		// The metafile names an import of the plugin's own modules (the env
		// support module, a listed package's shim) by its path in the
		// plugin's namespace, as an external one: no module imports those
		// from outside the bundle.
		own := map[string]bool{"env-" + binding + ".mjs": true}
		for _, m := range cfg.HostModules {
			own["host/"+m.Key] = true
		}
		for p, in := range inputs {
			kept := in.Imports[:0]
			for _, imp := range in.Imports {
				if !imp.External || !own[imp.Path] {
					kept = append(kept, imp)
				}
			}
			in.Imports = kept
			inputs[p] = in
		}
		if binding != "" {
			spelled := false
			for p := range inputs {
				if strings.HasPrefix(p, pluginNamespace+":") || strings.HasPrefix(p, supportNamespace+":") {
					continue
				}
				full := p
				if !filepath.IsAbs(full) {
					full = filepath.Join(cfg.Folder, filepath.FromSlash(p))
				}
				if b, err := os.ReadFile(full); err == nil && spells(string(b), binding) {
					spelled = true
					break
				}
			}
			if spelled {
				continue
			}
		}
		return r.OutputFiles[0].Contents, inputs, nil, nil
	}
	return nil, nil, nil, fmt.Errorf("bundle: the closure %s: every candidate name for %s occurs in its sources", g.id, EnvBinding)
}

// depPlugin is the plugin of a shared-layout module's build: an import by
// the entry file (entry, relative to folder) of a module of its own build
// resolves to a reference to that module in the instance of closure id,
// keyed by its path from folder, which record learns. An import that does
// not resolve to a file of the build (the support module of the env
// rewrite, a host-provided module's shim, an external module), of a listed
// host-provided module's file, which the build loads as its shim, or of
// the entry itself, is left as the per-script bundle has it. The package's
// sideEffects is kept, so the bundler prunes a reference as it would the
// module (ADR 0013, decision item 3).
func depPlugin(folder, entry, id string, esm bool, listed map[string]bool, proxies *injectProxySet, isESM func(key, file string) (esm, hasDefault bool), record func(key, file string)) api.Plugin {
	var mu sync.Mutex
	esmDeps := map[string]bool{} // key → its default export, for an ES module dependency
	own := realPath(filepath.Join(folder, filepath.FromSlash(entry)))
	support := closureSupport(id, esm)
	return api.Plugin{Name: "windmill-lite-shared", Setup: func(b api.PluginBuild) {
		b.OnResolve(api.OnResolveOptions{Filter: `^` + regexp.QuoteMeta(closureSpecifier) + `$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			return api.OnResolveResult{Path: "closure", Namespace: supportNamespace}, nil
		})
		b.OnResolve(api.OnResolveOptions{Filter: `^` + regexp.QuoteMeta(injectSpecifier) + `\d+$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			return api.OnResolveResult{Path: strings.TrimPrefix(a.Path, "windmill-lite:"), Namespace: supportNamespace}, nil
		})
		b.OnResolve(api.OnResolveOptions{Filter: `^` + regexp.QuoteMeta(cjsSpecifier)}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			return api.OnResolveResult{Path: "cjs/" + strings.TrimPrefix(a.Path, cjsSpecifier), Namespace: depNamespace}, nil
		})
		b.OnResolve(api.OnResolveOptions{Filter: `.*`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			if a.PluginData == (depGuard{}) {
				return api.OnResolveResult{}, nil
			}
			// The entry's own imports, and a proxy's of its inject file.
			byEntry := a.Namespace == "file" && a.Importer == own
			byProxy := a.Namespace == supportNamespace && strings.HasPrefix(a.Importer, "inject/")
			if !byEntry && !byProxy {
				return api.OnResolveResult{}, nil
			}
			switch a.Kind {
			case api.ResolveJSImportStatement, api.ResolveJSRequireCall, api.ResolveJSDynamicImport:
			default:
				return api.OnResolveResult{}, nil
			}
			r := b.Resolve(a.Path, api.ResolveOptions{Importer: a.Importer, ResolveDir: a.ResolveDir, Kind: a.Kind, PluginData: depGuard{}})
			if len(r.Errors) > 0 || r.External || r.Namespace != "file" || r.Path == own || listed[r.Path] {
				return api.OnResolveResult{}, nil
			}
			rel, err := filepath.Rel(folder, r.Path)
			if err != nil {
				return api.OnResolveResult{}, nil
			}
			key := filepath.ToSlash(rel)
			record(key, r.Path)
			if e, d := isESM(key, r.Path); e {
				mu.Lock()
				esmDeps[key] = d
				mu.Unlock()
			}
			effects := api.SideEffectsTrue
			if !r.SideEffects {
				effects = api.SideEffectsFalse
			}
			return api.OnResolveResult{Path: key, Namespace: depNamespace, SideEffects: effects}, nil
		})
		b.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: depNamespace}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			if key, ok := strings.CutPrefix(a.Path, "cjs/"); ok {
				text := depReference(key)
				return api.OnLoadResult{Contents: &text, Loader: api.LoaderJS}, nil
			}
			mu.Lock()
			hasDefault, esm := esmDeps[a.Path]
			mu.Unlock()
			text := depReference(a.Path)
			if esm {
				text = esmReference(a.Path, hasDefault)
			}
			return api.OnLoadResult{Contents: &text, Loader: api.LoaderJS}, nil
		})
		b.OnLoad(api.OnLoadOptions{Filter: `^closure$`, Namespace: supportNamespace}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			return api.OnLoadResult{Contents: &support, Loader: api.LoaderJS}, nil
		})
		b.OnLoad(api.OnLoadOptions{Filter: `^inject/\d+$`, Namespace: supportNamespace}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			text := proxies.source[a.Path]
			return api.OnLoadResult{Contents: &text, Loader: api.LoaderJS, ResolveDir: folder}, nil
		})
	}}
}

// injectSpecifier prefixes the specifier of an inject file's proxy in a
// shared-layout module's build.
const injectSpecifier = "windmill-lite:inject/"

// injectProxySet holds a group's inject files' proxies: by the file as the
// entry's inject list names it, the proxy's specifier, and by the proxy's
// path in the support namespace, its source.
type injectProxySet struct {
	byFile map[string]string
	source map[string]string
}

// injectProxies makes the proxy of each of a group's inject files: an ES
// module that re-exports the file's export names from the file, an import
// the dependency plugin resolves to a reference to it in the instance, so
// the script's own code reads the names from the instance's module, which
// the closure's dependency modules share (ADR 0013, decision item 3). A
// file with no export names (a CommonJS one, which inject only runs) is
// imported for its effects.
func injectProxies(folder string, s *CodebaseSettings) (*injectProxySet, error) {
	set := &injectProxySet{byFile: map[string]string{}, source: map[string]string{}}
	for i, f := range s.Inject {
		abs := f
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(folder, filepath.FromSlash(f))
		}
		names, err := moduleExports(folder, abs, s)
		if err != nil {
			return nil, fmt.Errorf("bundle: the inject file %s: %w", f, err)
		}
		target, _ := json.Marshal(abs)
		text := fmt.Sprintf("import %s;\n", target)
		if len(names) > 0 {
			quoted := make([]string, len(names))
			for j, n := range names {
				q, _ := json.Marshal(n)
				quoted[j] = string(q)
			}
			text = fmt.Sprintf("export { %s } from %s;\n", strings.Join(quoted, ", "), target)
		}
		key := fmt.Sprintf("inject/%d", i)
		set.byFile[f] = "windmill-lite:" + key
		set.source[key] = text
	}
	return set, nil
}

// moduleExports returns the export names of an ES module file, read from
// the metafile of an esbuild build of it alone, with the group's loaders
// and its packages left external.
func moduleExports(folder, file string, s *CodebaseSettings) ([]string, error) {
	o := api.BuildOptions{
		AbsWorkingDir: folder, EntryPoints: []string{file}, Bundle: true, Write: false, Metafile: true,
		Platform: api.PlatformNode, Format: api.FormatESModule, Packages: api.PackagesExternal, Outdir: "/", LogLevel: api.LogLevelSilent,
	}
	if s.Loader != nil {
		o.Loader = map[string]api.Loader{}
		for ext, l := range s.Loader {
			if v, ok := loaders[l]; ok {
				o.Loader[ext] = v
			}
		}
	}
	r := api.Build(o)
	if len(r.Errors) > 0 {
		return nil, fmt.Errorf("%s", esbuildMessages(r.Errors))
	}
	var meta struct {
		Inputs  map[string]struct{ Format string } `json:"inputs"`
		Outputs map[string]struct {
			EntryPoint string   `json:"entryPoint"`
			Exports    []string `json:"exports"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal([]byte(r.Metafile), &meta); err != nil {
		return nil, err
	}
	for _, o := range meta.Outputs {
		if o.EntryPoint == "" {
			continue
		}
		if meta.Inputs[o.EntryPoint].Format == "cjs" {
			return nil, nil
		}
		return slices.Sorted(slices.Values(o.Exports)), nil
	}
	return nil, nil
}
