package bundle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/expr"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/syncfolder"
)

// Config describes one bundle build (ADR 0010, `windmill-lite build`).
type Config struct {
	// Folder is a directory of the sync folder. The build reads the folder
	// from its root: the nearest directory upwards that holds wmill.yaml.
	Folder string
	// Contract is the host contract's text (ir.ParseContract); nil makes
	// every flow and script of the folder a root.
	Contract []byte
	// Strict refuses absent skip flags instead of defaulting them
	// (ir.Options).
	Strict bool
	// ModuleState is the module-state mode the manifest records (ADR 0006);
	// empty means warm.
	ModuleState engine.ModuleState
	// EngineVersion is the exact version of the engine that builds the
	// bundle. A runtime opens only a bundle of its own version.
	EngineVersion string

	// Bun, Helper, AllowBunMismatch, Env, Diagnostics and Timeout are those
	// of BunConfig.
	Bun              string
	Helper           string
	AllowBunMismatch bool
	Env              []string
	Diagnostics      io.Writer
	Timeout          time.Duration
	// HarnessRuntime is the directory of the Bun harness runtime
	// (harnesses/bun/runtime) the bundle is built for; the manifest records
	// its TreeDigest.
	HarnessRuntime string

	// Out is the bundle directory to write. It must not exist, or be an
	// empty directory. The build writes a sibling directory and renames it
	// to Out last, so a failed build leaves nothing at Out.
	Out string
	// Work is a scratch directory for the helper; empty means a fresh
	// temporary one.
	Work string
}

// Result is what a build produced.
type Result struct {
	// Digest is the bundle digest: the Digest of manifest.json.
	Digest   string
	Manifest *Manifest
	Program  *ir.Program
	// Warnings are the folder's and the Bun build's diagnostics.
	Warnings []string
}

// Problem kinds of the build, beyond those of BuildBun.
const (
	// ProblemMissingLock: a bundled Bun script imports a package and has no
	// lock. windmill-lite does not generate locks (ADR 0002, validation 5).
	ProblemMissingLock = "missing_lock"
	// ProblemUnsupportedFeature: the build refuses the script or module
	// (`unsupported: <feature>`), as for workflow-as-code (ADR 0001 §3).
	ProblemUnsupportedFeature = "unsupported_feature"
)

// Build loads and compiles a sync folder, bundles its Bun scripts and
// host-provided modules, and writes the bundle with its manifest (ADR
// 0010). A folder or contract the compiler refuses fails with its errors
// (*ir.Error, joined); refused scripts and modules fail with a
// *BuildError that lists them all.
func Build(ctx context.Context, cfg Config) (*Result, error) {
	if cfg.Folder == "" || cfg.Out == "" || cfg.EngineVersion == "" || cfg.HarnessRuntime == "" {
		return nil, errors.New("bundle: Config needs Folder, Out, EngineVersion and HarnessRuntime")
	}
	if cfg.ModuleState == "" {
		cfg.ModuleState = engine.ModuleStateWarm
	}
	if cfg.ModuleState != engine.ModuleStateWarm && cfg.ModuleState != engine.ModuleStateFresh {
		return nil, fmt.Errorf("bundle: module state %q is neither warm nor fresh", cfg.ModuleState)
	}
	out, err := filepath.Abs(cfg.Out)
	if err != nil {
		return nil, err
	}
	if err := checkOut(out); err != nil {
		return nil, err
	}

	// The folder and the program.
	root, err := syncfolder.Find(cfg.Folder)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	src := newSourceFS(r.FS())
	folder, err := syncfolder.Load(src)
	if err != nil {
		return nil, err
	}
	res := &Result{Warnings: slices.Clone(folder.Warnings)}
	var contract *ir.Contract
	if cfg.Contract != nil {
		if contract, err = ir.ParseContract(cfg.Contract); err != nil {
			return nil, err
		}
	}
	prog, err := ir.Compile(folder, contract, ir.Options{Strict: cfg.Strict})
	if err != nil {
		return nil, err
	}
	res.Program = prog
	data, secrets, err := readData(folder)
	if err != nil {
		return nil, err
	}
	// A secret's file holds its value in plain text when wmill.yaml sets
	// plainSecrets; a digest of it in a shipped manifest would let anyone
	// test guesses of the value. It is not a source.
	src.forget(secrets)

	// What the Bun build needs: every Bun script of the folder, which
	// workspace imports resolve to, and the host-provided modules. Reading
	// them is not an input of the bundle; what the bundles inline is added
	// below.
	src.pause()
	scripts, byFile := folderScripts(folder)
	var problems []ItemProblem
	var hosts []HostModule
	for _, key := range prog.Contract.HostModules {
		p, ok := byFile[key]
		if !ok {
			// A script of the folder that fails to read says why; anything
			// else is a package.
			if path, _, _ := strings.Cut(key, "."); strings.HasSuffix(key, ".ts") {
				if _, err := folder.Script(path); err != nil {
					problems = append(problems, ItemProblem{"host module " + key, Problem{"load", err.Error()}})
					continue
				}
			}
			problems = append(problems, ItemProblem{"host module " + key, Problem{ProblemUnsupportedFeature,
				"unsupported: host-provided module " + key + ": it is not the file of a Bun script of the folder, " +
					"and host-provided packages are not supported yet"}})
			continue
		}
		s := scripts[p]
		hosts = append(hosts, HostModule{ID: p, Key: key, Path: p, File: s.File, Lock: lockText(s.Lock)})
	}
	files := map[string]string{}
	for p, s := range scripts {
		files[p] = s.File
	}
	ids := make([]string, 0, len(prog.Scripts))
	for id := range prog.Scripts {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var entries []BunScript
	var codebase []CodebaseScript
	names := map[string]string{} // script id → how problems name it
	for _, id := range ids {
		s := prog.Scripts[id]
		names[id] = scriptName(s)
		if s.Bundling == ir.BundlingCodebase {
			// esbuild bundles it with its entry's settings (C2b).
			codebase = append(codebase, CodebaseScript{ID: id, Path: s.Path, File: s.File,
				Settings: folder.Config.Codebases[s.Codebase.Index].Settings})
			continue
		}
		e := BunScript{ID: id, Path: s.ImportBase, Lock: lockText(s.Lock), Language: string(s.Language)}
		if s.Kind == ir.PathScript {
			e.File = s.File
		} else {
			e.Content = s.Content
		}
		entries = append(entries, e)
	}
	if len(problems) > 0 {
		return res, &BuildError{Problems: problems}
	}

	// The Bun build, into a staging directory beside Out.
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(out), "."+filepath.Base(out)+".build-")
	if err != nil {
		return nil, err
	}
	staged := false
	defer func() {
		if !staged {
			os.RemoveAll(stage)
		}
	}()
	if err := os.Chmod(stage, 0o755); err != nil {
		return nil, err
	}
	bres, err := BuildBun(ctx, BunConfig{
		Bun: cfg.Bun, Helper: cfg.Helper, AllowBunMismatch: cfg.AllowBunMismatch,
		Folder: root, Out: stage, Work: cfg.Work,
		Scripts: files, Entries: entries, HostModules: hosts,
		Env: cfg.Env, Diagnostics: cfg.Diagnostics, Timeout: cfg.Timeout,
	})
	var be *BuildError
	if err != nil && !errors.As(err, &be) {
		return nil, err
	}
	// Codebase scripts, with esbuild, once the scan has written the shims
	// of the host-provided modules their imports resolve to.
	cres := &CodebaseResult{Scripts: map[string]*ScriptMeta{}}
	var cbe *BuildError
	if len(codebase) > 0 && be == nil {
		var chosts []CodebaseHostModule
		for _, m := range hosts {
			chosts = append(chosts, CodebaseHostModule{Key: m.Key, File: m.File, Package: m.Package,
				Shim: filepath.Join(stage, "scripts", "bun", "provided", "shims", filepath.FromSlash(m.ID)+".mjs")})
		}
		cres, err = BuildCodebase(ctx, CodebaseConfig{
			Bun: cfg.Bun, Helper: cfg.Helper, AllowBunMismatch: cfg.AllowBunMismatch,
			Env: cfg.Env, Diagnostics: cfg.Diagnostics, Timeout: cfg.Timeout,
			Folder: root, Out: stage, Work: cfg.Work, Entries: codebase, HostModules: chosts,
		})
		if err != nil && !errors.As(err, &cbe) {
			return nil, err
		}
	}
	for _, e := range []*BuildError{be, cbe} {
		if e == nil {
			continue
		}
		for _, p := range e.Problems {
			if id, ok := strings.CutPrefix(p.Item, "script "); ok && names[id] != "" {
				p.Item = names[id]
			}
			problems = append(problems, p)
		}
	}
	for _, w := range append(bres.Warnings, cres.Warnings...) {
		res.Warnings = append(res.Warnings, w.Code+": "+w.Message)
	}
	listed := map[string]bool{}
	for _, m := range hosts {
		if m.Package != "" {
			listed[m.Package] = true
		}
	}
	for _, id := range ids {
		meta := bres.Scripts[id]
		if meta == nil {
			meta = cres.Scripts[id]
		}
		if meta == nil {
			continue
		}
		problems = append(problems, scriptProblems(names[id], prog.Scripts[id], meta, listed)...)
	}
	if len(problems) > 0 {
		slices.SortStableFunc(problems, func(a, b ItemProblem) int { return strings.Compare(a.Item, b.Item) })
		return res, &BuildError{Problems: problems}
	}

	// The sources: what the loader read, and the files the helper read
	// itself.
	for _, m := range hosts {
		if err := addScriptSources(src, scripts, m.Path); err != nil {
			return nil, err
		}
		for _, p := range bres.HostModules[m.ID].Imports {
			if err := src.add(files[p]); err != nil {
				return nil, err
			}
		}
	}
	for _, meta := range bres.Scripts {
		for _, p := range meta.Imports {
			if err := src.add(files[p]); err != nil {
				return nil, err
			}
		}
	}
	// What esbuild read from the folder, and the assets' sources in it.
	// Packages and files outside the folder reach the bundle only through
	// the modules and assets the manifest digests.
	for _, meta := range cres.Scripts {
		for _, p := range codebaseInputs(meta) {
			if err := src.add(p); err != nil {
				return nil, err
			}
		}
	}
	for _, from := range cres.Assets {
		if p := path.Clean(filepath.ToSlash(from)); inside(p) {
			if err := src.add(p); err != nil {
				return nil, err
			}
		}
	}
	sources, err := src.inventory()
	if err != nil {
		return nil, err
	}

	// ir.json, data/, then the manifest, last.
	irJSON, err := prog.MarshalIndent()
	if err != nil {
		return nil, err
	}
	if err := writeFile(filepath.Join(stage, "ir.json"), irJSON); err != nil {
		return nil, err
	}
	for name, v := range data {
		if err := writeJSON(filepath.Join(stage, "data", name), v); err != nil {
			return nil, err
		}
	}
	runtime, err := TreeDigest(cfg.HarnessRuntime)
	if err != nil {
		return nil, err
	}
	bundleFiles, err := digestTree(stage)
	if err != nil {
		return nil, err
	}
	m := &Manifest{
		Format: FormatVersion,
		Engine: Engine{
			Version:              cfg.EngineVersion,
			ExpressionStackBytes: expr.DefaultLimits().StackBytes,
		},
		Bundlers:             Bundlers{Bun: bres.BunVersion},
		NonconformantRuntime: bres.NonconformantRuntime || cres.NonconformantRuntime,
		ModuleState:          cfg.ModuleState,
		Deviations:           manifestDeviations(prog.Deviations, cfg.ModuleState),
		Contract:             prog.Contract,
		HostModules:          []HostModuleEntry{},
		HarnessRuntimes:      map[string]string{string(ir.ClassBun): runtime},
		Sources:              sources,
		Files:                bundleFiles,
	}
	if len(codebase) > 0 {
		m.Bundlers.Esbuild = EsbuildVersion
	}
	for _, id := range bres.HostModuleOrder {
		m.HostModules = append(m.HostModules, HostModuleEntry{Key: bres.HostModules[id].Key, ID: id})
	}
	manifest, err := m.Canonical()
	if err != nil {
		return nil, err
	}
	if err := writeFile(filepath.Join(stage, ManifestFile), manifest); err != nil {
		return nil, err
	}
	if fi, err := os.Stat(out); err == nil && fi.IsDir() {
		if err := os.Remove(out); err != nil {
			return nil, err
		}
	}
	if err := os.Rename(stage, out); err != nil {
		return nil, err
	}
	staged = true
	res.Manifest, res.Digest = m, Digest(manifest)
	return res, nil
}

// checkOut refuses an output path that holds anything.
func checkOut(out string) error {
	fi, err := os.Lstat(out)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("bundle: %s exists and is not a directory", out)
	}
	ents, err := os.ReadDir(out)
	if err != nil {
		return err
	}
	if len(ents) > 0 {
		return fmt.Errorf("bundle: %s is not empty", out)
	}
	return nil
}

// folderScripts reads every Bun script of the folder (a `.ts` file),
// skipping those that fail to read: an import of one fails its bundle. It
// returns them by path, and their paths by file.
func folderScripts(f *syncfolder.Folder) (map[string]*syncfolder.Script, map[string]string) {
	scripts := map[string]*syncfolder.Script{}
	byFile := map[string]string{}
	for _, p := range f.Paths(syncfolder.KindScript) {
		s, err := f.Script(p)
		if err != nil || !strings.HasSuffix(s.File, ".ts") {
			continue
		}
		scripts[p] = s
		byFile[s.File] = p
	}
	return scripts, byFile
}

// addScriptSources records a script's source, metadata and lock files.
func addScriptSources(src *sourceFS, scripts map[string]*syncfolder.Script, p string) error {
	s := scripts[p]
	for _, f := range []string{s.File, s.MetadataFile, s.LockFile} {
		if f == "" {
			continue
		}
		if err := src.add(f); err != nil {
			return err
		}
	}
	return nil
}

// codebaseInputs are the files of the folder a codebase bundle read, from
// its metafile: inputs that are not the build's own modules, outside
// node_modules and inside the folder.
func codebaseInputs(meta *ScriptMeta) []string {
	var mf struct {
		Inputs map[string]json.RawMessage `json:"inputs"`
	}
	if json.Unmarshal(meta.Metafile, &mf) != nil {
		return nil
	}
	var out []string
	for p := range mf.Inputs {
		if strings.HasPrefix(p, pluginNamespace+":") || !inside(p) || slices.Contains(strings.Split(p, "/"), "node_modules") {
			continue
		}
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

func lockText(l *string) string {
	if l == nil {
		return ""
	}
	return *l
}

// scriptName is how problems name an inventory script: a path script by
// its path, an inline script by its flow and step.
func scriptName(s *ir.Script) string {
	if s.Kind == ir.InlineScript {
		return fmt.Sprintf("%s step '%s' (script %s)", s.Flow, s.Module, s.ID)
	}
	return "script " + s.Path
}

// scriptProblems are the rules the build checks on a script's metadata:
// workflow-as-code is refused (ADR 0001 §3; WA-33, WR-32), and so is a
// script that imports a package and has no lock (ADR 0002, validation 5).
// Listed host-provided packages resolve to their shims and need no lock,
// and a codebase script has none: esbuild inlines its packages from the
// folder's node_modules, as the CLI does (WR-47, WR-50).
func scriptProblems(name string, s *ir.Script, meta *ScriptMeta, listed map[string]bool) []ItemProblem {
	var out []ItemProblem
	if meta.WorkflowAsCode.Any() {
		out = append(out, ItemProblem{name, Problem{ProblemUnsupportedFeature, "unsupported: workflow_as_code"}})
	}
	if s.Bundling != ir.BundlingCodebase && strings.TrimSpace(lockText(s.Lock)) == "" {
		var pkgs []string
		for _, p := range meta.Packages {
			if !listed[p] {
				pkgs = append(pkgs, p)
			}
		}
		if len(pkgs) > 0 {
			out = append(out, ItemProblem{name, Problem{ProblemMissingLock, fmt.Sprintf(
				"it imports %s and has no lock; windmill-lite installs packages only from a script's lock and does not generate one",
				strings.Join(pkgs, ", "))}})
		}
	}
	return out
}

// readData reads the data the bundle carries (ADR 0010, data/): every
// resource and resource type the folder keeps, and every variable but
// secrets, which a bundle never holds. Each kind is one file, an object
// keyed by path (a resource type by name). It also returns the files of
// the secrets.
func readData(f *syncfolder.Folder) (map[string]any, []string, error) {
	var errs []error
	resources := map[string]*syncfolder.Resource{}
	for _, p := range f.Paths(syncfolder.KindResource) {
		r, err := f.Resource(p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		resources[p] = r
	}
	types := map[string]*syncfolder.ResourceType{}
	for _, p := range f.Paths(syncfolder.KindResourceType) {
		t, err := f.ResourceType(p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		types[p] = t
	}
	variables := map[string]*syncfolder.Variable{}
	var secrets []string
	for _, p := range f.Paths(syncfolder.KindVariable) {
		v, err := f.Variable(p)
		var se *syncfolder.Error
		switch {
		case errors.As(err, &se) && se.Kind == syncfolder.ErrFiltered:
			// A secret that wmill.yaml's skipSecrets filters out.
			secrets = append(secrets, se.File)
			continue
		case err != nil:
			errs = append(errs, err)
			continue
		case v.IsSecret:
			secrets = append(secrets, v.File)
			continue
		}
		variables[p] = v
	}
	if len(errs) > 0 {
		return nil, nil, errors.Join(errs...)
	}
	return map[string]any{
		"resources.json":      resources,
		"resource_types.json": types,
		"variables.json":      variables,
	}, secrets, nil
}

func writeJSON(path string, v any) error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	return writeFile(path, b.Bytes())
}

// digestTree returns the digest of every file under dir but the manifest,
// by slash path. A bundle holds only regular files and directories.
func digestTree(dir string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !d.Type().IsRegular() {
			return fmt.Errorf("bundle: %s is not a regular file", rel)
		}
		if rel == ManifestFile {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = Digest(data)
		return nil
	})
	return out, err
}
