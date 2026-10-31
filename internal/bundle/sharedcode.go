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
	// the step module's scope (its module, exports, require, filename,
	// dirname and, for an ES module, import.meta).
	ClosureRegistry = `Symbol.for("windmill-lite.closures")`
	// ClosureTable is the global property through which a closure's entry
	// hands the registry its table of dependency modules while the closure
	// runs, so that the instance's module stays the step module's.
	ClosureTable = `Symbol.for("windmill-lite.closure-table")`
	// closureMeta is the base of the name of a closure's parameter that an
	// ES module group's dependency code reads as import.meta; the build
	// takes the first of closureMeta, closureMeta_2, … that no input spells.
	closureMeta = "__wl_meta"
)

// depGuard marks the resolution the dependency plugin asks esbuild for, so
// that its own callback leaves it alone.
type depGuard struct{}

// closureSupport is the source of a shared-layout module's support module
// for closure id: dep(key) returns the exports of the dependency module
// key in the module's own instance of the closure, made at its first call
// with the module's scope. A CommonJS module passes its module and exports,
// which a dependency of its per-script bundle reads, a codebase bundle
// running as the job's own module. An ES module
// has neither, as Bun runs it; it reaches its require, file and import.meta
// through import.meta.
func closureSupport(id string, esm bool) string {
	scope := "{ module, exports, require, filename: __filename, dirname: __dirname }"
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
// module's namespace. With the module's export names all known, it lists
// them, so its namespace object has the module's names, in the order the
// bundler gives a namespace, and not the CommonJS interop marker
// (__esModule) of the reference it reads them from; otherwise it
// re-exports them all (export *), and the build keeps a script whose entry
// takes the namespace object on its per-script bundle (FallbackExportNames).
// The Bun helper's esmReference writes the same.
func esmReference(key string, m moduleNames) string {
	k, _ := json.Marshal(key)
	cjs, _ := json.Marshal(cjsSpecifier + key)
	// The CommonJS reference initialises the module, whatever names an
	// importer reads, as the per-script bundle does for an import of it.
	text := fmt.Sprintf("import %s;\nexport * from %s;\n", cjs, cjs)
	if m.known() {
		var list []string
		for _, n := range m.names {
			if n != "default" {
				q, _ := json.Marshal(n)
				list = append(list, string(q))
			}
		}
		text = fmt.Sprintf("import %s;\n", cjs)
		if len(list) > 0 {
			text += fmt.Sprintf("export { %s } from %s;\n", strings.Join(list, ", "), cjs)
		}
	}
	if m.hasDefault() {
		text += fmt.Sprintf("import { dep } from %q;\nexport default dep(%s).default;\n", closureSpecifier, k)
	}
	return text
}

// cjsSpecifier prefixes the specifier an ES module reference re-exports its
// dependency's CommonJS reference by.
const cjsSpecifier = "windmill-lite:dep-cjs/"

// closureEntrySource is the source of a closure's entry: a table from each
// dependency module's key to a function that requires its file, so each
// module initialises in an instance when a script first reaches it. The
// entry hands the table over through ClosureTable rather than its
// module.exports, since the module a closure runs with is the step
// module's own.
func closureEntrySource(files map[string]string) string {
	var b strings.Builder
	b.WriteString("globalThis[" + ClosureTable + "] = {\n")
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
// call with the step module's module, exports, require and file names and,
// for an ES module group, its import.meta (the parameter meta), so the
// dependency code of an instance sees the step's as in its per-script
// bundle. The entry hands the instance's table over through ClosureTable.
// A strict group's closure is strict, as its per-script bundles are. A Bun
// CommonJS bundle is already a function of (exports, require, module,
// __filename, __dirname), which it calls. The file is in Bun's own form of
// a CommonJS module that a bundler wrote (the `// @bun @bun-cjs` pragma
// and a function of the module's variables), which Bun loads without
// transpiling it again.
//
// With module, the closure of an ES module codebase group, the file is an
// ES module instead (`// @bun`, Bun's form of a module it bundled), whose
// default export is the factory of (require, __filename, __dirname, meta):
// an ES module binds no module or exports, so dependency code that reads
// either finds no binding, as in its ES module per-script bundle. Bun's own
// bundler gives the ES module dependencies of a Bun-built group free
// placeholders for those names in either form, so its closure stays
// CommonJS.
func wrapClosure(text []byte, strict bool, meta string, module bool) []byte {
	var b bytes.Buffer
	if module {
		b.WriteString("// @bun\nexport default function windmillLiteClosure(require, __filename, __dirname, " + meta + ") {\n")
		b.Write(text)
		if len(text) > 0 && text[len(text)-1] != '\n' {
			b.WriteByte('\n')
		}
		b.WriteString("}\n")
		return b.Bytes()
	}
	b.WriteString("// @bun @bun-cjs\n(function(exports, require, module, __filename, __dirname) {")
	if strict {
		b.WriteString("\"use strict\";")
	}
	b.WriteString("\nmodule.exports = function windmillLiteClosure(module, exports, require, __filename, __dirname, " + meta + ") {\n")
	if rest, ok := bytes.CutPrefix(text, []byte("// @bun @bun-cjs\n")); ok {
		b.Write(bytes.TrimRight(rest, "\n"))
		b.WriteString("(exports, require, module, __filename, __dirname);\n")
	} else {
		b.Write(text)
		if len(text) > 0 && text[len(text)-1] != '\n' {
			b.WriteByte('\n')
		}
	}
	b.WriteString("};\n})\n")
	return b.Bytes()
}

// validMetaName reports whether a name is one of the build's candidates for
// a closure's import.meta parameter: closureMeta, or it with a numeric
// suffix.
func validMetaName(name string) bool {
	rest, ok := strings.CutPrefix(name, closureMeta)
	if !ok {
		return false
	}
	if rest == "" {
		return true
	}
	digits, ok := strings.CutPrefix(rest, "_")
	return ok && digits != "" && strings.Trim(digits, "0123456789") == ""
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
// holding it ([ADR 0013], decision item 5), and the specifiers that name a
// module the runtime has built in, which resolve to the same module from
// every file. An import() whose specifier is a literal naming one of those
// does not count. The build helper parses the bundles, so text that only
// looks like an import is not one, and lists the built-in specifiers from
// its runtime (kept op).
//
// [ADR 0013]: https://github.com/invakid404/windmill-lite/blob/main/docs/design/0013-shared-module-code.md#decision
func keptImports(ctx context.Context, bc *BunConfig, files map[string]string) (map[string]map[string]bool, builtins, error) {
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
		return nil, nil, err
	}
	if len(resp.Builtins) == 0 {
		return nil, nil, fmt.Errorf("bundle: the build helper listed no built-in modules")
	}
	out := map[string]map[string]bool{}
	for id, mods := range resp.Kept {
		out[id] = map[string]bool{}
		for _, m := range mods {
			out[id][m] = true
		}
	}
	b := builtins{}
	for _, m := range resp.Builtins {
		b[m] = true
	}
	return out, b, nil
}

// namespaceImportsOf returns, by module file, the specifiers whose
// namespace object each module takes, as the build helper parses it
// (namespaces op); a module it could not parse maps to nil.
func namespaceImportsOf(ctx context.Context, bc *BunConfig, files []string) (map[string][]string, error) {
	req := helperRequest{
		Version: 1, Op: "namespaces", Folder: bc.Folder, Work: bc.Work, Out: bc.Out,
		Scripts: map[string]string{}, Deps: map[string]string{}, Define: map[string]string{},
		HostModules: []helperHostModule{}, Entries: []helperEntry{}, Modules: files,
	}
	if !bc.AllowBunMismatch {
		req.RequireBun = harness.PinnedBun
	}
	resp, err := runHelper(ctx, bc, &req)
	if err != nil {
		return nil, err
	}
	return resp.Namespaces, nil
}

// metaInputs reads the inputs of a metafile: each input's imports.
type metaInputs map[string]struct {
	Format  string `json:"format"`
	Imports []struct {
		Path     string `json:"path"`
		Kind     string `json:"kind"`
		External bool   `json:"external"`
		Original string `json:"original"`
	} `json:"imports"`
}

// exportNamesClash returns the ES module dependency imported by a script's
// entry whose namespace its reference module cannot reproduce, or "" (see
// FallbackExportNames). ns is namespaceClash's; names gives a module's
// export names.
func exportNamesClash(inputs metaInputs, entry string, ns func(file string) ([]string, bool), names func(file string) moduleNames) string {
	taken := takenNamespaces(inputs, entry, ns)
	for _, imp := range inputs[entry].Imports {
		p := imp.Path
		if imp.External || imp.Kind == "require-call" || imp.Kind == "require-resolve" || p == entry || !fileInput(p) || inputs[p].Format != "esm" {
			continue
		}
		m := names(p)
		if slices.Contains(m.names, "__esModule") || taken[p] && !m.known() {
			return p
		}
	}
	return ""
}

// takenNamespaces returns the files whose namespace object a module of a
// script's bundle takes (namespaceClash).
func takenNamespaces(inputs metaInputs, file string, ns func(file string) ([]string, bool)) map[string]bool {
	specs, parsed := ns(file)
	out := map[string]bool{}
	for _, imp := range inputs[file].Imports {
		if imp.External || imp.Kind == "require-call" || imp.Kind == "require-resolve" {
			continue
		}
		if !parsed || slices.Contains(specs, imp.Original) {
			out[imp.Path] = true
		}
	}
	return out
}

// fileInput reports whether a metafile input is a file, not a module of a
// plugin's namespace or a virtual input of esbuild's (<define:NAME>).
func fileInput(p string) bool {
	return !strings.HasPrefix(p, "<") && (filepath.IsAbs(p) || !strings.Contains(p, ":"))
}

// namespaceClash returns the ES module dependency whose namespace object
// both a script's entry and another module of its per-script bundle take,
// or "". ns gives, by module file, the specifiers whose namespace object
// the module takes (nil for one the helper could not parse, read as
// taking every namespace it imports); each specifier is matched to the
// file the bundle resolved it to.
func namespaceClash(inputs metaInputs, entry string, ns func(file string) ([]string, bool)) string {
	takes := func(file string) map[string]bool { return takenNamespaces(inputs, file, ns) }
	fromEntry := map[string]bool{}
	for p := range takes(entry) {
		if p != entry && inputs[p].Format == "esm" {
			fromEntry[p] = true
		}
	}
	if len(fromEntry) == 0 {
		return ""
	}
	for _, file := range slices.Sorted(maps.Keys(inputs)) {
		if file == entry || !fileInput(file) {
			continue
		}
		for _, p := range slices.Sorted(maps.Keys(takes(file))) {
			if fromEntry[p] {
				return p
			}
		}
	}
	return ""
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
	// exports caches, by file, the export names of an ES module dependency.
	mu      sync.Mutex
	exports map[string]moduleNames
}

// exportsOf returns the export names of an ES module file (moduleExports).
func (g *codebaseGroup) exportsOf(folder, file string) moduleNames {
	g.mu.Lock()
	defer g.mu.Unlock()
	if m, ok := g.exports[file]; ok {
		return m
	}
	m := readModuleNames(folder, file, g.settings)
	if g.exports == nil {
		g.exports = map[string]moduleNames{}
	}
	g.exports[file] = m
	return m
}

// moduleNames is what the build reads of an ES module file's export names:
// the names, whether they are all of them (none comes from a star re-export
// of a module the build leaves external), and whether they read at all.
type moduleNames struct {
	names    []string
	complete bool
	ok       bool
}

// known reports whether the names are all the module's.
func (m moduleNames) known() bool { return m.ok && m.complete }

// hasDefault reports whether the module has a default export; one whose
// names do not read is taken to have one.
func (m moduleNames) hasDefault() bool { return !m.ok || slices.Contains(m.names, "default") }

func readModuleNames(folder, file string, s *CodebaseSettings) moduleNames {
	names, complete, err := moduleExports(folder, file, s)
	return moduleNames{names: names, complete: complete, ok: err == nil}
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
	// Each script's per-script inputs, and the namespace objects each
	// module of them takes, parsed by the build helper (namespaceClash).
	reach := map[string]metaInputs{}
	files := map[string]bool{}
	for _, e := range built {
		meta := res.Scripts[e.ID]
		if meta.Boundary == "native" || meta.Annotations.Native {
			continue
		}
		inputs, err := readMetaInputs(meta.Metafile)
		if err != nil {
			return nil, fmt.Errorf("bundle: script %s: its metafile: %w", e.ID, err)
		}
		reach[e.ID] = inputs
		for p := range inputs {
			if fileInput(p) {
				files[cfg.inputFile(p)] = true
			}
		}
	}
	var ns map[string][]string
	if len(files) > 0 {
		var err error
		if ns, err = namespaceImportsOf(ctx, bc, slices.Sorted(maps.Keys(files))); err != nil {
			return nil, err
		}
	}
	names := map[string]moduleNames{} // by settings and file
	groups := map[string]*codebaseGroup{}
	var order []string
	for _, e := range built {
		meta := res.Scripts[e.ID]
		if meta.Boundary == "native" || meta.Annotations.Native {
			// A native script runs in a realm of its own.
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
		inputs := reach[e.ID]
		if entryCycle(inputs, e.File) {
			fallback(res, e, FallbackEntryCycle, "a module it imports imports its own entry file")
			continue
		}
		nsOf := func(file string) ([]string, bool) {
			specs := ns[cfg.inputFile(file)]
			return specs, specs != nil
		}
		if m := namespaceClash(inputs, e.File, nsOf); m != "" {
			fallback(res, e, FallbackNamespaceImport, fallbackWhy(FallbackNamespaceImport)+" ("+m+")")
			continue
		}
		if m := exportNamesClash(inputs, e.File, nsOf, func(file string) moduleNames {
			key := string(e.Settings) + "\x00" + file
			if m, ok := names[key]; ok {
				return m
			}
			m := readModuleNames(cfg.Folder, cfg.inputFile(file), s)
			names[key] = m
			return m
		}); m != "" {
			fallback(res, e, FallbackExportNames, fallbackWhy(FallbackExportNames)+" ("+m+")")
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

// inputFile is the absolute path of a metafile input of the folder's
// bundles.
func (cfg *CodebaseConfig) inputFile(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(cfg.Folder, filepath.FromSlash(p))
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
		p := depPlugin(cfg.Folder, e.File, g.id, esm, listed, proxies, func(key, file string) (bool, moduleNames) {
			if reach[key].Format != "esm" {
				return false, moduleNames{}
			}
			return true, g.exportsOf(cfg.Folder, file)
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
		text, meta, inputs, failed, err := cfg.buildClosure(g, files, shims)
		if err != nil {
			return "", err
		}
		// What a CommonJS closure cannot hold, by module: top-level await
		// (the build fails on it), a static import of an external module in
		// an ES module group, and an import() the bundle kept. An import of
		// a module the runtime has built in is neither. Inputs are listed
		// only when the closure built, which is when builtin is listed.
		bad := map[string]string{} // module → reason
		for m := range failed {
			bad[m] = FallbackTopLevelAwait
		}
		var builtin builtins
		if text != nil {
			raw := filepath.Join(cfg.Work, "closures", g.id+".raw.cjs")
			if err := writeFile(raw, text); err != nil {
				return "", err
			}
			kept, b, err := keptImports(ctx, bc, map[string]string{g.id: raw})
			if err != nil {
				return "", err
			}
			builtin = b
			for m := range kept[g.id] {
				bad[m] = FallbackDynamicImport
			}
		}
		for m, in := range inputs {
			for _, imp := range in.Imports {
				switch {
				case !imp.External || builtin.has(imp.Path):
				case imp.Kind == "dynamic-import":
					bad[m] = FallbackDynamicImport
				case esm && (imp.Kind == "import-statement" || imp.Kind == "re-export"):
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
		file := ClosureFile(g.id)
		if esm {
			file = ClosureModuleFile(g.id)
		}
		if err := writeFile(filepath.Join(cfg.Out, filepath.FromSlash(file)), wrapClosure(text, g.strict, meta, esm)); err != nil {
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

// builtins are the specifiers that name a module the runtime has built
// in, as the build helper lists them on its Bun (the kept op): each name
// that node:module's builtinModules lists, alone or after node:, that
// Bun's resolver resolves to a built-in module. Bun 1.3.10 resolves
// node:fs/promises, for example, but not node:ws or node:bun:sqlite, whose
// names it lists only bare. The build requires that Bun to be the pinned
// Bun 1.3.10 unless BunConfig.AllowBunMismatch is set. The runtime
// resolves an import of one to the same module from every file, and a
// require of one loads it as an import does. So neither a static import
// nor an import() of one costs a script its shared layout.
type builtins map[string]bool

// has reports whether an import's specifier names a module the runtime has
// built in: one of the listed specifiers, spelled exactly.
func (b builtins) has(spec string) bool {
	return b[spec]
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
		return "a module it imports keeps an import() that resolves from the file holding it: one whose specifier is not a literal naming a module the runtime has built in"
	case FallbackNamespaceImport:
		return "it and a module it imports both take the namespace object of one ES module, which the shared layout would make two objects"
	case FallbackExportNames:
		return "it imports an ES module whose namespace the shared layout cannot reproduce: it takes the namespace object and the build cannot list all the module's export names, or the module exports __esModule"
	}
	return reason
}

// buildClosure bundles a group's closure: its entry requires each
// dependency module's file, with the group's settings (the first script's
// options with the entry, banner and format replaced), the env rewrite
// when the group has it, the host-provided modules' shims, and, for an ES
// module group, import.meta read from the instance's parameter. It returns
// the output, the name of that parameter, the inputs, and the modules
// whose top-level await failed the build. A bundler replaces only free
// references with a define, so a binding of the parameter's name in a
// dependency, at its top level or in a function, would capture import.meta;
// as with the env rewrite's binding, the name is one no input spells.
func (cfg *CodebaseConfig) buildClosure(g *codebaseGroup, files map[string]string, shims map[string]string) ([]byte, string, metaInputs, map[string]bool, error) {
	o, err := g.settings.CLIOptions(cfg.Folder, g.scripts[0].File)
	if err != nil {
		return nil, "", nil, nil, err
	}
	o.EntryPoints = []string{closureEntry}
	o.Format = api.FormatCommonJS
	o.Metafile = true
	if o.Define == nil {
		o.Define = map[string]string{}
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
		meta := closureMeta
		if n > 1 {
			meta = fmt.Sprintf("%s_%d", closureMeta, n)
		}
		if g.settings.Format != CJS {
			o.Define["import.meta"] = meta
		}
		support, binding := "", ""
		if g.env {
			binding = EnvBinding
			if n > 1 {
				binding = fmt.Sprintf("%s_%d", EnvBinding, n)
			}
			define, sup, err := EnvRewrite(g.settings.Format)
			if err != nil {
				return nil, "", nil, nil, err
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
					return nil, "", nil, nil, fmt.Errorf("bundle: the closure %s: %s", g.id, esbuildMessages(r.Errors))
				}
				failed[e.Location.File] = true
			}
			return nil, "", nil, failed, nil
		}
		inputs, err := readMetaInputs([]byte(r.Metafile))
		if err != nil {
			return nil, "", nil, nil, err
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
		spelled := false
		for p := range inputs {
			if strings.HasPrefix(p, pluginNamespace+":") || strings.HasPrefix(p, supportNamespace+":") || strings.HasPrefix(p, "<") {
				continue
			}
			full := p
			if !filepath.IsAbs(full) {
				full = filepath.Join(cfg.Folder, filepath.FromSlash(p))
			}
			if b, err := os.ReadFile(full); err == nil && (spells(string(b), meta) || binding != "" && spells(string(b), binding)) {
				spelled = true
				break
			}
		}
		if spelled {
			continue
		}
		return r.OutputFiles[0].Contents, meta, inputs, nil, nil
	}
	return nil, "", nil, nil, fmt.Errorf("bundle: the closure %s: every candidate name for %s or %s occurs in its sources", g.id, closureMeta, EnvBinding)
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
func depPlugin(folder, entry, id string, esm bool, listed map[string]bool, proxies *injectProxySet, isESM func(key, file string) (bool, moduleNames), record func(key, file string)) api.Plugin {
	var mu sync.Mutex
	esmDeps := map[string]moduleNames{} // key → its export names, for an ES module dependency
	own := realPath(filepath.Join(folder, filepath.FromSlash(entry)))
	// esbuild resolves files to their real paths, so a module's key, its
	// path from the sync root as the metafile names it, is taken from the
	// folder's real path, which a folder reached through a symbolic link
	// (a temporary directory on macOS) does not share.
	root := realPath(folder)
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
			rel, err := filepath.Rel(root, r.Path)
			if err != nil {
				return api.OnResolveResult{}, nil
			}
			key := filepath.ToSlash(rel)
			record(key, r.Path)
			if e, names := isESM(key, r.Path); e {
				mu.Lock()
				esmDeps[key] = names
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
			names, esm := esmDeps[a.Path]
			mu.Unlock()
			text := depReference(a.Path)
			if esm {
				text = esmReference(a.Path, names)
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
		names, _, err := moduleExports(folder, abs, s)
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

// starExport matches a star re-export that an ES module output keeps.
var starExport = regexp.MustCompile(`(?m)^export \* from `)

// moduleExports returns the export names of an ES module file, read from
// the metafile of an esbuild build of it alone, with the group's loaders
// and its packages left external, and whether they are all of them: a
// star re-export of an external module stays in the output as one, and
// its names are not listed.
func moduleExports(folder, file string, s *CodebaseSettings) ([]string, bool, error) {
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
		return nil, false, fmt.Errorf("%s", esbuildMessages(r.Errors))
	}
	complete := len(r.OutputFiles) > 0 && !starExport.Match(r.OutputFiles[0].Contents)
	var meta struct {
		Inputs  map[string]struct{ Format string } `json:"inputs"`
		Outputs map[string]struct {
			EntryPoint string   `json:"entryPoint"`
			Exports    []string `json:"exports"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal([]byte(r.Metafile), &meta); err != nil {
		return nil, false, err
	}
	for _, o := range meta.Outputs {
		if o.EntryPoint == "" {
			continue
		}
		if meta.Inputs[o.EntryPoint].Format == "cjs" {
			return nil, false, nil
		}
		return slices.Sorted(slices.Values(o.Exports)), complete, nil
	}
	return nil, false, nil
}
