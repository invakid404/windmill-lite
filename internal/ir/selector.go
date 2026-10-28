package ir

import (
	"fmt"
	"strings"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/expr"
	"github.com/invakid404/windmill-lite/internal/openflow"
)

// matches reports whether selector s names module m of flow, whose
// segment in its module list is seg. A module selector names m by flow and
// id, and by occurrence when it gives one (a unique id is its own
// occurrence 0); a script selector names a script step by its path and an
// inline step by `<flow path>/<module id>` (engine.Site.Script). ambiguous
// is true when a module selector without an occurrence names an id that
// occurs more than once in m's list (0008, 0001 §5a).
func (s Selector) matches(flow string, m *openflow.Module, seg conformance.Segment) (match, ambiguous bool) {
	if s.Script != "" {
		switch v := m.Value.(type) {
		case *openflow.PathScript:
			return v.Path == s.Script, false
		case *openflow.RawScript:
			return flow+"/"+m.ID == s.Script, false
		}
		return false, false
	}
	if s.Flow != flow || s.Module == nil || *s.Module != m.ID {
		return false, false
	}
	k, repeated := seg.Occurrence()
	if s.Occurrence == nil {
		return true, repeated
	}
	return *s.Occurrence == k, false
}

// provided returns the Provided value for a module that a contract's
// provided site selects, or nil. Each match is recorded, so that
// checkMatches can require every mandatory selector to match exactly one
// site.
func (fc *flowCompiler) provided(m *openflow.Module, seg conformance.Segment, site conformance.SitePath, sc scope) *Provided {
	chosen := -1
	for i, p := range fc.contract.Provided {
		match, ambiguous := p.Selector.matches(sc.flow, m, seg)
		if !match {
			continue
		}
		if ambiguous && !fc.flagged[i] {
			fc.flagged[i] = true
			fc.fail(&Error{Kind: ErrAmbiguousSelector, Flow: sc.flow, Step: m.ID, Selector: i,
				Msg: fmt.Sprintf("provided site %d: selector %s names an id that occurs more than once in one module list; give its occurrence", i, p.Selector)})
		}
		fc.matches[i] = append(fc.matches[i], sc.flow+" "+site.String())
		if chosen >= 0 {
			fc.fail(&Error{Kind: ErrContract, Flow: sc.flow, Step: m.ID, Selector: i,
				Msg: fmt.Sprintf("provided sites %d and %d select the same site", chosen, i)})
			continue
		}
		chosen = i
	}
	if chosen < 0 {
		return nil
	}
	p := fc.contract.Provided[chosen]
	out := &Provided{Site: chosen, Static: p.Static, Provider: p.Provider}
	switch v := m.Value.(type) {
	case *openflow.PathScript:
		out.Target = Target{Kind: KindScript, Path: v.Path}
		out.Inputs = inputs(v.InputTransforms, inputSite(sc))
	case *openflow.PathFlow:
		out.Target = Target{Kind: KindSubflow, Path: v.Path}
		out.Inputs = inputs(v.InputTransforms, inputSite(sc))
	case *openflow.RawScript:
		out.Target = Target{Kind: KindRawScript}
		out.Inputs = inputs(v.InputTransforms, inputSite(sc))
	default:
		fc.fail(&Error{Kind: ErrContract, Flow: sc.flow, Step: m.ID, Selector: chosen,
			Msg: fmt.Sprintf("provided site %d selects a %s module; a provided site must be a script, flow or rawscript step", chosen, m.Value.Kind())})
		return nil
	}
	return out
}

// inputSite is the site kind of a module's input transforms: those of the
// failure module itself have their own scope (JE-35).
func inputSite(sc scope) expr.SiteKind {
	if sc.failureSlot {
		return expr.SiteFailureInputTransform
	}
	return expr.SiteInputTransform
}

// checkMatches requires every mandatory provided-site selector to match
// exactly one site, because every match becomes opaque to the compiler
// (0008).
func (c *compiler) checkMatches() {
	for i, ms := range c.matches {
		sel := c.contract.Provided[i].Selector
		switch {
		case len(ms) == 0:
			msg := fmt.Sprintf("provided site %d: selector %s matches no site", i, sel)
			if sel.Flow != "" && c.prog.Flows[sel.Flow] == nil {
				msg += fmt.Sprintf("; flow %s is not in the program", sel.Flow)
			}
			c.fail(&Error{Kind: ErrContract, Selector: i, Msg: msg})
		case len(ms) > 1:
			c.fail(&Error{Kind: ErrAmbiguousSelector, Selector: i,
				Msg: fmt.Sprintf("provided site %d: mandatory selector %s matches %d sites (%s); it must match exactly one", i, sel, len(ms), strings.Join(ms, ", "))})
		}
	}
}
