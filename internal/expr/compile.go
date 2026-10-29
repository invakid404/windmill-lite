package expr

import (
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// contextNames are the names JE-6's shortcut answers: the context values
// that some site binds (JE-22).
var contextNames = []string{"previous_result", "result", "params", "resume", "resumes", "approvers", "all_iters", "started_at", "flow_job_id"}

// bindable are the names an evaluation can bind: the context values,
// skip_expr's WM_SCHEDULED_FOR (JE-33), flow_input and flow_env, and the
// lexical results and error (JE-10, JE-17).
var bindable = append(slices.Clone(contextNames), "WM_SCHEDULED_FOR", "flow_input", "flow_env", "results", "error")

// Compile runs the textual stages of the pipeline on an expression as
// written at a site and returns the compiled expression (ADR 0003). Nothing
// in it depends on a run: the shortcuts it records fire or miss at
// evaluation time, against the site's scope.
//
//   - Trimmed: the text with leading and trailing white space removed; a
//     predicate's text is wrapped as Boolean(<text>) first (JE-5, JE-24).
//   - Shortcut: the shortcuts of JE-6 to JE-9 that the trimmed text allows.
//   - Mode: statement mode or expression mode (JE-13).
//   - Binds: the names the trimmed text mentions (JE-10, JE-17).
//   - Source: the trimmed text after the rewrites of JE-11 and JE-12, in
//     the wrapper the mode selects (JE-13, JE-14); or, when JE-11's
//     rewrite would close a call inside a character, where Windmill's
//     evaluator panics, no Source and that byte offset as RewritePanic.
func Compile(site SiteKind, text string) *Expr {
	t := text
	if site.Predicate() {
		t = "Boolean(" + t + ")"
	}
	// Rust's str::trim and Go's strings.TrimSpace both remove Unicode
	// White_Space, newlines included (JE-5).
	t = strings.TrimSpace(t)
	e := &Expr{
		Site:    site,
		Text:    text,
		Trimmed: t,
		Shortcut: Shortcut{
			FlowKey:     flowKey(t),
			PreviousID:  previousID(t),
			ResultsPath: resultsPath(t),
		},
		Mode:  mode(t),
		Binds: binds(t),
	}
	if slices.Contains(contextNames, t) {
		e.Shortcut.ContextName = t
	}
	code, at := rewriteCalls(t)
	if at != 0 {
		e.RewritePanic = at
		return e
	}
	e.Source = wrap(e.Mode, rewriteResults(code))
	return e
}

// flowKey returns the key of JE-7's shortcut: everything after the first
// dot of flow_input.<key>, or the text between the quotes of
// flow_input["<key>"], which must not be empty; flow_env alike.
func flowKey(t string) *FlowKey {
	for _, f := range []struct {
		name string
		env  bool
	}{{"flow_input", false}, {"flow_env", true}} {
		if key, ok := strings.CutPrefix(t, f.name+"."); ok {
			return &FlowKey{Env: f.env, Key: key}
		}
		if rest, ok := strings.CutPrefix(t, f.name+`["`); ok {
			if key, ok := strings.CutSuffix(rest, `"]`); ok && key != "" {
				return &FlowKey{Env: f.env, Key: key}
			}
		}
	}
	return nil
}

// previousID returns the id that the text names when it has the shape of
// one of JE-8's spellings: results.<id>, results?.<id>, results["<id>"] or
// results?.["<id>"]. The bracket forms are read first, so results?.["a"]
// names a, not ["a"]; evaluation compares the whole text with the four
// spellings of the previous id (isSpelling), so either id fires.
func previousID(t string) *string {
	for _, p := range []string{`results["`, `results?.["`} {
		if rest, ok := strings.CutPrefix(t, p); ok {
			if id, ok := strings.CutSuffix(rest, `"]`); ok {
				return &id
			}
		}
	}
	for _, p := range []string{"results.", "results?."} {
		if id, ok := strings.CutPrefix(t, p); ok {
			return &id
		}
	}
	return nil
}

// spellings returns JE-8's four spellings of id.
func spellings(id string) []string {
	return []string{"results." + id, "results?." + id, `results["` + id + `"]`, `results?.["` + id + `"]`}
}

// isSpelling reports whether t is exactly one of JE-8's spellings of id.
func isSpelling(t, id string) bool { return slices.Contains(spellings(id), t) }

// spellsPrevious reports whether t holds one of JE-8's spellings of the
// previous id anywhere, which binds previous_result (JE-10).
func spellsPrevious(t, id string) bool {
	return slices.ContainsFunc(spellings(id), func(s string) bool { return strings.Contains(t, s) })
}

// resultsLine is JE-9's qualifying line: results or results?, a dot and a
// step id, an optional [<digits>] right after the id, then any .segment
// parts, alone on the line from column 0.
var resultsLine = regexp.MustCompile(`^results\??\.([A-Za-z0-9_]+)(?:\[([0-9]+)\])?((?:\.[A-Za-z0-9_]+)*)$`)

// resultsPath returns JE-9's first qualifying line of t, which alone
// decides the shortcut: when its last segment is length, the shortcut
// declines and later lines are not searched.
func resultsPath(t string) *ResultsPath {
	for line := range strings.SplitSeq(t, "\n") {
		m := resultsLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		p := &ResultsPath{Step: m[1], Index: m[2]}
		if m[3] != "" {
			p.Segments = strings.Split(m[3][1:], ".")
			p.Declines = p.Segments[len(p.Segments)-1] == "length"
		}
		return p
	}
	return nil
}

// statementPrefixes are the starts of a text that select statement mode,
// beside return (JE-13).
var statementPrefixes = []string{"const ", "let ", "var ", "if ", "if(", "for ", "for(", "while ", "while(", "switch ", "switch(", "try ", "try{", "throw ", "function ", "class ", "async ", "await "}

// mode selects statement mode for a text that starts with return or a
// statement keyword, or holds a semicolon outside quotes; any other text is
// an expression (JE-13).
func mode(t string) Mode {
	if t == "return" || strings.HasPrefix(t, "return ") || strings.HasPrefix(t, "return;") {
		return ModeStatement
	}
	for _, p := range statementPrefixes {
		if strings.HasPrefix(t, p) {
			return ModeStatement
		}
	}
	if semicolonOutsideQuotes(t) {
		return ModeStatement
	}
	return ModeExpression
}

// semicolonOutsideQuotes scans t as JE-13 says: a quote character that
// does not follow a backslash opens a quote of its kind, or closes the one
// it is in, and a semicolon counts outside every quote. The scan knows
// nothing of regular expression literals, comments or ${} nesting.
func semicolonOutsideQuotes(t string) bool {
	var quote byte
	for i := 0; i < len(t); i++ {
		switch c := t[i]; c {
		case ';':
			if quote == 0 {
				return true
			}
		case '\'', '"', '`':
			if i > 0 && t[i-1] == '\\' {
				continue
			}
			switch quote {
			case 0:
				quote = c
			case c:
				quote = 0
			}
		}
	}
	return false
}

// binds returns the names of bindable that t mentions as substrings, so
// results also names result, and previous_result when t mentions error,
// whose value is derived from it (JE-10, JE-17). Whether t spells the
// previous id, which binds previous_result too, is known only at
// evaluation time.
func binds(t string) []string {
	var out []string
	for _, n := range bindable {
		if strings.Contains(t, n) {
			out = append(out, n)
		}
	}
	if slices.Contains(out, "error") && !slices.Contains(out, "previous_result") {
		out = append(out, "previous_result")
	}
	slices.Sort(out)
	return out
}

// helperCalls are the call texts JE-11 awaits.
var helperCalls = []string{"variable(", "resource(", "flow_user_state("}

// rewriteCalls applies JE-11: every occurrence of a helper call text, with
// no word boundary and inside strings and comments too, becomes
// (await <call>). The helpers are rewritten in helperCalls' order, each
// over the text the one before produced (rewriteCall). Where an inserted )
// would land inside a character, Windmill's evaluator panics; rewriteCalls
// then returns that byte offset, in the text the panicking helper's pass
// rewrites, as panicAt, with no code.
func rewriteCalls(t string) (code string, panicAt int) {
	for _, call := range helperCalls {
		var at int
		if t, at = rewriteCall(t, call); at != 0 {
			return "", at
		}
	}
	return t, 0
}

// rewriteCall is one helper's pass of JE-11. The text is cut at every
// occurrence of call; the part before the first is kept, and each later
// part, from just after one occurrence up to the next or the end of the
// text, becomes (await <call><part>, with one ) inserted in the part.
// Parentheses are counted from the part's start, at level 1, and the )
// goes before the one that brings the level to 0, or at the end of the
// part when none does. Windmill counts that position in characters from
// the part's start and applies the count as a byte offset: for a part that
// is ASCII up to there the two agree, and otherwise the ) lands earlier by
// the extra bytes of the characters before it, so text before the call
// moves nothing. Where it would land inside a character the insertion
// panics; rewriteCall then returns that byte offset in t as panicAt.
func rewriteCall(t, call string) (code string, panicAt int) {
	parts := strings.Split(t, call)
	if len(parts) == 1 {
		return t, 0
	}
	var b strings.Builder
	b.WriteString(parts[0])
	from := len(parts[0])
	for _, part := range parts[1:] {
		from += len(call)
		at, level := utf8.RuneCountInString(part), 1
	scan:
		for n, r := range []rune(part) {
			switch r {
			case '(':
				level++
			case ')':
				if level--; level == 0 {
					at = n
					break scan
				}
			}
		}
		if at < len(part) && !utf8.RuneStart(part[at]) {
			return "", from + at
		}
		b.WriteString("(await " + call + part[:at] + ")" + part[at:])
		from += len(part)
	}
	return b.String(), 0
}

// resultsRead is JE-12's pattern: results, an optional ?, then a dot and
// [A-Za-z0-9_]+, or ["…"] with double quotes, matched lazily on one line.
var resultsRead = regexp.MustCompile(`results\??(?:\.[A-Za-z0-9_]+|\[".*?"\])`)

// rewriteResults applies JE-12: every match becomes (await <match>), with
// no word boundary and inside strings and comments too.
func rewriteResults(t string) string {
	return resultsRead.ReplaceAllString(t, "(await ${0})")
}

// The wrapper of each mode (JE-13): an async function, called at once,
// whose opening and closing text sit on the first and last line of the
// code, so that a trailing // comment on the last line comments the
// closing text out and the source does not compile (JE-14). Its text is
// observable: the engine's stack frames give positions in it (JE-21), and
// the opening text puts the code at column 21 in statement mode and 28 in
// expression mode, the positions E3a's recorded stacks show.
const (
	wrapOpen           = "(async function() { "
	wrapReturn         = "return "
	wrapCloseExpr      = "; })()"
	wrapCloseStatement = " })()"
)

// wrap places the rewritten code in the wrapper of mode m: as the function
// body in statement mode, as return <code>; in expression mode, so that a
// leading { is an object literal.
func wrap(m Mode, code string) string {
	if m == ModeStatement {
		return wrapOpen + code + wrapCloseStatement
	}
	return wrapOpen + wrapReturn + code + wrapCloseExpr
}
