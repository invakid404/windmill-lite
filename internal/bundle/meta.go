package bundle

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ScriptMeta is what the build records for one Bun script, written beside
// its module as scripts/bun/<id>.meta.json (ADR 0010).
type ScriptMeta struct {
	// Path is the script's import base (WR-29).
	Path string `json:"path"`
	// Target is the bundler target its annotations selected (WR-40):
	// "bun", "browser" for `//native`, "node" for `//nodejs`.
	Target         string         `json:"target"`
	Annotations    Annotations    `json:"annotations"`
	WorkflowAsCode WorkflowAsCode `json:"workflow_as_code"`
	// Exports are the bundled module's export names.
	Exports []string `json:"exports"`
	// Signatures holds the parsed signature of every export, and of every
	// other top-level function declaration, by name (ADR 0002, WR-7).
	Signatures map[string]Signature `json:"signatures"`
	// Imports is the closure of workspace scripts the bundle inlined, by
	// Windmill path (WR-28).
	Imports []string `json:"imports"`
	// Packages are the npm packages the script's own source imports.
	Packages []string `json:"packages"`
	// HostModules are the keys of the host-provided modules whose shims the
	// bundle uses (D12).
	HostModules []string `json:"host_modules"`
	// Metafile is the bundler's metafile, with paths relative to the
	// dependency set's directory; for a codebase script, esbuild's inputs,
	// with paths relative to the sync root.
	Metafile json.RawMessage `json:"metafile,omitempty"`
	// Bundler is "esbuild" for a codebase script (BuildCodebase), whose
	// module the harness loads from its working directory; empty for a
	// module of the Bun build.
	Bundler string `json:"bundler,omitempty"`
	// Format is a codebase script's module format: "cjs", whose module is
	// scripts/bun/<id>.cjs, or "esm". Empty means ESM, the Bun build's.
	Format Format `json:"format,omitempty"`

	esbuildWarnings []string
}

// Annotations are a script's leading-comment flags (WR-31).
type Annotations struct {
	Npm        bool `json:"npm"`
	Nodejs     bool `json:"nodejs"`
	Native     bool `json:"native"`
	Nobundling bool `json:"nobundling"`
	Sandbox    bool `json:"sandbox"`
}

// WorkflowAsCode records each way Windmill would treat a script as
// workflow-as-code, which windmill-lite refuses at load (ADR 0001 §3):
// the worker's wrapper scan (WR-32) and the parser's two detection paths
// (WA-33).
type WorkflowAsCode struct {
	Wrapper   bool `json:"wrapper"`
	Parsed    bool `json:"parsed"`
	Substring bool `json:"substring"`
}

// Any reports whether any detection fired.
func (w WorkflowAsCode) Any() bool { return w.Wrapper || w.Parsed || w.Substring }

// Signature is one export's parsed parameter list (WR-6 to WR-8), or the
// parse failure that fails a job calling it as its entrypoint (WR-7).
type Signature struct {
	Params []Param
	Error  string
}

// Param is one parameter: its name (`anonN` for a destructured object) and
// whether its value is hydrated as a Date (WR-8).
type Param struct {
	Name string `json:"name"`
	Date bool   `json:"date"`
}

func (s Signature) MarshalJSON() ([]byte, error) {
	if s.Error != "" {
		return json.Marshal(struct {
			Error string `json:"error"`
		}{s.Error})
	}
	params := s.Params
	if params == nil {
		params = []Param{}
	}
	return json.Marshal(struct {
		Params []Param `json:"params"`
	}{params})
}

func (s *Signature) UnmarshalJSON(b []byte) error {
	var v struct {
		Params *[]Param `json:"params"`
		Error  *string  `json:"error"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch {
	case v.Error != nil && v.Params == nil:
		*s = Signature{Error: *v.Error}
	case v.Params != nil && v.Error == nil:
		*s = Signature{Params: *v.Params}
		if s.Params == nil {
			s.Params = []Param{}
		}
	default:
		return fmt.Errorf("bundle: a signature has either params or an error: %s", b)
	}
	return nil
}

// HostModuleMeta is what the build records for one host-provided module.
type HostModuleMeta struct {
	Key string
	// Exports are its export names, from the scan; its shim takes these.
	Exports []string
	// HostModules are the keys of other host-provided modules it imports,
	// which must be registered before it.
	HostModules []string
	// Packages are the npm packages its own source imports.
	Packages []string
	// Imports is the closure of workspace scripts its bundled copy
	// inlined, by Windmill path (WR-28).
	Imports []string
}

// Problem is something the build could not do for one script or module.
type Problem struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// Problem kinds.
const (
	// ProblemBundleFailed: the bundler failed, for example on an import
	// that resolves to no script or one that a `.ts` rewrite collision
	// corrupted (WR-38). Windmill fails such a script at every run; the
	// build refuses it (ADR 0001 §5a).
	ProblemBundleFailed = "bundle_failed"
	// ProblemParseFailed: the source does not parse as TypeScript.
	ProblemParseFailed = "parse_failed"
	// ProblemInvalidLock: the lock is not in the format of WR-25.
	ProblemInvalidLock = "invalid_lock"
	// ProblemInstallFailed: installing the lock's dependencies failed.
	ProblemInstallFailed = "install_failed"
	// ProblemUnsupported: a feature windmill-lite does not run (`//npm`).
	ProblemUnsupported = "unsupported_feature"
	// ProblemLiveBinding: a host-provided module exports a binding it can
	// reassign (ADR 0001 §5a).
	ProblemLiveBinding = "host_module_live_binding"
	// ProblemExportsDiffer: a host-provided module's bundled copy exports
	// other names than its scan found, which happens when it re-exports
	// another host-provided module with `export *`.
	ProblemExportsDiffer = "host_module_exports_differ"
	// ProblemHostModuleCycle: host-provided modules import each other in a
	// cycle, so none can be registered first.
	ProblemHostModuleCycle = "host_module_cycle"
	// ProblemHostModuleCommonJS: a host-provided module is CommonJS. Its
	// export names are its exports object's properties, known only when it
	// runs, so a shim cannot take them by name (ADR 0001 §5a).
	ProblemHostModuleCommonJS = "host_module_commonjs"
)

// ItemProblem is a Problem of one script or host-provided module.
type ItemProblem struct {
	// Item is "script <id>" or "host module <id>".
	Item string
	Problem
}

// BuildError lists every problem of a build.
type BuildError struct {
	Problems []ItemProblem
}

func (e *BuildError) Error() string {
	var b strings.Builder
	b.WriteString("bundle: build failed:")
	for _, p := range e.Problems {
		fmt.Fprintf(&b, "\n  %s: %s: %s", p.Item, p.Kind, p.Message)
	}
	return b.String()
}

// HelperError is a request the build helper could not serve at all, such
// as one run on a Bun other than the pin (kind "runtime_mismatch").
type HelperError struct {
	Kind    string
	Message string
}

func (e *HelperError) Error() string {
	return fmt.Sprintf("bundle: build helper: %s: %s", e.Kind, e.Message)
}

// Warning is a build diagnostic that does not fail the build.
type Warning struct {
	Code    string
	Message string
}

// WarningSharedDependency: a host-provided module imports a package that a
// script using it also imports directly, so that package exists twice in
// the process with different configuration (ADR 0010).
const WarningSharedDependency = "host_module_shared_dependency"
