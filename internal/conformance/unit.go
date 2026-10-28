package conformance

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// UnitCase restates one expression of a flow fixture at unit level: the
// expression at its site, the context its evaluation sees, and the
// evaluator output (point O1 of docs/semantics/expressions.md) that the
// catalogue expects. A run lists them in runs/<name>.yaml under unit.
//
// The oracle cannot observe O1: an argument is stored as jsonb before the
// job API returns it, which reorders keys and respells numbers (JE-44), so a
// recording cannot tell a verbatim producer text from its stored form. The
// expression milestones (B2a, B2b) check the pipeline against these cases
// with the context supplied directly, and a test of the recording batch ties
// each case to its run's recording through stored forms: the context's
// producer and stored texts must be what the recording holds, and, at an
// input transform, the expected text's stored form must be the recorded
// argument.
type UnitCase struct {
	// Spec is the catalogue case, or the finding for a case beyond the
	// catalogue.
	Spec string `json:"spec"`
	// Site is the module's site path, for example m:b.
	Site string `json:"site"`
	// Input names the expression at the site: an input transform's name,
	// or a module field (skip_if, stop_after_if, timeout, sleep, iterator).
	Input string `json:"input"`
	// Expr is the expression text, as written in the flow.
	Expr    string      `json:"expr"`
	Context UnitContext `json:"context"`
	Expect  UnitExpect  `json:"expect"`
	Note    string      `json:"note,omitempty"`
}

// UnitContext is what an evaluation sees. Texts are JSON texts.
type UnitContext struct {
	// PreviousID is the id of the module before the site in its list, ""
	// at a list's first module (JE-8).
	PreviousID *string `json:"previous_id"`
	// PreviousResult is previous_result as held in memory: the producer
	// text of the previous step's result (JE-36 rule 2, JE-44), or the
	// flow input at module 0.
	PreviousResult *string `json:"previous_result,omitempty"`
	// FlowInput is the flow input's text.
	FlowInput *string `json:"flow_input,omitempty"`
	// Results are stored texts that a database read returns, by module id
	// (JE-9, JE-18 rules 2 and 3).
	Results map[string]string `json:"results,omitempty"`
}

// UnitExpect is the expected evaluation: an exact output text, or an
// error message.
type UnitExpect struct {
	// Text is the exact output text (O1).
	Text  *string       `json:"text,omitempty"`
	Error *MessageMatch `json:"error,omitempty"`
}

// MessageMatch matches an error object's name and message the way the
// catalogue compares engine messages: exactly, or by prefix and
// substrings, because stack text and the Rust location suffix are not part
// of the expectation (docs/semantics/conformance.md, expressions notation).
type MessageMatch struct {
	Name        string   `json:"name,omitempty"`
	Message     *string  `json:"message,omitempty"`
	Prefix      *string  `json:"prefix,omitempty"`
	Contains    []string `json:"contains,omitempty"`
	NotContains []string `json:"not_contains,omitempty"`
}

func (m *MessageMatch) check() error {
	if m.Message == nil && m.Prefix == nil && len(m.Contains) == 0 && len(m.NotContains) == 0 && m.Name == "" {
		return errors.New("an error match needs a name, a message, a prefix or substrings")
	}
	if m.Message != nil && (m.Prefix != nil || len(m.Contains) > 0) {
		return errors.New("an error match gives either the exact message or a prefix and substrings")
	}
	return nil
}

// Claims returns the claims the match makes about the error object at
// pointer at (an object with name and message).
func (m *MessageMatch) Claims(at string) []Claim {
	var out []Claim
	if m.Name != "" {
		out = append(out, Claim{At: at + "/name", Equals: rawString(m.Name)})
	}
	if m.Message != nil {
		out = append(out, Claim{At: at + "/message", Equals: rawString(*m.Message)})
	}
	if m.Prefix != nil || len(m.Contains) > 0 || len(m.NotContains) > 0 {
		out = append(out, Claim{At: at + "/message", Prefix: m.Prefix, Contains: m.Contains, NotContains: m.NotContains})
	}
	return out
}

// rawString returns s as a JSON string value.
func rawString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func (u *UnitCase) check() error {
	if u.Spec == "" || u.Site == "" || u.Input == "" {
		return errors.New("unit case needs spec, site and input")
	}
	if _, err := ParseSitePath(u.Site); err != nil {
		return fmt.Errorf("unit case %s: %w", u.Spec, err)
	}
	if u.Context.PreviousID == nil {
		return fmt.Errorf("unit case %s: context.previous_id is required (\"\" at a list's first module)", u.Spec)
	}
	for name, t := range map[string]*string{"previous_result": u.Context.PreviousResult, "flow_input": u.Context.FlowInput, "expect.text": u.Expect.Text} {
		if t != nil && !json.Valid([]byte(*t)) {
			return fmt.Errorf("unit case %s: %s is not JSON text", u.Spec, name)
		}
	}
	for id, t := range u.Context.Results {
		if !json.Valid([]byte(t)) {
			return fmt.Errorf("unit case %s: context.results.%s is not JSON text", u.Spec, id)
		}
	}
	switch {
	case u.Expect.Text != nil && u.Expect.Error != nil:
		return fmt.Errorf("unit case %s: expect gives both text and error", u.Spec)
	case u.Expect.Text == nil && u.Expect.Error == nil:
		return fmt.Errorf("unit case %s: expect gives neither text nor error", u.Spec)
	case u.Expect.Error != nil:
		if err := u.Expect.Error.check(); err != nil {
			return fmt.Errorf("unit case %s: %w", u.Spec, err)
		}
	}
	if strings.TrimSpace(u.Expr) == "" {
		return fmt.Errorf("unit case %s: expr is empty", u.Spec)
	}
	return nil
}
