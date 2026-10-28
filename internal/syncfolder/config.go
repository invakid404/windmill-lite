package syncfolder

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/invakid404/windmill-lite/internal/openflow"
	"github.com/invakid404/windmill-lite/internal/yamlx"
)

// ConfigFile is the name of the file that marks the sync root (SY-1).
const ConfigFile = "wmill.yaml"

// Config is what windmill-lite reads from wmill.yaml (SY-13). Keys it does
// not use are ignored; the keys it uses are type-checked.
type Config struct {
	// DefaultTs is the language of a `.ts` script: bun (the default) or
	// deno (SY-3).
	DefaultTs openflow.Language `json:"defaultTs"`
	// NonDottedPaths selects the `__flow` folder suffix instead of `.flow`
	// (SY-6).
	NonDottedPaths bool `json:"nonDottedPaths"`
	// Includes, Excludes and ExtraIncludes are the path filter's globs
	// (SY-13, SY-V2). Nil means the key is absent or null; an empty, non-nil
	// list means the key is an empty list, which for Includes matches
	// nothing.
	Includes      []string `json:"includes"`
	Excludes      []string `json:"excludes"`
	ExtraIncludes []string `json:"extraIncludes"`

	SkipScripts       bool `json:"skipScripts"`
	SkipFlows         bool `json:"skipFlows"`
	SkipResources     bool `json:"skipResources"`
	SkipResourceTypes bool `json:"skipResourceTypes"`
	SkipVariables     bool `json:"skipVariables"`
	SkipSecrets       bool `json:"skipSecrets"`

	// PlainSecrets says the tree was pulled with secret values in plain
	// text rather than as ciphertext (SY-11).
	PlainSecrets bool `json:"plainSecrets"`
	// NoCdToRoot is read only to label the divergence of SY-V4: the root is
	// always the directory of wmill.yaml.
	NoCdToRoot bool `json:"noCdToRoot"`

	Codebases []Codebase `json:"codebases"`
	// Workspaces are the declared workspace names, from `workspaces` and
	// its deprecated spellings, sorted. They identify per-workspace
	// override files (SY-12).
	Workspaces []string `json:"workspaces"`

	includes, excludes, extraIncludes []*glob
}

// Codebase is one entry of wmill.yaml's `codebases` (SY-8).
type Codebase struct {
	// Index is the entry's position in `codebases`.
	Index        int      `json:"index"`
	RelativePath string   `json:"relative_path"`
	Includes     []string `json:"includes"`
	Excludes     []string `json:"excludes"`
	// Settings is the whole entry as JSON, for the bundler: `format`,
	// `external`, `inject`, `define`, `loader`, `banner`, `customBundler`,
	// `assets`.
	Settings json.RawMessage `json:"settings"`

	includes, excludes []*glob
}

func parseConfig(src []byte) (*Config, []string, error) {
	f, err := parseMapping(ConfigFile, ErrConfig, src)
	if err != nil {
		return nil, nil, err
	}
	c := &Config{DefaultTs: openflow.LangBun}
	var warnings []string
	switch ts, err := f.str("defaultTs"); {
	case err != nil:
		return nil, nil, err
	case ts == nil:
		warnings = append(warnings, "wmill.yaml sets no defaultTs, so .ts scripts are bun (SY-13)")
	case *ts == "bun" || *ts == "deno":
		c.DefaultTs = openflow.Language(*ts)
	default:
		return nil, nil, f.wrong(f.get("defaultTs"), "defaultTs", `"bun" or "deno"`)
	}
	for _, b := range []struct {
		key string
		dst *bool
	}{
		{"nonDottedPaths", &c.NonDottedPaths},
		{"skipScripts", &c.SkipScripts},
		{"skipFlows", &c.SkipFlows},
		{"skipResources", &c.SkipResources},
		{"skipResourceTypes", &c.SkipResourceTypes},
		{"skipVariables", &c.SkipVariables},
		{"skipSecrets", &c.SkipSecrets},
		{"plainSecrets", &c.PlainSecrets},
		{"noCdToRoot", &c.NoCdToRoot},
	} {
		if *b.dst, err = f.boolean(b.key); err != nil {
			return nil, nil, err
		}
	}
	for _, l := range []struct {
		key  string
		dst  *[]string
		glob *[]*glob
	}{
		{"includes", &c.Includes, &c.includes},
		{"excludes", &c.Excludes, &c.excludes},
		{"extraIncludes", &c.ExtraIncludes, &c.extraIncludes},
	} {
		if *l.dst, err = f.strings(l.key); err != nil {
			return nil, nil, err
		}
		if *l.glob, err = compileGlobs(f, l.key, *l.dst); err != nil {
			return nil, nil, err
		}
	}
	if c.Codebases, err = parseCodebases(f); err != nil {
		return nil, nil, err
	}
	if c.Workspaces, err = parseWorkspaces(f); err != nil {
		return nil, nil, err
	}
	if n := f.get("overrides"); n != nil && !(n.Kind == yamlx.Mapping && len(n.Pairs) == 0) {
		return nil, nil, &Error{Kind: ErrConfig, File: ConfigFile, Line: n.Line, Column: n.Column,
			Msg: "a top-level overrides is not supported; overrides belong under workspaces (SY-13)"}
	}
	return c, warnings, nil
}

func compileGlobs(f fields, key string, patterns []string) ([]*glob, error) {
	var out []*glob
	for _, p := range patterns {
		g, err := compileGlob(p)
		if err != nil {
			e := err.(*Error)
			e.File = ConfigFile
			if n := f.get(key); n != nil {
				e.Line, e.Column = n.Line, n.Column
			}
			e.Msg = key + ": " + e.Msg
			return nil, e
		}
		out = append(out, g)
	}
	return out, nil
}

func parseCodebases(f fields) ([]Codebase, error) {
	n := f.get("codebases")
	if n == nil {
		return nil, nil
	}
	if n.Kind != yamlx.Sequence {
		return nil, f.wrong(n, "codebases", "a list")
	}
	out := make([]Codebase, 0, len(n.Items))
	for i, it := range n.Items {
		key := fmt.Sprintf("codebases[%d]", i)
		if it.Kind != yamlx.Mapping {
			return nil, f.wrong(it, key, "a mapping")
		}
		e := fields{file: f.file, kind: f.kind, m: it}
		cb := Codebase{Index: i, Settings: it.AppendJSON(nil)}
		rel, err := e.str("relative_path")
		if err != nil {
			return nil, err
		}
		if rel != nil {
			cb.RelativePath = *rel
		}
		if cb.Includes, err = e.strings("includes"); err != nil {
			return nil, err
		}
		if cb.Excludes, err = e.strings("excludes"); err != nil {
			return nil, err
		}
		if cb.includes, err = compileGlobs(e, "includes", cb.Includes); err != nil {
			return nil, err
		}
		if cb.excludes, err = compileGlobs(e, "excludes", cb.Excludes); err != nil {
			return nil, err
		}
		out = append(out, cb)
	}
	return out, nil
}

// parseWorkspaces collects the declared workspace names: the keys of
// `workspaces` and of its deprecated spellings `gitBranches`,
// `environments` and `git_branches` (SY-13).
func parseWorkspaces(f fields) ([]string, error) {
	seen := map[string]bool{}
	for _, key := range []string{"workspaces", "gitBranches", "environments", "git_branches"} {
		n := f.get(key)
		if n == nil {
			continue
		}
		if n.Kind != yamlx.Mapping {
			return nil, f.wrong(n, key, "a mapping")
		}
		for _, p := range n.Pairs {
			seen[p.Key] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// pathFilter applies includes, excludes and extraIncludes to a file's
// root-relative path (SY-13, SY-V2). It returns "" when the file passes,
// or the reason it does not.
func (c *Config) pathFilter(p string) string {
	if len(c.Includes) == 0 && len(c.Excludes) == 0 {
		return "" // the filter is not active; extraIncludes alone does nothing
	}
	if c.Includes != nil && !anyMatch(c.includes, p) {
		if len(c.Includes) == 0 {
			return "wmill.yaml's includes is an empty list, which matches nothing"
		}
		return "no includes glob of wmill.yaml matches it"
	}
	for _, g := range c.excludes {
		if g.match(p) {
			return fmt.Sprintf("the excludes glob %q of wmill.yaml matches it", g.source)
		}
	}
	if len(c.ExtraIncludes) > 0 && !anyMatch(c.extraIncludes, p) {
		return "no extraIncludes glob of wmill.yaml matches it"
	}
	return ""
}

func anyMatch(gs []*glob, p string) bool {
	for _, g := range gs {
		if g.match(p) {
			return true
		}
	}
	return false
}

// codebase returns the first codebases entry that selects the script
// source file p (SY-8): an entry without includes selects every file,
// otherwise one of its includes must match, and a matching excludes glob
// vetoes the entry.
func (c *Config) codebase(p string) *Codebase {
	for i := range c.Codebases {
		cb := &c.Codebases[i]
		if cb.Includes != nil && !anyMatch(cb.includes, p) {
			continue
		}
		if anyMatch(cb.excludes, p) {
			continue
		}
		return cb
	}
	return nil
}

// overrideWorkspace reports whether base, the item path of a `<p>.<type>`
// file, is `<base>.<workspace>` for a declared workspace, which makes the
// file a per-workspace override (SY-12). Workspace names are matched in
// their file-name form, with `/ \ : * ? " < > | .` replaced by `_`.
func (c *Config) overrideWorkspace(base string) (string, bool) {
	dot := strings.LastIndexByte(base, '.')
	if dot < 0 || strings.ContainsRune(base[dot:], '/') {
		return "", false
	}
	suffix := base[dot+1:]
	for _, w := range c.Workspaces {
		if fileNameWorkspace(w) == suffix {
			return w, true
		}
	}
	return "", false
}

func fileNameWorkspace(w string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|.`, r) {
			return '_'
		}
		return r
	}, w)
}
