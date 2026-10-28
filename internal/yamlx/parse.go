package yamlx

import (
	"bytes"
	"cmp"
	"math"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// maxAliasWeight bounds what aliases add to a document: each alias adds the
// weight of the value it repeats (one per node plus the bytes of its
// scalars and keys). The CLI has its own, different guard: it refuses a document
// in which one anchor is used 100 or more times. yamlx does not reproduce
// that guard; it only keeps an alias chain from expanding without bound.
const maxAliasWeight = 1 << 22

// yaml11Tags are the tags the CLI's parser resolves to JavaScript-specific
// values (a Buffer, a Date, a Set, a Map) or, for !!merge, drops.
var yaml11Tags = map[string]bool{
	"!!binary":    true,
	"!!timestamp": true,
	"!!set":       true,
	"!!omap":      true,
	"!!pairs":     true,
	"!!merge":     true,
}

type parser struct {
	src     []byte
	restore *strings.Replacer // puts back what prepare hid, or nil
	lines   []int             // byte offset of the start of each line

	done   map[*yaml.Node]*Node // converted anchored nodes, for aliases
	active map[*yaml.Node]bool  // anchored nodes being converted
	weight map[*Node]int64      // memoised expanded weights
	added  int64                // weight added by aliases so far
}

func (p *parser) node(n *yaml.Node) (*Node, error) {
	if n.Kind == yaml.AliasNode {
		if _, _, err := p.properties(n); err != nil {
			return nil, err
		}
		return p.alias(n)
	}
	if n.Anchor != "" {
		if _, _, err := p.properties(n); err != nil {
			return nil, err
		}
		p.active[n] = true
	}
	var (
		out *Node
		err error
	)
	switch n.Kind {
	case yaml.ScalarNode:
		out, err = p.scalar(n)
	case yaml.SequenceNode:
		out, err = p.sequence(n)
	case yaml.MappingNode:
		out, err = p.mapping(n)
	default:
		err = errorAt(Syntax, n, "unexpected node kind %d", n.Kind)
	}
	if err != nil {
		return nil, err
	}
	if n.Anchor != "" {
		delete(p.active, n)
		p.done[n] = out
	}
	return out, nil
}

func (p *parser) alias(n *yaml.Node) (*Node, error) {
	target := n.Alias
	if target == nil {
		return nil, errorAt(Syntax, n, "alias *%s has no anchor", n.Value)
	}
	if p.active[target] {
		return nil, errorAt(Syntax, n, "alias *%s refers to a value that contains it", n.Value)
	}
	out, ok := p.done[target]
	if !ok {
		return nil, errorAt(Syntax, n, "alias *%s refers to an anchor that is not defined before it", n.Value)
	}
	p.added += p.weigh(out)
	if p.added > maxAliasWeight {
		return nil, errorAt(Unsupported, n, "aliases add more than %d to the document's weight (nodes plus bytes of text)", maxAliasWeight)
	}
	return out, nil
}

// weigh returns the expanded weight of n, saturating above maxAliasWeight.
func (p *parser) weigh(n *Node) int64 {
	if w, ok := p.weight[n]; ok {
		return w
	}
	w := 1 + int64(len(n.Text))
	for _, it := range n.Items {
		w = min(w+p.weigh(it), maxAliasWeight+1)
	}
	for _, pr := range n.Pairs {
		w = min(w+int64(len(pr.Key))+p.weigh(pr.Value), maxAliasWeight+1)
	}
	p.weight[n] = w
	return w
}

// checkTag refuses the explicit tags yamlx cannot resolve as the CLI
// does. Any other tag on a collection, a scalar tag included, leaves the
// collection as it is: the CLI warns about the tag and keeps the
// collection.
func checkTag(n *yaml.Node) error {
	if n.Style&yaml.TaggedStyle == 0 {
		return nil
	}
	if yaml11Tags[n.Tag] {
		return errorAt(Unsupported, n, "tag %s: the CLI turns it into a JavaScript-specific value", n.Tag)
	}
	if strings.ContainsAny(n.Tag, ",[]{}") {
		// YAML 1.2 ends a tag at a flow indicator; the YAML 1.1 parser
		// reads the indicator as part of the tag.
		return errorAt(Unsupported, n, "tag %s contains a flow indicator", n.Tag)
	}
	return nil
}

func (p *parser) sequence(n *yaml.Node) (*Node, error) {
	if err := checkTag(n); err != nil {
		return nil, err
	}
	out := &Node{Kind: Sequence, Line: n.Line, Column: n.Column, Items: make([]*Node, 0, len(n.Content))}
	for _, c := range n.Content {
		it, err := p.node(c)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, it)
	}
	return out, nil
}

// keyIdentity is a key's resolved value, which decides whether two keys
// are duplicates: the CLI's parser refuses two scalar keys with equal
// values (so 1 and 0x1 collide, but 1 and "1" do not).
type keyIdentity struct {
	kind Kind
	text string
	num  float64
}

func (p *parser) mapping(n *yaml.Node) (*Node, error) {
	if err := checkTag(n); err != nil {
		return nil, err
	}
	if len(n.Content)%2 != 0 {
		return nil, errorAt(Syntax, n, "mapping has a key without a value")
	}
	out := &Node{Kind: Mapping, Line: n.Line, Column: n.Column}
	index := map[string]int{}
	seen := map[keyIdentity]bool{}
	for i := 0; i < len(n.Content); i += 2 {
		kn, vn := n.Content[i], n.Content[i+1]
		k, err := p.node(kn)
		if err != nil {
			return nil, err
		}
		if k.Kind == Sequence || k.Kind == Mapping {
			return nil, errorAt(Unsupported, kn, "a %s as a mapping key: the CLI turns it into YAML text", k.Kind)
		}
		// Alias keys are never duplicates in the CLI's parser, which
		// compares key nodes, not what an alias stands for.
		if kn.Kind != yaml.AliasNode {
			id := keyIdentity{kind: k.Kind, text: k.Text}
			if k.Kind == Number {
				id.text, id.num = "", k.num
				if id.num == 0 {
					id.num = 0 // -0 and 0 are equal
				}
			}
			if !(k.Kind == Number && math.IsNaN(k.num)) {
				if seen[id] {
					return nil, errorAt(Syntax, kn, "duplicate mapping key %s", strconv.Quote(propertyName(k)))
				}
				seen[id] = true
			}
		}
		v, err := p.node(vn)
		if err != nil {
			return nil, err
		}
		name := propertyName(k)
		if j, ok := index[name]; ok {
			// Distinct keys with one property name, such as 1 and "1":
			// the later value replaces the earlier one in place.
			out.Pairs[j].Value = v
			continue
		}
		index[name] = len(out.Pairs)
		out.Pairs = append(out.Pairs, Pair{Key: name, Value: v})
	}
	sortProperties(out.Pairs)
	return out, nil
}

// propertyName converts a scalar key as JavaScript's String() does.
func propertyName(k *Node) string {
	switch k.Kind {
	case Null:
		return ""
	case Number:
		return jsNumberString(k.num)
	default:
		return k.Text
	}
}

// sortProperties puts array-index keys first, in ascending numeric order,
// keeping the order of the others: the property order of a JavaScript
// object.
func sortProperties(pairs []Pair) {
	slices.SortStableFunc(pairs, func(a, b Pair) int {
		ai, aok := arrayIndex(a.Key)
		bi, bok := arrayIndex(b.Key)
		switch {
		case aok && bok:
			return cmp.Compare(ai, bi)
		case aok:
			return -1
		case bok:
			return 1
		}
		return 0
	})
}

// arrayIndex reports whether s is the canonical decimal form of an array
// index, an integer from 0 to 2^32-2.
func arrayIndex(s string) (uint64, bool) {
	if s == "" || len(s) > 10 || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	var v uint64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		v = v*10 + uint64(c-'0')
	}
	return v, v <= math.MaxUint32-1
}

func (p *parser) scalar(n *yaml.Node) (*Node, error) {
	out := &Node{Line: n.Line, Column: n.Column}
	v := n.Value
	if p.restore != nil {
		v = p.restore.Replace(v)
	}
	if n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 && v != "" && !strings.HasSuffix(v, "\n") {
		// A clipped or kept block scalar ends with a line break unless
		// its last line ends the file without one. The CLI's parser
		// treats the end of the file as that line break; the YAML 1.1
		// parser drops it.
		strip, err := p.stripChomping(n)
		if err != nil {
			return nil, err
		}
		if !strip {
			v += "\n"
		}
	}
	if n.Style&yaml.TaggedStyle == 0 {
		quoted := n.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle) != 0
		if quoted {
			out.Kind, out.Text = String, v
			return out, nil
		}
		resolvePlain(out, v)
		if out.Kind != String {
			// The library hides a non-specific tag "!", which makes a
			// plain scalar a string.
			bang, _, err := p.properties(n)
			if err != nil {
				return nil, err
			}
			if bang {
				out.Kind, out.Text, out.num = String, v, 0
			}
		}
		return out, nil
	}
	if err := checkTag(n); err != nil {
		return nil, err
	}
	resolveTagged(out, n.Tag, v)
	return out, nil
}

// stripChomping reports whether the header of the block scalar n has the
// strip indicator "-".
func (p *parser) stripChomping(n *yaml.Node) (bool, error) {
	_, s, err := p.properties(n)
	if err != nil {
		return false, err
	}
	if len(s) == 0 || (s[0] != '|' && s[0] != '>') {
		return false, errorAt(Unsupported, n, "cannot locate the block scalar header in the source")
	}
	for _, c := range s[1:] {
		switch {
		case c == '-':
			return true, nil
		case c != '+' && (c < '1' || c > '9'):
			return false, nil
		}
	}
	return false, nil
}

// properties reads the source of n's node properties, which the library
// reports only in part. It reports whether they include the non-specific
// tag "!", and returns the source from the node's content on. It refuses
// an anchor or alias name that YAML 1.2 would read as longer than the
// YAML 1.1 parser did: YAML 1.2 allows characters such as ':' and '.' in
// names, which the YAML 1.1 parser treats as the end of the name or
// refuses.
func (p *parser) properties(n *yaml.Node) (bang bool, rest []byte, err error) {
	off, ok := p.offset(n.Line, n.Column)
	if !ok {
		return false, nil, errorAt(Unsupported, n, "cannot locate the node in the source")
	}
	s := p.src[off:]
	if n.Kind == yaml.AliasNode {
		_, err := p.name(n, s, '*', n.Value)
		return false, nil, err
	}
	// A node has at most two properties, a tag and an anchor, in either
	// order.
	for range 2 {
		switch {
		case len(s) > 0 && s[0] == '&' && n.Anchor != "":
			if s, err = p.name(n, s, '&', n.Anchor); err != nil {
				return false, nil, err
			}
		case len(s) > 0 && s[0] == '!':
			end := 1
			for end < len(s) && !isSeparator(s[end]) {
				end++
			}
			bang = bang || end == 1
			s = s[end:]
		default:
			return bang, s, nil
		}
		s = skipSeparation(s)
	}
	return bang, s, nil
}

// name checks that s starts with sigil and name, followed by a character
// that ends a name in YAML 1.2 too, and returns the rest of s.
func (p *parser) name(n *yaml.Node, s []byte, sigil byte, name string) ([]byte, error) {
	if len(s) == 0 || s[0] != sigil || !bytes.HasPrefix(s[1:], []byte(name)) {
		return nil, errorAt(Unsupported, n, "cannot locate %c%s in the source", sigil, name)
	}
	rest := s[1+len(name):]
	if len(rest) > 0 && !isSeparator(rest[0]) && !strings.ContainsRune(",]}", rune(rest[0])) {
		return nil, errorAt(Unsupported, n, "%c%s is followed by %q, which YAML 1.2 reads as part of the name", sigil, name, rest[0])
	}
	return rest, nil
}

func isSeparator(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// skipSeparation skips blanks, line breaks and comments between node
// properties and content.
func skipSeparation(s []byte) []byte {
	for len(s) > 0 {
		switch s[0] {
		case ' ', '\t', '\n', '\r':
			s = s[1:]
		case '#':
			for len(s) > 0 && s[0] != '\n' {
				s = s[1:]
			}
		default:
			return s
		}
	}
	return s
}
