package expr

import (
	"slices"
	"strings"
)

// pathExpr is an expression that only reads member paths of one JSON
// value and compares them with null or undefined (milestone PS2, lever
// J3):
//
//   - a member path, as memberPath reads it;
//   - a null test: a member path compared with null or undefined by ==,
//     !=, === or !==, alone (true or false) or choosing between two terms
//     with ?:, each term a member path, null or undefined;
//   - an object literal, in parentheses, whose members' values are member
//     paths, each key an identifier or a quoted string.
//
// Every member path reads the same root: one context value, or one step's
// result read as results.<id>. Nothing else runs in such an expression:
// no call, no assignment, no comparison of two values that could be
// objects, so it cannot tell whether two reads of the root gave one value
// or two equal ones, nor replace JSON.parse before a read.
type pathExpr struct {
	// root and step are the paths' common root, as memberPath has them.
	root, step string
	// reads is the number of results reads the text makes (JE-12).
	reads int

	kind  pathExprKind
	path  *memberPath // pathSingle, and the tested path of a test
	op    string      // ==, !=, === or !==
	lit   string      // null or undefined, what the path is compared with
	then  pathTerm    // pathCond
	other pathTerm    // pathCond
	keys  []string    // pathObject, with vals
	vals  []*memberPath
}

type pathExprKind uint8

const (
	pathSingle pathExprKind = iota
	pathTest
	pathCond
	pathObject
)

// pathTerm is a branch of a conditional: a member path, or the literal
// null or undefined.
type pathTerm struct {
	path *memberPath
	lit  string
}

// parsePathExpr returns e as a pathExpr, or nil for anything else. Like
// parseMemberPath, it leaves to the engine a text that JE-11 rewrites, one
// that binds error, and one whose results reads JE-12 would rewrite other
// than as the roots it reads.
func parsePathExpr(e *Expr) *pathExpr {
	if e.Mode != ModeExpression || e.RewritePanic != 0 || slices.Contains(e.Binds, "error") {
		return nil
	}
	t := e.Trimmed
	if code, _ := rewriteCalls(t); code != t {
		return nil
	}
	ps := &pathParser{t: t, matches: resultsRead.FindAllStringIndex(t, -1)}
	x := ps.expr()
	if x == nil || ps.i != len(t) || ps.used != len(ps.matches) {
		return nil
	}
	x.reads = ps.used
	return x
}

// pathParser reads a pathExpr's text.
type pathParser struct {
	t       string
	i       int
	matches [][]int
	// used counts the results reads consumed as roots, in order.
	used int
	// root and step are the first path's root; every later path must
	// share it.
	rooted     bool
	root, step string
}

func (ps *pathParser) space() {
	for ps.i < len(ps.t) && strings.IndexByte(" \t\n\r", ps.t[ps.i]) >= 0 {
		ps.i++
	}
}

func (ps *pathParser) lit(w string) bool {
	if strings.HasPrefix(ps.t[ps.i:], w) {
		ps.i += len(w)
		return true
	}
	return false
}

// word reads the identifier w, not followed by another identifier
// character.
func (ps *pathParser) word(w string) bool {
	rest := ps.t[ps.i:]
	if !strings.HasPrefix(rest, w) || identPrefix(rest) != len(w) {
		return false
	}
	ps.i += len(w)
	return true
}

func (ps *pathParser) expr() *pathExpr {
	if ps.lit("(") {
		return ps.object()
	}
	p := ps.path()
	if p == nil {
		return nil
	}
	x := &pathExpr{path: p}
	ps.space()
	if ps.i == len(ps.t) {
		x.kind = pathSingle
		return ps.done(x)
	}
	for _, op := range []string{"===", "!==", "==", "!="} {
		if ps.lit(op) {
			x.op = op
			break
		}
	}
	if x.op == "" {
		return nil
	}
	ps.space()
	switch {
	case ps.word("null"):
		x.lit = "null"
	case ps.word("undefined"):
		x.lit = "undefined"
	default:
		return nil
	}
	ps.space()
	if ps.i == len(ps.t) {
		x.kind = pathTest
		return ps.done(x)
	}
	// A ? followed by a dot would read null?.x or undefined?.x, or a
	// number such as .5; either way not a pathExpr.
	if !ps.lit("?") || strings.HasPrefix(ps.t[ps.i:], ".") {
		return nil
	}
	ps.space()
	var ok bool
	if x.then, ok = ps.term(); !ok {
		return nil
	}
	ps.space()
	if !ps.lit(":") {
		return nil
	}
	ps.space()
	if x.other, ok = ps.term(); !ok {
		return nil
	}
	ps.space()
	x.kind = pathCond
	return ps.done(x)
}

// object reads an object literal after its opening parenthesis.
func (ps *pathParser) object() *pathExpr {
	ps.space()
	if !ps.lit("{") {
		return nil
	}
	x := &pathExpr{kind: pathObject}
	for {
		ps.space()
		if ps.lit("}") {
			break
		}
		if ps.i == len(ps.t) {
			return nil
		}
		var key string
		switch rest := ps.t[ps.i:]; {
		case identPrefix(rest) > 0:
			key = rest[:identPrefix(rest)]
			ps.i += len(key)
		case rest[0] == '"' || rest[0] == '\'':
			end := strings.IndexByte(rest[1:], rest[0])
			if end < 0 || !plainString(rest[1:1+end], rest[0]) {
				return nil
			}
			key = rest[1 : 1+end]
			ps.i += end + 2
		default:
			return nil
		}
		// A __proto__ member sets the literal's prototype, not a property.
		if key == "__proto__" {
			return nil
		}
		ps.space()
		if !ps.lit(":") {
			return nil
		}
		ps.space()
		p := ps.path()
		if p == nil {
			return nil
		}
		x.keys, x.vals = append(x.keys, key), append(x.vals, p)
		ps.space()
		if !ps.lit(",") {
			ps.space()
			if !ps.lit("}") {
				return nil
			}
			break
		}
	}
	ps.space()
	if len(x.keys) == 0 || !ps.lit(")") {
		return nil
	}
	ps.space()
	return ps.done(x)
}

// done fills x's root once the text is read.
func (ps *pathParser) done(x *pathExpr) *pathExpr {
	x.root, x.step = ps.root, ps.step
	return x
}

func (ps *pathParser) term() (pathTerm, bool) {
	switch {
	case ps.word("null"):
		return pathTerm{lit: "null"}, true
	case ps.word("undefined"):
		return pathTerm{lit: "undefined"}, true
	}
	p := ps.path()
	return pathTerm{path: p}, p != nil
}

// path reads a member path: its root, which every path of the text
// shares, then its steps.
func (ps *pathParser) path() *memberPath {
	mp := &memberPath{}
	rest := ps.t[ps.i:]
	if strings.HasPrefix(rest, "results") {
		// The root must be the next of JE-12's rewrites, exactly.
		if ps.used >= len(ps.matches) || ps.matches[ps.used][0] != ps.i {
			return nil
		}
		root := ps.t[ps.i:ps.matches[ps.used][1]]
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
		ps.used++
		ps.i += len(root)
	} else {
		n := identPrefix(rest)
		if n == 0 || !slices.Contains(hostStringNames, rest[:n]) {
			return nil
		}
		mp.root = rest[:n]
		ps.i += n
	}
	if !ps.rooted {
		ps.rooted, ps.root, ps.step = true, mp.root, mp.step
	} else if mp.root != ps.root || mp.step != ps.step {
		return nil
	}
	steps, n, ok := parseSteps(ps.t[ps.i:])
	if !ok {
		return nil
	}
	mp.steps = steps
	ps.i += n
	return mp
}

// sharedRead returns the step id whose results reads one evaluation of x
// may share: x's root, when it is a step's result read through the
// results lookup more than once. Each read through the lookup fetches the
// result and parses it again (JE-18 rule 2, JE-16), so two reads give two
// equal values; x cannot tell them from one, so they may be one, fetched
// and parsed once.
func (x *pathExpr) sharedRead() string {
	if x == nil || x.root != "" || x.reads < 2 {
		return ""
	}
	return x.step
}
