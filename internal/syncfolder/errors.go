package syncfolder

import (
	"fmt"
	"strings"
)

// ErrorKind classifies an [Error].
type ErrorKind string

// The error kinds. A load error names the item or file it is about; see the
// package README for when each one is raised.
const (
	// ErrConfig: wmill.yaml has a value of the wrong type, or a non-empty
	// top-level `overrides` (SY-13).
	ErrConfig ErrorKind = "invalid_config"
	// ErrGlob: a wmill.yaml glob outside the syntax windmill-lite reads
	// as minimatch does (a restriction of 0001 §5a).
	ErrGlob ErrorKind = "unsupported_glob"
	// ErrYAML: a file is not YAML that yamlx reads (SY-9). The wrapped
	// *yamlx.Error says whether the CLI rejects it too.
	ErrYAML ErrorKind = "yaml"
	// ErrNotInFolder: no item of that kind has the path.
	ErrNotInFolder ErrorKind = "not_in_folder"
	// ErrFiltered: the item is excluded by wmill.yaml's filters or skip
	// switches (SY-13, SY-15).
	ErrFiltered ErrorKind = "filtered"
	// ErrHubPath: a step's target is a `hub/` path, which a folder cannot
	// supply (SY-15).
	ErrHubPath ErrorKind = "hub_path"
	// ErrDuplicate: two files define one item (SY-2, PA-1).
	ErrDuplicate ErrorKind = "duplicate_path"
	// ErrDottedPath: a script file whose path the CLI would truncate at
	// its first dot (SY-1; a divergence of 0002).
	ErrDottedPath ErrorKind = "dotted_path"
	// ErrFlowLayout: a flow folder whose suffix style is not the one
	// nonDottedPaths selects, or that has no flow.yaml (SY-6).
	ErrFlowLayout ErrorKind = "flow_layout"
	// ErrMissingSource: script metadata without a source file (SY-2).
	ErrMissingSource ErrorKind = "missing_source"
	// ErrLanguage: a script's language cannot be inferred from its file
	// name (SY-3), or its source is a `.js` file (SY-18).
	ErrLanguage ErrorKind = "unknown_language"
	// ErrMetadata: a metadata, resource, variable or resource-type file
	// has a value of the wrong type (SY-4, SY-5, SY-10, SY-11), or lacks
	// one the push needs (SY-10, SY-11).
	ErrMetadata ErrorKind = "invalid_metadata"
	// ErrFlow: the flow document does not decode (OF-2). The wrapped
	// *openflow.Error has Windmill's message.
	ErrFlow ErrorKind = "invalid_flow"
	// ErrMissingInline: inline-script content files are missing (SY-7).
	ErrMissingInline ErrorKind = "missing_inline_file"
	// ErrPathEscape: an `!inline` reference leaves the sync root (SY-7; a
	// divergence of 0002).
	ErrPathEscape ErrorKind = "path_escape"
	// ErrText: a text file the CLI's decoder refuses, for its UTF-16 or
	// UTF-32 byte order mark (SY-19), or a script, inline or lock file
	// that is not valid UTF-8 (a restriction of 0001 §5a).
	ErrText ErrorKind = "invalid_text"
	// ErrUnsupported: a feature windmill-lite does not load (multi-file
	// scripts, JSON mode and JSON-mode metadata, file and fileset
	// resources). The message
	// is `unsupported: <feature>`, as 0001 §3 has it.
	ErrUnsupported ErrorKind = "unsupported_feature"
	// ErrIO: reading the folder failed.
	ErrIO ErrorKind = "io"
)

// Error is a load error.
type Error struct {
	Kind ErrorKind
	// Item names the item, as "<kind> <path>" (`script f/a/x`), when the
	// error is about one.
	Item string
	// Step names the flow step whose target this is, as "<flow path>
	// step '<id>'", for errors from Target.
	Step string
	// File is the root-relative file the error is about, and Line and
	// Column its 1-based position when known.
	File         string
	Line, Column int
	Msg          string
	// Err is the underlying error: a *yamlx.Error, an *openflow.Error or
	// an I/O error.
	Err error
}

func (e *Error) Error() string {
	var b strings.Builder
	for _, s := range []string{e.Step, e.Item} {
		if s != "" {
			b.WriteString(s)
			b.WriteString(": ")
		}
	}
	if e.File != "" {
		b.WriteString(e.File)
		if e.Line > 0 {
			fmt.Fprintf(&b, ":%d", e.Line)
			if e.Column > 0 {
				fmt.Fprintf(&b, ":%d", e.Column)
			}
		}
		b.WriteString(": ")
	}
	b.WriteString(e.Msg)
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// withItem returns a copy of e naming item, unless e already names one.
func (e *Error) withItem(item string) *Error {
	c := *e
	if c.Item == "" {
		c.Item = item
	}
	return &c
}
