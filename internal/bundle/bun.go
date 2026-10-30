package bundle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/invakid404/windmill-lite/internal/harness"
)

// BunScript is one Bun script to bundle.
type BunScript struct {
	// ID names its files in the bundle: scripts/bun/<ID>.mjs and
	// <ID>.meta.json. It is an inventory id (ADR 0002): one or more
	// `/`-separated segments, as the harness runtime resolves it.
	ID string
	// Path is its import base (WR-29): a path script's Windmill path, or an
	// inline script's ImportBase.
	Path string
	// File is a path script's source file, relative to the folder. An
	// inline script has none and gives Content instead.
	File    string
	Content string
	// Lock is its lock in the format of WR-25; empty means no dependencies.
	Lock string
	// Language is the language a deploy records (ADR 0002): a nativets
	// script is transpiled, not bundled, and a bun script with the
	// `//native` header is bundled for the browser target; both are built
	// into their native form, scripts/bun/<ID>.native.js (deviation D5).
	// Empty means bun.
	Language string
}

// HostModule is one host-provided module of the host contract (ADR 0010):
// a script of the folder (Path and File) or a package (Package and the
// Lock it installs from).
type HostModule struct {
	// ID names its files: scripts/bun/provided/<ID>.mjs, the bundled copy,
	// and scripts/bun/provided/shims/<ID>.mjs, the shim. It has the form of
	// a script's ID.
	ID string
	// Key is the module as the host contract names it: the registry key.
	Key     string
	Path    string
	File    string
	Package string
	Lock    string
}

// BunConfig describes the Bun side of one bundle build.
type BunConfig struct {
	// Bun is the bun executable and Helper the build helper's main.ts
	// (harnesses/bun/build/main.ts), whose dependencies are installed.
	Bun    string
	Helper string
	// AllowBunMismatch lets the build run on a Bun other than the pin
	// (harness.PinnedBun); the result then says NonconformantRuntime
	// (ADR 0010 step 2, `--bun-mismatch`).
	AllowBunMismatch bool
	// Folder is the sync root; Out the bundle directory the modules are
	// written into; Work a scratch directory for dependency installs and
	// the helper's files, a fresh temporary one when empty.
	Folder string
	Out    string
	Work   string
	// Scripts maps the folder's scripts by Windmill path to their files,
	// relative to Folder: the scripts that workspace imports resolve to
	// (WR-28).
	Scripts     map[string]string
	Entries     []BunScript
	HostModules []HostModule
	// Env is the environment of the helper and of the `bun install` runs
	// it starts (registry configuration, BUN_INSTALL_CACHE_DIR); nil means
	// this process's environment. NODE_ENV is always removed from it.
	Env []string
	// Diagnostics receives the helper's own output; nil discards it.
	Diagnostics io.Writer
	// Timeout bounds each helper run; zero means ten minutes.
	Timeout time.Duration
	// SharedCode builds the scripts' shared layout too (ADR 0013): one
	// closure per build group and a shared-layout module per script.
	SharedCode bool
}

// BunResult is what a Bun build produced.
type BunResult struct {
	// BunVersion is the version of the Bun that built the bundle;
	// NonconformantRuntime is set when it is not the pin.
	BunVersion           string
	NonconformantRuntime bool
	// Scripts holds the metadata of every script, by ID, including those
	// with a problem (with what was recorded before it).
	Scripts map[string]*ScriptMeta
	// HostModules holds every host-provided module scanned, by ID, and
	// HostModuleOrder their IDs in registration order: a module after the
	// modules it imports.
	HostModules     map[string]*HostModuleMeta
	HostModuleOrder []string
	Warnings        []Warning
	// Closures are the ids of the closures of shared code it wrote,
	// sorted (ADR 0013).
	Closures []string
}

// validID reports whether id can name files in the bundle tree: one or
// more `/`-separated segments, none empty, `.` or `..`, and none holding a
// backslash or NUL. The harness runtime applies the same rule.
func validID(id string) bool {
	for _, seg := range strings.Split(id, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, "\\\x00") {
			return false
		}
	}
	return true
}

func (cfg *BunConfig) validate() error {
	if cfg.Bun == "" || cfg.Helper == "" || cfg.Folder == "" || cfg.Out == "" {
		return errors.New("bundle: BunConfig needs Bun, Helper, Folder and Out")
	}
	// IDs name files, so two that differ only in case would collide on a
	// case-insensitive file system.
	ids := map[string]bool{}
	for _, e := range cfg.Entries {
		// scripts/bun/provided/ holds the host-provided modules.
		first, _, _ := strings.Cut(e.ID, "/")
		if !validID(e.ID) || ids[strings.ToLower(e.ID)] || strings.EqualFold(first, "provided") {
			return fmt.Errorf("bundle: script id %q is invalid or repeated", e.ID)
		}
		ids[strings.ToLower(e.ID)] = true
		if e.Path == "" || (e.File != "" && e.Content != "") {
			return fmt.Errorf("bundle: script %s needs a path and either a file or content", e.ID)
		}
	}
	hids, keys := map[string]bool{}, map[string]bool{}
	for _, m := range cfg.HostModules {
		if !validID(m.ID) || hids[strings.ToLower(m.ID)] {
			return fmt.Errorf("bundle: host module id %q is invalid or repeated", m.ID)
		}
		if m.Key == "" || keys[m.Key] {
			return fmt.Errorf("bundle: host module %s: key %q is empty or repeated", m.ID, m.Key)
		}
		hids[strings.ToLower(m.ID)], keys[m.Key] = true, true
		pkg, folder := m.Package != "", m.Path != "" || m.File != ""
		switch {
		case pkg == folder:
			return fmt.Errorf("bundle: host module %s needs either a package or a path and a file", m.ID)
		case folder && (m.Path == "" || m.File == ""):
			return fmt.Errorf("bundle: host module %s needs both a path and a file", m.ID)
		case folder && cfg.Scripts[m.Path] != m.File:
			// The build reads the module through the script map, and checks
			// the file for live bindings: the two must be one file.
			return fmt.Errorf("bundle: host module %s: %s is not the script at %s", m.ID, m.File, m.Path)
		}
	}
	return nil
}

// depKey is the dependency set of a lock: its digest (ADR 0002).
func depKey(lock string) string {
	if lock == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(lock))
	return hex.EncodeToString(sum[:16])
}

// BuildBun bundles every Bun script and host-provided module of cfg. It runs
// the build helper twice on cfg.Bun: a scan of the host-provided modules,
// after which it writes their shims into the bundle, then the build. On
// problems with scripts or modules it returns the result so far and a
// *BuildError listing them all.
func BuildBun(ctx context.Context, cfg BunConfig) (*BunResult, error) {
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
		if cfg.Work, err = os.MkdirTemp("", "windmill-lite-build-"); err != nil {
			return nil, err
		}
		defer os.RemoveAll(cfg.Work)
	} else if cfg.Work, err = filepath.Abs(cfg.Work); err != nil {
		return nil, err
	}
	for _, dir := range []string{cfg.Out, cfg.Work} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}

	define, support, err := EnvRewrite(ESM)
	if err != nil {
		return nil, err
	}
	req := helperRequest{
		Version: 1, Folder: cfg.Folder, Work: cfg.Work, Out: cfg.Out,
		Scripts: cfg.Scripts, Deps: map[string]string{}, Define: define, EnvSupport: support, EnvBinding: EnvBinding,
		HostModules: []helperHostModule{}, Entries: []helperEntry{},
	}
	if req.Scripts == nil {
		req.Scripts = map[string]string{}
	}
	if !cfg.AllowBunMismatch {
		req.RequireBun = harness.PinnedBun
	}
	addDeps := func(lock string) string {
		k := depKey(lock)
		if k != "" {
			req.Deps[k] = lock
		}
		return k
	}
	for _, m := range cfg.HostModules {
		req.HostModules = append(req.HostModules, helperHostModule{
			ID: m.ID, Key: m.Key, Path: m.Path, File: m.File, Package: m.Package, Deps: addDeps(m.Lock),
		})
	}
	for _, e := range cfg.Entries {
		req.Entries = append(req.Entries, helperEntry{ID: e.ID, Path: e.Path, File: e.File, Content: e.Content, Deps: addDeps(e.Lock), Language: e.Language})
	}

	res := &BunResult{Scripts: map[string]*ScriptMeta{}, HostModules: map[string]*HostModuleMeta{}}
	var problems []ItemProblem
	hostItem := func(id string) string { return "host module " + id }

	if len(req.HostModules) > 0 {
		req.Op = "scan"
		resp, err := runHelper(ctx, &cfg, &req)
		if err != nil {
			return nil, err
		}
		res.setRuntime(resp.BunVersion)
		for i, m := range req.HostModules {
			r, ok := resp.HostModules[m.ID]
			switch {
			case !ok:
				problems = append(problems, ItemProblem{hostItem(m.ID), Problem{"internal", "the scan did not report it"}})
				continue
			case r.Error != nil:
				problems = append(problems, ItemProblem{hostItem(m.ID), *r.Error})
				continue
			}
			res.HostModules[m.ID] = &HostModuleMeta{Key: m.Key, Exports: r.Exports, HostModules: r.HostModules, Packages: r.Packages}
			shim, err := Shim(m.Key, r.Exports)
			if err != nil {
				return nil, err
			}
			rel := filepath.Join("scripts", "bun", "provided", "shims", filepath.FromSlash(m.ID)+".mjs")
			if err := writeFile(filepath.Join(cfg.Out, rel), shim); err != nil {
				return nil, err
			}
			req.HostModules[i].Shim = filepath.Join(cfg.Out, rel)
			req.HostModules[i].Exports = r.Exports
		}
		if len(problems) > 0 {
			return res, &BuildError{Problems: problems}
		}
		order, err := hostModuleOrder(cfg.HostModules, res.HostModules)
		if err != nil {
			return res, &BuildError{Problems: []ItemProblem{{"host modules", Problem{ProblemHostModuleCycle, err.Error()}}}}
		}
		res.HostModuleOrder = order
	}

	req.Op = "build"
	req.SharedCode = cfg.SharedCode
	resp, err := runHelper(ctx, &cfg, &req)
	if err != nil {
		return nil, err
	}
	res.setRuntime(resp.BunVersion)
	for _, m := range req.HostModules {
		r, ok := resp.HostModules[m.ID]
		switch {
		case !ok:
			problems = append(problems, ItemProblem{hostItem(m.ID), Problem{"internal", "the build did not report it"}})
		case r.Error != nil:
			problems = append(problems, ItemProblem{hostItem(m.ID), *r.Error})
		case !sameNames(r.Exports, res.HostModules[m.ID].Exports):
			problems = append(problems, ItemProblem{hostItem(m.ID), Problem{ProblemExportsDiffer, fmt.Sprintf(
				"its bundled copy exports %v, its scan found %v", r.Exports, res.HostModules[m.ID].Exports)}})
		default:
			res.HostModules[m.ID].Imports = nonNilStrings(r.Imports)
		}
	}
	for _, e := range req.Entries {
		item := "script " + e.ID
		r, ok := resp.Entries[e.ID]
		if !ok {
			problems = append(problems, ItemProblem{item, Problem{"internal", "the build did not report it"}})
			continue
		}
		meta, err := decodeMeta(r.Meta)
		if err != nil {
			return nil, fmt.Errorf("bundle: script %s: %w", e.ID, err)
		}
		res.Scripts[e.ID] = meta
		if r.Error != nil {
			problems = append(problems, ItemProblem{item, *r.Error})
			continue
		}
		if meta.Shared != nil && meta.Shared.Fallback != "" {
			res.Warnings = append(res.Warnings, Warning{WarningSharedCodeFallback, fmt.Sprintf(
				"script %s (%s) keeps its per-script bundle: %s", e.ID, meta.Path, fallbackWhy(meta.Shared.Fallback))})
		}
		b, err := json.MarshalIndent(meta, "", "  ")
		if err != nil {
			return nil, err
		}
		if err := writeFile(filepath.Join(cfg.Out, "scripts", "bun", filepath.FromSlash(e.ID)+".meta.json"), append(b, '\n')); err != nil {
			return nil, err
		}
	}
	res.Warnings = append(append(sharedDependencyWarnings(cfg, res), nativeImportWarnings(cfg, res)...), res.Warnings...)
	res.Closures = slices.Sorted(slices.Values(resp.Closures))
	// Each closure bundle the helper wrote, wrapped into its factory: a
	// Bun-built group's modules are ES modules, so its closure is strict
	// (ADR 0013).
	for _, id := range res.Closures {
		meta := resp.ClosureMeta[id]
		if !validClosureID(id) || !validMetaName(meta) {
			return nil, fmt.Errorf("bundle: the build helper reported the closure %q with the import.meta parameter %q", id, meta)
		}
		text, err := os.ReadFile(filepath.Join(cfg.Work, "closures", id+".cjs"))
		if err != nil {
			return nil, err
		}
		if err := writeFile(filepath.Join(cfg.Out, filepath.FromSlash(ClosureFile(id))), wrapClosure(text, true, meta, false)); err != nil {
			return nil, err
		}
	}
	if len(problems) > 0 {
		return res, &BuildError{Problems: problems}
	}
	return res, nil
}

func (r *BunResult) setRuntime(v string) {
	r.BunVersion = v
	r.NonconformantRuntime = v != harness.PinnedBun
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func sameNames(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// decodeMeta reads the helper's metadata of one script, with the metafile
// re-encoded canonically (sorted keys), so the file does not depend on the
// order in which the bundler visited its inputs.
func decodeMeta(raw json.RawMessage) (*ScriptMeta, error) {
	var m ScriptMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if len(m.Metafile) > 0 {
		var v any
		if err := json.Unmarshal(m.Metafile, &v); err != nil {
			return nil, err
		}
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		m.Metafile = b
	}
	for _, s := range []*[]string{&m.Exports, &m.Imports, &m.Packages, &m.HostModules} {
		if *s == nil {
			*s = []string{}
		}
	}
	if m.Boundary == "" {
		m.Boundary = "bun"
	}
	if n := m.Native; n != nil {
		if n.Imports == nil {
			n.Imports = []string{}
		}
		if n.Links == nil {
			n.Links = []NativeLink{}
		}
	}
	if m.Signatures == nil {
		m.Signatures = map[string]Signature{}
	}
	return &m, nil
}

// hostModuleOrder orders host-provided modules so that each comes after the
// modules it imports, keeping the configured order otherwise.
func hostModuleOrder(mods []HostModule, meta map[string]*HostModuleMeta) ([]string, error) {
	byKey := map[string]string{}
	for _, m := range mods {
		byKey[m.Key] = m.ID
	}
	const (
		visiting = 1
		done     = 2
	)
	state := map[string]int{}
	var order []string
	var visit func(id string, chain []string) error
	visit = func(id string, chain []string) error {
		switch state[id] {
		case done:
			return nil
		case visiting:
			return fmt.Errorf("host-provided modules import each other: %s", strings.Join(append(chain, id), " → "))
		}
		state[id] = visiting
		for _, k := range meta[id].HostModules {
			if dep, ok := byKey[k]; ok {
				if err := visit(dep, append(chain, id)); err != nil {
					return err
				}
			}
		}
		state[id] = done
		order = append(order, id)
		return nil
	}
	for _, m := range mods {
		if err := visit(m.ID, nil); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// sharedDependencyWarnings reports each package that a script imports
// directly and a host-provided module it uses imports too (ADR 0010).
func sharedDependencyWarnings(cfg BunConfig, res *BunResult) []Warning {
	byKey := map[string]*HostModuleMeta{}
	for _, m := range res.HostModules {
		byKey[m.Key] = m
	}
	// A listed package resolves to its shim everywhere, so it is shared.
	listed := map[string]bool{}
	for _, m := range cfg.HostModules {
		if m.Package != "" {
			listed[m.Package] = true
		}
	}
	var out []Warning
	for _, e := range cfg.Entries {
		meta := res.Scripts[e.ID]
		if meta == nil {
			continue
		}
		for _, k := range meta.HostModules {
			hm := byKey[k]
			if hm == nil {
				continue
			}
			for _, p := range meta.Packages {
				if !listed[p] && slices.Contains(hm.Packages, p) {
					out = append(out, Warning{WarningSharedDependency, fmt.Sprintf(
						"script %s (%s) imports %s directly, and so does the host-provided module %s it uses; "+
							"the package exists twice in the process, with different configuration", e.ID, meta.Path, p, k)})
				}
			}
		}
	}
	return out
}

// nativeImportWarnings reports each static import of a native-boundary
// script that the native runtime cannot load: anything but the embedded
// client, for a nativets script (WR-40). The script still builds, since
// Windmill fails such a step only when it runs; the warning is the build's
// check of D5's API surface (ADR 0010).
func nativeImportWarnings(cfg BunConfig, res *BunResult) []Warning {
	var out []Warning
	for _, e := range cfg.Entries {
		meta := res.Scripts[e.ID]
		if meta == nil || meta.Native == nil {
			continue
		}
		for _, spec := range meta.Native.Imports {
			if meta.Native.Kind == "nativets" && nativeURL(spec) == nativeClientURL {
				continue
			}
			out = append(out, Warning{WarningNativeImport, fmt.Sprintf(
				"script %s (%s) imports %q, which the native runtime cannot load; the step fails when it runs", e.ID, meta.Path, spec)})
		}
	}
	return out
}

// nativeClientURL is the embedded client's URL in the native runtime.
const nativeClientURL = "file:///windmill.ts"

// nativeURL resolves an import specifier as the native runtime does,
// against file:///eval.ts: a path that starts with /, ./ or ../, or a URL;
// "" for a bare specifier, which cannot be resolved.
func nativeURL(spec string) string {
	base := &url.URL{Scheme: "file", Path: "/eval.ts"}
	if strings.HasPrefix(spec, "/") || strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") {
		u, err := base.Parse(spec)
		if err != nil {
			return ""
		}
		return u.String()
	}
	if u, err := url.Parse(spec); err == nil && u.Scheme != "" {
		return u.String()
	}
	return ""
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// The helper's request and response (harnesses/bun/build/protocol.ts).
type helperRequest struct {
	Version     int                `json:"version"`
	Op          string             `json:"op"`
	RequireBun  string             `json:"require_bun,omitempty"`
	Folder      string             `json:"folder"`
	Work        string             `json:"work"`
	Out         string             `json:"out"`
	Scripts     map[string]string  `json:"scripts"`
	Deps        map[string]string  `json:"deps"`
	Define      map[string]string  `json:"define"`
	EnvSupport  string             `json:"env_support"`
	EnvBinding  string             `json:"env_binding"`
	HostModules []helperHostModule `json:"host_modules"`
	Entries     []helperEntry      `json:"entries"`
	// SharedCode asks the build op for the shared layout too (ADR 0013).
	SharedCode bool `json:"shared_code,omitempty"`
	// Closures are the closure bundles the kept op parses.
	Closures []helperClosure `json:"closures,omitempty"`
	// Modules are the module files, by absolute path, the namespaces op
	// parses.
	Modules []string `json:"modules,omitempty"`
}

// helperClosure is a closure bundle the kept op parses, by its id.
type helperClosure struct {
	ID   string `json:"id"`
	File string `json:"file"`
}

type helperHostModule struct {
	ID      string `json:"id"`
	Key     string `json:"key"`
	Path    string `json:"path,omitempty"`
	File    string `json:"file,omitempty"`
	Package string `json:"package,omitempty"`
	Deps    string `json:"deps,omitempty"`
	// Source is the absolute path of a module outside the folder, whose
	// source the parse checks (OutsideHostModule).
	Source string `json:"source,omitempty"`
	// Loader is the esbuild loader that bundles Source (loaderFor), whose
	// syntax the parse reads it with.
	Loader string `json:"loader,omitempty"`
	Shim   string `json:"shim,omitempty"`
	// Exports are the scan's export names (build only), which the bundled
	// copy's entry re-exports.
	Exports []string `json:"exports,omitempty"`
}

type helperEntry struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	File     string `json:"file,omitempty"`
	Content  string `json:"content,omitempty"`
	Deps     string `json:"deps,omitempty"`
	Language string `json:"language,omitempty"`
	// Exports are a codebase script's bundle exports (parse only), which
	// the signatures cover; nil makes the helper scan the source's.
	Exports []string `json:"exports,omitempty"`
	// Bundle is a file under the work directory holding esbuild's bundle of
	// a codebase script with the //native header (native only).
	Bundle string `json:"bundle,omitempty"`
}

type helperResponse struct {
	Version     int                         `json:"version"`
	BunVersion  string                      `json:"bun_version"`
	Error       *Problem                    `json:"error"`
	HostModules map[string]helperHostResult `json:"host_modules"`
	Entries     map[string]helperEntryRes   `json:"entries"`
	// Closures are the closures of shared code the build op wrote, and
	// ClosureMeta, by closure, the name of its parameter that dependency
	// code reads as import.meta, one no input of the closure spells.
	Closures    []string          `json:"closures"`
	ClosureMeta map[string]string `json:"closure_meta"`
	// Kept holds, by closure, the modules the kept op found keeping an
	// import() of a specifier that names no module the runtime has built
	// in, and Builtins the names of those modules: node:module's
	// builtinModules on the Bun that runs the helper.
	Kept     map[string][]string `json:"kept"`
	Builtins []string            `json:"builtins"`
	// Namespaces holds, by module file, the specifiers whose namespace
	// object the module takes (the namespaces op); null for a module the
	// helper could not parse.
	Namespaces map[string][]string `json:"namespaces"`
}

type helperHostResult struct {
	Error       *Problem `json:"error"`
	Exports     []string `json:"exports"`
	HostModules []string `json:"host_modules"`
	Packages    []string `json:"packages"`
	Imports     []string `json:"imports"`
}

type helperEntryRes struct {
	Error *Problem        `json:"error"`
	Meta  json.RawMessage `json:"meta"`
}

// runHelper runs one helper request and reads its response.
func runHelper(ctx context.Context, cfg *BunConfig, req *helperRequest) (*helperResponse, error) {
	reqPath := filepath.Join(cfg.Work, "request-"+req.Op+".json")
	respPath := filepath.Join(cfg.Work, "response-"+req.Op+".json")
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(reqPath, b, 0o600); err != nil {
		return nil, err
	}
	_ = os.Remove(respPath)
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, cfg.Bun, cfg.Helper, reqPath, respPath)
	cmd.Dir = cfg.Work
	cmd.Env = helperEnv(cfg.Env)
	var out bytes.Buffer
	var w io.Writer = &out
	if cfg.Diagnostics != nil {
		w = io.MultiWriter(&out, cfg.Diagnostics)
	}
	cmd.Stdout, cmd.Stderr = w, w
	cmd.WaitDelay = 5 * time.Second
	// The helper runs `bun install`, whose lifecycle scripts may start
	// processes of their own: the helper gets a process group, and the
	// whole group goes when the run ends, by timeout, cancellation or
	// otherwise, before runHelper returns.
	ownProcessGroup(cmd)
	runErr := cmd.Run()
	if err := endProcessGroup(cmd); err != nil {
		return nil, fmt.Errorf("bundle: build helper (%s): %w", req.Op, err)
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("bundle: build helper (%s): %w: %s", req.Op, context.Cause(ctx), tail(out.String()))
	}
	data, readErr := os.ReadFile(respPath)
	if readErr != nil {
		return nil, fmt.Errorf("bundle: build helper (%s): %w: %s", req.Op, errors.Join(runErr, readErr), tail(out.String()))
	}
	var resp helperResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("bundle: build helper (%s): bad response: %w", req.Op, err)
	}
	if resp.Error != nil {
		return nil, &HelperError{Kind: resp.Error.Kind, Message: resp.Error.Message}
	}
	if runErr != nil {
		return nil, fmt.Errorf("bundle: build helper (%s): %w: %s", req.Op, runErr, tail(out.String()))
	}
	if resp.Version != 1 {
		return nil, fmt.Errorf("bundle: build helper (%s): response version %d", req.Op, resp.Version)
	}
	return &resp, nil
}

// helperEnv is the helper's environment without NODE_ENV: Bun.build
// inlines process.env.NODE_ENV from the environment it runs in, so a
// bundle would otherwise depend on the machine that built it. Unset, it
// inlines "development".
func helperEnv(env []string) []string {
	if env == nil {
		env = os.Environ()
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); name != "NODE_ENV" {
			out = append(out, kv)
		}
	}
	return out
}

func tail(s string) string {
	const n = 2000
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}
