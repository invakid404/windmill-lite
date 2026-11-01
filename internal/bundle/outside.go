package bundle

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/syncfolder"
)

// ProblemUnknownHostModule: the host contract lists a module that is
// neither the file of a Bun script of the folder nor a file of a codebase
// entry's relative_path outside it.
const ProblemUnknownHostModule = "host_module_unknown"

// EsbuildHostModule is a host-provided module that esbuild builds, as it
// builds the codebase scripts that import it (ADR 0010), rather than the
// Bun build. It is one of two kinds:
//
//   - a file outside the sync folder, in a codebase entry's relative_path,
//     which may lie outside the sync root ([WR-49]). The host contract names
//     it by its path relative to the sync root, such as ../lib/ctx.ts. Only
//     codebase scripts can import it, since a Bun script's relative imports
//     stay in the folder ([WR-28]);
//   - a codebase script of the folder, which a codebases entry selects. Its
//     shared copy is built with that entry's settings, as the script's own
//     bundle is ([WR-47]), so a Bun script cannot import it.
//
// Its bundled copy is built with the entry's settings, in ES module format,
// with the env rewrite, and with every other listed module resolved to its
// shim.
//
// [WR-28]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/languages.md#wr-28
// [WR-47]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/languages.md#wr-47
// [WR-49]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/languages.md#wr-49
type EsbuildHostModule struct {
	// ID names its files, as HostModule.ID does: OutsideID's for a file
	// outside the folder, and the script's Windmill path for a codebase
	// script.
	ID string
	// Key is the module as the host contract names it: its path relative
	// to the sync root; it is also its file.
	Key string
	// Settings is the codebases entry that selects the script, or whose
	// relative_path holds the file outside the folder, as JSON
	// (syncfolder.Codebase.Settings).
	Settings json.RawMessage
}

// OutsideID returns the id of the host-provided module at rel, a clean
// slash path relative to the sync root that leaves it: "outside/<n>/" and
// the rest of rel, where n is the number of leading "..", so that ids stay
// distinct and never take the form of a Windmill path (u/, f/, g/).
func OutsideID(rel string) string {
	n := 0
	for strings.HasPrefix(rel, "../") {
		rel, n = rel[3:], n+1
	}
	return "outside/" + strconv.Itoa(n) + "/" + rel
}

// EsbuildHostConfig describes the build of the host-provided modules that
// esbuild builds.
type EsbuildHostConfig struct {
	// Bun, Helper, AllowBunMismatch, Env, Diagnostics and Timeout are those
	// of BunConfig: the build helper parses each module's source.
	Bun              string
	Helper           string
	AllowBunMismatch bool
	Env              []string
	Diagnostics      io.Writer
	Timeout          time.Duration
	// Folder is the sync root; Out the bundle directory, where each
	// module's copy and shim go (scripts/bun/provided/<ID>.mjs and
	// scripts/bun/provided/shims/<ID>.mjs); Work a scratch directory.
	Folder string
	Out    string
	Work   string

	Modules []EsbuildHostModule
	// Inside are the host-provided modules of the Bun build, with the shims
	// BuildBun generated, which the modules' imports of them resolve to.
	Inside []CodebaseHostModule
}

// EsbuildHostResult is what the build of the modules produced.
type EsbuildHostResult struct {
	// HostModules holds each module's metadata, by ID, and Order the IDs
	// in the order a harness registers them: a module after the modules of
	// this build that it imports. The modules of the Bun build never import
	// one of these, and are all registered before.
	HostModules map[string]*HostModuleMeta
	Order       []string
	// Inputs are the files of the folder the copies read (folderInputs),
	// sorted: sources of the bundle, as a codebase script's are. The files
	// of modules outside the folder are not among them.
	Inputs []string
}

// BuildEsbuildHostModules builds the host-provided modules that esbuild
// builds. It checks each module's source with the build helper (an export
// a shim cannot follow, host_module_live_binding; a file with no loader is
// not read, and esbuild's refusal of it is its problem), learns which
// other listed modules each imports, and then, in that order, bundles each
// with esbuild, the module itself as the entry, which gives its export
// names (an entry that is CommonJS source has none a shim can take,
// host_module_commonjs). It writes the bundle as the module's copy and
// generates its shim from those names. Problems are returned in a
// *BuildError, by item "host module <key>".
func BuildEsbuildHostModules(ctx context.Context, cfg EsbuildHostConfig) (*EsbuildHostResult, error) {
	res := &EsbuildHostResult{HostModules: map[string]*HostModuleMeta{}}
	if len(cfg.Modules) == 0 {
		return res, nil
	}
	var err error
	for _, p := range []*string{&cfg.Folder, &cfg.Out, &cfg.Helper} {
		if *p, err = filepath.Abs(*p); err != nil {
			return nil, err
		}
	}
	if cfg.Work == "" {
		if cfg.Work, err = os.MkdirTemp("", "windmill-lite-esbuild-host-"); err != nil {
			return nil, err
		}
		defer os.RemoveAll(cfg.Work)
	} else if cfg.Work, err = filepath.Abs(cfg.Work); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.Work, 0o755); err != nil {
		return nil, err
	}
	item := func(m EsbuildHostModule) string { return "host module " + m.Key }

	// The helper's parse of each source.
	bc := BunConfig{
		Bun: cfg.Bun, Helper: cfg.Helper, AllowBunMismatch: cfg.AllowBunMismatch, Folder: cfg.Folder,
		Out: cfg.Out, Work: cfg.Work, Env: cfg.Env, Diagnostics: cfg.Diagnostics, Timeout: cfg.Timeout,
	}
	req := helperRequest{
		Version: 1, Op: "parse", Folder: cfg.Folder, Work: cfg.Work, Out: cfg.Out,
		Scripts: map[string]string{}, Deps: map[string]string{}, Define: map[string]string{},
		HostModules: []helperHostModule{}, Entries: []helperEntry{},
	}
	if !cfg.AllowBunMismatch {
		req.RequireBun = harness.PinnedBun
	}
	// Each module's settings, and the syntax of its source: that of the
	// loader esbuild bundles it with.
	var problems []ItemProblem
	settings := map[string]*CodebaseSettings{}
	for _, m := range cfg.Modules {
		s, err := ParseCodebaseSettings(m.Settings)
		if err != nil {
			problems = append(problems, ItemProblem{item(m), Problem{ProblemBundleFailed, err.Error()}})
			continue
		}
		// The copy is an ES module, as every bundled copy is
		// (scripts/bun/provided/<ID>.mjs).
		s.Format = ESM
		settings[m.ID] = s
		req.HostModules = append(req.HostModules, helperHostModule{ID: m.ID, Key: m.Key,
			Source: filepath.Join(cfg.Folder, filepath.FromSlash(m.Key)), Loader: s.loaderFor(m.Key)})
	}
	if len(problems) > 0 {
		return res, &BuildError{Problems: problems}
	}
	resp, err := runHelper(ctx, &bc, &req)
	if err != nil {
		return nil, err
	}
	for _, m := range cfg.Modules {
		r, ok := resp.HostModules[m.ID]
		switch {
		case !ok:
			problems = append(problems, ItemProblem{item(m), Problem{"internal", "the parse did not report it"}})
			continue
		case r.Error != nil:
			problems = append(problems, ItemProblem{item(m), *r.Error})
			continue
		}
		res.HostModules[m.ID] = &HostModuleMeta{Key: m.Key, Packages: nonNilStrings(r.Packages)}
	}
	if len(problems) > 0 {
		return res, &BuildError{Problems: problems}
	}

	// Which listed modules each imports, from a build in which the shims
	// of this build's modules are not generated yet: each is a CommonJS
	// placeholder, from which esbuild lets an import take any name.
	all := append([]CodebaseHostModule(nil), cfg.Inside...)
	shims := map[string]string{}
	for _, m := range cfg.Inside {
		b, err := os.ReadFile(m.Shim)
		if err != nil {
			return nil, err
		}
		shims[m.Key] = string(b)
	}
	for _, m := range cfg.Modules {
		all = append(all, CodebaseHostModule{Key: m.Key, File: m.Key, Shim: shimFile(cfg.Out, m.ID)})
		shims[m.Key] = "module.exports = {};\n"
	}
	cc := &CodebaseConfig{Folder: cfg.Folder, HostModules: all}
	mods := make([]HostModule, 0, len(cfg.Modules))
	graphs := map[string]*importGraph{}
	for _, m := range cfg.Modules {
		out, _, used, _, p := cc.esbuild(CodebaseScript{ID: m.ID, Path: m.Key, File: m.Key}, settings[m.ID], "", shims, nil)
		if p != nil {
			problems = append(problems, ItemProblem{item(m), *p})
			continue
		}
		res.HostModules[m.ID].HostModules = nonNilStrings(used)
		res.HostModules[m.ID].LoadHostModules = []string{}
		if len(used) > 0 {
			g, err := esbuildGraph(cfg.Folder, m.Key, out.metafile, all)
			if err != nil {
				return nil, err
			}
			graphs[m.ID] = &g
		}
		mods = append(mods, HostModule{ID: m.ID, Key: m.Key})
	}
	if len(problems) > 0 {
		return res, &BuildError{Problems: problems}
	}
	if err := lazyOfGraphs(ctx, &bc, &req, graphs, settings); err != nil {
		return nil, err
	}
	for id, g := range graphs {
		res.HostModules[id].LoadHostModules = loadTimeHostModules(*g, res.HostModules[id].HostModules)
	}
	order, late, err := hostModuleOrder(mods, res.HostModules)
	if err != nil {
		return res, &BuildError{Problems: []ItemProblem{{"host modules", Problem{ProblemHostModuleCycle, err.Error()}}}}
	}
	res.Order = order

	// Each copy, after the modules it imports at load time, with their
	// shims. A module that imports a later one lazily is built again once
	// every copy is: its first copy has that module's placeholder, and the
	// second its late shim (LateShim), whose export names come from that
	// module's copy, or its plain shim when it has no exports.
	byID := map[string]EsbuildHostModule{}
	for _, m := range cfg.Modules {
		byID[m.ID] = m
	}
	copyOf := func(id string, shims map[string]string, extra *api.Plugin) (*esbuildOutput, bool) {
		m := byID[id]
		out, p := cc.esbuildCopy(m, settings[id], shims, extra)
		if p != nil {
			problems = append(problems, ItemProblem{item(m), *p})
			return nil, false
		}
		for _, p := range folderInputs(out.metafile) {
			if !slices.Contains(res.Inputs, p) {
				res.Inputs = append(res.Inputs, p)
			}
		}
		return out, true
	}
	writeCopy := func(id string, out *esbuildOutput) error {
		return writeFile(filepath.Join(cfg.Out, "scripts", "bun", "provided", filepath.FromSlash(id)+".mjs"), out.text)
	}
	for _, id := range order {
		out, ok := copyOf(id, shims, nil)
		if !ok {
			continue
		}
		res.HostModules[id].Exports = out.exports
		shim, err := Shim(byID[id].Key, out.exports)
		if err != nil {
			return nil, err
		}
		if err := writeFile(shimFile(cfg.Out, id), shim); err != nil {
			return nil, err
		}
		shims[byID[id].Key] = string(shim)
		if err := writeCopy(id, out); err != nil {
			return nil, err
		}
	}
	if len(problems) > 0 {
		return res, &BuildError{Problems: problems}
	}
	byKey := map[string]string{}
	for _, m := range cfg.Modules {
		byKey[m.Key] = m.ID
	}
	for _, id := range order {
		if len(late[id]) == 0 {
			continue
		}
		with := maps.Clone(shims)
		helpers := map[string]string{}
		for _, k := range late[id] {
			esm, cjs, ok, err := LateShim(k, res.HostModules[byKey[k]].Exports)
			if err != nil {
				return nil, err
			}
			if ok {
				with[k], helpers[k] = string(esm), string(cjs)
			}
		}
		out, ok := copyOf(id, with, latePlugin(helpers))
		if !ok {
			continue
		}
		if !sameNames(out.exports, res.HostModules[id].Exports) {
			problems = append(problems, ItemProblem{item(byID[id]), Problem{ProblemExportsDiffer, fmt.Sprintf(
				"its copy with late shims exports %v, without them %v", out.exports, res.HostModules[id].Exports)}})
			continue
		}
		if err := writeCopy(id, out); err != nil {
			return nil, err
		}
	}
	if len(problems) > 0 {
		return res, &BuildError{Problems: problems}
	}
	slices.Sort(res.Inputs)
	return res, nil
}

// latePlugin resolves the specifier by which a late shim's ES module
// imports its CommonJS module (lateSpecifier) to that module's source,
// helpers, by the key of the host-provided module. It goes before the
// build's own plugin, which resolves nothing that its modules import.
func latePlugin(helpers map[string]string) *api.Plugin {
	return &api.Plugin{Name: "windmill-lite-late", Setup: func(b api.PluginBuild) {
		b.OnResolve(api.OnResolveOptions{Filter: "^" + regexp.QuoteMeta(lateSpecifier)}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			key := strings.TrimPrefix(a.Path, lateSpecifier)
			if _, ok := helpers[key]; !ok {
				return api.OnResolveResult{}, fmt.Errorf("no late shim of the host-provided module %s", key)
			}
			return api.OnResolveResult{Path: "late/" + key, Namespace: pluginNamespace}, nil
		})
		b.OnLoad(api.OnLoadOptions{Filter: "^late/", Namespace: pluginNamespace}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			text := helpers[strings.TrimPrefix(a.Path, "late/")]
			return api.OnLoadResult{Contents: &text, Loader: api.LoaderJS}, nil
		})
	}}
}

// esbuildGraph is the import graph of an esbuild bundle of the listed
// module whose file is entry (relative to the folder), from the bundle's
// metafile. esbuild names each file by its path relative to the folder,
// and resolves each import to the file it names, with the specifier as
// original when the two differ.
//
// An import resolves to a listed module when
// it names that module's file (whose source the build's plugin replaced
// with the module's shim) or its plugin path (host/<key>, a package).
// Imports esbuild left external load nothing of the bundle and are left
// out.
func esbuildGraph(folder, entry string, metafile json.RawMessage, listed []CodebaseHostModule) (importGraph, error) {
	var mf struct {
		Inputs map[string]struct {
			Imports []struct {
				Path     string `json:"path"`
				Kind     string `json:"kind"`
				Original string `json:"original"`
				External bool   `json:"external"`
			} `json:"imports"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(metafile, &mf); err != nil {
		return importGraph{}, fmt.Errorf("bundle: esbuild's metafile: %w", err)
	}
	real := func(p string) string {
		if !filepath.IsAbs(p) {
			p = filepath.Join(folder, filepath.FromSlash(p))
		}
		return realPath(p)
	}
	own := real(entry)
	byFile, byPlugin := map[string]string{}, map[string]string{}
	for _, m := range listed {
		if m.Package != "" {
			byPlugin[pluginNamespace+":host/"+m.Key] = m.Key
		} else if p := real(m.File); p != own {
			byFile[p] = m.Key
		}
	}
	g := importGraph{Entry: filepath.ToSlash(entry), Imports: []moduleImport{}, Lazy: map[string][]string{}}
	for _, from := range slices.Sorted(maps.Keys(mf.Inputs)) {
		if !strings.Contains(from, ":") && real(from) == own {
			g.Entry = from
		}
		for _, im := range mf.Inputs[from].Imports {
			if im.External {
				continue
			}
			spec := im.Original
			if spec == "" {
				spec = im.Path
			}
			mi := moduleImport{From: from, Kind: im.Kind, Specifier: spec}
			if key, ok := byPlugin[im.Path]; ok {
				mi.Host = key
			} else if key, ok := byFile[real(im.Path)]; ok && !strings.Contains(im.Path, ":") {
				mi.Host = key
			} else {
				mi.To = im.Path
			}
			g.Imports = append(g.Imports, mi)
		}
	}
	return g, nil
}

// lazyOfGraphs fills in each graph's lazy imports: the build helper parses
// every module of the folder or its packages that holds an import(), with
// the syntax of the loader esbuild read it with (that of the codebase entry
// whose settings built the graph's module).
//
// A module of another loader holds no JavaScript to parse. A module that
// two graphs read with two syntaxes is parsed with the first: the other
// graph takes it as importing nothing lazily, the reading that orders more.
func lazyOfGraphs(ctx context.Context, bc *BunConfig, base *helperRequest, graphs map[string]*importGraph, settings map[string]*CodebaseSettings) error {
	type use struct{ id, input, file string }
	var uses []use
	syntaxes := map[string]string{} // absolute path → the syntax it is parsed with
	var mods []helperLazyModule
	for _, id := range slices.Sorted(maps.Keys(graphs)) {
		seen := map[string]bool{}
		for _, im := range graphs[id].Imports {
			if im.Kind != "dynamic-import" || seen[im.From] || strings.Contains(im.From, ":") {
				continue
			}
			seen[im.From] = true
			syntax := settings[id].loaderFor(im.From)
			if syntax != "ts" && syntax != "tsx" && syntax != "js" && syntax != "jsx" {
				continue
			}
			file := im.From
			if !filepath.IsAbs(file) {
				file = filepath.Join(bc.Folder, filepath.FromSlash(file))
			}
			switch had, ok := syntaxes[file]; {
			case !ok:
				syntaxes[file] = syntax
				mods = append(mods, helperLazyModule{File: file, Syntax: syntax})
			case had != syntax:
				continue
			}
			uses = append(uses, use{id, im.From, file})
		}
	}
	if len(mods) == 0 {
		return nil
	}
	req := *base
	req.Op, req.HostModules, req.Lazy = "lazy", []helperHostModule{}, mods
	resp, err := runHelper(ctx, bc, &req)
	if err != nil {
		return err
	}
	for _, u := range uses {
		graphs[u.id].Lazy[u.input] = resp.Lazy[u.file]
	}
	return nil
}

// esbuildCopy bundles the copy of one host-provided module that esbuild
// builds: the module as the entry, with the env rewrite under the first
// binding no input of the bundle spells, and the shims of the other listed
// modules (some of them late shims, which extra resolves the imports of).
func (cfg *CodebaseConfig) esbuildCopy(m EsbuildHostModule, s *CodebaseSettings, shims map[string]string, extra *api.Plugin) (*esbuildOutput, *Problem) {
	for n := 1; n <= 20; n++ {
		binding := EnvBinding
		if n > 1 {
			binding = fmt.Sprintf("%s_%d", EnvBinding, n)
		}
		out, inputs, _, _, p := cfg.esbuild(CodebaseScript{ID: m.ID, Path: m.Key, File: m.Key}, s, binding, shims, extra)
		if p != nil {
			return nil, p
		}
		spelled := false
		for _, in := range inputs {
			if spells(in, binding) {
				spelled = true
				break
			}
		}
		if spelled {
			continue
		}
		if out.commonJS {
			return nil, &Problem{ProblemHostModuleCommonJS, fmt.Sprintf(
				"host-provided module %s is CommonJS; a host-provided module must be an ES module", m.Key)}
		}
		return out, nil
	}
	return nil, &Problem{ProblemBundleFailed, fmt.Sprintf("every candidate name for %s occurs in the bundle's sources", EnvBinding)}
}

// shimFile is where the shim of the host-provided module id goes below the
// bundle directory out.
func shimFile(out, id string) string {
	return filepath.Join(out, "scripts", "bun", "provided", "shims", filepath.FromSlash(id)+".mjs")
}

// outsideModule resolves key, a host contract's host_modules entry, as a
// module outside the folder root: a clean relative path that leaves the
// root, of a regular file inside the relative_path directory of one of
// codebases (relative to the root, as wmill.yaml gives it). It returns the
// module, or why key is not one; ok is false when key does not leave the
// root, so it is something else.
func outsideModule(root, key string, codebases []syncfolder.Codebase) (m EsbuildHostModule, ok bool, problem string) {
	rel := path.Clean(key)
	if rel != key || !strings.HasPrefix(rel, "../") {
		return EsbuildHostModule{}, false, ""
	}
	abs := filepath.Join(root, filepath.FromSlash(rel))
	fi, err := os.Stat(abs)
	if err != nil {
		return EsbuildHostModule{}, true, fmt.Sprintf("unknown host-provided module %s: it leaves the folder, and %v", key, err)
	}
	if !fi.Mode().IsRegular() {
		return EsbuildHostModule{}, true, fmt.Sprintf("unknown host-provided module %s: it leaves the folder, and it is not a file", key)
	}
	// The codebase entry whose relative_path holds the file, the deepest
	// one when several do.
	best, depth := -1, -1
	for i, c := range codebases {
		if c.RelativePath == "" {
			continue
		}
		dir := filepath.Join(root, filepath.FromSlash(c.RelativePath))
		r, err := filepath.Rel(dir, abs)
		if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			continue
		}
		if d := len(strings.Split(filepath.Clean(dir), string(filepath.Separator))); d > depth {
			best, depth = i, d
		}
	}
	if best < 0 {
		return EsbuildHostModule{}, true, fmt.Sprintf(
			"unknown host-provided module %s: it leaves the folder, and no codebases entry's relative_path holds it", key)
	}
	return EsbuildHostModule{ID: OutsideID(rel), Key: key, Settings: codebases[best].Settings}, true, ""
}
