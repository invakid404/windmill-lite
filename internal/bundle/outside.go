package bundle

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/syncfolder"
)

// ProblemUnknownHostModule: the host contract lists a module that is
// neither the file of a Bun script of the folder nor a file of a codebase
// entry's relative_path outside it.
const ProblemUnknownHostModule = "host_module_unknown"

// OutsideHostModule is a host-provided module outside the sync folder
// (ADR 0010, milestone F3h): a file of a codebase entry's relative_path,
// which may lie outside the sync root (WR-49), that the entry's scripts
// import and esbuild bundles (WR-47). The host contract names it by its
// path relative to the sync root, such as ../lib/ctx.ts. Only codebase
// scripts can import it, since a Bun script's relative imports stay in the
// folder (WR-28), so it is built as they are, with esbuild: its bundled
// copy with its entry's settings, in ES module format, with the env
// rewrite, and every other listed module resolved to its shim.
type OutsideHostModule struct {
	// ID names its files, as HostModule.ID does (OutsideID).
	ID string
	// Key is the module as the host contract names it: its path relative
	// to the sync root, which leaves the folder; it is also File.
	Key string
	// Settings is the codebases entry whose relative_path holds the file,
	// as JSON (syncfolder.Codebase.Settings).
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

// OutsideConfig describes the build of the host-provided modules outside
// the folder.
type OutsideConfig struct {
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

	Modules []OutsideHostModule
	// Inside are the other host-provided modules, with the shims BuildBun
	// generated, which an outside module's imports of them resolve to.
	Inside []CodebaseHostModule
}

// OutsideResult is what the build of the outside modules produced.
type OutsideResult struct {
	// HostModules holds each module's metadata, by ID, and Order the IDs
	// in the order a harness registers them: a module after the outside
	// modules it imports (the inside ones, which never import an outside
	// one, are all registered before).
	HostModules map[string]*HostModuleMeta
	Order       []string
	// Inputs are the files of the folder the copies read (folderInputs),
	// sorted: sources of the bundle, as a codebase script's are. The
	// modules' own files lie outside the folder.
	Inputs []string
}

// BuildOutside builds the host-provided modules outside the folder: it
// checks each module's source with the build helper (an export a shim
// cannot follow, host_module_live_binding), learns which other listed
// modules each imports, and then, in that order, bundles each with
// esbuild, the module itself as the entry, which gives its export names
// (an entry that is CommonJS source has none a shim can take,
// host_module_commonjs), writes the bundle as its copy and generates its
// shim from those names. Problems are returned in a *BuildError, by item
// "host module <key>".
func BuildOutside(ctx context.Context, cfg OutsideConfig) (*OutsideResult, error) {
	res := &OutsideResult{HostModules: map[string]*HostModuleMeta{}}
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
		if cfg.Work, err = os.MkdirTemp("", "windmill-lite-outside-"); err != nil {
			return nil, err
		}
		defer os.RemoveAll(cfg.Work)
	} else if cfg.Work, err = filepath.Abs(cfg.Work); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.Work, 0o755); err != nil {
		return nil, err
	}
	item := func(m OutsideHostModule) string { return "host module " + m.Key }

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

	// Which listed modules each imports, from a build in which the
	// outside modules' shims are not generated yet: each is a CommonJS
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
	for _, m := range cfg.Modules {
		_, _, used, _, p := cc.esbuild(CodebaseScript{ID: m.ID, Path: m.Key, File: m.Key}, settings[m.ID], "", shims, nil)
		if p != nil {
			problems = append(problems, ItemProblem{item(m), *p})
			continue
		}
		res.HostModules[m.ID].HostModules = nonNilStrings(used)
		mods = append(mods, HostModule{ID: m.ID, Key: m.Key})
	}
	if len(problems) > 0 {
		return res, &BuildError{Problems: problems}
	}
	order, err := hostModuleOrder(mods, res.HostModules)
	if err != nil {
		return res, &BuildError{Problems: []ItemProblem{{"host modules", Problem{ProblemHostModuleCycle, err.Error()}}}}
	}
	res.Order = order

	// Each copy, after the modules it imports, with their shims.
	byID := map[string]OutsideHostModule{}
	for _, m := range cfg.Modules {
		byID[m.ID] = m
	}
	for _, id := range order {
		m := byID[id]
		out, p := cc.outsideCopy(m, settings[id], shims)
		if p != nil {
			problems = append(problems, ItemProblem{item(m), *p})
			continue
		}
		res.HostModules[id].Exports = out.exports
		for _, p := range folderInputs(out.metafile) {
			if !slices.Contains(res.Inputs, p) {
				res.Inputs = append(res.Inputs, p)
			}
		}
		shim, err := Shim(m.Key, out.exports)
		if err != nil {
			return nil, err
		}
		if err := writeFile(shimFile(cfg.Out, id), shim); err != nil {
			return nil, err
		}
		shims[m.Key] = string(shim)
		if err := writeFile(filepath.Join(cfg.Out, "scripts", "bun", "provided", filepath.FromSlash(id)+".mjs"), out.text); err != nil {
			return nil, err
		}
	}
	if len(problems) > 0 {
		return res, &BuildError{Problems: problems}
	}
	slices.Sort(res.Inputs)
	return res, nil
}

// outsideCopy bundles the bundled copy of one outside module: the module
// as the entry, with the env rewrite under the first binding no input of
// the bundle spells, and the shims of the other listed modules.
func (cfg *CodebaseConfig) outsideCopy(m OutsideHostModule, s *CodebaseSettings, shims map[string]string) (*esbuildOutput, *Problem) {
	for n := 1; n <= 20; n++ {
		binding := EnvBinding
		if n > 1 {
			binding = fmt.Sprintf("%s_%d", EnvBinding, n)
		}
		out, inputs, _, _, p := cfg.esbuild(CodebaseScript{ID: m.ID, Path: m.Key, File: m.Key}, s, binding, shims, nil)
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
func outsideModule(root, key string, codebases []syncfolder.Codebase) (m OutsideHostModule, ok bool, problem string) {
	rel := path.Clean(key)
	if rel != key || !strings.HasPrefix(rel, "../") {
		return OutsideHostModule{}, false, ""
	}
	abs := filepath.Join(root, filepath.FromSlash(rel))
	fi, err := os.Stat(abs)
	if err != nil {
		return OutsideHostModule{}, true, fmt.Sprintf("unknown host-provided module %s: it leaves the folder, and %v", key, err)
	}
	if !fi.Mode().IsRegular() {
		return OutsideHostModule{}, true, fmt.Sprintf("unknown host-provided module %s: it leaves the folder, and it is not a file", key)
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
		return OutsideHostModule{}, true, fmt.Sprintf(
			"unknown host-provided module %s: it leaves the folder, and no codebases entry's relative_path holds it", key)
	}
	return OutsideHostModule{ID: OutsideID(rel), Key: key, Settings: codebases[best].Settings}, true, ""
}
