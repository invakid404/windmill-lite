package syncfolder

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/invakid404/windmill-lite/internal/openflow"
	"github.com/invakid404/windmill-lite/internal/yamlx"
)

// Resource is a `<p>.resource.yaml` file (SY-10).
type Resource struct {
	Path         string  `json:"path"`
	File         string  `json:"file"`
	ResourceType string  `json:"resource_type"`
	Description  *string `json:"description"`
	// Value is the resource's value as the JSON the CLI sends. `$var:` and
	// `$res:` strings in it are kept; they are resolved at run time.
	Value json.RawMessage `json:"value"`
}

// ResourceType is a `<name>.resource-type.yaml` file at the root (SY-10).
type ResourceType struct {
	Name            string          `json:"name"`
	File            string          `json:"file"`
	Schema          json.RawMessage `json:"schema"`
	Description     *string         `json:"description"`
	FormatExtension *string         `json:"format_extension"`
	IsFileset       bool            `json:"is_fileset"`
}

// Variable is a `<p>.variable.yaml` file (SY-11).
type Variable struct {
	Path        string  `json:"path"`
	File        string  `json:"file"`
	Value       string  `json:"value"`
	IsSecret    bool    `json:"is_secret"`
	Description *string `json:"description"`
	// Encrypted reports a secret whose Value is ciphertext under the
	// workspace key, as a pull writes it unless wmill.yaml sets
	// plainSecrets. A folder alone cannot supply such a value (SY-11,
	// SY-15); the secret resolver decides what to do with it.
	Encrypted bool `json:"encrypted"`
}

func (f *Folder) readData(k ItemKind, p string) (*entry, fields, error) {
	e, err := f.lookup(k, p)
	if err != nil {
		return nil, fields{}, err
	}
	item := string(k) + " " + p
	data, found, err := f.readFile(e.file)
	if err != nil {
		return nil, fields{}, err.(*Error).withItem(item)
	}
	if !found {
		return nil, fields{}, &Error{Kind: ErrIO, Item: item, File: e.file, Msg: "the file is missing"}
	}
	m, err := parseMapping(e.file, ErrMetadata, data)
	if err != nil {
		return nil, fields{}, err.(*Error).withItem(item)
	}
	return e, m, nil
}

// Resource reads the resource at a Windmill path. File and fileset
// resources, whose values are `!inline` and `!inline_fileset` references,
// are `later` scope and fail with ErrUnsupported.
func (f *Folder) Resource(p string) (*Resource, error) {
	e, m, err := f.readData(KindResource, p)
	if err != nil {
		return nil, err
	}
	item := "resource " + p
	r := &Resource{Path: p, File: e.file, Value: json.RawMessage("null")}
	rt, err := m.str("resource_type")
	if err != nil {
		return nil, err.(*Error).withItem(item)
	}
	if rt != nil {
		r.ResourceType = *rt
	}
	if r.Description, err = m.str("description"); err != nil {
		return nil, err.(*Error).withItem(item)
	}
	if v := m.get("value"); v != nil {
		if feature := fileResource(v); feature != "" {
			return nil, &Error{Kind: ErrUnsupported, Item: item, File: e.file, Line: v.Line, Column: v.Column,
				Msg: "unsupported: " + feature + " (SY-10, later scope)"}
		}
		r.Value = v.AppendJSON(nil)
	}
	return r, nil
}

// fileResource names the kind of a file or fileset resource value: a
// string `!inline_fileset <dir>`, or a mapping whose `content` is
// `!inline <file>` (SY-10).
func fileResource(v *yamlx.Node) string {
	if v.Kind == yamlx.String && strings.HasPrefix(v.Text, "!inline_fileset ") {
		return "fileset resource"
	}
	if c := v.Get("content"); c != nil && c.Kind == yamlx.String && strings.HasPrefix(c.Text, "!inline ") {
		return "file resource"
	}
	return ""
}

// ResourceType reads a resource type by name.
func (f *Folder) ResourceType(name string) (*ResourceType, error) {
	e, m, err := f.readData(KindResourceType, name)
	if err != nil {
		return nil, err
	}
	item := "resource_type " + name
	rt := &ResourceType{Name: name, File: e.file, Schema: m.raw("schema")}
	if rt.Description, err = m.str("description"); err != nil {
		return nil, err.(*Error).withItem(item)
	}
	if rt.FormatExtension, err = m.str("format_extension"); err != nil {
		return nil, err.(*Error).withItem(item)
	}
	if rt.IsFileset, err = m.boolean("is_fileset"); err != nil {
		return nil, err.(*Error).withItem(item)
	}
	return rt, nil
}

// Variable reads the variable at a Windmill path. A secret is filtered
// out when wmill.yaml sets skipSecrets (SY-11, SY-13).
func (f *Folder) Variable(p string) (*Variable, error) {
	e, m, err := f.readData(KindVariable, p)
	if err != nil {
		return nil, err
	}
	item := "variable " + p
	v := &Variable{Path: p, File: e.file}
	if v.IsSecret, err = m.boolean("is_secret"); err != nil {
		return nil, err.(*Error).withItem(item)
	}
	if v.IsSecret && f.Config.SkipSecrets {
		return nil, &Error{Kind: ErrFiltered, Item: item, File: e.file,
			Msg: "filtered out: a secret, and wmill.yaml sets skipSecrets"}
	}
	val, err := m.str("value")
	if err != nil {
		return nil, err.(*Error).withItem(item)
	}
	if val != nil {
		v.Value = *val
	}
	if v.Description, err = m.str("description"); err != nil {
		return nil, err.(*Error).withItem(item)
	}
	v.Encrypted = v.IsSecret && !f.Config.PlainSecrets
	return v, nil
}

// Item is a by-path target: a *Script or a *Flow.
type Item interface {
	ItemKind() ItemKind
	ItemPath() string
}

func (s *Script) ItemKind() ItemKind { return KindScript }
func (s *Script) ItemPath() string   { return s.Path }
func (f *Flow) ItemKind() ItemKind   { return KindFlow }
func (f *Flow) ItemPath() string     { return f.Path }

// Target reads the target of a `script` or `flow` step of the flow at
// path from. A step whose target the folder cannot supply fails with an
// error naming the step (SY-15): a `hub/` path (ErrHubPath), a path not in
// the folder or filtered out, or a target that fails to read. Whether a
// script step's `hash` matches is the compiler's check.
func (f *Folder) Target(from string, m *openflow.Module) (Item, error) {
	return f.target(from, m, f.Flow)
}

// TargetDocument is Target, except that a flow target is read with
// FlowDocument, its inline references unresolved.
func (f *Folder) TargetDocument(from string, m *openflow.Module) (Item, error) {
	return f.target(from, m, f.FlowDocument)
}

func (f *Folder) target(from string, m *openflow.Module, flow func(string) (*Flow, error)) (Item, error) {
	step := fmt.Sprintf("flow %s step '%s'", from, m.ID)
	var (
		kind ItemKind
		p    string
	)
	switch v := m.Value.(type) {
	case *openflow.PathScript:
		kind, p = KindScript, v.Path
	case *openflow.PathFlow:
		kind, p = KindFlow, v.Path
	default:
		return nil, &Error{Kind: ErrNotInFolder, Step: step, Msg: fmt.Sprintf("a %s step has no by-path target", m.Value.Kind())}
	}
	if strings.HasPrefix(p, "hub/") {
		return nil, &Error{Kind: ErrHubPath, Step: step,
			Msg: fmt.Sprintf("the %s %s is a hub path, which a sync folder cannot supply (SY-15)", kind, p)}
	}
	var (
		it  Item
		err error
	)
	if kind == KindScript {
		var s *Script
		if s, err = f.Script(p); err == nil {
			it = s
		}
	} else {
		var fl *Flow
		if fl, err = flow(p); err == nil {
			it = fl
		}
	}
	if err != nil {
		e := *err.(*Error)
		e.Step = step
		return nil, &e
	}
	return it, nil
}
