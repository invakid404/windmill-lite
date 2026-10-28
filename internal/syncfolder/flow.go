package syncfolder

import (
	"fmt"
	"path"
	"strings"

	"github.com/invakid404/windmill-lite/internal/openflow"
	"github.com/invakid404/windmill-lite/internal/yamlx"
)

// Flow is a flow folder as the CLI would push it (SY-6, SY-7).
type Flow struct {
	Path string `json:"path"`
	// Dir is the flow folder and File its flow.yaml, root-relative.
	Dir  string `json:"dir"`
	File string `json:"file"`
	// Document is the decoded flow.yaml (A3's decoder), with the content
	// and lock of every inline script read from its files.
	Document *openflow.Flow `json:"flow"`
	// Inline lists the rawscripts whose content was an `!inline`
	// reference, in traversal order.
	Inline []InlineScript `json:"inline"`
}

// InlineScript records where a rawscript's content and lock came from.
type InlineScript struct {
	// Module is the id of the module, or of the AI-agent tool, whose value
	// is the rawscript.
	Module string `json:"module"`
	// ContentFile is the root-relative file the content was read from.
	ContentFile string `json:"content_file"`
	// LockFile is the file the lock was read from, "" when the lock was
	// not a reference or its file is missing.
	LockFile string `json:"lock_file,omitempty"`
	// Script is the rawscript in Document.
	Script *openflow.RawScript `json:"-"`
}

// Flow reads the flow at a Windmill path.
func (f *Folder) Flow(p string) (*Flow, error) {
	e, err := f.lookup(KindFlow, p)
	if err != nil {
		return nil, err
	}
	item := "flow " + p
	fl := &Flow{Path: p, Dir: e.file, File: e.file + "/flow.yaml"}
	data, found, err := f.readFile(fl.File)
	if err != nil {
		return nil, err.(*Error).withItem(item)
	}
	if !found {
		return nil, &Error{Kind: ErrFlowLayout, Item: item, File: fl.Dir,
			Msg: "the flow folder has no flow.yaml, the only file the CLI pushes from it (SY-6)"}
	}
	js, err := yamlx.ToJSON(data)
	if err != nil {
		return nil, yamlError(fl.File, err).withItem(item)
	}
	if fl.Document, err = openflow.DecodeFlow(js); err != nil {
		return nil, &Error{Kind: ErrFlow, Item: item, File: fl.File, Msg: err.Error(), Err: err}
	}
	if err := f.inline(fl); err != nil {
		return nil, err.withItem(item)
	}
	return fl, nil
}

// inline replaces `!inline` references with their files' text (SY-7,
// SY-V1). It visits every rawscript in the decoder's traversal order,
// including the rawscript tools of AI agents, and reports every missing
// content file at once, as the CLI does.
func (f *Folder) inline(fl *Flow) *Error {
	var missing []string
	visit := func(id string, rs *openflow.RawScript) *Error {
		in, miss, err := f.inlineScript(fl.Dir, id, rs)
		if err != nil {
			return err
		}
		if miss != "" {
			missing = append(missing, miss)
		} else if in != nil {
			fl.Inline = append(fl.Inline, *in)
		}
		return nil
	}
	for m := range fl.Document.Value.AllModules() {
		switch v := m.Value.(type) {
		case *openflow.RawScript:
			if err := visit(m.ID, v); err != nil {
				return err
			}
		case *openflow.AIAgent:
			for _, t := range v.Tools {
				if mt, ok := t.Value.(*openflow.ModuleTool); ok {
					if rs, ok := mt.Value.(*openflow.RawScript); ok {
						if err := visit(t.ID, rs); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	if len(missing) > 0 {
		return &Error{Kind: ErrMissingInline, File: fl.File,
			Msg: "missing inline script file(s): " + strings.Join(missing, ", ")}
	}
	return nil
}

// inlineScript resolves one rawscript. Only content that starts with
// `!inline` is a reference; otherwise the lock, even an `!inline` string,
// stays as written (SY-V1). The file is the second space-separated token
// of the string. A missing content file is tried again as `<module id>.`
// plus everything after the first dot of the name; a missing lock file is
// the empty lock (SY-7).
func (f *Folder) inlineScript(dir, id string, rs *openflow.RawScript) (*InlineScript, string, *Error) {
	if !strings.HasPrefix(rs.Content, "!inline") {
		return nil, "", nil
	}
	in := &InlineScript{Module: id, Script: rs}
	name, ok := secondToken(rs.Content)
	if !ok {
		return nil, fmt.Sprintf("%q names no file (module %s)", rs.Content, id), nil
	}
	p, err := flowPath(dir, name)
	if err != nil {
		return nil, "", err
	}
	data, found, rerr := f.readFile(p)
	if rerr != nil {
		return nil, "", rerr.(*Error)
	}
	if !found {
		if _, rest, ok := strings.Cut(name, "."); ok {
			fb, err := flowPath(dir, id+"."+rest)
			if err != nil {
				return nil, "", err
			}
			if data, found, rerr = f.readFile(fb); rerr != nil {
				return nil, "", rerr.(*Error)
			}
			if found {
				p = fb
			}
		}
	}
	if !found {
		return nil, fmt.Sprintf("%s (module %s)", p, id), nil
	}
	text, terr := checkText(data, p, "")
	if terr != nil {
		return nil, "", terr.(*Error)
	}
	rs.Content, in.ContentFile = text, p
	if rs.Lock != nil {
		if name, ok := inlineLockFile(*rs.Lock); ok {
			lp, err := flowPath(dir, name)
			if err != nil {
				return nil, "", err
			}
			lock, lockFile, err := f.readLockFile(lp)
			if err != nil {
				return nil, "", err
			}
			rs.Lock, in.LockFile = &lock, lockFile
		}
	}
	return in, "", nil
}

// inlineLockFile recognises a lock reference: a string that, after leading
// white space, starts with `!inline ` (with the space). The file is the
// second space-separated token of the untrimmed string, so one leading
// space selects `!inline` itself and two select "" (SY-V1).
func inlineLockFile(s string) (string, bool) {
	if !strings.HasPrefix(trimStartJS(s), "!inline ") {
		return "", false
	}
	return secondToken(s)
}

// secondToken is JavaScript's `s.split(" ")[1]`; ok is false when s has no
// space.
func secondToken(s string) (string, bool) {
	_, rest, ok := strings.Cut(s, " ")
	if !ok {
		return "", false
	}
	tok, _, _ := strings.Cut(rest, " ")
	return tok, true
}

// flowPath joins an inline reference to the flow folder, as the CLI's path
// join does: `..` steps are resolved and a leading `/` is not special. Any
// path inside the sync root is accepted; one that leaves it is refused
// (ADR 0002; the CLI would read it).
func flowPath(dir, name string) (string, *Error) {
	p := path.Join(dir, name)
	if p == ".." || strings.HasPrefix(p, "../") {
		return "", &Error{Kind: ErrPathEscape, File: dir + "/flow.yaml",
			Msg: fmt.Sprintf("the inline reference %q leaves the sync root, which windmill-lite refuses (ADR 0002)", name)}
	}
	return p, nil
}
