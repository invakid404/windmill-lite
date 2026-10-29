package bundle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/invakid404/windmill-lite/internal/harness"
)

// EsbuildVersion is the esbuild that bundles codebase scripts: the version
// the pinned CLI uses (WR-47), which go.mod pins for
// github.com/evanw/esbuild. The manifest records it.
const EsbuildVersion = "0.28.0"

// Problem kinds of codebase scripts.
const (
	// ProblemAssetCollision: two codebase scripts extract different
	// content to the same path of the working directory (ADR 0002,
	// decision 3).
	ProblemAssetCollision = "asset_collision"
	// ProblemInvalidAsset: an asset of a codebase entry cannot be read, or
	// its `to` is not a clean relative path.
	ProblemInvalidAsset = "invalid_asset"
)

// CodebaseSettings are the bundling settings of one `codebases` entry of
// wmill.yaml (SY-8, WR-47): what the pinned CLI passes to esbuild, and the
// assets it uploads with the bundle (WR-49).
type CodebaseSettings struct {
	// Format is "cjs", the default, or "esm" (WR-47).
	Format Format
	// External, Inject, Define and Banner are passed to esbuild unchanged;
	// nil means esbuild's default.
	External []string
	Inject   []string
	Define   map[string]string
	Banner   map[string]string
	// Loader replaces the default loader map, {".node": "file"}, entirely;
	// nil keeps the default.
	Loader map[string]string
	Assets []Asset
}

// Asset is one entry of a codebase entry's `assets`: the file From, read
// relative to the sync root, extracted at To in the working directory
// (WR-49, WR-50).
type Asset struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ParseCodebaseSettings reads a codebases entry as wmill.yaml gives it
// (syncfolder.Codebase.Settings). Keys that do not shape the bundle
// (relative_path, includes, excludes) are ignored; customBundler is the
// loader's to refuse (WR-48). A value of the wrong type fails, as the
// CLI's esbuild call would.
func ParseCodebaseSettings(raw json.RawMessage) (*CodebaseSettings, error) {
	var v struct {
		Format   *string           `json:"format"`
		External []string          `json:"external"`
		Inject   []string          `json:"inject"`
		Define   map[string]string `json:"define"`
		Banner   map[string]string `json:"banner"`
		Loader   map[string]string `json:"loader"`
		Assets   []Asset           `json:"assets"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("the codebase entry's settings: %w", err)
		}
	}
	s := &CodebaseSettings{Format: CJS, External: v.External, Inject: v.Inject, Define: v.Define, Banner: v.Banner, Loader: v.Loader, Assets: v.Assets}
	if v.Format != nil {
		switch Format(*v.Format) {
		case CJS, ESM:
			s.Format = Format(*v.Format)
		default:
			return nil, fmt.Errorf("the codebase entry's format %q is neither cjs nor esm", *v.Format)
		}
	}
	return s, nil
}

// loaders are esbuild's loader names, as its JavaScript API takes them.
var loaders = map[string]api.Loader{
	"base64": api.LoaderBase64, "binary": api.LoaderBinary, "copy": api.LoaderCopy, "css": api.LoaderCSS,
	"dataurl": api.LoaderDataURL, "default": api.LoaderDefault, "empty": api.LoaderEmpty, "file": api.LoaderFile,
	"global-css": api.LoaderGlobalCSS, "js": api.LoaderJS, "json": api.LoaderJSON, "jsx": api.LoaderJSX,
	"local-css": api.LoaderLocalCSS, "text": api.LoaderText, "ts": api.LoaderTS, "tsx": api.LoaderTSX,
}

// CLIOptions returns the esbuild options with which the pinned CLI
// bundles the script file (relative to root, the sync root, which is the
// CLI's working directory) under these settings (WR-47): the file as the
// single entry point, bundled for platform node with packages inlined,
// in the entry's format (target node20.15.1 for cjs, esnext for esm), into
// memory under the output directory "/", with the entry's external,
// inject, define and banner, and its loader map in place of the default
// {".node": "file"}. Minification, source maps, code splitting and the
// metafile are left off. This is the reproduction that the CLI parity
// check compares (ADR 0002, decision 4).
func (s *CodebaseSettings) CLIOptions(root, file string) (api.BuildOptions, error) {
	o := api.BuildOptions{
		EntryPoints:   []string{file},
		AbsWorkingDir: root,
		Bundle:        true,
		Platform:      api.PlatformNode,
		Packages:      api.PackagesBundle,
		Outdir:        "/",
		Write:         false,
		LogLevel:      api.LogLevelSilent,
		External:      slices.Clone(s.External),
		Inject:        slices.Clone(s.Inject),
		Define:        cloneMap(s.Define),
		Banner:        cloneMap(s.Banner),
		Loader:        map[string]api.Loader{".node": api.LoaderFile},
	}
	switch s.Format {
	case CJS:
		// The JavaScript API's target "node20.15.1" is an engine, with the
		// language target left at its default.
		o.Format = api.FormatCommonJS
		o.Engines = []api.Engine{{Name: api.EngineNode, Version: "20.15.1"}}
	case ESM:
		o.Format = api.FormatESModule
		o.Target = api.ESNext
	default:
		return api.BuildOptions{}, fmt.Errorf("bundle: unknown format %q", s.Format)
	}
	if s.Loader != nil {
		o.Loader = map[string]api.Loader{}
		for ext, name := range s.Loader {
			l, ok := loaders[name]
			if !ok {
				return api.BuildOptions{}, fmt.Errorf("the codebase entry's loader %q for %q is not one of esbuild's", name, ext)
			}
			o.Loader[ext] = l
		}
	}
	return o, nil
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// CLIBundle bundles one codebase script exactly as the pinned CLI does
// (CLIOptions), without windmill-lite's transforms, and returns the
// output files by their path below "/". It is the reproduction the CLI
// parity check compares with the CLI's upload.
func (s *CodebaseSettings) CLIBundle(root, file string) (map[string][]byte, error) {
	o, err := s.CLIOptions(root, file)
	if err != nil {
		return nil, err
	}
	r := api.Build(o)
	if len(r.Errors) > 0 {
		return nil, errors.New(esbuildMessages(r.Errors))
	}
	out := map[string][]byte{}
	for _, f := range r.OutputFiles {
		out[strings.TrimPrefix(filepath.ToSlash(f.Path), "/")] = f.Contents
	}
	return out, nil
}

// bundleExports returns the export names of a codebase script's bundle:
// what its entry exports, including what it re-exports with export *, as
// esbuild links the modules. A CommonJS bundle's module.exports takes the
// same names, so they are read from the metafile of an ES module build of
// the same entry, with the same plugin as the real build, so that an
// import of a host-provided module resolves to its shim there too (c2b
// review 2, finding 2).
//
// known is false when no static list exists: the entry is CommonJS source,
// whose names are its exports object's properties, known only when it
// runs (an ES module build would list only its wrapper's default; c2b
// review 2, finding 4), or the build failed, and err says why. The
// harness then gives any callable export the metadata does not sign an
// empty parameter list, as Windmill gives a function its parser does not
// find in the source (WR-6, WR-7).
//
// The helper records a signature for each name: the entry source's own
// parameter list for a function it declares, and none (an empty list) for
// a name it does not declare, such as one export * brings in. A
// dependency's declarations are never read as the entry's (WR-6, WR-7).
func (cfg *CodebaseConfig) bundleExports(e CodebaseScript, s *CodebaseSettings, shims map[string]string) (names []string, known bool, err error) {
	o, err := s.CLIOptions(cfg.Folder, e.File)
	if err != nil {
		return nil, false, err
	}
	o.Format, o.Target, o.Engines, o.Metafile = api.FormatESModule, api.ESNext, nil, true
	o.Plugins = []api.Plugin{cfg.plugin("", "", shims, nil)}
	r := api.Build(o)
	if len(r.Errors) > 0 {
		return nil, false, errors.New(esbuildMessages(r.Errors))
	}
	var mf struct {
		Inputs map[string]struct {
			Format string `json:"format"`
		} `json:"inputs"`
		Outputs map[string]struct {
			EntryPoint string   `json:"entryPoint"`
			Exports    []string `json:"exports"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal([]byte(r.Metafile), &mf); err != nil {
		return nil, false, err
	}
	for _, out := range mf.Outputs {
		if out.EntryPoint == "" {
			continue
		}
		if mf.Inputs[out.EntryPoint].Format == "cjs" {
			return nil, false, nil
		}
		names := slices.Clone(out.Exports)
		if names == nil {
			names = []string{}
		}
		slices.Sort(names)
		return names, true, nil
	}
	return nil, false, errors.New("the metafile names no entry point")
}

// CodebaseScript is one codebase script to bundle (SY-8).
type CodebaseScript struct {
	// ID names its files in the bundle, as BunScript.ID does:
	// scripts/bun/<ID>.cjs (or .mjs for ESM) and <ID>.meta.json.
	ID string
	// Path is its Windmill path, which is its import base.
	Path string
	// File is its source file, relative to the folder.
	File string
	// Settings is the codebases entry that selects it, as JSON
	// (syncfolder.Codebase.Settings).
	Settings json.RawMessage
}

// CodebaseHostModule is a host-provided module as codebase bundles see it
// (ADR 0010, D12): its key, the folder file or package that imports of it
// name, and the shim BuildBun generated for it, which those imports
// resolve to.
type CodebaseHostModule struct {
	Key     string
	File    string
	Package string
	// Shim is the shim's file (BuildBun's scripts/bun/provided/shims/<id>.mjs).
	Shim string
}

// CodebaseConfig describes the codebase side of one bundle build.
type CodebaseConfig struct {
	// Bun, Helper, AllowBunMismatch, Env, Diagnostics and Timeout are
	// those of BunConfig: the build helper parses each script's source for
	// its annotations and signatures.
	Bun              string
	Helper           string
	AllowBunMismatch bool
	Env              []string
	Diagnostics      io.Writer
	Timeout          time.Duration
	// Folder is the sync root, the directory esbuild resolves the entry,
	// inject paths and asset sources from, as the CLI does from its working
	// directory; Out the bundle directory; Work a scratch directory for
	// the helper, a fresh temporary one when empty.
	Folder string
	Out    string
	Work   string

	Entries     []CodebaseScript
	HostModules []CodebaseHostModule
}

// CodebaseResult is what a codebase build produced.
type CodebaseResult struct {
	// BunVersion is the version of the Bun that parsed the sources;
	// NonconformantRuntime is set when it is not the pin.
	BunVersion           string
	NonconformantRuntime bool
	// Scripts holds the metadata of every script, by ID, including those
	// with a problem (with what was recorded before it).
	Scripts map[string]*ScriptMeta
	// Assets holds every asset the bundle carries, by its path in the
	// working directory, with the source file it was read from, relative
	// to the folder.
	Assets   map[string]string
	Warnings []Warning
}

func (cfg *CodebaseConfig) validate() error {
	if cfg.Bun == "" || cfg.Helper == "" || cfg.Folder == "" || cfg.Out == "" {
		return errors.New("bundle: CodebaseConfig needs Bun, Helper, Folder and Out")
	}
	ids := map[string]bool{}
	for _, e := range cfg.Entries {
		first, _, _ := strings.Cut(e.ID, "/")
		if !validID(e.ID) || ids[strings.ToLower(e.ID)] || strings.EqualFold(first, "provided") {
			return fmt.Errorf("bundle: script id %q is invalid or repeated", e.ID)
		}
		ids[strings.ToLower(e.ID)] = true
		if e.Path == "" || e.File == "" {
			return fmt.Errorf("bundle: codebase script %s needs a path and a file", e.ID)
		}
	}
	for _, m := range cfg.HostModules {
		if m.Key == "" || m.Shim == "" || (m.File == "") == (m.Package == "") {
			return fmt.Errorf("bundle: codebase host module %q needs a key, a shim and either a file or a package", m.Key)
		}
	}
	return nil
}

// BuildCodebase bundles codebase scripts with esbuild through its Go API
// (ADR 0002, ADR 0010 step 3): each with the options the pinned CLI uses
// for its entry (CLIOptions), plus windmill-lite's two transforms, from
// the same single implementation as the Bun build:
//
//   - the env rewrite (EnvRewrite, for the entry's format): its define
//     table, whose keys take precedence over the entry's own, and its
//     support module, injected before the entry's own inject files so that
//     it runs before any code of the bundle. Its binding is EnvBinding, or
//     EnvBinding with a suffix when an input of the bundle spells it, as
//     the Bun helper picks it. Scripts whose annotations select another
//     target than bun (`//native`, `//nodejs`) are not rewritten;
//   - host-provided modules: an import that resolves to a listed module's
//     file, or names a listed package, resolves to its shim instead.
//
// Both reach esbuild through a plugin that only maps paths: the support
// module and the shims load from its `windmill-lite` namespace, so no
// bundle names a file outside the folder. It writes scripts/bun/<ID>.cjs
// (or .mjs) and <ID>.meta.json for each script, and every asset at
// assets/<to>. The build helper parses each source for its annotations,
// workflow-as-code markers and signatures. On problems with scripts it
// returns the result so far and a *BuildError listing them all.
func BuildCodebase(ctx context.Context, cfg CodebaseConfig) (*CodebaseResult, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	var err error
	for _, p := range []*string{&cfg.Folder, &cfg.Out, &cfg.Helper} {
		if *p, err = filepath.Abs(*p); err != nil {
			return nil, err
		}
	}
	if cfg.Work == "" {
		if cfg.Work, err = os.MkdirTemp("", "windmill-lite-codebase-"); err != nil {
			return nil, err
		}
		defer os.RemoveAll(cfg.Work)
	} else if cfg.Work, err = filepath.Abs(cfg.Work); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.Work, 0o755); err != nil {
		return nil, err
	}
	res := &CodebaseResult{Scripts: map[string]*ScriptMeta{}, Assets: map[string]string{}}
	if len(cfg.Entries) == 0 {
		return res, nil
	}

	// What each source records: its annotations, workflow-as-code markers
	// and signatures, from the helper's parse.
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
	shims := map[string]string{} // key → shim source
	for _, m := range cfg.HostModules {
		b, err := os.ReadFile(m.Shim)
		if err != nil {
			return nil, err
		}
		shims[m.Key] = string(b)
	}
	var unknown []Warning
	for _, e := range cfg.Entries {
		h := helperEntry{ID: e.ID, Path: e.Path, File: e.File}
		// The signatures cover what the bundle exports, re-exports through
		// export * included (c2b review 1, finding 2).
		if s, err := ParseCodebaseSettings(e.Settings); err == nil {
			names, known, err := cfg.bundleExports(e, s, shims)
			switch {
			case known:
				h.Exports = names
			case err != nil:
				unknown = append(unknown, Warning{WarningEsbuild, fmt.Sprintf(
					"script %s (%s): its export names could not be read (%v); exports its source does not declare bind no parameters", e.ID, e.Path, err)})
			}
		}
		req.Entries = append(req.Entries, h)
	}
	resp, err := runHelper(ctx, &bc, &req)
	if err != nil {
		return nil, err
	}
	res.BunVersion, res.NonconformantRuntime = resp.BunVersion, resp.BunVersion != harness.PinnedBun

	// An entry that fails is reported, and the others are still built:
	// each entry whose module and assets succeed gets its metadata and
	// assets written, as BuildBun writes the metadata of every script that
	// built, so a caller that runs the scripts that built can run them
	// (c2b review 3, finding 5).
	var problems []ItemProblem
	var built []CodebaseScript
	assets := map[string]assetSource{} // to → where it came from
	for _, e := range cfg.Entries {
		item := "script " + e.ID
		r, ok := resp.Entries[e.ID]
		if !ok {
			problems = append(problems, ItemProblem{item, Problem{"internal", "the parse did not report it"}})
			continue
		}
		meta, err := decodeMeta(r.Meta)
		if err != nil {
			return nil, fmt.Errorf("bundle: script %s: %w", e.ID, err)
		}
		meta.Bundler = "esbuild"
		res.Scripts[e.ID] = meta
		if r.Error != nil {
			problems = append(problems, ItemProblem{item, *r.Error})
			continue
		}
		settings, err := ParseCodebaseSettings(e.Settings)
		if err != nil {
			problems = append(problems, ItemProblem{item, Problem{ProblemBundleFailed, err.Error()}})
			continue
		}
		meta.Format = settings.Format
		// A //native script runs in a realm of its own, which the
		// process-wide registry of host-provided modules does not reach, so
		// it inlines them as the Bun build does for one (D5).
		sh := shims
		if meta.Annotations.Native {
			sh = nil
		}
		if p := cfg.bundleOne(e, settings, meta, sh); p != nil {
			problems = append(problems, ItemProblem{item, *p})
			continue
		}
		own, p := entryAssets(cfg.Folder, e.ID, settings.Assets, assets)
		if p != nil {
			problems = append(problems, ItemProblem{item, *p})
			continue
		}
		for to, a := range own {
			assets[to] = a
		}
		built = append(built, e)
	}
	// A //native script's bundle runs in the native runtime (WR-50): its
	// native form is the helper's, as for every native script.
	var err2 error
	if built, problems, err2 = cfg.nativeForms(ctx, &bc, built, res, problems); err2 != nil {
		return nil, err2
	}
	// Paths that would not extract to one tree (a file that is also a
	// directory of another) collide too. The working directory is the
	// process's, so that is the bundle's problem, not one script's: no
	// asset or metadata is written.
	tree := map[string]string{}
	for to := range assets {
		tree[to] = ""
	}
	if err := portablePaths(tree); err != nil {
		problems = append(problems, ItemProblem{"assets", Problem{ProblemAssetCollision, err.Error()}})
		return res, &BuildError{Problems: problems}
	}
	for to, a := range assets {
		if err := writeFile(filepath.Join(cfg.Out, "assets", filepath.FromSlash(to)), a.data); err != nil {
			return nil, err
		}
		res.Assets[to] = a.from
	}
	res.Warnings = append(res.Warnings, unknown...)
	for _, e := range built {
		meta := res.Scripts[e.ID]
		for _, w := range meta.esbuildWarnings {
			res.Warnings = append(res.Warnings, Warning{WarningEsbuild, fmt.Sprintf("script %s (%s): %s", e.ID, e.Path, w)})
		}
		b, err := json.MarshalIndent(meta, "", "  ")
		if err != nil {
			return nil, err
		}
		if err := writeFile(filepath.Join(cfg.Out, "scripts", "bun", filepath.FromSlash(e.ID)+".meta.json"), append(b, '\n')); err != nil {
			return nil, err
		}
	}
	if len(problems) > 0 {
		return res, &BuildError{Problems: problems}
	}
	return res, nil
}

// nativeForms has the build helper write the native form of each built
// //native codebase script (WR-50), the module the harness runs at the
// native boundary, and records it in the script's metadata. A script whose
// form fails is a problem, and is no longer built.
func (cfg *CodebaseConfig) nativeForms(ctx context.Context, bc *BunConfig, built []CodebaseScript, res *CodebaseResult, problems []ItemProblem) ([]CodebaseScript, []ItemProblem, error) {
	req := helperRequest{
		Version: 1, Op: "native", Folder: cfg.Folder, Work: cfg.Work, Out: cfg.Out,
		Scripts: map[string]string{}, Deps: map[string]string{}, Define: map[string]string{},
		HostModules: []helperHostModule{}, Entries: []helperEntry{},
	}
	if !cfg.AllowBunMismatch {
		req.RequireBun = harness.PinnedBun
	}
	for _, e := range built {
		if b := res.Scripts[e.ID].nativeBundle; b != "" {
			req.Entries = append(req.Entries, helperEntry{ID: e.ID, Path: e.Path, File: e.File, Bundle: b})
		}
	}
	if len(req.Entries) == 0 {
		return built, problems, nil
	}
	resp, err := runHelper(ctx, bc, &req)
	if err != nil {
		return nil, nil, err
	}
	var kept []CodebaseScript
	for _, e := range built {
		meta := res.Scripts[e.ID]
		if meta.nativeBundle == "" {
			kept = append(kept, e)
			continue
		}
		item := "script " + e.ID
		r, ok := resp.Entries[e.ID]
		var form struct {
			Native *NativeMeta `json:"native"`
		}
		switch {
		case !ok:
			problems = append(problems, ItemProblem{item, Problem{"internal", "the native form was not reported"}})
			continue
		case r.Error != nil:
			problems = append(problems, ItemProblem{item, *r.Error})
			continue
		case json.Unmarshal(r.Meta, &form) != nil || form.Native == nil:
			problems = append(problems, ItemProblem{item, Problem{"internal", "the native form has no record"}})
			continue
		}
		form.Native.Imports = nonNilStrings(form.Native.Imports)
		if form.Native.Links == nil {
			form.Native.Links = []NativeLink{}
		}
		meta.Boundary, meta.Native = "native", form.Native
		kept = append(kept, e)
	}
	return kept, problems, nil
}

// WarningEsbuild: esbuild warned while bundling a codebase script, for
// example that import.meta is empty in CommonJS output.
const WarningEsbuild = "esbuild_warning"

// envSpecifier is the specifier the support module of the env rewrite is
// injected by, which the plugin resolves into its namespace.
const envSpecifier = "windmill-lite:env"

// pluginNamespace holds the modules the build supplies itself: the env
// support module and the host-provided modules' shims.
const pluginNamespace = "windmill-lite"

// resolving marks the plugin's own calls of the resolver, which it lets
// through.
type resolving struct{}

// plugin is the plugin of a codebase build, which only maps paths: with
// support, the env rewrite's support module (envSpecifier) loads from its
// namespace under the binding's name; an import that names a listed
// package, or resolves to a listed module's file, resolves to the module's
// shim there, and record, when not nil, is told its key. esbuild calls it
// concurrently.
func (cfg *CodebaseConfig) plugin(support, binding string, shims map[string]string, record func(key string)) api.Plugin {
	byFile := map[string]string{} // real path of a listed module's file → key
	byPackage := map[string]string{}
	for _, m := range cfg.HostModules {
		if m.Package != "" {
			byPackage[m.Package] = m.Key
			continue
		}
		p := filepath.Join(cfg.Folder, filepath.FromSlash(m.File))
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		}
		byFile[p] = m.Key
	}
	shimPath := func(key string) api.OnResolveResult {
		if record != nil {
			record(key)
		}
		return api.OnResolveResult{Path: "host/" + key, Namespace: pluginNamespace}
	}
	return api.Plugin{Name: "windmill-lite", Setup: func(b api.PluginBuild) {
		b.OnResolve(api.OnResolveOptions{Filter: `.*`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			if _, ours := a.PluginData.(resolving); ours {
				return api.OnResolveResult{}, nil
			}
			if a.Path == envSpecifier && support != "" {
				return api.OnResolveResult{Path: "env-" + binding + ".mjs", Namespace: pluginNamespace}, nil
			}
			if a.Namespace == pluginNamespace {
				// The shims and the support module import nothing.
				return api.OnResolveResult{}, nil
			}
			if key, ok := byPackage[a.Path]; ok {
				return shimPath(key), nil
			}
			if len(byFile) == 0 || a.Kind == api.ResolveEntryPoint {
				return api.OnResolveResult{}, nil
			}
			r := b.Resolve(a.Path, api.ResolveOptions{
				Importer: a.Importer, Namespace: a.Namespace, ResolveDir: a.ResolveDir, Kind: a.Kind, With: a.With, PluginData: resolving{},
			})
			if len(r.Errors) > 0 || r.External || r.Namespace != "file" {
				return api.OnResolveResult{}, nil
			}
			if key, ok := byFile[r.Path]; ok {
				return shimPath(key), nil
			}
			return api.OnResolveResult{}, nil
		})
		b.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: pluginNamespace}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			text := support
			if key, ok := strings.CutPrefix(a.Path, "host/"); ok {
				text = shims[key]
			}
			return api.OnLoadResult{Contents: &text, Loader: api.LoaderJS}, nil
		})
	}}
}

// bundleOne bundles one codebase script into cfg.Out and completes its
// metadata, or returns its problem.
func (cfg *CodebaseConfig) bundleOne(e CodebaseScript, s *CodebaseSettings, meta *ScriptMeta, shims map[string]string) *Problem {
	if !inside(e.File) {
		return &Problem{ProblemBundleFailed, fmt.Sprintf("the script file %q leaves the folder", e.File)}
	}
	env := meta.Target == "bun"
	base := EnvBinding
	for n := 1; n <= 20; n++ {
		binding := ""
		if env {
			binding = base
			if n > 1 {
				binding = fmt.Sprintf("%s_%d", base, n)
			}
		}
		out, inputs, used, warnings, p := cfg.esbuild(e, s, binding, shims)
		if p != nil {
			return p
		}
		if binding != "" {
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
		}
		ext := ".mjs"
		if s.Format == CJS {
			ext = ".cjs"
		}
		dst := filepath.Join(cfg.Out, "scripts", "bun", filepath.FromSlash(e.ID)+ext)
		if meta.Annotations.Native {
			// It waits in the work directory for its native form, which
			// is the bundle's module instead.
			dst = filepath.Join(cfg.Work, "native", filepath.FromSlash(e.ID)+".js")
			meta.nativeBundle = dst
		}
		if err := writeFile(dst, out.text); err != nil {
			return &Problem{"internal", err.Error()}
		}
		meta.HostModules = used
		meta.Metafile = out.metafile
		meta.esbuildWarnings = warnings
		return nil
	}
	return &Problem{ProblemBundleFailed, fmt.Sprintf("every candidate name for %s occurs in the bundle's sources", base)}
}

type esbuildOutput struct {
	text     []byte
	metafile json.RawMessage
}

// esbuild runs one build of a codebase script: the CLI's options plus,
// with a binding, the env rewrite, and the host-provided modules' shims.
// It returns the output, the text of every input of the folder (to check
// the binding against), the keys of the host-provided modules used, and
// esbuild's warnings.
func (cfg *CodebaseConfig) esbuild(e CodebaseScript, s *CodebaseSettings, binding string, shims map[string]string) (*esbuildOutput, []string, []string, []string, *Problem) {
	o, err := s.CLIOptions(cfg.Folder, e.File)
	if err != nil {
		return nil, nil, nil, nil, &Problem{ProblemBundleFailed, err.Error()}
	}
	o.Metafile = true
	support := ""
	if binding != "" {
		define, src, err := EnvRewrite(s.Format)
		if err != nil {
			return nil, nil, nil, nil, &Problem{"internal", err.Error()}
		}
		if o.Define == nil {
			o.Define = map[string]string{}
		}
		for k, v := range define {
			o.Define[k] = strings.ReplaceAll(v, EnvBinding, binding)
		}
		support = strings.ReplaceAll(src, EnvBinding, binding)
		o.Inject = append([]string{envSpecifier}, o.Inject...)
	}
	// esbuild runs the plugin's callbacks concurrently, one per import
	// being resolved, so what they record is kept under a lock.
	var mu sync.Mutex
	used := map[string]bool{}
	o.Plugins = []api.Plugin{cfg.plugin(support, binding, shims, func(key string) {
		mu.Lock()
		used[key] = true
		mu.Unlock()
	})}
	r := api.Build(o)
	if len(r.Errors) > 0 {
		return nil, nil, nil, nil, &Problem{ProblemBundleFailed, esbuildMessages(r.Errors)}
	}
	if len(r.OutputFiles) != 1 {
		// Several output files (a `file` loader, a native add-on) make the
		// CLI upload a tar whose layout is `later` (WR-49).
		var names []string
		for _, f := range r.OutputFiles {
			names = append(names, filepath.ToSlash(f.Path))
		}
		return nil, nil, nil, nil, &Problem{ProblemUnsupported, fmt.Sprintf(
			"unsupported: a codebase bundle with %d output files (%s); only a single module is supported", len(r.OutputFiles), strings.Join(names, ", "))}
	}
	var meta struct {
		Inputs map[string]json.RawMessage `json:"inputs"`
	}
	if err := json.Unmarshal([]byte(r.Metafile), &meta); err != nil {
		return nil, nil, nil, nil, &Problem{"internal", "esbuild's metafile: " + err.Error()}
	}
	// The metafile's inputs, whose paths are relative to the folder; its
	// outputs name "/" relative to the folder, so they are left out.
	mf, err := json.Marshal(map[string]any{"inputs": meta.Inputs})
	if err != nil {
		return nil, nil, nil, nil, &Problem{"internal", err.Error()}
	}
	var inputs []string
	if binding != "" {
		for p := range meta.Inputs {
			if strings.HasPrefix(p, pluginNamespace+":") {
				continue
			}
			full := p
			if !filepath.IsAbs(full) {
				full = filepath.Join(cfg.Folder, filepath.FromSlash(p))
			}
			b, err := os.ReadFile(full)
			if err != nil {
				return nil, nil, nil, nil, &Problem{"internal", err.Error()}
			}
			inputs = append(inputs, string(b))
		}
	}
	var warnings []string
	for _, w := range r.Warnings {
		warnings = append(warnings, esbuildMessage(w))
	}
	// The build has returned, so no callback runs any more.
	keys := make([]string, 0, len(used))
	for k := range used {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return &esbuildOutput{text: r.OutputFiles[0].Contents, metafile: mf}, inputs, keys, warnings, nil
}

func esbuildMessages(ms []api.Message) string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = esbuildMessage(m)
	}
	return strings.Join(out, "\n")
}

func esbuildMessage(m api.Message) string {
	if l := m.Location; l != nil {
		return fmt.Sprintf("%s:%d:%d: %s", l.File, l.Line, l.Column, m.Text)
	}
	return m.Text
}

// inside reports whether a slash path relative to the folder stays inside
// it.
func inside(rel string) bool {
	return rel != "" && !path.IsAbs(rel) && !slices.Contains(strings.Split(rel, "/"), "..") && !strings.Contains(rel, "\\")
}

type assetSource struct {
	from, script string
	data         []byte
}

// entryAssets reads the assets of one script's entry, all or none, by
// their paths in the working directory, or returns its problem: a source
// that does not read, a `to` that is not a clean relative path, or a path
// that an earlier script, or the same entry, takes with different content
// (ADR 0002, decision 3). The same content at one path is shared.
func entryAssets(folder, script string, list []Asset, taken map[string]assetSource) (map[string]assetSource, *Problem) {
	own := map[string]assetSource{}
	for _, a := range list {
		if !validID(a.To) || path.IsAbs(a.To) {
			return nil, &Problem{ProblemInvalidAsset, fmt.Sprintf("the asset's to %q is not a clean relative path", a.To)}
		}
		from := filepath.FromSlash(a.From)
		if !filepath.IsAbs(from) {
			from = filepath.Join(folder, from)
		}
		data, err := os.ReadFile(from)
		if err != nil {
			return nil, &Problem{ProblemInvalidAsset, fmt.Sprintf("the asset %q: %v", a.From, err)}
		}
		for _, prev := range []map[string]assetSource{taken, own} {
			if p, ok := prev[a.To]; ok && !bytes.Equal(p.data, data) {
				return nil, &Problem{ProblemAssetCollision, fmt.Sprintf(
					"the asset %q of script %s (from %q) and of script %s (from %q) have different content", a.To, p.script, p.from, script, a.From)}
			}
		}
		if _, ok := taken[a.To]; !ok {
			if _, ok := own[a.To]; !ok {
				own[a.To] = assetSource{from: a.From, script: script, data: data}
			}
		}
	}
	return own, nil
}

// unicodeEscape matches an identifier's Unicode escapes, `\u0075` and
// `\u{75}`.
var unicodeEscape = regexp.MustCompile(`\\u\{([0-9a-fA-F]+)\}|\\u([0-9a-fA-F]{4})`)

// spells reports whether text spells the identifier name, as written or
// through Unicode escapes, which an identifier may use for any of its
// characters. Escapes are decoded everywhere, in strings and comments too:
// a false match only moves the build to another name. The Bun helper's
// spells (harnesses/bun/build/build.ts) applies the same rule.
func spells(text, name string) bool {
	if strings.Contains(text, name) {
		return true
	}
	if !strings.Contains(text, `\u`) {
		return false
	}
	decoded := unicodeEscape.ReplaceAllStringFunc(text, func(m string) string {
		sub := unicodeEscape.FindStringSubmatch(m)
		hex := sub[1]
		if hex == "" {
			hex = sub[2]
		}
		cp, err := strconv.ParseUint(hex, 16, 32)
		if err != nil || cp > 0x10ffff {
			return m
		}
		return string(rune(cp))
	})
	return strings.Contains(decoded, name)
}
