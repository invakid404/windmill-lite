package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"slices"
	"strconv"
	"strings"
)

// SecretMask is what replaces a secret's value in the text that the engine
// passes on from a harness process (Secrets).
const SecretMask = "[secret]"

// secretMaskEscaped is SecretMask as a URL's path or query escapes it.
const secretMaskEscaped = "%5Bsecret%5D"

// Secrets are the values of a host's secret environment entries
// (ProcessConfig.Secrets), which the engine masks with SecretMask in the
// text that it takes from a harness process and passes on.
//
//   - It masks each value, and each line of a value that spans lines, since
//     the process's output reaches the engine one line at a time.
//   - It masks each of them as an error text can quote it: inside a Go
//     quoted string (%q, strconv.Quote) and inside the JSON string that
//     JavaScript's JSON.stringify writes, which the harness's own errors
//     use.
//   - In a text with %XX escapes, such as a request's URL that the platform
//     facade quotes as sent, it also masks each value as the text reads
//     once they are decoded, whatever the client escaped.
//   - In a JSON value, a value that is itself a JSON object or array also
//     matches a part of the value that equals it, however it is written.
//
// A nil *Secrets masks nothing, and its methods return their input.
type Secrets struct {
	// olds are the strings to mask, the longest first, so that a secret
	// that holds another is masked whole: a Replacer tries its old strings
	// in argument order at each position.
	olds    []string
	r       *strings.Replacer
	longest int
	// docs are the values that are JSON objects or arrays, decoded with
	// json.Number for numbers.
	docs []any
	// structural is set when a string to mask holds a character of JSON's
	// structure, so that it can span the strings of a JSON text written
	// with spaces between them.
	structural bool
}

// NewSecrets returns the Secrets of values, leaving out empty values and
// lines, or nil when nothing is left to mask.
func NewSecrets(values []string) *Secrets {
	seen := map[string]bool{}
	var olds []string
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			olds = append(olds, s)
		}
	}
	forms := func(v string) {
		add(v)
		add(unquote(strconv.Quote(v)))
		add(jsonStringify(v))
	}
	var docs []any
	for _, v := range values {
		forms(v)
		if strings.Contains(v, "\n") {
			for _, l := range strings.Split(v, "\n") {
				forms(strings.TrimSuffix(l, "\r"))
			}
		}
		if t := strings.TrimSpace(v); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
			if doc, ok := decodeValue([]byte(t)); ok {
				docs = append(docs, doc)
			}
		}
	}
	if len(olds) == 0 {
		return nil
	}
	slices.SortStableFunc(olds, func(a, b string) int { return len(b) - len(a) })
	pairs := make([]string, 0, 2*len(olds))
	for _, o := range olds {
		pairs = append(pairs, o, SecretMask)
	}
	structural := slices.ContainsFunc(olds, func(o string) bool { return strings.ContainsAny(o, "\",:[]{}") })
	return &Secrets{olds: olds, r: strings.NewReplacer(pairs...), longest: len(olds[0]), docs: docs, structural: structural}
}

// decodeValue decodes exactly one JSON value, with json.Number for
// numbers.
func decodeValue(text []byte) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return v, true
}

// jsonStringify is s as JavaScript's JSON.stringify writes it inside its
// quotes: a quote, a backslash and the control characters that have a
// short escape are escaped so, the other control characters as \u00xx.
func jsonStringify(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// unquote is a quoted string without its quotes.
func unquote(q string) string {
	if len(q) < 2 {
		return ""
	}
	return q[1 : len(q)-1]
}

// Mask returns text with each occurrence of a secret replaced by
// SecretMask, also where the text reads a secret once its %XX escapes are
// decoded: there the escaped span becomes SecretMask, and every other
// byte stays.
func (s *Secrets) Mask(text string) string {
	if s == nil {
		return text
	}
	text = s.r.Replace(text)
	if strings.IndexByte(text, '%') >= 0 {
		text = s.maskDecoded(text, false, SecretMask)
	}
	return text
}

// MaskURL returns a URL's escaped path, or its raw query when query is
// set, with each secret masked as it reads raw and as decoded, + as a
// space in a query: each span that holds one becomes SecretMask, escaped,
// and every other byte stays, so other segments and parameters keep their
// escapes and their boundaries.
func (s *Secrets) MaskURL(text string, query bool) string {
	if s == nil {
		return text
	}
	// As sent, for a secret that itself holds an escape or a +, then as
	// decoded.
	text = replaceSpans(text, s.find(text), nil, secretMaskEscaped)
	return s.maskDecoded(text, query, secretMaskEscaped)
}

// MaskTail returns the log tail of an escaped invocation, which its error
// quotes, masked as Mask masks text, and its start too: the tail is the
// end of a longer log, so it may begin inside a secret. A leading piece of
// 3 bytes or more that ends a secret, as written or as its %XX escapes
// decode (after up to 2 hex digits that end an escape the cut split),
// becomes SecretMask with what follows it masked.
func (s *Secrets) MaskTail(tail string) string {
	if s == nil {
		return tail
	}
	cut := 0
	piece := func(view string) int {
		best := 0
		for _, o := range s.olds {
			for k := min(len(o)-1, len(view)); k >= 3 && k > best; k-- {
				if strings.HasSuffix(o, view[:k]) {
					best = k
					break
				}
			}
		}
		return best
	}
	cut = piece(tail)
	for skip := 0; skip <= 2 && skip <= len(tail); skip++ {
		if skip > 0 && !isHex(tail[skip-1]) {
			break
		}
		dec, from := decodeEscapes(tail[skip:], false)
		if k := piece(string(dec)); k > 0 {
			cut = max(cut, skip+from[k])
		}
	}
	if cut == 0 {
		return s.Mask(tail)
	}
	return SecretMask + s.Mask(tail[cut:])
}

// find returns the spans of the secrets in text, as Mask's Replacer finds
// them: from the left, the longest at one position, without overlaps.
func (s *Secrets) find(text string) [][2]int {
	var spans [][2]int
	for i := 0; i < len(text); {
		matched := 0
		for _, o := range s.olds {
			if strings.HasPrefix(text[i:], o) {
				matched = len(o)
				break
			}
		}
		if matched == 0 {
			i++
			continue
		}
		spans = append(spans, [2]int{i, i + matched})
		i += matched
	}
	return spans
}

// maskDecoded decodes the %XX escapes of text, and + as a space when plus
// is set, finds the secrets in what it decoded, and replaces the span of
// text that each one was decoded from with marker. It returns text itself
// when no secret is found.
func (s *Secrets) maskDecoded(text string, plus bool, marker string) string {
	dec, from := decodeEscapes(text, plus)
	return replaceSpans(text, s.find(string(dec)), from, marker)
}

// decodeEscapes decodes the %XX escapes of text, and + as a space when plus
// is set, leaving any other % as it is. It returns the decoded bytes, and
// for each of them the offset in text where it starts, with len(text)
// after the last.
func decodeEscapes(text string, plus bool) ([]byte, []int) {
	dec := make([]byte, 0, len(text))
	from := make([]int, 0, len(text)+1)
	for i := 0; i < len(text); {
		switch c := text[i]; {
		case c == '%' && i+2 < len(text) && isHex(text[i+1]) && isHex(text[i+2]):
			dec = append(dec, unhex(text[i+1])<<4|unhex(text[i+2]))
			from = append(from, i)
			i += 3
		case c == '+' && plus:
			dec = append(dec, ' ')
			from = append(from, i)
			i++
		default:
			dec = append(dec, c)
			from = append(from, i)
			i++
		}
	}
	return dec, append(from, len(text))
}

// replaceSpans replaces each span of text with marker. The spans' offsets
// are text's when from is nil, and otherwise those of a decoded text, whose
// byte i came from text's offset from[i].
func replaceSpans(text string, spans [][2]int, from []int, marker string) string {
	if len(spans) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	for _, sp := range spans {
		start, end := sp[0], sp[1]
		if from != nil {
			start, end = from[start], from[end]
		}
		b.WriteString(text[last:start])
		b.WriteString(marker)
		last = end
	}
	b.WriteString(text[last:])
	return b.String()
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c <= '9':
		return c - '0'
	case c <= 'F':
		return c - 'A' + 10
	default:
		return c - 'a' + 10
	}
}

// MaskJSON returns the JSON text with each secret masked, as decoded:
//
//   - in its strings and object keys, with Mask;
//   - in a number or a literal whose text holds a secret, such as a secret
//     of digits that a step returned as a number: it becomes the string
//     SecretMask, so the text stays JSON;
//   - in an object or array that equals a secret that is itself a JSON
//     object or array, however each is written: it becomes the string
//     SecretMask.
//
// When a secret still shows in the text after that, it spans the JSON's
// structure, and the whole value becomes the string SecretMask. Key order,
// duplicate keys and the other numbers stay as they were. It returns text
// itself, byte for byte, when nothing holds a secret, and also when text is
// not JSON.
func (s *Secrets) MaskJSON(text []byte) []byte {
	return s.maskJSON(text, nil)
}

// MaskJSONAt is MaskJSON for only the parts of the JSON text at pointers,
// JSON pointers relative to it ("" for the whole): each is masked as
// MaskJSON masks a whole value, and the rest stays as it was. A pointer
// that leads nowhere in the text is passed over.
func (s *Secrets) MaskJSONAt(text []byte, pointers []string) []byte {
	if len(pointers) == 0 {
		return text
	}
	return s.maskJSON(text, pointers)
}

// maskJSON masks the parts of text at pointers, or all of it when pointers
// is nil.
func (s *Secrets) maskJSON(text []byte, pointers []string) []byte {
	if s == nil || len(text) == 0 {
		return text
	}
	// A secret can appear in a string only as itself, with escapes or
	// percent-encoded, but one that holds JSON's structure, a secret
	// document among them, in any spelling.
	if !s.structural && bytes.IndexByte(text, '\\') < 0 && bytes.IndexByte(text, '%') < 0 &&
		!slices.ContainsFunc(s.olds, func(o string) bool { return bytes.Contains(text, []byte(o)) }) {
		return text
	}
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.UseNumber()
	root, err := decodeTree(dec)
	if err != nil {
		return text
	}
	if _, err := dec.Token(); err != io.EOF {
		return text
	}
	changed := false
	if pointers == nil {
		changed = s.maskWhole(root)
	}
	for _, ptr := range pointers {
		if n := root.at(ptr); n != nil {
			changed = s.maskWhole(n) || changed
		}
	}
	if !changed {
		return text
	}
	var b bytes.Buffer
	root.write(&b)
	return b.Bytes()
}

// at returns the part of n that the JSON pointer ptr names, or nil when
// there is none. In an object that repeats a key, the key's last member
// counts.
func (n *jnode) at(ptr string) *jnode {
	if ptr == "" {
		return n
	}
	if ptr[0] != '/' {
		return nil
	}
	for _, tok := range strings.Split(ptr[1:], "/") {
		tok = strings.NewReplacer("~1", "/", "~0", "~").Replace(tok)
		var next *jnode
		switch n.kind {
		case '{':
			for i, k := range n.keys {
				if k == tok {
					next = n.kids[i]
				}
			}
		case '[':
			if i, err := strconv.Atoi(tok); err == nil && i >= 0 && i < len(n.kids) && strconv.Itoa(i) == tok {
				next = n.kids[i]
			}
		}
		if next == nil {
			return nil
		}
		n = next
	}
	return n
}

// maskWhole masks n as MaskJSON masks a whole value: maskNode, then, where
// a secret still shows in n written again compactly, across its structure
// (the strings of an array, a key and its value), the smallest object or
// array that holds it becomes SecretMask. It reports whether it changed n.
func (s *Secrets) maskWhole(n *jnode) bool {
	changed := s.maskNode(n)
	var b bytes.Buffer
	n.write(&b)
	if !s.holds(b.Bytes()) {
		return changed
	}
	s.spill(n)
	return true
}

// holds reports whether text holds one of the strings to mask.
func (s *Secrets) holds(text []byte) bool {
	return slices.ContainsFunc(s.olds, func(o string) bool { return bytes.Contains(text, []byte(o)) })
}

// spill returns n written compactly, after it turns each smallest object
// or array in n whose writing holds a secret into SecretMask.
func (s *Secrets) spill(n *jnode) []byte {
	var b bytes.Buffer
	switch n.kind {
	case '{', '[':
		b.WriteByte(n.kind)
		for i, k := range n.kids {
			if i > 0 {
				b.WriteByte(',')
			}
			if n.kind == '{' {
				writeJSONString(&b, n.keys[i])
				b.WriteByte(':')
			}
			b.Write(s.spill(k))
		}
		b.WriteByte(n.kind + 2) // '}' and ']' follow '{' and '[' by two
		if s.holds(b.Bytes()) {
			*n = jnode{kind: 's', text: SecretMask}
			b.Reset()
			n.write(&b)
		}
	default:
		n.write(&b)
	}
	return b.Bytes()
}

// jnode is a decoded JSON value that keeps what writing it again needs: its
// kind ('{', '[', 's' for a string, 'l' for a number or a literal), a
// string's value or a number's or literal's text, an object's keys in order
// and its members or elements.
type jnode struct {
	kind byte
	text string
	keys []string
	kids []*jnode
}

// decodeTree decodes one JSON value from dec.
func decodeTree(dec *json.Decoder) (*jnode, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch v := tok.(type) {
	case json.Delim:
		n := &jnode{kind: byte(v)}
		for dec.More() {
			if v == '{' {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				n.keys = append(n.keys, kt.(string))
			}
			kid, err := decodeTree(dec)
			if err != nil {
				return nil, err
			}
			n.kids = append(n.kids, kid)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return n, nil
	case string:
		return &jnode{kind: 's', text: v}, nil
	case json.Number:
		return &jnode{kind: 'l', text: string(v)}, nil
	case bool:
		return &jnode{kind: 'l', text: strconv.FormatBool(v)}, nil
	default:
		return &jnode{kind: 'l', text: "null"}, nil
	}
}

// nodeEqual reports whether n equals d, a value as decodeValue decodes
// it: objects by their members, the last of a repeated key counting, as
// decodeValue keeps it; arrays element by element; numbers by their value,
// so 1.0 equals 1, as a JSON parser that writes the value again spells it.
// It compares n in place, without decoding it again.
func nodeEqual(n *jnode, d any) bool {
	switch n.kind {
	case '{':
		m, ok := d.(map[string]any)
		if !ok {
			return false
		}
		last := make(map[string]int, len(n.keys))
		for i, k := range n.keys {
			last[k] = i
		}
		if len(last) != len(m) {
			return false
		}
		for k, i := range last {
			v, ok := m[k]
			if !ok || !nodeEqual(n.kids[i], v) {
				return false
			}
		}
		return true
	case '[':
		a, ok := d.([]any)
		if !ok || len(a) != len(n.kids) {
			return false
		}
		for i, k := range n.kids {
			if !nodeEqual(k, a[i]) {
				return false
			}
		}
		return true
	case 's':
		v, ok := d.(string)
		return ok && v == n.text
	}
	switch n.text {
	case "true":
		return d == true
	case "false":
		return d == false
	case "null":
		return d == nil
	}
	v, ok := d.(json.Number)
	return ok && numbersEqual(n.text, string(v))
}

// numbersEqual reports whether two JSON numbers have one value, exactly.
// A number whose exponent passes 400 either way compares by its text, so a
// comparison never builds an enormous value.
func numbersEqual(a, b string) bool {
	if a == b {
		return true
	}
	small := func(x string) bool {
		i := strings.IndexAny(x, "eE")
		if i < 0 {
			return len(x) <= 400
		}
		e, err := strconv.Atoi(strings.TrimPrefix(x[i+1:], "+"))
		return err == nil && e >= -400 && e <= 400 && i <= 400
	}
	if !small(a) || !small(b) {
		return false
	}
	ra, okA := new(big.Rat).SetString(a)
	rb, okB := new(big.Rat).SetString(b)
	return okA && okB && ra.Cmp(rb) == 0
}

// write writes n as JSON, compact.
func (n *jnode) write(b *bytes.Buffer) {
	switch n.kind {
	case '{', '[':
		b.WriteByte(n.kind)
		for i, k := range n.kids {
			if i > 0 {
				b.WriteByte(',')
			}
			if n.kind == '{' {
				writeJSONString(b, n.keys[i])
				b.WriteByte(':')
			}
			k.write(b)
		}
		if n.kind == '{' {
			b.WriteByte('}')
		} else {
			b.WriteByte(']')
		}
	case 's':
		writeJSONString(b, n.text)
	default:
		b.WriteString(n.text)
	}
}

// maskNode masks n as MaskJSON masks a whole value, and reports whether it
// changed it.
func (s *Secrets) maskNode(n *jnode) bool {
	if n.kind == '{' || n.kind == '[' {
		for _, d := range s.docs {
			if nodeEqual(n, d) {
				*n = jnode{kind: 's', text: SecretMask}
				return true
			}
		}
	}
	changed := false
	switch n.kind {
	case 's':
		if m := s.Mask(n.text); m != n.text {
			n.text, changed = m, true
		}
	case 'l':
		if s.r.Replace(n.text) != n.text {
			*n = jnode{kind: 's', text: SecretMask}
			return true
		}
	}
	for i, k := range n.keys {
		if m := s.Mask(k); m != k {
			n.keys[i], changed = m, true
		}
	}
	for _, k := range n.kids {
		if s.maskNode(k) {
			changed = true
		}
	}
	return changed
}

// writeJSONString writes s as a JSON string, without the HTML escapes of
// encoding/json's default.
func writeJSONString(out *bytes.Buffer, s string) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	out.Write(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
}

// cut returns where an output line that has grown too long, buf, may be
// cut, so that the part before the cut can be forwarded masked and the
// rest kept for the next write: no secret that starts before the cut ends
// after it, or could still complete with the bytes to come, as the line
// reads raw and as its %XX escapes decode (Mask reads it both ways), and
// no escape that the next bytes could complete starts before it. What is
// kept is shorter than three times the longest secret and two bytes.
func (s *Secrets) cut(buf []byte) int {
	if s == nil {
		return len(buf)
	}
	c := s.cutAt(buf)
	// An escape that the next bytes may complete ends the view as decoded,
	// which may then end in a secret's start.
	end := len(buf)
	if n := len(buf); n >= 1 && buf[n-1] == '%' {
		end = n - 1
	} else if n >= 2 && buf[n-2] == '%' && isHex(buf[n-1]) {
		end = n - 2
	}
	if dec, from := decodeEscapes(string(buf[:end]), false); end < len(buf) || len(dec) != end {
		c = min(c, end, from[s.cutAt(dec)])
	}
	return c
}

// cutAt is cut for one view of the line, buf. It scans buf as Mask's
// Replacer does, the leftmost secret first and the longest at one
// position. At each position it first asks whether the rest of buf is the
// start of a secret longer than that rest: then the Replacer's choice
// there waits on bytes to come, even when a shorter secret matches there
// already, so it cuts there.
func (s *Secrets) cutAt(buf []byte) int {
	for i := 0; i < len(buf); {
		if rest := len(buf) - i; rest < s.longest {
			for _, o := range s.olds {
				if len(o) > rest && strings.HasPrefix(o, string(buf[i:])) {
					return i
				}
			}
		}
		matched := 0
		for _, o := range s.olds {
			if bytes.HasPrefix(buf[i:], []byte(o)) {
				matched = len(o)
				break
			}
		}
		if matched > 0 {
			i += matched
			continue
		}
		i++
	}
	return len(buf)
}
