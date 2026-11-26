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
	// olds are the strings to mask, the longest first; longest is the
	// length of the first.
	olds    []string
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
	structural := slices.ContainsFunc(olds, func(o string) bool { return strings.ContainsAny(o, "\",:[]{}") })
	return &Secrets{olds: olds, longest: len(olds[0]), docs: docs, structural: structural}
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
// byte stays. Secrets that overlap, the end of one the start of another,
// become one SecretMask.
func (s *Secrets) Mask(text string) string {
	if s == nil {
		return text
	}
	if strings.IndexByte(text, '%') < 0 {
		return replaceSpans(text, s.find(text), nil, SecretMask)
	}
	return replaceSpans(text, s.spans(text, false), nil, SecretMask)
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
	return replaceSpans(text, s.spans(text, query), nil, secretMaskEscaped)
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
	spans := s.spans(tail, false)
	if cut > 0 {
		// Found on the tail as it is, with the whole secrets, so that a
		// secret whose start is also its end is masked whole.
		spans = mergeSpans(append(spans, [2]int{0, cut}))
	}
	return replaceSpans(tail, spans, nil, SecretMask)
}

// find returns the spans of text that hold a secret: every occurrence of
// each, those that overlap merged, so that two secrets that share bytes,
// the end of one the start of the other, are masked as one span.
func (s *Secrets) find(text string) [][2]int {
	var spans [][2]int
	for _, oc := range s.occurrences(text) {
		spans = append(spans, [2]int{oc.start, oc.end})
	}
	return mergeSpans(spans)
}

// occurrence is a secret, old, at [start, end) of a text.
type occurrence struct {
	start, end int
	old        string
}

// occurrences returns every occurrence of each secret in text, those that
// overlap included.
func (s *Secrets) occurrences(text string) []occurrence {
	var out []occurrence
	for _, o := range s.olds {
		for i := 0; i+len(o) <= len(text); {
			j := strings.Index(text[i:], o)
			if j < 0 {
				break
			}
			out = append(out, occurrence{i + j, i + j + len(o), o})
			i += j + 1
		}
	}
	return out
}

// spans returns the spans of text that hold a secret, as written and as
// its %XX escapes decode (and + as a space when plus is set), both found
// on text as it is and merged where they overlap: a secret as written
// that starts a longer one as decoded is masked with all of it.
func (s *Secrets) spans(text string, plus bool) [][2]int {
	all := s.find(text)
	if strings.IndexByte(text, '%') >= 0 || plus && strings.IndexByte(text, '+') >= 0 {
		dec, from := decodeEscapes(text, plus)
		for _, sp := range s.find(string(dec)) {
			all = append(all, [2]int{from[sp[0]], from[sp[1]]})
		}
	}
	return mergeSpans(all)
}

// mergeSpans sorts spans by their start and merges those that overlap.
func mergeSpans(spans [][2]int) [][2]int {
	slices.SortFunc(spans, func(a, b [2]int) int { return a[0] - b[0] })
	var out [][2]int
	for _, sp := range spans {
		if n := len(out); n > 0 && sp[0] < out[n-1][1] {
			out[n-1][1] = max(out[n-1][1], sp[1])
			continue
		}
		out = append(out, sp)
	}
	return out
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
//   - in an object or array that equals a secret that is itself a JSON
//     object or array, however each is written: it becomes the string
//     SecretMask.
//
// A secret that still shows after that, as the value is written compactly
// or as the text itself writes it, is in one token's JSON or spans the
// JSON's structure. The smallest part that holds it then becomes the
// string SecretMask, so the text stays JSON, and the rest of the value
// stays: a number or a literal, such as a secret of digits that a step
// returned as a number; in a string or a key whose JSON holds a secret
// that its text does not, such as a secret with its quotes, or one that a
// JSON escape spells, the part of its text under the secret, or all of it
// where the JSON has an escape; or else an object or array across whose
// tokens it lies, such as the strings of an array. Key order, duplicate keys and the other numbers stay as
// they were, and the result is written compactly. It returns text itself,
// byte for byte, when nothing holds a secret, and also when text is not
// JSON.
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
	root, err := decodeTree(dec, text)
	if err != nil {
		return text
	}
	if _, err := dec.Token(); err != io.EOF {
		return text
	}
	// The secrets in the text as it is written, which may span its tokens.
	var occ []occurrence
	if s.holds(text) {
		occ = s.occurrences(string(text))
	}
	changed := false
	if pointers == nil {
		changed = s.maskWhole(root, occ)
	}
	for _, ptr := range pointers {
		if n := root.at(ptr); n != nil {
			changed = s.maskWhole(n, occ) || changed
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
// a secret still shows across n's structure (the strings of an array, a key
// and its value), the smallest object or array that holds it becomes
// SecretMask: as n is written again compactly, and as its source writes
// it, where occ are the secrets in the source. It reports whether it
// changed n.
func (s *Secrets) maskWhole(n *jnode, occ []occurrence) bool {
	changed := s.maskNode(n, occ)
	var b bytes.Buffer
	n.write(&b)
	if s.holds(b.Bytes()) {
		s.spill(n)
		changed = true
	}
	for _, oc := range occ {
		if n.maskSource([2]int{oc.start, oc.end}) {
			changed = true
		}
	}
	return changed
}

// maskSource masks the smallest object or array in n across whose tokens
// in the source the span sp of the source lies. A key or value token that
// holds sp whole is left as it is: maskNode masked it. It reports whether
// it changed n.
func (n *jnode) maskSource(sp [2]int) bool {
	if !n.src || sp[0] < n.start || sp[1] > n.end || n.kind != '{' && n.kind != '[' {
		return false
	}
	for _, k := range n.keySrc {
		if sp[0] >= k[0] && sp[1] <= k[1] {
			return false
		}
	}
	for _, k := range n.kids {
		if k.src && sp[0] >= k.start && sp[1] <= k.end {
			return k.maskSource(sp)
		}
	}
	n.mask()
	return true
}

// mask turns n into the string SecretMask, keeping where its source is.
func (n *jnode) mask() {
	*n = jnode{kind: 's', text: SecretMask, src: n.src, start: n.start, end: n.end}
}

// holds reports whether text holds one of the strings to mask.
func (s *Secrets) holds(text []byte) bool {
	return slices.ContainsFunc(s.olds, func(o string) bool { return bytes.Contains(text, []byte(o)) })
}

// spill returns n written compactly, after it turns each smallest object
// or array in n whose writing holds a secret into SecretMask. maskNode
// has masked each key and value token whose own JSON holds one.
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
			n.mask()
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
	// start and end are where the value is in its source text, when src is
	// set, and keySrc where an object's keys are.
	src        bool
	start, end int
	keySrc     [][2]int
}

// decodeTree decodes one JSON value from dec.
func decodeTree(dec *json.Decoder, text []byte) (*jnode, error) {
	start := tokenStart(dec, text)
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	var n *jnode
	switch v := tok.(type) {
	case json.Delim:
		n = &jnode{kind: byte(v)}
		for dec.More() {
			if v == '{' {
				ks := tokenStart(dec, text)
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				n.keys = append(n.keys, kt.(string))
				n.keySrc = append(n.keySrc, [2]int{ks, int(dec.InputOffset())})
			}
			kid, err := decodeTree(dec, text)
			if err != nil {
				return nil, err
			}
			n.kids = append(n.kids, kid)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
	case string:
		n = &jnode{kind: 's', text: v}
	case json.Number:
		n = &jnode{kind: 'l', text: string(v)}
	case bool:
		n = &jnode{kind: 'l', text: strconv.FormatBool(v)}
	default:
		n = &jnode{kind: 'l', text: "null"}
	}
	n.src, n.start, n.end = true, start, int(dec.InputOffset())
	return n, nil
}

// tokenStart returns where dec's next token starts in text, its source:
// after the spaces and the comma or colon that come before it.
func tokenStart(dec *json.Decoder, text []byte) int {
	i := int(dec.InputOffset())
	for i < len(text) && strings.IndexByte(" \t\r\n,:", text[i]) >= 0 {
		i++
	}
	return i
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

// numbersEqual reports whether two JSON numbers have one value, exactly,
// however each is spelled, such as 1.0 and 1, or 0e401 and 0. It compares
// their signs, significant digits and the powers of ten after them, and
// never computes a value, so an exponent of any size costs only its
// digits.
func numbersEqual(a, b string) bool {
	if a == b {
		return true
	}
	na, okA := decimalOf(a)
	nb, okB := decimalOf(b)
	return okA && okB && na.neg == nb.neg && na.digits == nb.digits && na.exp.Cmp(nb.exp) == 0
}

// decimal is a number as digits × 10^exp: its sign, its significant digits
// with no leading or trailing zero, and exp. Zero has no digits, no sign
// and exp 0.
type decimal struct {
	neg    bool
	digits string
	exp    *big.Int
}

// decimalOf returns the JSON number s as a decimal, or false when s is
// not one.
func decimalOf(s string) (decimal, bool) {
	var d decimal
	if strings.HasPrefix(s, "-") {
		d.neg, s = true, s[1:]
	}
	d.exp = new(big.Int)
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		if _, ok := d.exp.SetString(s[i+1:], 10); !ok {
			return decimal{}, false
		}
		s = s[:i]
	}
	whole, frac, _ := strings.Cut(s, ".")
	digits := whole + frac
	if digits == "" || strings.Trim(digits, "0123456789") != "" {
		return decimal{}, false
	}
	d.exp.Sub(d.exp, big.NewInt(int64(len(frac))))
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return decimal{exp: new(big.Int)}, true
	}
	trimmed := strings.TrimRight(digits, "0")
	d.exp.Add(d.exp, big.NewInt(int64(len(digits)-len(trimmed))))
	d.digits = trimmed
	return d, true
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

// maskNode masks n's tokens and the documents in it: an object or array
// equal to a secret document becomes SecretMask; a number or literal whose
// text holds a secret does too; a string or a key is masked by maskToken.
// occ are the secrets in the source. It reports whether it changed n.
func (s *Secrets) maskNode(n *jnode, occ []occurrence) bool {
	switch n.kind {
	case '{', '[':
		for _, d := range s.docs {
			if nodeEqual(n, d) {
				n.mask()
				return true
			}
		}
		changed := false
		for i, k := range n.keys {
			var src [2]int
			if i < len(n.keySrc) {
				src = n.keySrc[i]
			}
			if m := s.maskToken(k, src, n.src, occ); m != k {
				n.keys[i], changed = m, true
			}
		}
		for _, k := range n.kids {
			if s.maskNode(k, occ) {
				changed = true
			}
		}
		return changed
	case 's':
		if m := s.maskToken(n.text, [2]int{n.start, n.end}, n.src, occ); m != n.text {
			n.text = m
			return true
		}
	case 'l':
		if s.holds([]byte(n.text)) {
			n.mask()
			return true
		}
	}
	return false
}

// maskToken returns a string or a key, text as decoded, with each secret
// in it masked as Mask masks it, and also each secret that its JSON token
// holds and text does not, such as a secret with its quotes: as the token
// is written compactly, and as the source writes it at src (when known).
// Where that token is text in quotes with no escape, only the part of text
// under such a secret is masked, its quotes left out, so the text beside
// it stays; otherwise all of text.
func (s *Secrets) maskToken(text string, src [2]int, known bool, occ []occurrence) string {
	spans := s.spans(text, false)
	whole := false
	under := func(a, b, tokenLen int) {
		if tokenLen != len(text)+2 {
			whole = true
			return
		}
		if a, b = max(a-1, 0), min(b-1, len(text)); a < b {
			spans = append(spans, [2]int{a, b})
		}
	}
	var w bytes.Buffer
	writeJSONString(&w, text)
	for _, oc := range s.occurrences(w.String()) {
		if !strings.Contains(text, oc.old) {
			under(oc.start, oc.end, w.Len())
		}
	}
	if known {
		for _, oc := range occ {
			if oc.start >= src[0] && oc.end <= src[1] && !strings.Contains(text, oc.old) {
				under(oc.start-src[0], oc.end-src[0], src[1]-src[0])
			}
		}
	}
	if whole {
		return SecretMask
	}
	return replaceSpans(text, mergeSpans(spans), nil, SecretMask)
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

// pending returns where, in an output line that has grown too long, buf,
// the first secret starts that the bytes to come could still complete, as
// the line reads raw or as its %XX escapes decode, or the first escape that
// they could complete: len(buf) when there is none. The line may go out up
// to there. What is kept is shorter than three times the longest secret
// and two bytes, so every cut makes progress.
func (s *Secrets) pending(buf []byte) int {
	if s == nil {
		return len(buf)
	}
	c := s.pendingAt(buf)
	end := len(buf)
	if n := len(buf); n >= 1 && buf[n-1] == '%' {
		end = n - 1
	} else if n >= 2 && buf[n-2] == '%' && isHex(buf[n-1]) {
		end = n - 2
	}
	dec, from := decodeEscapes(string(buf[:end]), false)
	return min(c, end, from[s.pendingAt(dec)])
}

// lookBack is how far a secret can reach in an output line, as written or
// percent-encoded: three times the longest secret and two bytes. pending
// keeps no more, and maskPart needs no more of what went out.
func (s *Secrets) lookBack() int { return 3*s.longest + 2 }

// pendingAt is pending for one view of the line, buf: the first position
// where the rest of buf is the start of a secret longer than that rest.
func (s *Secrets) pendingAt(buf []byte) int {
	for i := max(0, len(buf)-s.longest+1); i < len(buf); i++ {
		rest := string(buf[i:])
		if slices.ContainsFunc(s.olds, func(o string) bool { return len(o) > len(rest) && strings.HasPrefix(o, rest) }) {
			return i
		}
	}
	return len(buf)
}

// maskPart returns the part of an output line that goes out, buf[:c],
// with each secret masked as Mask masks it, found in ctx, the end of what
// went out of the line before, followed by buf, the line since: so also a
// secret that ctx starts, or one that starts before c and ends after it.
func (s *Secrets) maskPart(ctx, buf []byte, c int) string {
	if s == nil {
		return string(buf[:c])
	}
	full := string(ctx) + string(buf)
	var spans [][2]int
	for _, sp := range s.spans(full, false) {
		a, b := max(sp[0], len(ctx)), min(sp[1], len(ctx)+c)
		if a < b {
			spans = append(spans, [2]int{a - len(ctx), b - len(ctx)})
		}
	}
	return replaceSpans(string(buf[:c]), spans, nil, SecretMask)
}
