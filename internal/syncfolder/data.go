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
	data, found, refused, rerr := f.readDecoded(e.file)
	switch {
	case rerr != nil:
		return nil, fields{}, rerr.withItem(item)
	case !found:
		return nil, fields{}, &Error{Kind: ErrIO, Item: item, File: e.file, Msg: "the file is missing"}
	case refused != nil:
		return nil, fields{}, refused.withItem(item)
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
	r := &Resource{Path: p, File: e.file}
	// The CLI looks for a `content` reference in the value before it
	// sends anything, and reading a field of a missing or null value
	// throws, so the push stops at this item (SY-10).
	switch v := m.m.Get("value"); {
	case v == nil:
		return nil, &Error{Kind: ErrMetadata, Item: item, File: e.file,
			Msg: "the resource has no value, which fails the CLI's push: TypeError: Cannot read properties of undefined (reading 'content') (SY-10)"}
	case v.Kind == yamlx.Null:
		return nil, &Error{Kind: ErrMetadata, Item: item, File: e.file, Line: v.Line, Column: v.Column,
			Msg: "the resource's value is null, which fails the CLI's push: TypeError: Cannot read properties of null (reading 'content') (SY-10)"}
	default:
		if feature := fileResource(v); feature != "" {
			return nil, &Error{Kind: ErrUnsupported, Item: item, File: e.file, Line: v.Line, Column: v.Column,
				Msg: "unsupported: " + feature + " (SY-10, later scope)"}
		}
		r.Value = v.AppendJSON(nil)
	}
	rt, err := m.str("resource_type")
	switch {
	case err != nil:
		return nil, err.(*Error).withItem(item)
	case rt == nil:
		return nil, &Error{Kind: ErrMetadata, Item: item, File: e.file,
			Msg: "the resource has no resource_type, which the server requires (SY-10)"}
	}
	r.ResourceType = *rt
	if r.Description, err = m.str("description"); err != nil {
		return nil, err.(*Error).withItem(item)
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
	// The CLI sends the file's keys as they are, and the server refuses to
	// create a variable without value, is_secret or description, naming
	// the first one missing (SY-11). A value of the wrong type, null
	// included, is refused first.
	required := []struct{ key, want string }{{"value", "a string"}, {"is_secret", "a boolean"}, {"description", "a string"}}
	for _, k := range required {
		if n := m.m.Get(k.key); n != nil && n.Kind == yamlx.Null {
			return nil, m.wrong(n, k.key, k.want).withItem(item)
		}
	}
	val, err := m.str("value")
	if err != nil {
		return nil, err.(*Error).withItem(item)
	}
	if v.Description, err = m.str("description"); err != nil {
		return nil, err.(*Error).withItem(item)
	}
	for _, k := range required {
		if !m.has(k.key) {
			return nil, &Error{Kind: ErrMetadata, Item: item, File: e.file,
				Msg: fmt.Sprintf("missing field `%s`: the server refuses a new variable without value, is_secret and description (SY-11)", k.key)}
		}
	}
	v.Value = *val
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
