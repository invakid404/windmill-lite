package expr

import (
	"context"
	"math"
	"time"

	"github.com/invakid404/windmill-lite/internal/jsonval"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// jsExpr is an expression over JSON values that the pipeline evaluates in
// Go with JavaScript's semantics, without the engine (ADR 0003): member
// paths of any bound root (a context value, flow_input, or a step's
// result read as results.<id>, from memory or through the lookup) combined
// by template literals, string, number, boolean and null literals, .length,
// !, unary -, the arithmetic operators, the relational operators, === and
// !==, && and ||, and ?:.
//
// It declines (eval's ok false) wherever JavaScript would do more than
// compute over the values' own data, so that the engine answers, or fails,
// as it would: a read of a member of null or undefined, a root that is not
// bound, a lookup that fails, an object compared or returned, an object
// with a toString or valueOf key converted to a primitive, a string or an
// object converted to a number, a string with a lone surrogate, a key
// named __proto__, and any text it does not parse whole (a call, an
// assignment, a comment, a new line).
type jsExpr struct {
	n jsNode
}

type jsNode struct {
	op   string // "lit", "path", "tmpl", "len", "!", "neg", binary ops, "?:"
	lit  jsVal
	path *jsPath
	kids []jsNode
	strs []string // tmpl: the cooked strings, one more than kids
}

// jsPath is a root and the keys read from it.
type jsPath struct {
	root string // a context name, flow_input, or "" for results.<id>
	step string
	keys []jsKey
}

type jsKey struct {
	key      string
	optional bool
}

// spelling identifies p's read: two paths with the same spelling read the
// same value. Every field is length-delimited and every component carries
// an explicit flag, optional ('o') or not ('n'), so that no key can imitate
// another component or a flag (a "?" prefix would alias a?.b with
// a["?b"]).
func (p *jsPath) spelling() string {
	var b strings.Builder
	field := func(s string) {
		b.WriteString(strconv.Itoa(len(s)))
		b.WriteByte(':')
		b.WriteString(s)
	}
	field(p.root)
	field(p.step)
	for _, k := range p.keys {
		if k.optional {
			b.WriteByte('o')
		} else {
			b.WriteByte('n')
		}
		field(k.key)
	}
	return b.String()
}

// jsVal is a JavaScript value read from JSON: nil is undefined, jsxNull
// null, and the rest as encoding/json decodes them (string, float64, bool,
// []any, map[string]any).
type jsVal = any

type jsxNullT struct{}

// jsRaw is an object or an array a path read, kept as the member path
// walker built it, so that a result that is one is written in JavaScript's
// key order (appendJSON); an operator converts it first (norm).
type jsRaw struct {
	v *jsValue
	// size bounds the value's JSON text (its root's text), for jsWork.
	size int
}

// norm converts a jsRaw for an operator.
func norm(v jsVal) jsVal {
	if r, ok := v.(jsRaw); ok {
		return fromValue(r.v)
	}
	return v
}

var jsxNull = jsxNullT{}

// parseJSExpr returns e as a jsExpr, or nil.
func parseJSExpr(e *Expr) *jsExpr {
	if e.Mode != ModeExpression || e.RewritePanic != 0 || slices.Contains(e.Binds, "error") {
		return nil
	}
	t := e.Trimmed
	if strings.ContainsAny(t, "\n\r") || strings.Contains(t, "//") || strings.Contains(t, "/*") {
		return nil
	}
	// QuickJS rejects a source that is not UTF-8 (a SyntaxError), where a
	// Go string would carry the bytes as U+FFFD.
	if !utf8.ValidString(e.Text) || !utf8.ValidString(t) {
		return nil
	}
	if code, _ := rewriteCalls(t); code != t {
		return nil
	}
	if len(t) > jsMaxText {
		return nil
	}
	p := &jsParser{t: t, matches: resultsRead.FindAllStringIndex(t, -1)}
	n, ok := p.cond()
	p.space()
	// Every match of JE-12's rewrite must be a results root this parser
	// read, spelled as the rewrite reads it: a match anywhere else (in a
	// string, in another identifier) changes the program's text.
	if ok && p.used != len(p.matches) {
		return nil
	}
	if !ok || p.i != len(t) || n.op == "lit" {
		// A literal alone gains nothing. A path alone reaches jsExpr only
		// where the member path shortcuts declined it: a read through the
		// results lookup, or a string's length.
		return nil
	}
	return &jsExpr{n: n}
}

// memoResults is a Results that keeps what each lookup returned, so that
// an evaluation that jsExpr declines after reading a step's result reads it
// once: the engine's lookup gets the same answer without a second read.
type memoResults struct {
	Results
	got  map[string]memoRead
	used bool
}

type memoRead struct {
	v   jsonval.Value
	err error
}

func (m *memoResults) Lookup(ctx context.Context, id string) (jsonval.Value, error) {
	if r, ok := m.got[id]; ok {
		return r.v, r.err
	}
	v, err := m.Results.Lookup(ctx, id)
	if err != nil && ctx.Err() != nil {
		// A read the caller's time limit cut short is not an answer.
		return v, err
	}
	if m.got == nil {
		m.got = map[string]memoRead{}
	}
	m.got[id], m.used = memoRead{v, err}, true
	return v, err
}

type jsParser struct {
	t string
	i int
	// depth is the nesting so far (cond), held under jsMaxDepth.
	depth int
	// matches are JE-12's rewrite matches in t, and used how many of them
	// the parser read as results roots.
	matches [][]int
	used    int
}

func (p *jsParser) space() {
	for p.i < len(p.t) && (p.t[p.i] == ' ' || p.t[p.i] == '\t') {
		p.i++
	}
}

func (p *jsParser) peek(s string) bool {
	p.space()
	return strings.HasPrefix(p.t[p.i:], s)
}

func (p *jsParser) take(s string) bool {
	if p.peek(s) {
		p.i += len(s)
		return true
	}
	return false
}

func (p *jsParser) cond() (jsNode, bool) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > jsMaxDepth {
		return jsNode{}, false
	}
	c, ok := p.or()
	if !ok {
		return c, false
	}
	if p.peek("?") && !p.peek("?.") && !p.peek("??") {
		p.i++
		a, ok := p.cond()
		if !ok || !p.take(":") {
			return c, false
		}
		b, ok := p.cond()
		if !ok {
			return c, false
		}
		return jsNode{op: "?:", kids: []jsNode{c, a, b}}, true
	}
	return c, true
}

// binary parses a left-associative level of ops over next.
func (p *jsParser) binary(next func() (jsNode, bool), ops ...string) (jsNode, bool) {
	l, ok := next()
	if !ok {
		return l, false
	}
	for {
		var op string
		for _, o := range ops {
			if p.peek(o) {
				op = o
				break
			}
		}
		if op == "" {
			return l, true
		}
		// ++ and -- are other tokens.
		if (op == "+" || op == "-") && strings.HasPrefix(p.t[p.i+1:], op) {
			return l, false
		}
		p.i += len(op)
		r, ok := next()
		if !ok {
			return l, false
		}
		l = jsNode{op: op, kids: []jsNode{l, r}}
	}
}

func (p *jsParser) or() (jsNode, bool)  { return p.binary(p.and, "||") }
func (p *jsParser) and() (jsNode, bool) { return p.binary(p.eq, "&&") }
func (p *jsParser) eq() (jsNode, bool)  { return p.binary(p.rel, "===", "!==") }
func (p *jsParser) rel() (jsNode, bool) { return p.binary(p.add, "<=", ">=", "<", ">") }
func (p *jsParser) add() (jsNode, bool) { return p.binary(p.mul, "+", "-") }
func (p *jsParser) mul() (jsNode, bool) { return p.binary(p.unary, "*", "/", "%") }

func (p *jsParser) unary() (jsNode, bool) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > jsMaxDepth {
		return jsNode{}, false
	}
	switch {
	case p.peek("!") && !p.peek("!="):
		p.i++
		n, ok := p.unary()
		return jsNode{op: "!", kids: []jsNode{n}}, ok
	case p.peek("-") && !p.peek("--"):
		p.i++
		n, ok := p.unary()
		return jsNode{op: "neg", kids: []jsNode{n}}, ok
	}
	return p.primary()
}

func (p *jsParser) primary() (jsNode, bool) {
	p.space()
	if p.i >= len(p.t) {
		return jsNode{}, false
	}
	c := p.t[p.i]
	switch {
	case c == '(':
		p.i++
		n, ok := p.cond()
		if !ok || !p.take(")") {
			return n, false
		}
		return p.length(n)
	case c == '`':
		return p.template()
	case c == '"' || c == '\'':
		s, ok := p.str(c)
		if !ok {
			return jsNode{}, false
		}
		return p.length(jsNode{op: "lit", lit: s})
	case c >= '0' && c <= '9' || c == '.':
		return p.number()
	}
	id := p.t[p.i:]
	id = id[:identPrefix(id)]
	if id == "" {
		return jsNode{}, false
	}
	if id == "Boolean" && strings.HasPrefix(strings.TrimLeft(p.t[p.i+len(id):], " \t"), "(") {
		// The predicate sites' wrapper (JE-24): ToBoolean of its argument.
		p.i += len(id)
		p.take("(")
		n, ok := p.cond()
		if !ok || !p.take(")") {
			return n, false
		}
		return jsNode{op: "bool", kids: []jsNode{n}}, true
	}
	switch id {
	case "true", "false":
		p.i += len(id)
		return jsNode{op: "lit", lit: id == "true"}, true
	case "null":
		p.i += len(id)
		return jsNode{op: "lit", lit: jsxNull}, true
	case "undefined":
		p.i += len(id)
		return jsNode{op: "lit", lit: nil}, true
	}
	return p.pathNode()
}

// length reads a trailing .length of a parenthesised term or a literal.
func (p *jsParser) length(n jsNode) (jsNode, bool) {
	for p.peek(".length") && identPrefix(p.t[p.i+1:]) == len("length") {
		p.i += len(".length")
		n = jsNode{op: "len", kids: []jsNode{n}}
	}
	return n, true
}

func (p *jsParser) number() (jsNode, bool) {
	j := p.i
	for j < len(p.t) && (p.t[j] >= '0' && p.t[j] <= '9' || p.t[j] == '.') {
		j++
	}
	if j < len(p.t) && (p.t[j] == 'e' || p.t[j] == 'E') {
		j++
		if j < len(p.t) && (p.t[j] == '+' || p.t[j] == '-') {
			j++
		}
		for j < len(p.t) && p.t[j] >= '0' && p.t[j] <= '9' {
			j++
		}
	}
	text := p.t[p.i:j]
	// A leading zero (octal), a second dot, an identifier after the
	// number: the engine's.
	if j < len(p.t) && (identPrefix(p.t[j:]) > 0 || p.t[j] == '_') || len(text) > 1 && text[0] == '0' && text[1] != '.' || strings.Count(text, ".") > 1 || text == "." {
		return jsNode{}, false
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return jsNode{}, false
	}
	p.i = j
	return jsNode{op: "lit", lit: f}, true
}

// str reads a quoted string with the escapes \n, \t, \r, \\, \', \" and
// \`; any other escape is the engine's.
func (p *jsParser) str(q byte) (string, bool) {
	var b strings.Builder
	for j := p.i + 1; j < len(p.t); j++ {
		c := p.t[j]
		switch {
		case c == q:
			p.i = j + 1
			return b.String(), true
		case c == '\\':
			if j+1 >= len(p.t) {
				return "", false
			}
			j++
			switch p.t[j] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '\\', '\'', '"', '`':
				b.WriteByte(p.t[j])
			default:
				return "", false
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", false
}

func (p *jsParser) template() (jsNode, bool) {
	n := jsNode{op: "tmpl"}
	var b strings.Builder
	for j := p.i + 1; j < len(p.t); j++ {
		c := p.t[j]
		switch {
		case c == '`':
			n.strs = append(n.strs, b.String())
			p.i = j + 1
			return p.length(n)
		case c == '$' && j+1 < len(p.t) && p.t[j+1] == '{':
			n.strs = append(n.strs, b.String())
			b.Reset()
			p.i = j + 2
			k, ok := p.cond()
			if !ok || !p.take("}") {
				return n, false
			}
			n.kids = append(n.kids, k)
			j = p.i - 1
		case c == '\\':
			if j+1 >= len(p.t) {
				return n, false
			}
			j++
			switch p.t[j] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case '\\', '`', '$', '\'', '"':
				b.WriteByte(p.t[j])
			default:
				return n, false
			}
		default:
			b.WriteByte(c)
		}
	}
	return n, false
}

// pathNode reads a member path: a root identifier, or results.<id> (or
// results?.<id>, results["<id>"]), then .key, ?.key, [n], ["k"] or ['k'].
func (p *jsParser) pathNode() (jsNode, bool) {
	id := p.t[p.i:]
	id = id[:identPrefix(id)]
	start := p.i
	p.i += len(id)
	path := &jsPath{root: id}
	if id == "results" {
		path.root = ""
		k, ok := p.key()
		if !ok || k.key == "" {
			return jsNode{}, false
		}
		// The read must be JE-12's rewrite match, exactly.
		if p.used >= len(p.matches) || p.matches[p.used][0] != start || p.matches[p.used][1] != p.i {
			return jsNode{}, false
		}
		p.used++
		path.step = k.key
	}
	for {
		save := p.i
		k, ok := p.key()
		if !ok {
			p.i = save
			break
		}
		if k.key == "__proto__" {
			return jsNode{}, false
		}
		path.keys = append(path.keys, k)
	}
	return jsNode{op: "path", path: path}, true
}

// key reads one member access.
func (p *jsParser) key() (jsKey, bool) {
	p.space()
	rest := p.t[p.i:]
	switch {
	case strings.HasPrefix(rest, "?.[") || strings.HasPrefix(rest, "?.") && len(rest) > 2 && rest[2] >= '0' && rest[2] <= '9':
		return jsKey{}, false
	case strings.HasPrefix(rest, "?."):
		n := identPrefix(rest[2:])
		if n == 0 {
			return jsKey{}, false
		}
		p.i += 2 + n
		return jsKey{key: rest[2 : 2+n], optional: true}, true
	case strings.HasPrefix(rest, "."):
		n := identPrefix(rest[1:])
		if n == 0 {
			return jsKey{}, false
		}
		p.i += 1 + n
		return jsKey{key: rest[1 : 1+n]}, true
	case strings.HasPrefix(rest, "["):
		p.i++
		p.space()
		if p.i < len(p.t) && (p.t[p.i] == '"' || p.t[p.i] == '\'') {
			s, ok := p.str(p.t[p.i])
			if !ok || !p.take("]") {
				return jsKey{}, false
			}
			return jsKey{key: s}, true
		}
		j := p.i
		for j < len(p.t) && p.t[j] >= '0' && p.t[j] <= '9' {
			j++
		}
		text := p.t[p.i:j]
		if text == "" || len(text) > 1 && text[0] == '0' {
			return jsKey{}, false
		}
		p.i = j
		if !p.take("]") {
			return jsKey{}, false
		}
		return jsKey{key: text}, true
	}
	return jsKey{}, false
}

// jsDecline is the evaluation's way out: the engine answers.
type jsDecline struct{}

// The evaluation limits jsExpr keeps. It answers only where the engine
// certainly would not meet them, and leaves everything else to the engine,
// which then meets them as it always does:
//
//   - JE-3, the heap cap: jsBudget adds up an upper bound of what the same
//     evaluation costs QuickJS (a fresh context, every bound and looked-up
//     JSON text as a string and parsed, every string the expression makes,
//     the result's serialisation) and declines past the cap;
//   - JE-48, the stack: the parser declines a text nested deeper than
//     jsMaxDepth or longer than jsMaxText, far inside what QuickJS's 1 MiB
//     stack parses;
//   - JE-2, the attempt limit: jsExpr runs only when the limit is at least
//     jsMinAttempt, and reads results only through a lookup that reads memory
//     (MemoryResults), which never waits. Its own work is bounded before it
//     is done: every scan of a JSON text, every walk of a path, every reuse
//     of a value read and every string built is charged by its size in bytes
//     against jsMaxWork before it runs, and an evaluation declines at once
//     when the charge would pass it; identical reads are walked once. Every
//     node also checks the clock and the context, and declines once its work
//     has taken a hundredth of the limit. So jsExpr hands an expression to
//     the engine after at most jsMaxWork bytes of work and at most a
//     hundredth of the limit, and the engine's own attempt then follows, as
//     it does after the member paths: the outcome cannot change, only the
//     latency, by that much.
//
// Why each heap constant is an upper bound (TestJSExprKeepsTheHeapCap holds
// them to the engine: across caps of 2, 4 and 8 MiB and the shapes below,
// jsExpr never answers what the engine fails):
//
//   - jsBaseline, 1 MiB: a fresh runtime and context with the context and
//     results preludes and a template over them evaluates under a 1 MiB cap,
//     the smallest the engine takes (measured on this QuickJS-ng).
//   - jsParseFactor + jsStringFactor, 66 bytes per byte of JSON text: the
//     smallest cap under which the engine binds a 1 MiB value and reads its
//     length is, per byte of text, 45 bytes for an array of empty objects
//     and 40 for an array of empty arrays, the costliest shapes measured,
//     then 22 (short strings), 18 (one-key objects), 14 (distinct keys), 12
//     (numbers) and 3 (one long string, either byte width); each figure holds
//     the bound string, its parse and the context together.
//   - jsStringFactor and jsStringHeader: a string the expression makes takes
//     at most 2 bytes per character (UTF-16; Latin-1 strings take 1) and a
//     header under 64 bytes.
const (
	jsMaxDepth = 32
	jsMaxText  = 16 << 10
	// jsBaseline bounds a fresh runtime and context with the preludes and
	// the program; jsParseFactor bounds QuickJS's heap per byte of JSON text
	// it parses; jsStringFactor is a string's bytes per character (UTF-16 at
	// worst), plus jsStringHeader each. See above for the measurements.
	jsBaseline     = 1 << 20
	jsParseFactor  = 64
	jsStringFactor = 2
	jsStringHeader = 64
	// jsMinAttempt is the shortest attempt limit jsExpr runs under, so that
	// a hundredth of it is at least 50 ms.
	jsMinAttempt = 5 * time.Second
	// jsMaxWork bounds the bytes an evaluation scans, walks, reuses and
	// builds: a few milliseconds of work.
	jsMaxWork = 4 << 20
	// jsWorkPerOp is charged per read or string built besides its bytes, so
	// that many small operations count too.
	jsWorkPerOp = 64
)

// MemoryResults marks a Results whose Lookup reads memory under a short
// lock and never waits on anything else: jsExpr reads results through the
// lookup only on one (the engine's frame results, internal/engine), so that
// no read can run into the attempt limit (JE-2): such a read takes as long
// as a map read under a mutex, which bounds it far under a hundredth of
// jsMinAttempt. Any other Results (a database read, a remote store) is read
// by the engine only, under its own attempt limit.
type MemoryResults interface {
	MemoryLookup()
}

// jsBudget is the upper bound so far of what an evaluation costs QuickJS's
// heap, against the cap.
type jsBudget struct {
	cap, used int64
}

func (b *jsBudget) add(n int64) {
	b.used += n
	if b.used > b.cap {
		decline()
	}
}

// text counts a JSON text the evaluation binds or reads: the string, and
// what parsing it builds.
func (b *jsBudget) text(n int) {
	b.add(int64(n) * (jsStringFactor + jsParseFactor))
}

// str counts a string the evaluation makes.
func (b *jsBudget) str(s string) {
	b.add(int64(len(s))*jsStringFactor + jsStringHeader)
}

// jsLimits are the limits eval keeps: the evaluation's (expr.Limits), the
// JSON texts the engine would bind (Input.Strings), and whether the results
// lookup reads memory.
type jsLimits struct {
	limits      Limits
	bound       []HostString
	memoryReads bool
}

// eval answers x over s's bindings: the result text, as the engine's
// JSON.stringify would give it, and true; or false where it declines,
// including wherever the engine could meet a limit (jsLimits).
func (x *jsExpr) eval(ctx context.Context, s *Scope, lim jsLimits) (out []byte, ok bool) {
	if lim.limits.Attempt < jsMinAttempt {
		return nil, false
	}
	start := time.Now()
	deadline := start.Add(lim.limits.Attempt / 100)
	defer func() {
		if r := recover(); r != nil {
			if _, d := r.(jsDecline); !d {
				panic(r)
			}
			out, ok = nil, false
		}
		if ok && time.Since(start) > lim.limits.Attempt/100 {
			out, ok = nil, false
		}
	}()
	ev := &jsEval{ctx: ctx, s: s, roots: map[string][]byte{}, reads: map[string]jsVal{}, memoryReads: lim.memoryReads, deadline: deadline}
	ev.budget = jsBudget{cap: int64(lim.limits.MemoryMB) << 20}
	ev.budget.add(jsBaseline)
	for _, hs := range lim.bound {
		ev.budget.text(len(hs.Text))
	}
	v := ev.node(x.n)
	ev.charge(valueSize(v)) // the serialisation
	out = jsStringify(v)
	ev.budget.add(int64(len(out)) * jsStringFactor)
	return out, true
}

type jsEval struct {
	ctx         context.Context
	s           *Scope
	roots       map[string][]byte // each root's checked JSON text
	reads       map[string]jsVal  // each path read so far, by its spelling
	memoryReads bool
	budget      jsBudget
	work        int64
	deadline    time.Time
}

func decline() { panic(jsDecline{}) }

// charge counts n bytes of work about to be done (jsMaxWork), and checks the
// clock and the context; it declines rather than let the work begin past
// either bound.
func (ev *jsEval) charge(n int) {
	ev.work += int64(n) + jsWorkPerOp
	if ev.work > jsMaxWork {
		decline()
	}
	ev.tick()
}

// tick declines once the evaluation has used its share of the attempt limit
// or its context is done.
func (ev *jsEval) tick() {
	if time.Now().After(ev.deadline) || ev.ctx.Err() != nil {
		decline()
	}
}

// valueSize bounds the bytes an operation on v touches.
func valueSize(v jsVal) int {
	switch t := v.(type) {
	case string:
		return len(t)
	case jsRaw:
		return t.size
	}
	return 8
}

// fromValue converts the member path walker's value to a jsVal.
func fromValue(v *jsValue) jsVal {
	switch v.kind {
	case jsNull:
		return jsxNull
	case jsBool:
		return v.b
	case jsNumber:
		return v.n
	case jsString:
		return v.s
	case jsArray:
		a := make([]any, len(v.arr))
		for i, e := range v.arr {
			a[i] = fromValue(e)
		}
		return a
	}
	keys, vals := v.obj.members()
	m := make(map[string]any, len(keys))
	for i, k := range keys {
		m[k] = fromValue(vals[i])
	}
	return m
}

// root returns the checked JSON text of p's root.
func (ev *jsEval) root(p *jsPath) []byte {
	key := p.root
	if key == "" {
		key = "results." + p.step
	}
	if v, ok := ev.roots[key]; ok {
		return v
	}
	var text []byte
	s := ev.s
	switch {
	case p.root == "flow_input":
		text = textOr(s.FlowInput, jsonNullText)
	case p.root == "flow_env":
		text = textOr(s.FlowEnv, []byte("{}"))
	case p.root != "":
		v, ok := s.context(p.root)
		if !ok {
			decline()
		}
		text = v.Producer()
	default:
		if s.Results == nil {
			decline()
		}
		if prev, ok := s.context("previous_result"); ok && p.step == s.PreviousID && s.PreviousID != "" {
			// JE-18 rule 1: the previous step's result, from memory.
			text = prev.Producer()
			break
		}
		if !ev.memoryReads {
			// A lookup that could wait is the engine's, under its attempt
			// limit (JE-2).
			decline()
		}
		v, err := s.Results.Lookup(ev.ctx, p.step)
		if err != nil {
			decline()
		}
		if v.IsZero() {
			text = jsonNullText
		} else if text, err = v.Stored(); err != nil {
			decline()
		}
		// The results prelude parses what the lookup answers.
		ev.budget.text(len(text))
	}
	// A text JSON.parse rejects, or that JE-16's rebuild would change,
	// is the engine's.
	ev.charge(2 * len(text)) // validJSON and mayHoldProto scan it
	if !validJSON(text) || mayHoldProto(text) {
		decline()
	}
	ev.roots[key] = text
	return text
}

// read follows p through its root's text as the member path walker does,
// building only the value it ends on; a string's length, which the walker
// leaves to the engine, is read here.
func (ev *jsEval) read(p *jsPath) jsVal {
	key := p.spelling()
	if v, ok := ev.reads[key]; ok {
		// Walked once; its use is charged again, by its size.
		ev.charge(valueSize(v))
		return v
	}
	v := ev.walk(p)
	ev.reads[key] = v
	return v
}

// walk is read's first walk of p.
func (ev *jsEval) walk(p *jsPath) jsVal {
	text := ev.root(p)
	steps := make([]pathStep, len(p.keys))
	for i, k := range p.keys {
		steps[i] = pathStep{key: k.key, optional: k.optional}
	}
	ev.charge(len(text))
	if v, undefined, ok := pathValue(text, steps); ok {
		if undefined {
			return nil
		}
		if v.kind == jsArray || v.kind == jsObjectKind {
			return jsRaw{v, len(text)}
		}
		return fromValue(v)
	}
	n := len(steps)
	if n == 0 || steps[n-1].key != "length" {
		decline()
	}
	ev.charge(len(text))
	v, undefined, ok := pathValue(text, steps[:n-1])
	if !ok || undefined || v.kind != jsString {
		decline()
	}
	ev.charge(len(v.s))
	return float64(utf16Len(v.s))
}

var jsonNullText = []byte("null")

func (ev *jsEval) node(n jsNode) jsVal {
	ev.tick()
	switch n.op {
	case "lit":
		return n.lit
	case "path":
		return ev.read(n.path)
	case "len":
		v := ev.node(n.kids[0])
		ev.charge(valueSize(v))
		return jsMember(norm(v), "length", false)
	case "bool":
		return jsTruthy(ev.node(n.kids[0]))
	case "tmpl":
		var b strings.Builder
		for i, k := range n.kids {
			b.WriteString(n.strs[i])
			kv := ev.node(k)
			ev.charge(valueSize(kv) + b.Len()) // the conversion and the copy
			part := jsToString(norm(kv))
			ev.budget.str(part)
			b.WriteString(part)
		}
		b.WriteString(n.strs[len(n.strs)-1])
		ev.budget.str(b.String())
		return b.String()
	case "!":
		return !jsTruthy(ev.node(n.kids[0]))
	case "neg":
		v := ev.node(n.kids[0])
		ev.charge(valueSize(v))
		return -jsToNumber(norm(v))
	case "&&":
		l := ev.node(n.kids[0])
		if !jsTruthy(l) {
			return l
		}
		return ev.node(n.kids[1])
	case "||":
		l := ev.node(n.kids[0])
		if jsTruthy(l) {
			return l
		}
		return ev.node(n.kids[1])
	case "?:":
		if jsTruthy(ev.node(n.kids[0])) {
			return ev.node(n.kids[1])
		}
		return ev.node(n.kids[2])
	}
	lv, rv := ev.node(n.kids[0]), ev.node(n.kids[1])
	// The operator touches both operands (a comparison, a conversion, a
	// concatenation's copy).
	ev.charge(valueSize(lv) + valueSize(rv))
	l, r := norm(lv), norm(rv)
	switch n.op {
	case "+":
		lp, rp := jsToPrimitive(l), jsToPrimitive(r)
		_, ls := lp.(string)
		_, rs := rp.(string)
		if ls || rs {
			a, c := jsToString(lp), jsToString(rp)
			ev.budget.str(a)
			ev.budget.str(c)
			ev.budget.str(a + c)
			return a + c
		}
		return jsToNumber(lp) + jsToNumber(rp)
	case "-":
		return jsToNumber(l) - jsToNumber(r)
	case "*":
		return jsToNumber(l) * jsToNumber(r)
	case "/":
		return jsToNumber(l) / jsToNumber(r)
	case "%":
		return math.Mod(jsToNumber(l), jsToNumber(r))
	case "===", "!==":
		eq := jsStrictEqual(l, r)
		if n.op == "!==" {
			return !eq
		}
		return eq
	case "<", "<=", ">", ">=":
		return jsCompare(n.op, l, r)
	}
	decline()
	return nil
}

// jsMember reads key of v as a property read on a JSON value does.
func jsMember(v jsVal, key string, optional bool) jsVal {
	switch t := v.(type) {
	case nil, jsxNullT:
		if optional {
			return nil
		}
		decline() // a TypeError, with the engine's text
	case map[string]any:
		e, ok := t[key]
		if !ok {
			// An inherited property (toString, constructor, ...) is not data.
			if jsObjectProtoKey(key) {
				decline()
			}
			return nil
		}
		return e
	case []any:
		if key == "length" {
			return float64(len(t))
		}
		i, err := strconv.Atoi(key)
		if err != nil || strconv.Itoa(i) != key {
			if jsArrayProtoKey(key) {
				decline()
			}
			return nil
		}
		if i < len(t) {
			return t[i]
		}
		return nil
	case string:
		if key == "length" {
			return float64(utf16Len(t))
		}
		decline() // String.prototype's members, and indexes by code unit
	}
	decline()
	return nil
}

func jsObjectProtoKey(k string) bool {
	switch k {
	case "constructor", "toString", "toLocaleString", "valueOf", "hasOwnProperty", "isPrototypeOf", "propertyIsEnumerable", "__defineGetter__", "__defineSetter__", "__lookupGetter__", "__lookupSetter__", "__proto__":
		return true
	}
	return false
}

func jsArrayProtoKey(k string) bool {
	if jsObjectProtoKey(k) {
		return true
	}
	// Array.prototype has many members; every identifier-like key that is
	// not an index is left to the engine.
	return identPrefix(k) == len(k)
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func jsTruthy(v jsVal) bool {
	switch t := v.(type) {
	case nil, jsxNullT:
		return false
	case bool:
		return t
	case float64:
		return t != 0 && !math.IsNaN(t)
	case string:
		return t != ""
	}
	return true
}

// jsToPrimitive converts an object or an array to its string form, as
// ToPrimitive with no hint does through toString; an object that holds its
// own toString or valueOf would change that, so it declines.
func jsToPrimitive(v jsVal) jsVal {
	switch t := v.(type) {
	case map[string]any:
		if _, ok := t["toString"]; ok {
			decline()
		}
		if _, ok := t["valueOf"]; ok {
			decline()
		}
		return "[object Object]"
	case []any:
		return jsJoin(t)
	}
	return v
}

func jsJoin(a []any) string {
	var b strings.Builder
	for i, e := range a {
		if i > 0 {
			b.WriteByte(',')
		}
		switch e.(type) {
		case nil, jsxNullT:
		default:
			b.WriteString(jsToString(e))
		}
	}
	return b.String()
}

func jsToString(v jsVal) string {
	switch t := v.(type) {
	case nil:
		return "undefined"
	case jsxNullT:
		return "null"
	case bool:
		if t {
			return "true"
		}
		return "false"
	case string:
		return t
	case float64:
		return jsNumberString(t)
	}
	p := jsToPrimitive(v)
	if s, ok := p.(string); ok {
		return s
	}
	return jsToString(p)
}

// jsToNumber converts a primitive other than a string to a number; a string
// or an object (whose conversion parses a string) declines.
func jsToNumber(v jsVal) float64 {
	switch t := v.(type) {
	case nil:
		return math.NaN()
	case jsxNullT:
		return 0
	case bool:
		if t {
			return 1
		}
		return 0
	case float64:
		return t
	}
	decline()
	return 0
}

func jsStrictEqual(l, r jsVal) bool {
	switch a := l.(type) {
	case nil:
		return r == nil
	case jsxNullT:
		_, ok := r.(jsxNullT)
		return ok
	case bool:
		b, ok := r.(bool)
		return ok && a == b
	case float64:
		b, ok := r.(float64)
		return ok && a == b
	case string:
		b, ok := r.(string)
		return ok && a == b
	}
	// Objects compare by identity, which reads of JSON values decide.
	switch r.(type) {
	case nil, jsxNullT, bool, float64, string:
		return false
	}
	decline()
	return false
}

func jsCompare(op string, l, r jsVal) bool {
	ls, lok := l.(string)
	rs, rok := r.(string)
	if lok && rok {
		c := compareUTF16(ls, rs)
		switch op {
		case "<":
			return c < 0
		case "<=":
			return c <= 0
		case ">":
			return c > 0
		}
		return c >= 0
	}
	if lok || rok {
		decline()
	}
	a, b := jsToNumber(l), jsToNumber(r)
	switch op {
	case "<":
		return a < b
	case "<=":
		return a <= b
	case ">":
		return a > b
	}
	return a >= b
}

func compareUTF16(a, b string) int {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			if ua[i] < ub[i] {
				return -1
			}
			return 1
		}
	}
	return len(ua) - len(ub)
}

// jsNumberString is ECMAScript's Number::toString(10).
func jsNumberString(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case f == 0:
		return "0"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f < 0:
		return "-" + jsNumberString(-f)
	}
	// The shortest digits that read back as f, and the exponent n with
	// f = 0.digits × 10^n.
	e := strconv.FormatFloat(f, 'e', -1, 64)
	mant, exp, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	x, _ := strconv.Atoi(exp)
	n, k := x+1, len(digits)
	switch {
	case k <= n && n <= 21:
		return digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		return "0." + strings.Repeat("0", -n) + digits
	}
	sign := "+"
	if n-1 < 0 {
		sign = "-"
	}
	ex := strconv.Itoa(abs(n - 1))
	if k == 1 {
		return digits + "e" + sign + ex
	}
	return digits[:1] + "." + digits[1:] + "e" + sign + ex
}

func abs(i int) int {
	if i < 0 {
		return -i
	}
	return i
}

// jsStringify is JSON.stringify of a primitive result; an object or an
// array declines (its key order and text are the existing shortcuts' and
// the engine's to give).
func jsStringify(v jsVal) []byte {
	switch t := v.(type) {
	case jsRaw:
		return t.v.appendJSON(nil)
	case nil:
		// JSON.stringify(undefined) is not a string: the pipeline reads it
		// as null.
		return jsonNullText
	case jsxNullT:
		return jsonNullText
	case bool:
		if t {
			return []byte("true")
		}
		return []byte("false")
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return jsonNullText
		}
		return []byte(jsNumberString(t))
	case string:
		return jsQuote(t)
	}
	decline()
	return nil
}

func jsQuote(s string) []byte {
	b := []byte{'"'}
	for _, r := range s {
		switch r {
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
			if r < 0x20 {
				b = append(b, []byte(`\u00`)...)
				b = append(b, "0123456789abcdef"[r>>4], "0123456789abcdef"[r&15])
			} else {
				b = utf8.AppendRune(b, r)
			}
		}
	}
	return append(b, '"')
}
