package expr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// The expression suite on the real engine (milestone B2b, ADR 0012): every
// expression that recording batch E3a recorded, a context-free case of
// conformance/exprs or a unit case of a flow fixture, runs through the
// pipeline on QuickJS-ng with the context its recording gives, at unit
// level as in B2a. The evaluator is held to the case's whole expectation,
// and an error to the text the oracle recorded, byte for byte.

// engineSkipped are the E3a cases the suite does not run, with the reason.
var engineSkipped = map[string]string{
	// The oracle's monitor of flows hanging between steps cancels the run
	// at 60 s, before Windmill's third attempt ends at about 70 s, so the
	// recording holds no evaluator text. TestJE2OnEngine runs the same
	// expression's three attempts with a short limit.
	"JE-2/a": "the oracle canceled the run before the evaluator's text surfaced",
}

// engineDeviations are the E3a cases whose recorded outcome windmill-lite
// does not give, by a named deviation of ADR 0001, with the evaluator text
// it gives instead.
var engineDeviations = map[string]struct{ deviation, text string }{
	// Windmill's engine overflows at depth 1 000, this one does not (D17,
	// TestJE48OnEngine). The text holds a ;, so it runs in statement mode
	// and its value is null.
	"JE-48/a": {"D17", "null"},
}

// TestE3aOnEngine is the exit gate of B2b: each E3a case gives the value,
// the text or the error its recording expects, and each error that the
// oracle recorded holds the evaluator's text exactly.
func TestE3aOnEngine(t *testing.T) {
	e3a := loadE3a(t)
	p := NewPipeline(realEngine(t, DefaultLimits(), 0))
	var ran, exact, skipped int
	deviations := map[string][]string{}
	for _, k := range slices.Sorted(maps.Keys(e3a)) {
		c := e3a[k]
		t.Run(k, func(t *testing.T) {
			if why, ok := engineSkipped[c.spec]; ok {
				skipped++
				t.Skip(why)
			}
			ran++
			e := Compile(c.site, c.text)
			out, err := p.Eval(context.Background(), e, c.scope(t))
			var ee *EvalError
			if err != nil && !errors.As(err, &ee) {
				t.Fatalf("Eval: %v is not an evaluation's failure", err)
			}
			if d, ok := engineDeviations[c.spec]; ok {
				if err != nil || string(out.Producer()) != d.text {
					t.Errorf("%s %q: %s, %v; deviation %s gives %s", c.spec, c.text, out.Producer(), err, d.deviation, d.text)
				}
				deviations[k] = append(deviations[k], d.deviation+": "+d.text)
				return
			}
			if err := engineCheck(c, out, ee, deviations); err != nil {
				t.Errorf("%s %q: %v", c.spec, c.text, err)
			}
			if ee == nil {
				return
			}
			if !holdsText(c.recorded, ee.Message) {
				t.Errorf("%s %q: no recorded message holds the evaluator's text %q; recorded %q", c.spec, c.text, ee.Message, c.recorded)
				return
			}
			exact++
		})
	}
	// The suite cannot pass empty: E3a recorded over a hundred expressions,
	// and over ten of them fail with an engine text.
	if ran < 100 || exact < 10 {
		t.Errorf("%d cases ran and %d recorded texts matched; the suite has gone empty", ran, exact)
	}
	for k, d := range deviations {
		t.Logf("%s: the %s engine gives %s", k, testCLibrary, strings.Join(d, ", "))
	}
	t.Logf("%d E3a expressions on the engine, %d skipped; %d recorded error texts matched byte for byte", ran, skipped, exact)
}

// holdsText reports whether one of the recorded messages holds the
// evaluator's text whole: after the site's wrapping, and either at its end
// or before a Rust location suffix, which a predicate's message carries
// (JE-24).
func holdsText(recorded []string, text string) bool {
	for _, r := range recorded {
		for from := 0; ; {
			i := strings.Index(r[from:], text)
			if i < 0 {
				break
			}
			rest := r[from+i+len(text):]
			if rest == "" || strings.HasPrefix(rest, " @") {
				return true
			}
			from += i + 1
		}
	}
	return false
}

// engineCheck holds an evaluation's outcome to the case's expectation. An
// error's text is wrapped as the case's site wraps it, which is the site's
// rule and not the evaluator's (JE-23 to JE-35); a name in the expectation
// is the site's too and is not checked.
func engineCheck(c *e3aCase, out jsonval.Value, ee *EvalError, deviations map[string][]string) error {
	got := string(out.Producer())
	switch {
	case c.exprs != nil:
		x := c.exprs.Expect
		switch {
		case x.Value != nil:
			if ee != nil {
				return fmt.Errorf("failed with %q, want %s", ee.Message, *x.Value)
			}
			if x.Text != nil && got != *x.Text {
				return fmt.Errorf("evaluator text %s, want %s", got, *x.Text)
			}
			if strings.HasPrefix(c.exprs.Variant, "math-") {
				return mathCheck(c.key, got, *x.Value, deviations)
			}
			return sameStored(got, *x.Value)
		case x.Skipped != nil:
			if ee != nil {
				return fmt.Errorf("failed with %q, want skipped %v", ee.Message, *x.Skipped)
			}
			if want := fmt.Sprint(*x.Skipped); got != want {
				return fmt.Errorf("the predicate gives %s, want %s", got, want)
			}
			return nil
		case x.FlowError != nil:
			msg := ""
			switch {
			case ee != nil:
				msg = siteMessage(c.site, c.text, ee.Message)
			case got != "true" && got != "false":
				// JE-24's strict check, which the site applies.
				msg = `Expected a boolean value, found: "` + got + `"`
			default:
				return fmt.Errorf("the predicate gives %s, want the flow to fail", got)
			}
			return matchMessage(x.FlowError, msg)
		case x.Error != nil:
			if ee == nil {
				return fmt.Errorf("gives %s, want an error", got)
			}
			return matchMessage(x.Error, siteMessage(c.site, c.text, ee.Message))
		}
	case c.unit != nil:
		x := c.unit.Expect
		switch {
		case x.Text != nil:
			if ee != nil {
				return fmt.Errorf("failed with %q, want %s", ee.Message, *x.Text)
			}
			if got != *x.Text {
				return fmt.Errorf("evaluator text %s, want %s", got, *x.Text)
			}
			return nil
		case x.Error != nil:
			if ee == nil {
				return fmt.Errorf("gives %s, want an error", got)
			}
			return matchMessage(x.Error, siteMessage(c.site, c.text, ee.Message))
		}
	}
	return errors.New("the case has no expectation the suite knows")
}

// siteMessage wraps an evaluator's text as the site does: an input
// transform's step error (JE-23), the typed check of timeout and sleep
// (JE-28, JE-29), or the generic error of a predicate or iterator (JE-24,
// JE-30), without its location suffix.
func siteMessage(site SiteKind, text, msg string) string {
	switch site {
	case SiteInputTransform, SiteFailureInputTransform:
		return "Error during isolated evaluation of expression `" + text + "`:\n" + msg
	case SiteTimeout, SiteSleep:
		return "Error during evaluation of expression `" + text + "`:\n" + msg
	}
	return "Error: " + msg
}

func matchMessage(m *conformance.MessageMatch, msg string) error {
	switch {
	case m.Message != nil && msg != *m.Message:
		return fmt.Errorf("message %q, want %q", msg, *m.Message)
	case m.Prefix != nil && !strings.HasPrefix(msg, *m.Prefix):
		return fmt.Errorf("message %q, want the prefix %q", msg, *m.Prefix)
	}
	for _, s := range m.Contains {
		if !strings.Contains(msg, s) {
			return fmt.Errorf("message %q does not contain %q", msg, s)
		}
	}
	for _, s := range m.NotContains {
		if strings.Contains(msg, s) {
			return fmt.Errorf("message %q contains %q", msg, s)
		}
	}
	return nil
}

// sameStored compares an evaluator text with a recorded argument through
// their stored forms (point O2).
func sameStored(got, want string) error {
	g, err := jsonval.FromProducer([]byte(got)).Stored()
	if err != nil {
		return err
	}
	w, err := jsonval.FromProducer([]byte(want)).Stored()
	if err != nil {
		return err
	}
	if string(g) != string(w) {
		return fmt.Errorf("gives %s (stored %s), want %s", got, g, w)
	}
	return nil
}

// mathCheck compares the transcendental Math cases of JE-1 (review item
// cl R3-2), whose values come from the C library's libm. With glibc, the
// library of Windmill's image, every value is the oracle's. With another
// libm a value may be one unit in the last place apart, which deviation
// D16 of ADR 0001 names; the differences are logged.
func mathCheck(key, got, want string, deviations map[string][]string) error {
	var g, w []string
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		return fmt.Errorf("%s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		return err
	}
	if len(g) != len(w) {
		return fmt.Errorf("gives %s, want %s", got, want)
	}
	for i := range w {
		if g[i] == w[i] {
			continue
		}
		if testCLibrary == "glibc" {
			return fmt.Errorf("value %d is %s with glibc, the oracle's %s", i, g[i], w[i])
		}
		a, errA := strconv.ParseFloat(g[i], 64)
		b, errB := strconv.ParseFloat(w[i], 64)
		if errA != nil || errB != nil || ulps(a, b) > 1 {
			return fmt.Errorf("value %d is %s, the oracle's %s: more than one unit in the last place apart", i, g[i], w[i])
		}
		deviations[key] = append(deviations[key], fmt.Sprintf("D16: %s for %s", g[i], w[i]))
	}
	return nil
}

// ulps is the distance between two finite doubles of one sign in units in
// the last place.
func ulps(a, b float64) uint64 {
	if math.Signbit(a) != math.Signbit(b) {
		return math.MaxUint64
	}
	x, y := math.Float64bits(a), math.Float64bits(b)
	if x > y {
		return x - y
	}
	return y - x
}
