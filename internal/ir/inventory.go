package ir

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/openflow"
	"github.com/invakid404/windmill-lite/internal/syncfolder"
)

// ScriptKind says whether an inventory entry is a path script or an inline
// script of a flow.
type ScriptKind string

const (
	PathScript   ScriptKind = "path"
	InlineScript ScriptKind = "inline"
)

// Bundling is how a script's code reaches its harness (WR-27, WR-30,
// WR-40).
type Bundling string

const (
	// BundlingBundled: bundled with its dependencies inlined, as Windmill's
	// bundled mode does.
	BundlingBundled Bundling = "bundled"
	// BundlingNone: the script carries `//nobundling`. windmill-lite
	// bundles it at build time all the same (deviation D7).
	BundlingNone Bundling = "nobundling"
	// BundlingCodebase: a codebase entry of wmill.yaml selects the script,
	// which is bundled with esbuild (SY-8, WR-30, WR-47).
	BundlingCodebase Bundling = "codebase"
	// BundlingTranspiled: a nativets script, which Windmill transpiles and
	// does not bundle (WR-40).
	BundlingTranspiled Bundling = "transpiled"
)

// Script is one entry of the script inventory (0002): a path script, or an
// inline script of a flow.
type Script struct {
	// ID keys the inventory: a path script's path, or `inline-` plus 16
	// hex digits of the SHA-256 of the flow path and the step's site.
	ID   string     `json:"id"`
	Kind ScriptKind `json:"kind"`
	// Path is the script's Windmill path: a path script's own, or
	// `<flow path>/<module id>` for an inline step.
	Path string `json:"path"`
	// Flow, Module and Site locate an inline step: its flow, its module id
	// and its site path within the flow, iteration segments left out.
	Flow   string `json:"flow,omitempty"`
	Module string `json:"module,omitempty"`
	Site   string `json:"site,omitempty"`
	// File is the root-relative file the code was read from: a path
	// script's source, or an inline step's `!inline` content file; "" for
	// content written in flow.yaml.
	File string `json:"file,omitempty"`
	// Language is the language a successful deploy records, with the
	// bun/bunnative switch applied (SY-V3, OF-15).
	Language openflow.Language `json:"language"`
	Harness  Class             `json:"harness"`
	Boundary Boundary          `json:"boundary"`
	// Content is the code, which the build bundles; ContentDigest is its
	// SHA-256.
	Content       string `json:"-"`
	ContentDigest string `json:"content_digest"`
	// Lock is the lock text exactly as the folder gives it, nil when there
	// is none (OF-15, OF-V6). windmill-lite never generates or rewrites a
	// lock.
	Lock     *string  `json:"lock"`
	LockFile string   `json:"lock_file,omitempty"`
	Bundling Bundling `json:"bundling"`
	// Codebase is the codebases entry that selects the script.
	Codebase *Codebase `json:"codebase,omitempty"`
	// ImportBase is the path that relative imports resolve against, as a
	// file (WR-28, WR-29).
	ImportBase string `json:"import_base"`
	// Annotations are the recognised header annotations of a Bun-family
	// script (WR-31), in the order npm, nodejs, native, nobundling,
	// sandbox.
	Annotations []string `json:"annotations,omitempty"`
	// Schema is the `.script.yaml` schema of a path script, verbatim.
	Schema json.RawMessage `json:"schema,omitempty"`
	// Metadata holds a path script's `.script.yaml` keys that matter when
	// it runs.
	Metadata *Metadata `json:"metadata,omitempty"`
	// DependencySet keys the script's dependencies in the harness pool key
	// (0004): `bundled` for scripts whose dependencies are inlined,
	// `lock:<sha256>` for a nobundling script, `codebase:<index>` for a
	// codebase script.
	DependencySet string `json:"dependency_set"`
}

// Codebase is the codebases entry of wmill.yaml that selects a script
// (SY-8). The build reads the entry's settings from the folder's
// configuration by Index.
type Codebase struct {
	Index        int    `json:"index"`
	RelativePath string `json:"relative_path"`
	// Format is the bundle format: `esm` only when the entry says so,
	// otherwise `cjs` (WR-47).
	Format string `json:"format"`
}

// Metadata holds the `.script.yaml` keys of a path script that matter when
// it runs (SY-4).
type Metadata struct {
	Envs            []string `json:"envs,omitempty"`
	Timeout         *int32   `json:"timeout,omitempty"`
	CacheTTL        *int32   `json:"cache_ttl,omitempty"`
	HasPreprocessor *bool    `json:"has_preprocessor,omitempty"`
}

// annotationFlags are the flags of WR-31, in their table's order.
var annotationFlags = []string{"npm", "nodejs", "native", "nobundling", "sandbox"}

// pathScript adds a path script to the inventory once and returns its
// entry, or nil when it is refused. flow and step name the step that
// reaches it, "" for a root script; errors name them.
func (c *compiler) pathScript(s *syncfolder.Script, flow, step string) *Script {
	if e, ok := c.prog.Scripts[s.Path]; ok {
		return e
	}
	e := &Script{
		ID:         s.Path,
		Kind:       PathScript,
		Path:       s.Path,
		File:       s.File,
		Language:   s.Language,
		Content:    s.Content,
		Lock:       s.Lock,
		LockFile:   s.LockFile,
		ImportBase: s.Path,
		Schema:     s.Metadata.Schema,
	}
	if m := s.Metadata; len(m.Envs) > 0 || m.Timeout != nil || m.CacheTTL != nil || m.HasPreprocessor != nil {
		e.Metadata = &Metadata{Envs: m.Envs, Timeout: m.Timeout, CacheTTL: m.CacheTTL, HasPreprocessor: m.HasPreprocessor}
	}
	if !c.finish(e, flow, step) {
		return nil
	}
	if cb := s.Codebase; cb != nil {
		var settings struct {
			Format        *string         `json:"format"`
			CustomBundler json.RawMessage `json:"customBundler"`
		}
		_ = json.Unmarshal(cb.Settings, &settings)
		if b := string(settings.CustomBundler); b != "" && b != "null" && b != `""` {
			// The reference does not cover customBundler yet (WR-48), so
			// it is refused (0001 §5a).
			c.fail(&Error{Kind: ErrUnsupported, Flow: flow, Step: step, Script: s.Path, Selector: -1,
				Msg: "unsupported: customBundler (codebase entry " + fmt.Sprint(cb.Index) + ")"})
			return nil
		}
		format := "cjs"
		if settings.Format != nil && *settings.Format == "esm" {
			format = "esm"
		}
		e.Codebase = &Codebase{Index: cb.Index, RelativePath: cb.RelativePath, Format: format}
		e.Bundling = BundlingCodebase
		e.DependencySet = fmt.Sprintf("codebase:%d", cb.Index)
	}
	c.prog.Scripts[e.ID] = e
	return e
}

// rawScript reads an inline step's `!inline` files, adds it to the
// inventory and returns its leaf.
func (fc *flowCompiler) rawScript(m *openflow.Module, v *openflow.RawScript, site conformance.SitePath, sc scope, in []Input) *Leaf {
	leaf := &Leaf{Script: inlineID(sc.flow, site), Inputs: in}
	files, err := fc.folder.InlineScript(fc.doc, m.ID, v)
	if err != nil {
		fc.fail(&Error{Kind: ErrLoad, Flow: sc.flow, Step: m.ID, Selector: -1, Err: err})
		return leaf
	}
	if files == nil {
		files = &syncfolder.InlineScript{}
	}
	lang := v.Language
	if lang == openflow.LangBun || lang == openflow.LangBunNative {
		// A deploy relocks a Bun-family rawscript whose language disagrees
		// with its header, and relabels it from the `native` annotation
		// (OF-15, SY-V3, WR-40); one whose lock is empty is relocked and
		// relabelled too.
		lang = openflow.LangBun
		if syncfolder.HasNativeAnnotation(v.Content) {
			lang = openflow.LangBunNative
		}
	}
	e := &Script{
		ID:       inlineID(sc.flow, site),
		Kind:     InlineScript,
		Path:     sc.flow + "/" + m.ID,
		Flow:     sc.flow,
		Module:   m.ID,
		Site:     site.String(),
		File:     files.ContentFile,
		Language: lang,
		Content:  v.Content,
		Lock:     v.Lock,
		LockFile: files.LockFile,
	}
	// The import base is the step's runnable path, collapsed to
	// `<flow path>/flow` inside container bodies (WR-29). A rawscript's
	// own `path` replaces it only in the failure and preprocessor slots,
	// which a deploy keeps as raw module values; it drops the field from
	// the inline scripts of the module lists, which then run from the
	// default base.
	switch {
	case sc.slot && v.Path != nil && *v.Path != "":
		e.ImportBase = CollapseBodies(*v.Path)
	case sc.preprocessorSlot:
		e.ImportBase = sc.flow + "/preprocessor"
	case sc.nested:
		e.ImportBase = sc.flow + "/flow"
	default:
		e.ImportBase = sc.flow + "/" + m.ID
	}
	if !fc.finish(e, sc.flow, m.ID) {
		return leaf
	}
	fc.prog.Scripts[e.ID] = e
	leaf.Harness, leaf.Boundary = e.Harness, e.Boundary
	return leaf
}

// bodySegment matches a path segment that names a nested flow body (WR-29).
var bodySegment = regexp.MustCompile(`^(branchone|branchall|forloop|loop)-[0-9]+$`)

// CollapseBodies is the import base of a runnable path (WR-29): a path with
// a nested-body segment (`branchone-N`, `branchall-N`, `forloop-N`,
// `loop-N`) is collapsed to `<prefix>/flow`, so its imports resolve beside
// the prefix; any other path is its own import base.
func CollapseBodies(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if bodySegment.MatchString(s) {
			return strings.Join(append(segs[:i:i], "flow"), "/")
		}
	}
	return p
}

// finish fills the harness, annotations, bundling and dependency set of an
// entry, and refuses it when its language has no harness or it opts into
// argument validation.
func (c *compiler) finish(e *Script, flow, step string) bool {
	sum := sha256.Sum256([]byte(e.Content))
	e.ContentDigest = "sha256:" + hex.EncodeToString(sum[:])
	class, boundary, ok := harnessOf(e.Language)
	if !ok || !available(class) {
		c.fail(languageError(e.Language, flow, step, e.Path))
		return false
	}
	e.Harness, e.Boundary = class, boundary
	if bunFamily(e.Language) {
		for _, a := range annotationFlags {
			if syncfolder.HasAnnotation(e.Content, a) {
				e.Annotations = append(e.Annotations, a)
			}
		}
		if syncfolder.HasAnnotation(e.Content, "schema_validation") {
			// Argument validation is not covered by the reference yet
			// (OF-18, 0009), so a script that opts in is refused (0001
			// §5a).
			c.fail(&Error{Kind: ErrUnsupported, Flow: flow, Step: step, Script: e.Path, Selector: -1,
				Msg: "unsupported: schema_validation"})
			return false
		}
	}
	switch {
	case e.Language == openflow.LangNativeTS:
		e.Bundling = BundlingTranspiled
	case hasFlag(e.Annotations, "nobundling"):
		e.Bundling = BundlingNone
	default:
		e.Bundling = BundlingBundled
	}
	e.DependencySet = "bundled"
	if e.Bundling == BundlingNone {
		lock := ""
		if e.Lock != nil {
			lock = *e.Lock
		}
		sum := sha256.Sum256([]byte(lock))
		e.DependencySet = "lock:sha256:" + hex.EncodeToString(sum[:])
	}
	return true
}

func hasFlag(fs []string, f string) bool {
	for _, x := range fs {
		if x == f {
			return true
		}
	}
	return false
}

func languageError(l openflow.Language, flow, step, script string) *Error {
	e := &Error{Kind: ErrLanguage, Flow: flow, Step: step, Selector: -1, Msg: "unsupported_language: " + string(l)}
	if step == "" {
		e.Script = script
	}
	return e
}

// inlineID is the inventory id of an inline step: unique per flow and
// site, stable across revisions that leave the step where it is, and safe
// as a file name.
func inlineID(flow string, site conformance.SitePath) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d:%s%s", len(flow), flow, site.String())
	return "inline-" + hex.EncodeToString(h.Sum(nil))[:16]
}
