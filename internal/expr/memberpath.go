package expr

import (
	"bytes"
	"context"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf16"
	"unicode/utf8"
)

// ExactMemberPaths marks an Engine whose evaluation of a member path over
// JSON values gives what the pipeline's own answer gives (memberPath):
// JavaScript's JSON.parse, property read, optional chaining and
// JSON.stringify. The pipeline answers such paths itself only on an
// engine that implements it; TestMemberPathsMatchEngine holds the real
// engine to it.
type ExactMemberPaths interface {
	ExactMemberPaths()
}

// maxPathText bounds the JSON text a member path reads, and maxPathDepth
// its nesting: a larger or deeper value goes to the engine, whose memory
// and stack limits then apply as they would.
const (
	maxPathText  = 1 << 20
	maxPathDepth = 128
)

// memberPath is an expression that only reads a member path of a JSON
// value: a bound context value (flow_input, previous_result, ...) or a
// step's result (results.<id>, which JE-12 rewrites into an awaited read),
// then members and indexes, optionally chained. The engine would parse the
// value's text, read the members as JavaScript does and serialise what it
// finds (JE-15); the pipeline does the same without the engine, and
// declines (nil, or ok false from eval) wherever JavaScript would do more
// than read a JSON value's own data: a prototype's property, a primitive's
// member, a read of null or undefined that is not optional, or a value it
// cannot parse exactly.
type memberPath struct {
	// root is the context value's name, or "" for results.<id>.
	root string
	// step is the id of results.<id>.
	step  string
	steps []pathStep
}

// pathStep is one read: .key, ?.key, [index] or ["key"], optionally chained.
type pathStep struct {
	key      string
	optional bool
}

// parseMemberPath returns e's member path, or nil when e is anything else.
func parseMemberPath(e *Expr) *memberPath {
	if e.Mode != ModeExpression || e.RewritePanic != 0 || slices.Contains(e.Binds, "error") {
		return nil
	}
	t := e.Trimmed
	matches := resultsRead.FindAllStringIndex(t, -1)
	mp := &memberPath{}
	var rest string
	if strings.HasPrefix(t, "results") {
		// The one rewrite (JE-12) must be the root read: (await results.x).
		if len(matches) != 1 || matches[0][0] != 0 {
			return nil
		}
		root := t[:matches[0][1]]
		switch {
		case strings.HasPrefix(root, "results.") || strings.HasPrefix(root, "results?."):
			id := root[strings.IndexByte(root, '.')+1:]
			if !isIdent(id) {
				return nil
			}
			mp.step = id
		case strings.HasPrefix(root, `results["`):
			id := root[len(`results["`) : len(root)-len(`"]`)]
			if !plainString(id, '"') {
				return nil
			}
			mp.step = id
		default:
			return nil
		}
		rest = t[len(root):]
	} else {
		if len(matches) != 0 {
			return nil
		}
		n := identPrefix(t)
		if n == 0 || !slices.Contains(hostStringNames, t[:n]) {
			return nil
		}
		mp.root, rest = t[:n], t[n:]
	}
	for rest != "" {
		var m pathStep
		if r, ok := strings.CutPrefix(rest, "?."); ok {
			m.optional, rest = true, r
			if !strings.HasPrefix(rest, "[") {
				n := identPrefix(rest)
				if n == 0 {
					return nil
				}
				m.key, rest = rest[:n], rest[n:]
				mp.steps = append(mp.steps, m)
				continue
			}
		} else if r, ok := strings.CutPrefix(rest, "."); ok {
			n := identPrefix(r)
			if n == 0 {
				return nil
			}
			m.key, rest = r[:n], r[n:]
			mp.steps = append(mp.steps, m)
			continue
		}
		// [index], ["key"] or ['key'].
		if !strings.HasPrefix(rest, "[") {
			return nil
		}
		end := strings.IndexByte(rest, ']')
		if end < 0 {
			return nil
		}
		in := rest[1:end]
		switch {
		case len(in) >= 2 && (in[0] == '"' || in[0] == '\'') && in[len(in)-1] == in[0]:
			if !plainString(in[1:len(in)-1], in[0]) {
				return nil
			}
			m.key = in[1 : len(in)-1]
		case canonicalIndex(in) && len(in) <= 15:
			m.key = in
		default:
			return nil
		}
		rest = rest[end+1:]
		mp.steps = append(mp.steps, m)
	}
	return mp
}

// identPrefix returns the length of the ASCII identifier at the start of
// s, or 0.
func identPrefix(s string) int {
	n := 0
	for n < len(s) {
		c := s[n]
		if c == '_' || c == '$' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || n > 0 && '0' <= c && c <= '9' {
			n++
			continue
		}
		break
	}
	return n
}

func isIdent(s string) bool { return s != "" && identPrefix(s) == len(s) }

// plainString reports whether s can sit between quote characters q as a
// JavaScript string literal that means s itself: no quote, no backslash,
// no line terminator.
func plainString(s string, q byte) bool {
	return utf8.ValidString(s) && !strings.ContainsAny(s, string(q)+"\\\n\r\u2028\u2029")
}

// canonicalIndex reports whether s is a decimal integer as JavaScript
// writes it: 0, or digits without a leading zero.
func canonicalIndex(s string) bool {
	if s == "" || s[0] == '0' && len(s) > 1 {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// eval answers the member path from the values the evaluation binds (in),
// or reports false to leave it to the engine. The engine's context prelude
// parses every bound value before the path is read, so each must be JSON
// that parses exactly, and none may need the prelude's rebuild (JE-16).
// The root of results.<id> is the previous result when the results prelude
// answers it from memory (JE-18 rule 1), and otherwise the results
// lookup's answer, read once: when eval declines after the read, the
// engine's read of the same id gets that answer (in is changed so), as the
// engine would have made the only read.
func (mp *memberPath) eval(ctx context.Context, in *Input) ([]byte, bool) {
	rootName := mp.root
	if rootName == "" {
		var prev []byte
		for _, s := range in.Strings {
			if s.Name == hostPreviousID {
				prev = s.Text
			}
		}
		if prev != nil && string(prev) == mp.step {
			rootName = "previous_result"
		}
	}
	var root []byte
	rootBound := false
	for _, s := range in.Strings {
		switch s.Name {
		case hostRebuild:
			return nil, false
		case hostPreviousID:
		case rootName:
			root, rootBound = s.Text, true
		default:
			if !validJSON(s.Text) {
				return nil, false
			}
		}
	}
	if rootName != "" {
		if !rootBound {
			return nil, false // a ReferenceError
		}
		return walkPath(root, mp.steps)
	}
	lookup := slices.IndexFunc(in.Funcs, func(f HostFunc) bool { return f.Name == hostResults })
	if lookup < 0 {
		return nil, false // results is not defined
	}
	call := in.Funcs[lookup].Call
	text, err := call(ctx, mp.step)
	var used atomic.Bool
	in.Funcs = slices.Clone(in.Funcs)
	in.Funcs[lookup].Call = func(ctx context.Context, id string) ([]byte, error) {
		if id == mp.step && used.CompareAndSwap(false, true) {
			return text, err
		}
		return call(ctx, id)
	}
	if err != nil {
		return nil, false // the read's Error, which the engine throws
	}
	return walkPath(text, mp.steps)
}

// validJSON reports whether t is UTF-8 JSON text no deeper than
// maxPathDepth.
func validJSON(t []byte) bool {
	if len(t) > maxPathText || !utf8.Valid(t) {
		return false
	}
	sc := &pathScanner{s: t}
	sc.space()
	if !sc.skip(0) {
		return false
	}
	sc.space()
	return sc.i == len(t)
}

// walkPath reads steps from the value JSON.parse makes of text, and
// returns JSON.stringify(v ?? null) of what it finds. It checks the whole
// text, then follows the path through the text without building the
// values it passes, and builds only the value it ends on.
func walkPath(text []byte, steps []pathStep) ([]byte, bool) {
	if !validJSON(text) {
		return nil, false
	}
	sc := &pathScanner{s: text}
	sc.space()
	at, undefined := sc.i, false
	for n, m := range steps {
		if undefined || text[at] == 'n' {
			if m.optional {
				// Optional chaining ends the whole chain at undefined.
				undefined = true
				break
			}
			return nil, false // a TypeError, whose text is the engine's
		}
		switch text[at] {
		case '{':
			var found bool
			at, found = sc.member(at, m.key)
			if !found {
				if slices.Contains(objectProtoNames, m.key) {
					return nil, false // inherited from Object.prototype
				}
				undefined = true
			}
		case '[':
			if m.key == "length" {
				if n != len(steps)-1 {
					return nil, false // a number's member
				}
				return strconv.AppendInt(nil, int64(sc.count(at)), 10), true
			}
			i, ok := arrayIndex(m.key)
			if !ok {
				return nil, false // a property of Array.prototype, or none
			}
			var found bool
			if at, found = sc.element(at, i); !found {
				undefined = true
			}
		default:
			return nil, false // a primitive's member
		}
	}
	if undefined {
		return []byte("null"), true
	}
	sc.i = at
	v, ok := sc.value(0)
	if !ok {
		return nil, false
	}
	return v.appendJSON(nil), true
}

// pathScanner reads JSON text: it checks and skips values, finds an object's
// member or an array's element, and builds the value at a position.
type pathScanner struct {
	s []byte
	i int
}

func (sc *pathScanner) space() {
	for sc.i < len(sc.s) && (sc.s[sc.i] == ' ' || sc.s[sc.i] == '\t' || sc.s[sc.i] == '\n' || sc.s[sc.i] == '\r') {
		sc.i++
	}
}

func (sc *pathScanner) lit(w string) bool {
	if bytes.HasPrefix(sc.s[sc.i:], []byte(w)) {
		sc.i += len(w)
		return true
	}
	return false
}

// skip checks and skips one JSON value; depth is its nesting so far.
func (sc *pathScanner) skip(depth int) bool {
	if sc.i >= len(sc.s) || depth >= maxPathDepth {
		return false
	}
	switch c := sc.s[sc.i]; c {
	case '{', '[':
		sc.i++
		closing := byte('}')
		if c == '[' {
			closing = ']'
		}
		sc.space()
		if sc.lit(string(closing)) {
			return true
		}
		for {
			sc.space()
			if c == '{' {
				if _, ok := sc.rawString(); !ok {
					return false
				}
				sc.space()
				if !sc.lit(":") {
					return false
				}
				sc.space()
			}
			if !sc.skip(depth + 1) {
				return false
			}
			sc.space()
			if sc.lit(string(closing)) {
				return true
			}
			if !sc.lit(",") {
				return false
			}
		}
	case '"':
		_, ok := sc.rawString()
		return ok
	case 't':
		return sc.lit("true")
	case 'f':
		return sc.lit("false")
	case 'n':
		return sc.lit("null")
	}
	_, ok := sc.rawNumber()
	return ok
}

// rawString checks and skips a JSON string, and returns its text between
// the quotes, escapes as written.
func (sc *pathScanner) rawString() ([]byte, bool) {
	if !sc.lit(`"`) {
		return nil, false
	}
	start := sc.i
	for sc.i < len(sc.s) {
		switch c := sc.s[sc.i]; {
		case c == '"':
			sc.i++
			return sc.s[start : sc.i-1], true
		case c < 0x20:
			return nil, false
		case c == '\\':
			if sc.i+1 >= len(sc.s) {
				return nil, false
			}
			switch sc.s[sc.i+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				sc.i += 2
			case 'u':
				if sc.i+6 > len(sc.s) || !isHex4(sc.s[sc.i+2:sc.i+6]) {
					return nil, false
				}
				sc.i += 6
			default:
				return nil, false
			}
		default:
			sc.i++
		}
	}
	return nil, false
}

func isHex4(b []byte) bool {
	for _, c := range b {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return false
		}
	}
	return true
}

// rawNumber checks and skips a JSON number, and returns its text.
func (sc *pathScanner) rawNumber() ([]byte, bool) {
	start := sc.i
	digits := func() int {
		n := 0
		for sc.i < len(sc.s) && '0' <= sc.s[sc.i] && sc.s[sc.i] <= '9' {
			sc.i++
			n++
		}
		return n
	}
	sc.lit("-")
	if !sc.lit("0") && digits() == 0 {
		return nil, false
	}
	if sc.lit(".") && digits() == 0 {
		return nil, false
	}
	if sc.lit("e") || sc.lit("E") {
		if !sc.lit("+") {
			sc.lit("-")
		}
		if digits() == 0 {
			return nil, false
		}
	}
	return sc.s[start:sc.i], true
}

// member returns the position of the value of the object at at for key,
// the last one for a key the text repeats, as JSON.parse keeps it. The
// object has been checked.
func (sc *pathScanner) member(at int, key string) (int, bool) {
	sc.i = at + 1
	found, pos := false, 0
	for {
		sc.space()
		if sc.lit("}") {
			return pos, found
		}
		raw, _ := sc.rawString()
		sc.space()
		sc.lit(":")
		sc.space()
		if pathKeyIs(raw, key) {
			found, pos = true, sc.i
		}
		sc.skip(0)
		sc.space()
		sc.lit(",")
	}
}

// pathKeyIs reports whether the JSON string text raw, between its quotes,
// means key. A string with an unpaired surrogate means no Go string.
func pathKeyIs(raw []byte, key string) bool {
	if bytes.IndexByte(raw, '\\') < 0 {
		return string(raw) == key
	}
	s, ok := unquote(raw)
	return ok && s == key
}

// element returns the position of the array at at's element i. The array
// has been checked.
func (sc *pathScanner) element(at int, i uint64) (int, bool) {
	sc.i = at + 1
	for n := uint64(0); ; n++ {
		sc.space()
		if sc.lit("]") {
			return 0, false
		}
		if n == i {
			return sc.i, true
		}
		sc.skip(0)
		sc.space()
		sc.lit(",")
	}
}

// count returns the number of elements of the array at at, which has been
// checked.
func (sc *pathScanner) count(at int) int {
	sc.i = at + 1
	for n := 0; ; n++ {
		sc.space()
		if sc.lit("]") {
			return n
		}
		sc.skip(0)
		sc.space()
		sc.lit(",")
	}
}

// value builds the checked value at the pathScanner's position, and reports
// false for a string that holds an unpaired surrogate, which JavaScript
// keeps and Go's strings cannot hold.
func (sc *pathScanner) value(depth int) (*jsValue, bool) {
	switch sc.s[sc.i] {
	case '{':
		sc.i++
		o := &jsObject{}
		for {
			sc.space()
			if sc.lit("}") {
				return &jsValue{kind: jsObjectKind, obj: o}, true
			}
			raw, _ := sc.rawString()
			k, ok := unquote(raw)
			if !ok {
				return nil, false
			}
			sc.space()
			sc.lit(":")
			sc.space()
			v, ok := sc.value(depth + 1)
			if !ok {
				return nil, false
			}
			o.keys, o.vals = append(o.keys, k), append(o.vals, v)
			sc.space()
			sc.lit(",")
		}
	case '[':
		sc.i++
		a := &jsValue{kind: jsArray, arr: []*jsValue{}}
		for {
			sc.space()
			if sc.lit("]") {
				return a, true
			}
			v, ok := sc.value(depth + 1)
			if !ok {
				return nil, false
			}
			a.arr = append(a.arr, v)
			sc.space()
			sc.lit(",")
		}
	case '"':
		raw, _ := sc.rawString()
		s, ok := unquote(raw)
		return &jsValue{kind: jsString, s: s}, ok
	case 't':
		sc.i += len("true")
		return &jsValue{kind: jsBool, b: true}, true
	case 'f':
		sc.i += len("false")
		return &jsValue{kind: jsBool}, true
	case 'n':
		sc.i += len("null")
		return &jsValue{kind: jsNull}, true
	}
	raw, _ := sc.rawNumber()
	// JavaScript reads a number as the nearest double, and one too large
	// for a double as an infinity; ParseFloat does the same, with an
	// error for the infinity.
	f, _ := strconv.ParseFloat(string(raw), 64)
	return &jsValue{kind: jsNumber, n: f}, true
}

// unquote decodes the text of a checked JSON string between its quotes.
// An escaped surrogate pair becomes its code point; an unpaired surrogate
// escape fails.
func unquote(raw []byte) (string, bool) {
	if bytes.IndexByte(raw, '\\') < 0 {
		return string(raw), true
	}
	b := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			b = append(b, raw[i])
			continue
		}
		i++
		switch e := raw[i]; e {
		case 'b':
			b = append(b, '\b')
		case 'f':
			b = append(b, '\f')
		case 'n':
			b = append(b, '\n')
		case 'r':
			b = append(b, '\r')
		case 't':
			b = append(b, '\t')
		case 'u':
			r := hex4(raw[i+1 : i+5])
			i += 4
			if utf16.IsSurrogate(r) {
				if r >= 0xDC00 || i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
					return "", false
				}
				lo := hex4(raw[i+3 : i+7])
				if lo < 0xDC00 || lo > 0xDFFF {
					return "", false
				}
				r = utf16.DecodeRune(r, lo)
				i += 6
			}
			b = utf8.AppendRune(b, r)
		default: // " \ /
			b = append(b, e)
		}
	}
	return string(b), true
}

func hex4(b []byte) rune {
	n, _ := strconv.ParseUint(string(b), 16, 32)
	return rune(n)
}

// jsValue is a value JSON.parse makes: null, a boolean, a number, a
// string, an array, or an ordinary object.
type jsValue struct {
	kind jsKind
	b    bool
	n    float64
	s    string
	arr  []*jsValue
	obj  *jsObject
}

type jsKind uint8

const (
	jsNull jsKind = iota
	jsBool
	jsNumber
	jsString
	jsArray
	jsObjectKind
)

// jsObject is an object's members as the text gives them, repeated keys
// included.
type jsObject struct {
	keys []string
	vals []*jsValue
}

// objectProtoNames are the own properties of Object.prototype, which every
// object JSON.parse makes inherits: a read of one that the object does not
// have itself is left to the engine. TestMemberPathsMatchEngine compares
// the list with the engine's.
var objectProtoNames = []string{
	"__defineGetter__", "__defineSetter__", "__lookupGetter__", "__lookupSetter__", "__proto__",
	"constructor", "hasOwnProperty", "isPrototypeOf", "propertyIsEnumerable", "toLocaleString", "toString", "valueOf",
}

// arrayIndex returns key as an array index: a canonical decimal integer
// below 2^32 - 1.
func arrayIndex(key string) (uint64, bool) {
	if !canonicalIndex(key) || len(key) > 10 {
		return 0, false
	}
	i, err := strconv.ParseUint(key, 10, 64)
	return i, err == nil && i < math.MaxUint32
}

// appendJSON appends JSON.stringify(v) to b: an object's array-index keys
// first, ascending, then its other keys in the order they were first
// created, each with its last value, as JSON.parse makes the object and
// JavaScript enumerates it; numbers as JavaScript writes them; strings
// escaped as JSON.stringify escapes them.
func (v *jsValue) appendJSON(b []byte) []byte {
	switch v.kind {
	case jsNull:
		return append(b, "null"...)
	case jsBool:
		return strconv.AppendBool(b, v.b)
	case jsNumber:
		if math.IsInf(v.n, 0) || math.IsNaN(v.n) {
			return append(b, "null"...)
		}
		return appendJSNumber(b, v.n)
	case jsString:
		return appendJSString(b, v.s)
	case jsArray:
		b = append(b, '[')
		for i, m := range v.arr {
			if i > 0 {
				b = append(b, ',')
			}
			b = m.appendJSON(b)
		}
		return append(b, ']')
	}
	keys, vals := v.obj.members()
	b = append(b, '{')
	for i, k := range keys {
		if i > 0 {
			b = append(b, ',')
		}
		b = appendJSString(b, k)
		b = append(b, ':')
		b = vals[i].appendJSON(b)
	}
	return append(b, '}')
}

// members returns the object's own properties as JavaScript enumerates
// them: array indexes ascending, then the other keys in creation order; a
// repeated key keeps its first place and takes its last value.
func (o *jsObject) members() ([]string, []*jsValue) {
	type prop struct {
		key   string
		index uint64
		isIdx bool
		val   *jsValue
	}
	props := make([]prop, 0, len(o.keys))
	var seen map[string]int
	if len(o.keys) > 8 {
		seen = make(map[string]int, len(o.keys))
	}
	find := func(k string) int {
		if seen != nil {
			if i, ok := seen[k]; ok {
				return i
			}
			return -1
		}
		return slices.IndexFunc(props, func(p prop) bool { return p.key == k })
	}
	for n, k := range o.keys {
		if i := find(k); i >= 0 {
			props[i].val = o.vals[n]
			continue
		}
		idx, isIdx := arrayIndex(k)
		props = append(props, prop{k, idx, isIdx, o.vals[n]})
		if seen != nil {
			seen[k] = len(props) - 1
		}
	}
	slices.SortStableFunc(props, func(a, b prop) int {
		switch {
		case a.isIdx && b.isIdx:
			return cmpUint(a.index, b.index)
		case a.isIdx:
			return -1
		case b.isIdx:
			return 1
		}
		return 0
	})
	keys, vals := make([]string, len(props)), make([]*jsValue, len(props))
	for i, p := range props {
		keys[i], vals[i] = p.key, p.val
	}
	return keys, vals
}

func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// appendJSNumber appends f as JavaScript's Number::toString writes it: the
// shortest digits that read back as f, in plain notation from 1e-6 up to
// 1e21, and with an exponent outside it; -0 is 0.
func appendJSNumber(b []byte, f float64) []byte {
	if f == 0 {
		return append(b, '0')
	}
	if f < 0 {
		b = append(b, '-')
		f = -f
	}
	// The shortest round-trip digits d1 d2 ... dk and the exponent: f is
	// 0.d1...dk × 10^n.
	e := strconv.AppendFloat(nil, f, 'e', -1, 64)
	mant, exp, _ := bytes.Cut(e, []byte("e"))
	digits := bytes.ReplaceAll(mant, []byte("."), nil)
	x, _ := strconv.Atoi(string(exp))
	k, n := len(digits), x+1
	switch {
	case k <= n && n <= 21:
		b = append(b, digits...)
		for range n - k {
			b = append(b, '0')
		}
	case 0 < n && n <= 21:
		b = append(b, digits[:n]...)
		b = append(b, '.')
		b = append(b, digits[n:]...)
	case -6 < n && n <= 0:
		b = append(b, "0."...)
		for range -n {
			b = append(b, '0')
		}
		b = append(b, digits...)
	default:
		b = append(b, digits[0])
		if k > 1 {
			b = append(b, '.')
			b = append(b, digits[1:]...)
		}
		b = append(b, 'e')
		if n-1 >= 0 {
			b = append(b, '+')
		}
		b = strconv.AppendInt(b, int64(n-1), 10)
	}
	return b
}

// appendJSString appends s quoted as JSON.stringify quotes it: the quote
// and the backslash escaped, the control characters with a short escape
// by it, the other control characters as \u00XX, everything else as it
// is.
func appendJSString(b []byte, s string) []byte {
	const hex = "0123456789abcdef"
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			b = append(b, '\\', '"')
		case '\\':
			b = append(b, '\\', '\\')
		case '\b':
			b = append(b, '\\', 'b')
		case '\f':
			b = append(b, '\\', 'f')
		case '\n':
			b = append(b, '\\', 'n')
		case '\r':
			b = append(b, '\\', 'r')
		case '\t':
			b = append(b, '\\', 't')
		default:
			if c < 0x20 {
				b = append(b, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xf])
			} else {
				b = append(b, c)
			}
		}
	}
	return append(b, '"')
}
