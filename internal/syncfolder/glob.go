package syncfolder

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The CLI matches wmill.yaml's filter and codebase globs with minimatch,
// with its default options, against root-relative POSIX paths (SY-8,
// SY-13). glob implements the part of minimatch's syntax on which
// minimatch 9 and 10 agree and which a regular matcher can read:
//
//   - `*` (any characters but `/`), `?` (one character), classes `[...]`
//     with `!` or `^` negation and ranges, and `\` escapes;
//   - `**` as a whole segment: any number of segments;
//   - brace expansion: `{a,b}` lists, nested, and `{x..y}` sequences;
//   - a leading `!` negates the pattern, and a leading `#` makes it match
//     nothing.
//
// A path segment that starts with `.` is matched only by a pattern segment
// that starts with a literal `.`; `*`, `?`, classes and `**` never match a
// leading dot (minimatch's `dot: false`). A class of exactly one character,
// such as `[.]`, is that literal character. Wildcards never match a `.` or
// `..` path segment.
//
// Extglobs (`@(…)`, `!(…)`, `+(…)`, `?(…)`, `*(…)`), POSIX classes
// (`[[:alpha:]]`), segments that read as `.` or `..`, a trailing `\`, an
// escaped `\`, an escape in a segment that starts with `*` or `?`, and
// braces that do not expand are refused with ErrGlob: minimatch versions
// read some of them differently, minimatch reads others in ways a plain
// matcher does not, and extglobs need a matcher that is not regular.
type glob struct {
	source  string
	negate  bool
	comment bool
	// alts are the brace expansions; the pattern matches when one does.
	alts []globAlt
}

type globAlt struct {
	never bool // a leading or trailing `/`: no root-relative file path matches
	segs  []globSeg
}

type globSeg struct {
	globstar bool
	toks     []globTok
}

type tokKind uint8

const (
	tokLit  tokKind = iota
	tokAny          // ?
	tokStar         // *
	tokClass
)

type globTok struct {
	kind  tokKind
	r     rune
	class *globClass
}

type globClass struct {
	negate bool
	ranges [][2]rune
}

// single reports a class of exactly one character (`[.]`, `[.-.]`), which
// minimatch 9 and 10 read as that literal character.
func (c *globClass) single() (rune, bool) {
	if c.negate || len(c.ranges) != 1 || c.ranges[0][0] != c.ranges[0][1] {
		return 0, false
	}
	return c.ranges[0][0], true
}

// maxExpansions bounds brace expansion, against patterns that expand to
// millions of alternatives.
const maxExpansions = 4096

func compileGlob(pattern string) (*glob, error) {
	g := &glob{source: pattern}
	if !utf8.ValidString(pattern) {
		return nil, globError(pattern, "the pattern is not valid UTF-8")
	}
	if strings.HasPrefix(pattern, "#") {
		g.comment = true
		return g, nil
	}
	rest := pattern
	for strings.HasPrefix(rest, "!") {
		g.negate = !g.negate
		rest = rest[1:]
	}
	if strings.Contains(rest, `\\`) {
		// Brace expansion turns `\\` into an active `\`.
		return nil, globError(pattern, "an escaped backslash")
	}
	expanded, err := braceExpand(rest)
	if err != nil {
		return nil, globError(pattern, err.Error())
	}
	for _, p := range expanded {
		alt, err := compileAlt(p)
		if err != nil {
			return nil, globError(pattern, err.Error())
		}
		g.alts = append(g.alts, alt)
	}
	return g, nil
}

func globError(pattern, msg string) *Error {
	return &Error{Kind: ErrGlob, Msg: fmt.Sprintf("unsupported glob %q: %s", pattern, msg)}
}

func compileAlt(p string) (globAlt, error) {
	if p == "" {
		// minimatch's empty pattern matches only the empty path.
		return globAlt{never: true}, nil
	}
	parts := strings.Split(p, "/")
	var alt globAlt
	for i, part := range parts {
		switch {
		case part == "" && (i == 0 || i == len(parts)-1):
			alt.never = true
			continue
		case part == "":
			continue // minimatch collapses `//`
		case part == "." || part == "..":
			return globAlt{}, fmt.Errorf("a %q segment", part)
		case part == "**":
			alt.segs = append(alt.segs, globSeg{globstar: true})
			continue
		}
		toks, err := compileSeg(part)
		if err != nil {
			return globAlt{}, err
		}
		if lit, ok := literal(toks); ok && (lit == "." || lit == "..") {
			// minimatch 10 resolves these as directory steps.
			return globAlt{}, fmt.Errorf("a segment %q that reads as %q", part, lit)
		}
		alt.segs = append(alt.segs, globSeg{toks: toks})
	}
	return alt, nil
}

func compileSeg(s string) ([]globTok, error) {
	if (strings.HasPrefix(s, "*") || strings.HasPrefix(s, "?")) && strings.Contains(s, "\\") {
		// minimatch 9 and 10 compare such a segment's escapes as text.
		return nil, fmt.Errorf("an escape in a segment that starts with `*` or `?`")
	}
	var toks []globTok
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '\\':
			if i+n >= len(s) {
				return nil, fmt.Errorf("a trailing backslash")
			}
			e, m := utf8.DecodeRuneInString(s[i+n:])
			toks = append(toks, globTok{kind: tokLit, r: e})
			i += n + m
			continue
		case strings.ContainsRune("@!+?*", r) && i+n < len(s) && s[i+n] == '(':
			return nil, fmt.Errorf("an extglob %q", s[i:i+n+1])
		case r == '?':
			toks = append(toks, globTok{kind: tokAny})
		case r == '*':
			if len(toks) == 0 || toks[len(toks)-1].kind != tokStar {
				toks = append(toks, globTok{kind: tokStar})
			}
		case r == '[':
			c, m, ok, err := compileClass(s[i:])
			if err != nil {
				return nil, err
			}
			if ok {
				if lit, single := c.single(); single {
					toks = append(toks, globTok{kind: tokLit, r: lit})
				} else {
					toks = append(toks, globTok{kind: tokClass, class: c})
				}
				i += m
				continue
			}
			// An unclosed `[` is a literal character.
			toks = append(toks, globTok{kind: tokLit, r: r})
		default:
			toks = append(toks, globTok{kind: tokLit, r: r})
		}
		i += n
	}
	return toks, nil
}

// compileClass parses a class at the start of s. It returns ok false when
// the class is not closed, in which case `[` is literal.
func compileClass(s string) (*globClass, int, bool, error) {
	c := &globClass{}
	i := 1
	if i < len(s) && (s[i] == '!' || s[i] == '^') {
		c.negate = true
		i++
	}
	first := true
	for i < len(s) {
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == ']' && !first {
			return c, i + n, true, nil
		}
		if r == '[' && i+1 < len(s) && strings.IndexByte(":=.", s[i+1]) >= 0 {
			return nil, 0, false, fmt.Errorf("a POSIX class in %q", s)
		}
		first = false
		if r == '\\' {
			if i+n >= len(s) {
				return nil, 0, false, nil
			}
			r, n = utf8.DecodeRuneInString(s[i+n:])
			i++
		}
		i += n
		lo, hi := r, r
		if i+1 < len(s) && s[i] == '-' && s[i+1] != ']' {
			h, m := utf8.DecodeRuneInString(s[i+1:])
			if h == '\\' {
				if i+1+m >= len(s) {
					return nil, 0, false, nil
				}
				h, m = utf8.DecodeRuneInString(s[i+1+m:])
				i++
			}
			hi = h
			i += 1 + m
		}
		c.ranges = append(c.ranges, [2]rune{lo, hi})
	}
	return nil, 0, false, nil
}

func (c *globClass) has(r rune) bool {
	for _, rg := range c.ranges {
		if rg[0] <= r && r <= rg[1] {
			return true
		}
	}
	return false
}

func (c *globClass) matches(r rune) bool {
	return c.has(r) != c.negate
}

// match reports whether the root-relative file path p matches.
func (g *glob) match(p string) bool {
	if g.comment {
		return false
	}
	parts := strings.Split(p, "/")
	hit := false
	for _, alt := range g.alts {
		if !alt.never && matchSegs(alt.segs, parts) {
			hit = true
			break
		}
	}
	return hit != g.negate
}

func matchSegs(segs []globSeg, parts []string) bool {
	if len(segs) == 0 {
		return len(parts) == 0
	}
	if !segs[0].globstar {
		return len(parts) > 0 && matchSeg(segs[0].toks, parts[0]) && matchSegs(segs[1:], parts[1:])
	}
	if len(segs) == 1 {
		// A trailing `**` matches one or more segments, none of them dotted.
		if len(parts) == 0 {
			return false
		}
		for _, p := range parts {
			if strings.HasPrefix(p, ".") {
				return false
			}
		}
		return true
	}
	for i := 0; i <= len(parts); i++ {
		if matchSegs(segs[1:], parts[i:]) {
			return true
		}
		if i < len(parts) && strings.HasPrefix(parts[i], ".") {
			return false
		}
	}
	return false
}

func matchSeg(toks []globTok, s string) bool {
	if (s == "." || s == "..") && !allLiteral(toks) {
		return false // wildcards never match these segments
	}
	if strings.HasPrefix(s, ".") && !leadingDotAllowed(toks) {
		return false
	}
	return matchToks(toks, s)
}

func allLiteral(toks []globTok) bool {
	_, ok := literal(toks)
	return ok
}

// literal returns the text of a segment pattern made only of literals.
func literal(toks []globTok) (string, bool) {
	var b strings.Builder
	for _, t := range toks {
		if t.kind != tokLit {
			return "", false
		}
		b.WriteRune(t.r)
	}
	return b.String(), true
}

// leadingDotAllowed reports whether a segment pattern may match a segment
// that starts with `.`: it must start with a literal `.`.
func leadingDotAllowed(toks []globTok) bool {
	return len(toks) > 0 && toks[0].kind == tokLit && toks[0].r == '.'
}

func matchToks(toks []globTok, s string) bool {
	for len(toks) > 0 {
		t := toks[0]
		if t.kind == tokStar {
			rest := toks[1:]
			if len(rest) == 0 {
				return true
			}
			for i := 0; i <= len(s); {
				if matchToks(rest, s[i:]) {
					return true
				}
				if i == len(s) {
					break
				}
				_, n := utf8.DecodeRuneInString(s[i:])
				i += n
			}
			return false
		}
		if s == "" {
			return false
		}
		r, n := utf8.DecodeRuneInString(s)
		switch t.kind {
		case tokLit:
			if r != t.r {
				return false
			}
		case tokClass:
			if !t.class.matches(r) {
				return false
			}
		}
		toks, s = toks[1:], s[n:]
	}
	return s == ""
}

// braceExpand expands `{a,b}` lists and `{x..y[..step]}` sequences, left to
// right, as bash does. Escaped braces and commas, and a `}` with no `{`
// before it, are literal. Braces that do not expand (`{}`, `{a}`, an
// unclosed `{`) and `${` are refused: minimatch's brace expansion re-reads
// them in ways that differ from a plain literal.
func braceExpand(s string) ([]string, error) {
	open := -1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '{':
			if i > 0 && s[i-1] == '$' {
				return nil, fmt.Errorf("a `${`")
			}
			open = i
		}
		if open >= 0 {
			break
		}
	}
	if open < 0 {
		return []string{s}, nil
	}
	close, commas, err := matchBrace(s, open)
	if err != nil {
		return nil, err
	}
	pre, body, post := s[:open], s[open+1:close], s[close+1:]
	var alts []string
	if len(commas) > 0 {
		start := 0
		for _, c := range commas {
			alts = append(alts, body[start:c])
			start = c + 1
		}
		alts = append(alts, body[start:])
	} else if seq, ok, err := sequence(body); err != nil {
		return nil, err
	} else if ok {
		alts = seq
	} else {
		return nil, fmt.Errorf("braces that do not expand: {%s}", body)
	}
	posts, err := braceExpand(post)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, a := range alts {
		inner, err := braceExpand(a)
		if err != nil {
			return nil, err
		}
		for _, x := range inner {
			for _, p := range posts {
				out = append(out, pre+x+p)
				if len(out) > maxExpansions {
					return nil, fmt.Errorf("more than %d brace expansions", maxExpansions)
				}
			}
		}
	}
	return out, nil
}

// matchBrace finds the `}` that closes the `{` at open, and the offsets,
// relative to the body, of its top-level commas.
func matchBrace(s string, open int) (int, []int, error) {
	depth := 0
	var commas []int
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '{':
			if i > 0 && s[i-1] == '$' {
				return 0, nil, fmt.Errorf("a `${`")
			}
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, commas, nil
			}
		case ',':
			if depth == 1 {
				commas = append(commas, i-open-1)
			}
		}
	}
	return 0, nil, fmt.Errorf("a `{` without `}`")
}

// sequence expands a `{x..y}` or `{x..y..step}` body: integers, padded with
// zeros when either end is, or single ASCII letters.
func sequence(body string) ([]string, bool, error) {
	parts := strings.Split(body, "..")
	if len(parts) != 2 && len(parts) != 3 {
		return nil, false, nil
	}
	step := 1
	if len(parts) == 3 {
		n, err := strconv.Atoi(parts[2])
		if err != nil || !isInt(parts[2]) {
			return nil, false, nil
		}
		if n < 0 {
			n = -n
		}
		if n == 0 {
			n = 1
		}
		step = n
	}
	x, y := parts[0], parts[1]
	if isInt(x) && isInt(y) {
		a, errA := strconv.Atoi(x)
		b, errB := strconv.Atoi(y)
		if errA != nil || errB != nil {
			return nil, false, fmt.Errorf("a sequence out of range")
		}
		width := 0
		if padded(x) || padded(y) {
			width = max(len(x), len(y))
		}
		var out []string
		for i := a; ; {
			out = append(out, pad(i, width))
			if len(out) > maxExpansions {
				return nil, false, fmt.Errorf("more than %d brace expansions", maxExpansions)
			}
			if a <= b {
				if i += step; i > b {
					break
				}
			} else if i -= step; i < b {
				break
			}
		}
		return out, true, nil
	}
	if isLetter(x) && isLetter(y) {
		a, b := int(x[0]), int(y[0])
		var out []string
		for i := a; ; {
			if c := rune(i); c != '\\' {
				out = append(out, string(c))
			}
			if a <= b {
				if i += step; i > b {
					break
				}
			} else if i -= step; i < b {
				break
			}
		}
		return out, true, nil
	}
	return nil, false, nil
}

func isInt(s string) bool {
	s = strings.TrimPrefix(s, "-")
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func padded(s string) bool {
	s = strings.TrimPrefix(s, "-")
	return len(s) > 1 && s[0] == '0'
}

func pad(i, width int) string {
	neg := i < 0
	if neg {
		i = -i
	}
	s := strconv.Itoa(i)
	w := width
	if neg {
		w--
	}
	for len(s) < w {
		s = "0" + s
	}
	if neg {
		s = "-" + s
	}
	return s
}

func isLetter(s string) bool {
	return len(s) == 1 && (s[0] >= 'a' && s[0] <= 'z' || s[0] >= 'A' && s[0] <= 'Z')
}
