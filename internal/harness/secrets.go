package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
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
	return &Secrets{olds: olds, r: strings.NewReplacer(pairs...), longest: len(olds[0]), docs: docs}
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

// DecodeJSON decodes one JSON value as MaskJSONParts compares its parts,
// and reports whether text is one.
func DecodeJSON(text []byte) (any, bool) { return decodeValue(text) }

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
	return s.maskDecoded(text, query, secretMaskEscaped)
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
	var dec []byte
	from := make([]int, 0, len(text)+1) // the offset in text of each decoded byte
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
	from = append(from, len(text))
	return replaceSpans(text, s.find(string(dec)), from, marker)
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

// MaskJSONParts is MaskJSON for only the parts of the JSON text that equal
// one of parts, each decoded as DecodeJSON decodes it: each such part is
// masked as MaskJSON masks a whole value, and the rest stays as it was.
func (s *Secrets) MaskJSONParts(text []byte, parts []any) []byte {
	if len(parts) == 0 {
		return text
	}
	return s.maskJSON(text, parts)
}

func (s *Secrets) maskJSON(text []byte, parts []any) []byte {
	if s == nil || len(text) == 0 {
		return text
	}
	// A secret can appear in a string only as itself, with escapes or
	// percent-encoded, but a secret document in any spelling.
	if len(s.docs) == 0 && bytes.IndexByte(text, '\\') < 0 && bytes.IndexByte(text, '%') < 0 &&
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
	var changed bool
	if parts == nil {
		changed = s.maskNode(root)
	} else {
		changed = s.maskParts(root, parts)
	}
	masked := text
	if changed {
		var b bytes.Buffer
		root.write(&b)
		masked = b.Bytes()
	}
	if parts == nil && slices.ContainsFunc(s.olds, func(o string) bool { return bytes.Contains(masked, []byte(o)) }) {
		var whole bytes.Buffer
		writeJSONString(&whole, SecretMask)
		return whole.Bytes()
	}
	return masked
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

// value is n as decodeValue decodes it.
func (n *jnode) value() any {
	switch n.kind {
	case '{':
		m := make(map[string]any, len(n.kids))
		for i, k := range n.kids {
			m[n.keys[i]] = k.value()
		}
		return m
	case '[':
		a := make([]any, len(n.kids))
		for i, k := range n.kids {
			a[i] = k.value()
		}
		return a
	case 's':
		return n.text
	}
	switch n.text {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	return json.Number(n.text)
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
	if (n.kind == '{' || n.kind == '[') && len(s.docs) > 0 {
		v := n.value()
		for _, d := range s.docs {
			if reflect.DeepEqual(v, d) {
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

// maskParts masks, as maskNode does, each part of n that equals one of
// parts, and reports whether it changed one.
func (s *Secrets) maskParts(n *jnode, parts []any) bool {
	v := n.value()
	for _, p := range parts {
		if reflect.DeepEqual(v, p) {
			return s.maskNode(n)
		}
	}
	changed := false
	for _, k := range n.kids {
		if s.maskParts(k, parts) {
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
// rest kept for the next write: every secret that starts before the cut
// ends before it, and no secret that could still complete with the bytes
// to come starts before it. It scans buf as Mask's Replacer does, the
// leftmost secret first and the longest at one position. At each position
// it first asks whether the rest of buf is the start of a secret longer
// than that rest: then the Replacer's choice there waits on bytes to come,
// even when a shorter secret matches there already, so it cuts there. What
// is kept is shorter than the longest secret.
func (s *Secrets) cut(buf []byte) int {
	if s == nil {
		return len(buf)
	}
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
