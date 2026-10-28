package ir

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/invakid404/windmill-lite/internal/yamlx"
)

// Contract is the host contract, the build's second input beside the sync
// folder (0002): the bundle's roots, its mandatory host-provided call sites
// and its host-provided modules.
type Contract struct {
	// Roots are the flows and scripts the host runs, and every by-path
	// target that steps dispatch locally with runScript* or runFlow*.
	Roots []Root `json:"roots"`
	// Provided are the mandatory host-provided call sites. Each site's
	// target is opaque to the compiler: it is not loaded, validated or
	// bundled unless another path reaches it.
	Provided []ProvidedSite `json:"provided,omitempty"`
	// HostModules are the module paths whose one instance the host and
	// every script share (0010, deviation D12). The build resolves them;
	// the compiler only records them.
	HostModules []string `json:"host_modules,omitempty"`
}

// Root is one root of the bundle: exactly one of Flow and Script is set.
type Root struct {
	Flow   string `json:"flow,omitempty"`
	Script string `json:"script,omitempty"`
}

// ProvidedSite is a mandatory host-provided call site: the site a selector
// names, and its provider, which is a static value or a provider the host
// registers by name at run time.
type ProvidedSite struct {
	Selector Selector `json:"selector"`
	// Static is the provider's value as JSON text, verbatim; nil when the
	// provider is named.
	Static json.RawMessage `json:"static,omitempty"`
	// Provider is the name of the provider the host registers.
	Provider string `json:"provider,omitempty"`
	// Mandatory is always true: the contract lists only mandatory sites.
	Mandatory bool `json:"mandatory"`
}

// Selector names call sites (0008). It is either a module selector (Flow and
// Module, with an optional Occurrence, Iterations or Branches) or a script
// selector (Script).
type Selector struct {
	// Flow is the path of a flow: the root or a by-path flow. Body modules
	// of containers belong to their enclosing flow.
	Flow string `json:"flow,omitempty"`
	// Module is a module id in any module list of Flow.
	Module string `json:"module,omitempty"`
	// Occurrence selects the occurrence k, from 0 in list order, of an id
	// that occurs more than once in one module list. It is required for
	// such an id; a unique id is its own occurrence 0.
	Occurrence *int `json:"occurrence,omitempty"`
	// Iterations and Branches narrow a body module to some iterations or
	// branches. Branches holds indexes as decimal text, and "default".
	Iterations []int    `json:"iterations,omitempty"`
	Branches   []string `json:"branches,omitempty"`
	// Script selects every call of a script: a script step by its path, or
	// an inline step by `<flow path>/<module id>`.
	Script string `json:"script,omitempty"`
}

// String returns the selector as compact text, for messages.
func (s Selector) String() string {
	if s.Script != "" {
		return "{script: " + s.Script + "}"
	}
	t := "{flow: " + s.Flow + ", module: " + s.Module
	if s.Occurrence != nil {
		t += ", occurrence: " + strconv.Itoa(*s.Occurrence)
	}
	return t + "}"
}

// ParseContract reads a host contract from YAML or JSON. The YAML is read
// with the sync folder's dialect (internal/yamlx). Every object is decoded
// strictly: unknown and repeated keys are errors, and names are
// case-sensitive.
func ParseContract(src []byte) (*Contract, error) {
	text, err := yamlx.ToJSON(src)
	if err != nil {
		return nil, contractError("%v", err)
	}
	var c Contract
	top, err := object(text, "contract", "roots", "provided", "host_modules")
	if err != nil {
		return nil, err
	}
	roots, err := array(top["roots"], "roots")
	if err != nil {
		return nil, err
	}
	for i, r := range roots {
		root, err := parseRoot(r, fmt.Sprintf("roots[%d]", i))
		if err != nil {
			return nil, err
		}
		c.Roots = append(c.Roots, root)
	}
	if raw, ok := top["provided"]; ok {
		items, err := array(raw, "provided")
		if err != nil {
			return nil, err
		}
		for i, item := range items {
			p, err := parseProvided(item, fmt.Sprintf("provided[%d]", i))
			if err != nil {
				return nil, err
			}
			c.Provided = append(c.Provided, p)
		}
	}
	if raw, ok := top["host_modules"]; ok {
		items, err := array(raw, "host_modules")
		if err != nil {
			return nil, err
		}
		for i, item := range items {
			s, err := str(item, fmt.Sprintf("host_modules[%d]", i))
			if err != nil {
				return nil, err
			}
			c.HostModules = append(c.HostModules, s)
		}
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Validate checks a contract built in Go as ParseContract checks a file:
// at least one root, each root naming exactly one flow or script, once;
// each provided site with a valid selector, exactly one provider form and
// `mandatory: true`; host modules non-empty and listed once.
func (c *Contract) Validate() error {
	if len(c.Roots) == 0 {
		return contractError("roots: at least one root is required")
	}
	seen := map[Root]bool{}
	for i, r := range c.Roots {
		where := fmt.Sprintf("roots[%d]", i)
		if (r.Flow == "") == (r.Script == "") {
			return contractError("%s: give exactly one of flow and script", where)
		}
		if seen[r] {
			return contractError("%s: %s is listed twice", where, r)
		}
		seen[r] = true
	}
	for i, p := range c.Provided {
		where := fmt.Sprintf("provided[%d]", i)
		if err := p.Selector.validate(where + ".selector"); err != nil {
			return err
		}
		if (p.Static == nil) == (p.Provider == "") {
			return contractError("%s: give exactly one of static and provider", where)
		}
		if !p.Mandatory {
			return contractError("%s: a provided site must be mandatory (`mandatory: true`); optional interception is a run-time hook", where)
		}
		if len(p.Selector.Iterations) > 0 || len(p.Selector.Branches) > 0 {
			return contractError("%s.selector: a provided site replaces its module in every iteration and branch, so iterations and branches cannot narrow it", where)
		}
	}
	mods := map[string]bool{}
	for i, m := range c.HostModules {
		if m == "" {
			return contractError("host_modules[%d]: empty path", i)
		}
		if mods[m] {
			return contractError("host_modules[%d]: %s is listed twice", i, m)
		}
		mods[m] = true
	}
	return nil
}

func (r Root) String() string {
	if r.Flow != "" {
		return "flow " + r.Flow
	}
	return "script " + r.Script
}

func (s Selector) validate(where string) error {
	if s.Script != "" {
		if s.Flow != "" || s.Module != "" || s.Occurrence != nil || len(s.Iterations) > 0 || len(s.Branches) > 0 {
			return contractError("%s: a script selector has only `script`", where)
		}
		return nil
	}
	if s.Flow == "" || s.Module == "" {
		return contractError("%s: give `flow` and `module`, or `script`", where)
	}
	if s.Occurrence != nil && *s.Occurrence < 0 {
		return contractError("%s: occurrence must not be negative", where)
	}
	for _, n := range s.Iterations {
		if n < 0 {
			return contractError("%s: iterations must not be negative", where)
		}
	}
	for _, b := range s.Branches {
		if b != "default" {
			if n, err := strconv.Atoi(b); err != nil || n < 0 || strconv.Itoa(n) != b {
				return contractError("%s: branch %q is neither an index nor default", where, b)
			}
		}
	}
	return nil
}

func parseRoot(raw json.RawMessage, where string) (Root, error) {
	m, err := object(raw, where, "flow", "script")
	if err != nil {
		return Root{}, err
	}
	var r Root
	if v, ok := m["flow"]; ok {
		if r.Flow, err = str(v, where+".flow"); err != nil {
			return Root{}, err
		}
	}
	if v, ok := m["script"]; ok {
		if r.Script, err = str(v, where+".script"); err != nil {
			return Root{}, err
		}
	}
	return r, nil
}

func parseProvided(raw json.RawMessage, where string) (ProvidedSite, error) {
	m, err := object(raw, where, "selector", "static", "provider", "mandatory")
	if err != nil {
		return ProvidedSite{}, err
	}
	var p ProvidedSite
	sel, ok := m["selector"]
	if !ok {
		return ProvidedSite{}, contractError("%s: missing selector", where)
	}
	if p.Selector, err = parseSelector(sel, where+".selector"); err != nil {
		return ProvidedSite{}, err
	}
	if v, ok := m["static"]; ok {
		p.Static = slices.Clone(v)
	}
	if v, ok := m["provider"]; ok {
		if p.Provider, err = str(v, where+".provider"); err != nil {
			return ProvidedSite{}, err
		}
		if p.Provider == "" {
			return ProvidedSite{}, contractError("%s.provider: empty name", where)
		}
	}
	if v, ok := m["mandatory"]; ok {
		if err := json.Unmarshal(v, &p.Mandatory); err != nil {
			return ProvidedSite{}, contractError("%s.mandatory: expected a boolean", where)
		}
	}
	return p, nil
}

func parseSelector(raw json.RawMessage, where string) (Selector, error) {
	m, err := object(raw, where, "flow", "module", "occurrence", "iterations", "branches", "script")
	if err != nil {
		return Selector{}, err
	}
	var s Selector
	for _, f := range []struct {
		name string
		to   *string
	}{{"flow", &s.Flow}, {"module", &s.Module}, {"script", &s.Script}} {
		if v, ok := m[f.name]; ok {
			if *f.to, err = str(v, where+"."+f.name); err != nil {
				return Selector{}, err
			}
		}
	}
	if v, ok := m["occurrence"]; ok {
		n, err := index(v, where+".occurrence")
		if err != nil {
			return Selector{}, err
		}
		s.Occurrence = &n
	}
	if v, ok := m["iterations"]; ok {
		items, err := array(v, where+".iterations")
		if err != nil {
			return Selector{}, err
		}
		for i, it := range items {
			n, err := index(it, fmt.Sprintf("%s.iterations[%d]", where, i))
			if err != nil {
				return Selector{}, err
			}
			s.Iterations = append(s.Iterations, n)
		}
	}
	if v, ok := m["branches"]; ok {
		items, err := array(v, where+".branches")
		if err != nil {
			return Selector{}, err
		}
		for i, it := range items {
			at := fmt.Sprintf("%s.branches[%d]", where, i)
			if bytes.HasPrefix(it, []byte(`"`)) {
				b, err := str(it, at)
				if err != nil {
					return Selector{}, err
				}
				if b != "default" {
					return Selector{}, contractError("%s: expected an index or \"default\"", at)
				}
				s.Branches = append(s.Branches, b)
				continue
			}
			n, err := index(it, at)
			if err != nil {
				return Selector{}, err
			}
			s.Branches = append(s.Branches, strconv.Itoa(n))
		}
	}
	return s, nil
}

// object decodes a JSON object whose keys must be among names.
func object(raw json.RawMessage, where string, names ...string) (map[string]json.RawMessage, error) {
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return nil, contractError("%s: expected an object", where)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, contractError("%s: %v", where, err)
	}
	var unknown []string
	for k := range m {
		if !slices.Contains(names, k) {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		return nil, contractError("%s: unknown key %q (expected %s)", where, unknown[0], strings.Join(names, ", "))
	}
	return m, nil
}

func array(raw json.RawMessage, where string) ([]json.RawMessage, error) {
	var a []json.RawMessage
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) || json.Unmarshal(raw, &a) != nil {
		return nil, contractError("%s: expected a list", where)
	}
	return a, nil
}

func str(raw json.RawMessage, where string) (string, error) {
	var s string
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte(`"`)) || json.Unmarshal(raw, &s) != nil {
		return "", contractError("%s: expected a string", where)
	}
	return s, nil
}

func index(raw json.RawMessage, where string) (int, error) {
	t := string(bytes.TrimSpace(raw))
	n, err := strconv.Atoi(t)
	if err != nil || n < 0 || strconv.Itoa(n) != t {
		return 0, contractError("%s: expected a non-negative integer", where)
	}
	return n, nil
}

func contractError(format string, args ...any) *Error {
	return &Error{Kind: ErrContract, Selector: -1, Msg: fmt.Sprintf(format, args...)}
}
