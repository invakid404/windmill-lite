package syncfolder

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// The CLI matches wmill.yaml's filter and codebase globs with minimatch,
// with its default options, against root-relative POSIX paths (SY-8,
// SY-13). glob implements the part of minimatch's syntax on which
// minimatch 9 and 10 agree and which a regular matcher can read:
//
//   - `*` (any characters but `/`), `?` (one character), classes `[...]`
//     with `!` or `^` negation and ranges, and `\` escapes;
//   - characters are UTF-16 code units, as in minimatch's JavaScript
//     regular expressions: `?` matches one surrogate half of `😀`, which
//     takes `??`, and the class `[😀]` holds both halves and matches either
//     one but never the whole character, so `[😀]?` and `[😀][😀]` match
//     `😀` and `[😀]` alone does not;
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
//
// Matching takes time polynomial in the pattern and the path: `*` runs
// backtrack only to the last star, and `**` states are memoized.
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

// globTok is one token of a segment pattern. u is a literal's UTF-16 code
// unit.
type globTok struct {
	kind  tokKind
	u     uint16
	class *globClass
}

// globClass is a class of UTF-16 code units.
type globClass struct {
	negate bool
	ranges [][2]uint16
}

// single reports a class of exactly one code unit (`[.]`, `[.-.]`), which
// minimatch 9 and 10 read as that literal character.
func (c *globClass) single() (uint16, bool) {
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
	u := utf16.Encode([]rune(s))
	var toks []globTok
	for i := 0; i < len(u); i++ {
		c := u[i]
		switch {
		case c == '\\':
			if i+1 >= len(u) {
				return nil, fmt.Errorf("a trailing backslash")
			}
			i++
			toks = append(toks, globTok{kind: tokLit, u: u[i]})
		case strings.ContainsRune("@!+?*", rune(c)) && i+1 < len(u) && u[i+1] == '(':
			return nil, fmt.Errorf("an extglob %q", string(rune(c))+"(")
		case c == '?':
			toks = append(toks, globTok{kind: tokAny})
		case c == '*':
			if len(toks) == 0 || toks[len(toks)-1].kind != tokStar {
				toks = append(toks, globTok{kind: tokStar})
			}
		case c == '[':
			cl, n, ok, err := compileClass(u[i:])
			if err != nil {
				return nil, fmt.Errorf("%s in %q", err, s)
			}
			if !ok {
				// An unclosed `[` is a literal character.
				toks = append(toks, globTok{kind: tokLit, u: c})
				continue
			}
			if lit, single := cl.single(); single {
				toks = append(toks, globTok{kind: tokLit, u: lit})
			} else {
				toks = append(toks, globTok{kind: tokClass, class: cl})
			}
			i += n - 1
		default:
			toks = append(toks, globTok{kind: tokLit, u: c})
		}
	}
	return toks, nil
}

// compileClass parses a class at the start of u and returns its length in
// code units. It returns ok false when the class is not closed, in which
// case `[` is literal. A reversed range matches nothing.
func compileClass(u []uint16) (*globClass, int, bool, error) {
	c := &globClass{}
	i := 1
	if i < len(u) && (u[i] == '!' || u[i] == '^') {
		c.negate = true
		i++
	}
	first := true
	for i < len(u) {
		if u[i] == ']' && !first {
			return c, i + 1, true, nil
		}
		if u[i] == '[' && i+1 < len(u) && (u[i+1] == ':' || u[i+1] == '=' || u[i+1] == '.') {
			return nil, 0, false, fmt.Errorf("a POSIX class")
		}
		first = false
		lo, ok := classUnit(u, &i)
		if !ok {
			return nil, 0, false, nil
		}
		hi := lo
		if i+1 < len(u) && u[i] == '-' && u[i+1] != ']' {
			i++
			if hi, ok = classUnit(u, &i); !ok {
				return nil, 0, false, nil
			}
		}
		c.ranges = append(c.ranges, [2]uint16{lo, hi})
	}
	return nil, 0, false, nil
}

// classUnit reads one class member at u[*i], unescaping `\`.
func classUnit(u []uint16, i *int) (uint16, bool) {
	if u[*i] == '\\' {
		if *i+1 >= len(u) {
			return 0, false
		}
		*i++
	}
	c := u[*i]
	*i++
	return c, true
}

func (c *globClass) has(x uint16) bool {
	for _, rg := range c.ranges {
		if rg[0] <= x && x <= rg[1] {
			return true
		}
	}
	return false
}

func (c *globClass) matches(x uint16) bool {
	return c.has(x) != c.negate
}

// match reports whether the root-relative file path p matches.
func (g *glob) match(p string) bool {
	hit, _ := g.matchSteps(p)
	return hit
}

// matchSteps is match, also returning the number of matching steps taken,
// which the tests bound.
func (g *glob) matchSteps(p string) (bool, int) {
	if g.comment {
		return false, 0
	}
	parts := strings.Split(p, "/")
	units := make([][]uint16, len(parts))
	steps, hit := 0, false
	for _, alt := range g.alts {
		if alt.never {
			continue
		}
		m := &segMatcher{segs: alt.segs, parts: parts, units: units,
			memo: make([]int8, (len(alt.segs)+1)*(len(parts)+1))}
		hit = m.at(0, 0)
		steps += m.steps
		if hit {
			break
		}
	}
	return hit != g.negate, steps
}

// segMatcher matches pattern segments against path segments, memoizing
// each (pattern segment, path segment) state so that `**` runs do not
// revisit them.
type segMatcher struct {
	segs  []globSeg
	parts []string
	units [][]uint16 // the parts as UTF-16, converted on first use
	memo  []int8     // 0 unknown, 1 match, 2 no match
	steps int
}

func (m *segMatcher) at(si, pi int) bool {
	key := si*(len(m.parts)+1) + pi
	if v := m.memo[key]; v != 0 {
		return v == 1
	}
	m.steps++
	hit := m.compute(si, pi)
	m.memo[key] = 2
	if hit {
		m.memo[key] = 1
	}
	return hit
}

func (m *segMatcher) compute(si, pi int) bool {
	if si == len(m.segs) {
		return pi == len(m.parts)
	}
	if seg := m.segs[si]; !seg.globstar {
		return pi < len(m.parts) && m.segment(seg.toks, pi) && m.at(si+1, pi+1)
	}
	if si == len(m.segs)-1 {
		// A trailing `**` matches one or more segments, none of them dotted.
		if pi == len(m.parts) {
			return false
		}
		for _, p := range m.parts[pi:] {
			m.steps++
			if strings.HasPrefix(p, ".") {
				return false
			}
		}
		return true
	}
	for i := pi; i <= len(m.parts); i++ {
		if m.at(si+1, i) {
			return true
		}
		if i < len(m.parts) && strings.HasPrefix(m.parts[i], ".") {
			return false
		}
	}
	return false
}

func (m *segMatcher) segment(toks []globTok, pi int) bool {
	s := m.parts[pi]
	if (s == "." || s == "..") && !allLiteral(toks) {
		return false // wildcards never match these segments
	}
	if strings.HasPrefix(s, ".") && !leadingDotAllowed(toks) {
		return false
	}
	if m.units[pi] == nil {
		m.units[pi] = utf16.Encode([]rune(s))
	}
	return m.tokens(toks, m.units[pi])
}

// tokens matches one segment pattern against one path segment. Every token
// but `*` takes exactly one code unit, so on a mismatch it is enough to
// let the last `*` take one more unit; the steps are bounded by the
// segment's length times the pattern's.
func (m *segMatcher) tokens(toks []globTok, s []uint16) bool {
	ti, si := 0, 0
	star, mark := -1, 0
	for si < len(s) {
		m.steps++
		if ti < len(toks) {
			switch t := toks[ti]; {
			case t.kind == tokStar:
				star, mark = ti, si
				ti++
				continue
			case t.kind == tokAny,
				t.kind == tokLit && t.u == s[si],
				t.kind == tokClass && t.class.matches(s[si]):
				ti++
				si++
				continue
			}
		}
		if star < 0 {
			return false
		}
		mark++
		ti, si = star+1, mark
	}
	for ti < len(toks) && toks[ti].kind == tokStar {
		ti++
	}
	return ti == len(toks)
}

func allLiteral(toks []globTok) bool {
	_, ok := literal(toks)
	return ok
}

// literal returns the text of a segment pattern made only of literals.
func literal(toks []globTok) (string, bool) {
	u := make([]uint16, 0, len(toks))
	for _, t := range toks {
		if t.kind != tokLit {
			return "", false
		}
		u = append(u, t.u)
	}
	return string(utf16.Decode(u)), true
}

// leadingDotAllowed reports whether a segment pattern may match a segment
// that starts with `.`: it must start with a literal `.`.
func leadingDotAllowed(toks []globTok) bool {
	return len(toks) > 0 && toks[0].kind == tokLit && toks[0].u == '.'
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

// maxSafeInt is 2^53, beyond which JavaScript numbers, which minimatch's
// brace expansion computes with, are not exact integers.
const maxSafeInt = 1 << 53

// sequence expands a `{x..y}` or `{x..y..step}` body: integers, padded with
// zeros when either end is, or single ASCII letters. The step's sign is
// ignored and 0 counts as 1; a step beyond the ends' distance gives the
// start alone. Ends beyond 2^53, and a step beyond 2^53 that is not beyond
// that distance, are refused, because JavaScript would round them.
func sequence(body string) ([]string, bool, error) {
	parts := strings.Split(body, "..")
	if len(parts) != 2 && len(parts) != 3 {
		return nil, false, nil
	}
	step := uint64(1)
	if len(parts) == 3 {
		if !isInt(parts[2]) {
			return nil, false, nil
		}
		if step = absDigits(parts[2]); step == 0 {
			step = 1
		}
	}
	x, y := parts[0], parts[1]
	switch {
	case isInt(x) && isInt(y):
		a, okA := safeInt(x)
		b, okB := safeInt(y)
		if !okA || !okB {
			return nil, false, fmt.Errorf("a sequence end beyond 2^53")
		}
		width := 0
		if padded(x) || padded(y) {
			width = max(len(x), len(y))
		}
		return expandSequence(a, b, step, func(i int64) (string, bool) { return pad(i, width), true })
	case isLetter(x) && isLetter(y):
		return expandSequence(int64(x[0]), int64(y[0]), step, func(i int64) (string, bool) {
			return string(rune(i)), rune(i) != '\\'
		})
	}
	return nil, false, nil
}

// expandSequence lists a, a±step, … up to b. The ends are within ±2^53,
// so their distance and every offset fit in 64 bits.
func expandSequence(a, b int64, step uint64, item func(int64) (string, bool)) ([]string, bool, error) {
	var dist uint64
	if a <= b {
		dist = uint64(b - a)
	} else {
		dist = uint64(a - b)
	}
	if step > maxSafeInt && step <= dist {
		return nil, false, fmt.Errorf("a sequence step beyond 2^53")
	}
	n := dist/step + 1
	if n > maxExpansions {
		return nil, false, fmt.Errorf("more than %d brace expansions", maxExpansions)
	}
	out := make([]string, 0, n)
	for k := uint64(0); k < n; k++ {
		off := int64(k * step)
		i := a + off
		if a > b {
			i = a - off
		}
		if s, ok := item(i); ok {
			out = append(out, s)
		}
	}
	return out, true, nil
}

// absDigits is the absolute value of a decimal integer, saturated at the
// largest uint64.
func absDigits(s string) uint64 {
	var v uint64
	for i := 0; i < len(s); i++ {
		if s[i] == '-' {
			continue
		}
		d := uint64(s[i] - '0')
		if v > (math.MaxUint64-d)/10 {
			return math.MaxUint64
		}
		v = v*10 + d
	}
	return v
}

// safeInt parses a decimal integer within ±2^53.
func safeInt(s string) (int64, bool) {
	v := absDigits(s)
	if v > maxSafeInt {
		return 0, false
	}
	if strings.HasPrefix(s, "-") {
		return -int64(v), true
	}
	return int64(v), true
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

func pad(i int64, width int) string {
	neg := i < 0
	if neg {
		i = -i
	}
	s := strconv.FormatInt(i, 10)
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
