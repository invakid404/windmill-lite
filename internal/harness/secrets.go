package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/invakid404/windmill-lite/internal/jsonval"
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
// become one SecretMask. A secret that a mask makes with the text beside
// it is masked too (settleText).
func (s *Secrets) Mask(text string) string {
	if s == nil {
		return text
	}
	if strings.IndexByte(text, '%') < 0 {
		return s.settle(text, replaceSpans(text, s.find(text), nil, SecretMask), SecretMask, false)
	}
	return s.settle(text, replaceSpans(text, s.spans(text, false), nil, SecretMask), SecretMask, false)
}

// settle returns out, what masking made of text, with each secret that a
// mask, marker, makes with the text beside it masked too (settleText), as
// spans finds them, with + as a space when plus is set. When masking
// changed nothing, it made no secret.
func (s *Secrets) settle(text, out, marker string, plus bool) string {
	if out == text {
		return out
	}
	return settleText("", out, marker, func(t string) [][2]int { return s.spans(t, plus) })
}

// MaskAfter returns text, which masking returned, with each secret that
// prefix and text make together masked in text too, as masking reads its
// own output again (settleText): text follows prefix in what goes out, such
// as an error's message after the words that its type writes before it,
// and prefix stays as it is. A secret wholly in prefix, or wholly inside a
// mask, changes nothing, so with an empty prefix text comes back as it is
// exactly when it holds a secret nowhere but inside its masks.
func (s *Secrets) MaskAfter(prefix, text string) string {
	if s == nil {
		return text
	}
	return settleText(prefix, text, SecretMask, func(t string) [][2]int { return s.spans(t, false) })
}

// settleText masks again what masking made of out, the part of a text
// that goes out after prev, the end of what went out of it before (empty
// for a whole text), until a round changes nothing: a mask, marker, can
// make a secret with the text beside it, as a secret that starts with "]"
// makes with the text after the mask. find returns the spans of a text that
// hold a secret. A round masks a secret that overlaps a mask with all of
// that mask, so no piece of a mask is left, and one that lies wholly inside
// a mask, such as one that the mask's own text holds, changes nothing;
// prev went out already and stays as it is. Past maskRounds rounds, out is
// masked whole.
func settleText(prev, out, marker string, find func(string) [][2]int) string {
	for range maskRounds {
		full := prev + out
		regions := markerRegions(full, marker)
		var spans [][2]int
		for _, sp := range find(full) {
			a, b := sp[0], sp[1]
			if i := sort.Search(len(regions), func(i int) bool { return regions[i][1] > a }); i < len(regions) && regions[i][0] < b {
				a = min(a, regions[i][0])
			}
			if j := sort.Search(len(regions), func(j int) bool { return regions[j][0] >= b }) - 1; j >= 0 && regions[j][1] > a {
				b = max(b, regions[j][1])
			}
			if a, b = max(a, len(prev))-len(prev), b-len(prev); a < b {
				spans = append(spans, [2]int{a, b})
			}
		}
		next := replaceSpans(out, mergeSpans(spans), nil, marker)
		if next == out {
			return out
		}
		out = next
	}
	return marker
}

// markerRegions returns where marker is in text, in order. A marker does
// not overlap itself, so the regions do not overlap.
func markerRegions(text, marker string) [][2]int {
	var out [][2]int
	for i := 0; ; {
		j := strings.Index(text[i:], marker)
		if j < 0 {
			return out
		}
		out = append(out, [2]int{i + j, i + j + len(marker)})
		i += j + len(marker)
	}
}

// MaskURL returns a URL's escaped path, or its raw query when query is
// set, with each secret masked as it reads raw and as decoded, + as a
// space in a query: each span that holds one becomes SecretMask, escaped,
// and every other byte stays, so other segments and parameters keep their
// escapes and their boundaries. A secret that a mask makes with the text
// beside it, as written or decoded, is masked too (settleText).
func (s *Secrets) MaskURL(text string, query bool) string {
	if s == nil {
		return text
	}
	return s.settle(text, replaceSpans(text, s.spans(text, query), nil, secretMaskEscaped), secretMaskEscaped, query)
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
	return s.settle(tail, replaceSpans(tail, spans, nil, SecretMask), SecretMask, false)
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

// MaskJSON returns the JSON text with each secret masked. It finds the
// secrets in the value as it reads three ways, before it changes anything:
// in its strings and keys as decoded (as Mask finds them), in the value
// written compactly, and in the text as it is written, spaces included. A
// secret inside one string or key masks the part of that string or key
// under it, as decoded, whatever escapes its JSON uses, so the text beside
// it stays: a secret with its quotes, or one that a JSON escape spells,
// counts too. One inside a number or literal masks it. One across tokens,
// such as the strings of an array, masks the smallest object or array that
// holds it, and so does an object or array equal to a secret that is a JSON
// document, however each is written. One that crosses the value's edge,
// such as into the spaces that JSON allows around it, masks the value
// whole. It also reads the value's stored text (jsonval), which writes
// numbers in plain decimal, strings its own way and keys in its own order,
// and masks what that shows at the same places. What is masked becomes the
// string SecretMask, so the text stays JSON. Key order, duplicate keys and
// the other numbers stay as they were, and the result is written compactly.
// Masking can make a secret where there was none, as when a masked number's
// quote meets the next token, or a masked key moves in the stored order: the
// masked text is read again, as it is and as stored, and masked again until
// a round changes nothing; the result is the stored text when that is where
// one showed. A selected part masked whole whose mask then meets the data
// beside it in a secret is written as null instead, and if a secret still
// crosses its edge, the smallest object or array around both is masked, and
// its edges are read in turn. The rounds pass over a secret wholly inside a
// mask, its quotes included, or inside such a null: a secret that the
// mask's own text holds, such as cre in [secret], therefore shows in every
// mask. A chain of secrets, each of which the last mask makes, can need
// more rounds than maskRounds, which bounds the work: reached, it masks
// whole each selected part and each object or array a part gave way to,
// writes them as null if a mask would meet the data beside it in a secret,
// and keeps the rest; it masks the whole value only if a secret shows still.
//
// Cost: each escaped string's index from its bytes to the decoded bytes
// they spell is built once, in one pass, so placing a match is two lookups;
// one string's matches are then sorted to merge them. The rounds that read
// masked text again find where the masks are in it once, so passing over a
// secret inside one is a binary search. A string of N bytes that holds a
// secret M times, inside masks or not, costs O(N + M log M) a round, and
// there are at most maskRounds + 1 rounds, and the guard's.
// It returns text itself, byte for byte, when nothing holds a secret, and
// also when text is not JSON.
func (s *Secrets) MaskJSON(text []byte) []byte {
	out, _ := s.maskJSON(text, nil)
	return out
}

// HoldsJSON reports whether MaskJSON finds a secret in the JSON text, in any
// of its readings, also where what replaces it reads the same as before,
// as with a secret that is the mask itself.
func (s *Secrets) HoldsJSON(text []byte) bool {
	_, found := s.maskJSON(text, nil)
	return found
}

// MaskJSONAt is MaskJSON for only the parts of the JSON text at pointers,
// JSON pointers relative to it ("" for the whole): each is masked as
// MaskJSON masks a whole value, and the rest stays as it was, but for an
// object or array that a part gives way to (MaskJSON). A pointer that leads
// nowhere in the text is passed over.
func (s *Secrets) MaskJSONAt(text []byte, pointers []string) []byte {
	if len(pointers) == 0 {
		return text
	}
	out, _ := s.maskJSON(text, pointers)
	return out
}

// maskRounds bounds how many times maskJSON reads its masked text again,
// so that the work stays bounded; a chain of secrets, each of which the
// last mask makes, can need more (guard).
var maskRounds = 32

// maskJSON masks the parts of text at pointers, or all of it when pointers
// is nil, and reports whether it found a secret. Masking can make a secret
// that was not there: a masked number becomes a string, whose quote can
// meet the next token, and a masked key can move in the stored order
// (jsonval) next to another. So the masked text is read again, as it is and
// as stored, until a round changes nothing, and what returns is the last
// text masked: the stored text when that is where a secret showed. Those
// later rounds pass over a secret that lies wholly inside a mask, its
// quotes included, or inside a null written for one: such a secret, as one
// that the mask's own text holds, shows in every mask (mask's comment). An
// object or array that a part gave way to (cross) is a part from then on,
// so that its own edges are read too. After maskRounds rounds the parts
// still active are masked by guard.
func (s *Secrets) maskJSON(text []byte, pointers []string) ([]byte, bool) {
	if s == nil || len(text) == 0 {
		return text, false
	}
	out, found, up := s.maskOnce(text, pointers, false)
	active := withParts(pointers, up)
	for range maskRounds {
		next, f, up := s.maskOnce(out, active, true)
		if bytes.Equal(next, out) {
			st, ok := storedForm(out)
			if !ok {
				return out, found
			}
			if next, f, up = s.maskOnce(st, active, true); bytes.Equal(next, st) {
				return out, found
			}
		}
		found = found || f
		active = withParts(active, up)
		out = next
	}
	return s.guard(out, active), true
}

// withParts returns active, the pointers of the parts being masked, with
// those of up added, the objects or arrays that parts gave way to, in a new
// slice when it adds one, so the caller's pointers stay as they were. With
// no pointers, all of the value is the part, and stays so.
func withParts(active, up []string) []string {
	if active == nil || len(up) == 0 {
		return active
	}
	out := slices.Clone(active)
	seen := make(map[string]bool, len(active)+len(up))
	for _, p := range active {
		seen[p] = true
	}
	for _, p := range up {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// guard ends rounds that reached maskRounds. It masks each active part of
// out whole, or all of out when no pointer selects parts, and keeps the
// rest; if a secret then shows across a part's edge, it writes the parts as
// null instead; and if one shows still, it masks the whole value.
func (s *Secrets) guard(out []byte, active []string) []byte {
	if active == nil {
		return wholeParts(out, nil, false)
	}
	for _, null := range []bool{false, true} {
		g := wholeParts(out, active, null)
		if !s.showsAgain(g, active) {
			return g
		}
	}
	return wholeParts(out, nil, false)
}

// showsAgain reports whether a round of masking the parts of text at
// pointers would change it, as it is or as stored.
func (s *Secrets) showsAgain(text []byte, pointers []string) bool {
	if m, _, _ := s.maskOnce(text, pointers, true); !bytes.Equal(m, text) {
		return true
	}
	st, ok := storedForm(text)
	if !ok {
		return false
	}
	m, _, _ := s.maskOnce(st, pointers, true)
	return !bytes.Equal(m, st)
}

// storedForm returns the stored text of the JSON text b (jsonval), or false
// when it has none.
func storedForm(b []byte) ([]byte, bool) {
	st, err := jsonval.FromProducer(b).Stored()
	return st, err == nil
}

// wholeParts returns text, a JSON value, with each part at pointers, or all
// of it when pointers is nil, the string SecretMask, or null when null is
// set.
func wholeParts(text []byte, pointers []string, null bool) []byte {
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.UseNumber()
	root, err := decodeTree(dec, text)
	if err != nil {
		var b bytes.Buffer
		writeJSONString(&b, SecretMask)
		return b.Bytes()
	}
	for _, n := range root.parts(pointers) {
		if n.mask(); null {
			*n = jnode{kind: 'l', text: "null"}
		}
	}
	var b bytes.Buffer
	root.write(&b)
	return b.Bytes()
}

// parts returns the parts of n at pointers, or n itself when pointers is
// nil; a pointer that leads nowhere is passed over.
func (n *jnode) parts(pointers []string) []*jnode {
	if pointers == nil {
		return []*jnode{n}
	}
	var out []*jnode
	for _, ptr := range pointers {
		if p := n.at(ptr); p != nil {
			out = append(out, p)
		}
	}
	return out
}

// maskOnce masks the parts of text at pointers, or all of it, as it reads:
// as written, written compactly, its strings and keys as decoded, and its
// stored text, all read before anything changes. again is set for the
// rounds that read masked text again: they pass over a secret wholly
// inside a mask or a null written for one (masked).
func (s *Secrets) maskOnce(text []byte, pointers []string, again bool) ([]byte, bool, []string) {
	st, stored := storedForm(text)
	stored = stored && !bytes.Equal(st, text)
	// A secret can appear in a string only as itself, with escapes or
	// percent-encoded, but one that holds JSON's structure, a secret
	// document among them, in any spelling.
	plain := func(b []byte) bool {
		return !s.structural && bytes.IndexByte(b, '\\') < 0 && bytes.IndexByte(b, '%') < 0 && !s.holds(b)
	}
	if plain(text) && (!stored || plain(st)) {
		return text, false, nil
	}
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.UseNumber()
	root, err := decodeTree(dec, text)
	if err != nil {
		return text, false, nil
	}
	if _, err := dec.Token(); err != io.EOF {
		return text, false, nil
	}
	parts := root.parts(pointers)
	// Every secret is found on the value as it is, before anything changes:
	// in the text as written and in the value written compactly, which may
	// span tokens, and in each string and key as decoded.
	var compact bytes.Buffer
	root.writeIndexed(&compact)
	views := []struct {
		buf     []byte
		compact bool
		occ     []occurrence
	}{{buf: text}, {buf: compact.Bytes(), compact: true}}
	for i := range views {
		if s.holds(views[i].buf) {
			views[i].occ = s.occurrencesOutside(views[i].buf, again)
		}
	}
	idx := tokenIndex{}
	var up []string
	for _, n := range parts {
		s.markDocs(n)
		for _, v := range views {
			lo, hi := n.span(v.compact)
			for _, oc := range v.occ {
				switch {
				case oc.start >= lo && oc.end <= hi:
					n.mark(oc.start, oc.end, v.compact, v.buf, idx)
				case oc.start < hi && oc.end > lo:
					n.cross(root, oc, v.compact, v.buf, idx, again, &up)
				}
			}
		}
		s.markText(n, again)
	}
	// The stored text spells numbers in plain decimal, writes strings its own
	// way and orders keys, so it can hold a secret that the other readings do
	// not: what it finds goes over to the same places in the value.
	if stored && s.holds(st) {
		sdec := json.NewDecoder(bytes.NewReader(st))
		sdec.UseNumber()
		if sroot, err := decodeTree(sdec, st); err == nil {
			occ := s.occurrencesOutside(st, again)
			sidx := tokenIndex{}
			for _, n := range sroot.parts(pointers) {
				for _, oc := range occ {
					switch {
					case oc.start >= n.start && oc.end <= n.end:
						n.mark(oc.start, oc.end, false, st, sidx)
					case oc.start < n.end && oc.end > n.start:
						n.cross(sroot, oc, false, st, sidx, again, &up)
					}
				}
			}
			sroot.flagMarks()
			transfer(sroot, root)
		}
	}
	// The marks lie in the parts, and in an object or array around a part
	// that a crossing secret gave way to (cross).
	found := root.flagMarks()
	if !root.apply() {
		return text, found, up
	}
	var b bytes.Buffer
	root.write(&b)
	return b.Bytes(), found, up
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

// mask turns n into the string SecretMask, keeping where its source is.
// SecretMask is what a reader sees in place of a secret; a secret that it
// holds, such as cre or ]", shows in it and is not masked again (maskJSON).
func (n *jnode) mask() {
	*n = jnode{kind: 's', text: SecretMask, src: n.src, start: n.start, end: n.end}
}

// holds reports whether text holds one of the strings to mask.
func (s *Secrets) holds(text []byte) bool {
	return slices.ContainsFunc(s.olds, func(o string) bool { return bytes.Contains(text, []byte(o)) })
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
	// set, and keySrc where an object's keys are; cstart, cend and keyC are
	// the same in the value written compactly (writeIndexed).
	src          bool
	start, end   int
	keySrc       [][2]int
	cstart, cend int
	keyC         [][2]int
	// What masking found: all when the whole value is masked, the spans of
	// a string's text, and of each key's, as decoded; marked when any of
	// that holds for n or a part of it (flagMarks).
	all      bool
	null     bool
	spans    [][2]int
	keySpans map[int][][2]int
	marked   bool
}

// cross records a secret oc in buf that crosses the edge of n, a part
// being masked, into what lies beside it, which root holds: n is masked
// whole; a part masked whole already, whose mask then meets the data beside
// it, is written as null instead; and a null written so, when the secret
// still crosses its edge (again), gives way to the smallest object or array
// around both, which root masks whole and whose pointer up gets, so that
// the next rounds read its edges as a part's.
func (n *jnode) cross(root *jnode, oc occurrence, compact bool, buf []byte, idx tokenIndex, again bool, up *[]string) {
	switch {
	case n.kind == 's' && n.text == SecretMask:
		n.null = true
	case again && n.kind == 'l' && n.text == "null":
		*up = append(*up, root.markUp(oc.start, oc.end, compact))
	default:
		n.all = true
	}
}

// markUp marks whole the smallest object or array in n whose source, or
// compact writing, holds [a, b) across its tokens, and returns its JSON
// pointer relative to n.
func (n *jnode) markUp(a, b int, compact bool) string {
	var ptr strings.Builder
	for {
		j := sort.Search(len(n.kids), func(j int) bool { _, e := n.kids[j].span(compact); return e >= b })
		if j == len(n.kids) {
			break
		}
		k := n.kids[j]
		if s, _ := k.span(compact); s > a || k.kind != '{' && k.kind != '[' {
			break
		}
		tok := strconv.Itoa(j)
		if n.kind == '{' {
			tok = strings.NewReplacer("~", "~0", "/", "~1").Replace(n.keys[j])
		}
		ptr.WriteString("/" + tok)
		n = k
	}
	n.all = true
	return ptr.String()
}

// flagMarks sets marked on n and each of its parts where masking found
// something, and reports n's.
func (n *jnode) flagMarks() bool {
	n.marked = n.all || n.null || len(n.spans) > 0 || len(n.keySpans) > 0
	for _, k := range n.kids {
		if k.flagMarks() {
			n.marked = true
		}
	}
	return n.marked
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
// The part is then read again after sent, the end of what went out as
// masked, so that a secret that a mask makes with the text beside it, the
// part's own mask or one that went out, is masked too (settleText).
func (s *Secrets) maskPart(ctx, sent, buf []byte, c int) string {
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
	part := replaceSpans(string(buf[:c]), spans, nil, SecretMask)
	if len(spans) == 0 && !bytes.Contains(sent, []byte(SecretMask)) {
		return part
	}
	return settleText(string(sent), part, SecretMask, func(t string) [][2]int { return s.spans(t, false) })
}

// writeIndexed writes n compactly, as write does, and records where each
// value and key is in what it writes (cstart, cend, keyC).
func (n *jnode) writeIndexed(b *bytes.Buffer) {
	n.cstart = b.Len()
	switch n.kind {
	case '{', '[':
		b.WriteByte(n.kind)
		n.keyC = n.keyC[:0]
		for i, k := range n.kids {
			if i > 0 {
				b.WriteByte(',')
			}
			if n.kind == '{' {
				ks := b.Len()
				writeJSONString(b, n.keys[i])
				n.keyC = append(n.keyC, [2]int{ks, b.Len()})
				b.WriteByte(':')
			}
			k.writeIndexed(b)
		}
		b.WriteByte(n.kind + 2) // '}' and ']' follow '{' and '[' by two
	case 's':
		writeJSONString(b, n.text)
	default:
		b.WriteString(n.text)
	}
	n.cend = b.Len()
}

// span returns where n is in the source, or in the value written compactly
// when compact is set.
func (n *jnode) span(compact bool) (int, int) {
	if compact {
		return n.cstart, n.cend
	}
	if !n.src {
		return 0, -1
	}
	return n.start, n.end
}

// mark records the secret at [a, b) of buf, the source or the value written
// compactly, in the smallest part of n that holds it: a string or a key,
// the span of its text under the secret; a number or a literal, whole; an
// object or array across whose tokens it lies, whole.
func (n *jnode) mark(a, b int, compact bool, buf []byte, idx tokenIndex) {
	for !n.all {
		lo, hi := n.span(compact)
		switch n.kind {
		case 's':
			n.spans = append(n.spans, idx.span(buf, lo, hi, compact, a, b, len(n.text)))
			return
		case 'l':
			n.all = true
			return
		}
		keys := n.keySrc
		if compact {
			keys = n.keyC
		}
		if i := sort.Search(len(keys), func(i int) bool { return keys[i][1] >= b }); i < len(keys) && keys[i][0] <= a && i < len(n.keys) {
			if n.keySpans == nil {
				n.keySpans = map[int][][2]int{}
			}
			n.keySpans[i] = append(n.keySpans[i], idx.span(buf, keys[i][0], keys[i][1], compact, a, b, len(n.keys[i])))
			return
		}
		j := sort.Search(len(n.kids), func(j int) bool { _, e := n.kids[j].span(compact); return e >= b })
		if j < len(n.kids) {
			if s, _ := n.kids[j].span(compact); s <= a {
				n = n.kids[j]
				continue
			}
		}
		n.all = true
	}
}

// markDocs marks whole each object or array in n that equals a secret
// document.
func (s *Secrets) markDocs(n *jnode) {
	if n.kind != '{' && n.kind != '[' {
		return
	}
	for _, d := range s.docs {
		if nodeEqual(n, d) {
			n.all = true
			return
		}
	}
	for _, k := range n.kids {
		s.markDocs(k)
	}
}

// markText records, in each string and key of n, the spans of its text
// that hold a secret as Mask finds them.
func (s *Secrets) markText(n *jnode, again bool) {
	if n.kind == 's' {
		n.spans = append(n.spans, outsideMasks(n.text, s.spans(n.text, false), again)...)
	}
	for i, k := range n.keys {
		if sp := outsideMasks(k, s.spans(k, false), again); len(sp) > 0 {
			if n.keySpans == nil {
				n.keySpans = map[int][][2]int{}
			}
			n.keySpans[i] = append(n.keySpans[i], sp...)
		}
	}
	for _, k := range n.kids {
		s.markText(k, again)
	}
}

// outsideMasks returns spans, the secrets in text, less those wholly inside
// a mask in text when again is set (maskOnce).
func outsideMasks(text string, spans [][2]int, again bool) [][2]int {
	if !again || !strings.Contains(text, SecretMask) {
		return spans
	}
	regions := maskRegions([]byte(text), false)
	var out [][2]int
	for _, sp := range spans {
		if !insideAny(sp[0], sp[1], regions) {
			out = append(out, sp)
		}
	}
	return out
}

// occurrencesOutside returns the occurrences of each secret in b, a JSON
// text, less, when again is set (maskOnce), those wholly inside a mask, its
// quotes included, or inside a null literal: masking wrote those, and a
// secret there is one that the mask or the null itself holds.
func (s *Secrets) occurrencesOutside(b []byte, again bool) []occurrence {
	occ := s.occurrences(string(b))
	if !again {
		return occ
	}
	regions := maskRegions(b, true)
	var out []occurrence
	for _, oc := range occ {
		if !insideAny(oc.start, oc.end, regions) {
			out = append(out, oc)
		}
	}
	return out
}

// maskRegions returns where SecretMask is in b and, when json is set, the
// quotes right around it and each null literal, sorted by their start.
func maskRegions(b []byte, json bool) [][2]int {
	var out [][2]int
	for i := 0; ; {
		j := bytes.Index(b[i:], []byte(SecretMask))
		if j < 0 {
			break
		}
		a, e := i+j, i+j+len(SecretMask)
		if json && a > 0 && b[a-1] == '"' {
			a--
		}
		if json && e < len(b) && b[e] == '"' {
			e++
		}
		out = append(out, [2]int{a, e})
		i = i + j + 1
	}
	if json {
		word := func(c byte) bool {
			return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '"'
		}
		for i := 0; ; {
			j := bytes.Index(b[i:], []byte("null"))
			if j < 0 {
				break
			}
			a, e := i+j, i+j+4
			if (a == 0 || !word(b[a-1])) && (e == len(b) || !word(b[e])) {
				out = append(out, [2]int{a, e})
			}
			i = i + j + 1
		}
	}
	slices.SortFunc(out, func(x, y [2]int) int { return x[0] - y[0] })
	return out
}

// insideAny reports whether [a, b) lies wholly inside one of regions,
// which maskRegions sorts by their start. A region that starts later also
// ends later, since two masks share at most a quote and a null meets no
// mask, so the last region that starts at or before a holds [a, b) if any
// region does.
func insideAny(a, b int, regions [][2]int) bool {
	i := sort.Search(len(regions), func(i int) bool { return regions[i][0] > a }) - 1
	return i >= 0 && b <= regions[i][1]
}

// apply masks what mark, markDocs and markText found in n, and reports
// whether it changed n.
func (n *jnode) apply() bool {
	if n.null {
		*n = jnode{kind: 'l', text: "null", src: n.src, start: n.start, end: n.end}
		return true
	}
	if n.all {
		n.mask()
		return true
	}
	changed := false
	if n.kind == 's' && len(n.spans) > 0 {
		if m := replaceSpans(n.text, mergeSpans(n.spans), nil, SecretMask); m != n.text {
			n.text, changed = m, true
		}
	}
	for i, sp := range n.keySpans {
		if m := replaceSpans(n.keys[i], mergeSpans(sp), nil, SecretMask); m != n.keys[i] {
			n.keys[i], changed = m, true
		}
	}
	// What was found is spent: the spans index the text as it was.
	n.spans, n.keySpans = nil, nil
	for _, k := range n.kids {
		changed = k.apply() || changed
	}
	return changed
}

// transfer carries what masking found in t, a value as its stored text
// decodes, over to the same places in n, the value as written: by index in
// an array and by key in an object, the last of a repeated key, which is
// the one the stored text keeps, found through a map of n's keys built once
// per object. A part of t with nothing found (flagMarks) is passed over.
func transfer(t, n *jnode) {
	if !t.marked {
		return
	}
	if t.null {
		n.null = true
		return
	}
	if t.all {
		n.all = true
		return
	}
	if t.kind == 's' && n.kind == 's' {
		n.spans = append(n.spans, t.spans...)
	}
	switch {
	case t.kind == '[' && n.kind == '[':
		for i, k := range t.kids {
			if i < len(n.kids) {
				transfer(k, n.kids[i])
			}
		}
	case t.kind == '{' && n.kind == '{':
		last := make(map[string]int, len(n.keys))
		for j, key := range n.keys {
			last[key] = j
		}
		for i, key := range t.keys {
			j, ok := last[key]
			if !ok {
				continue
			}
			if sp := t.keySpans[i]; len(sp) > 0 {
				if n.keySpans == nil {
					n.keySpans = map[int][][2]int{}
				}
				n.keySpans[j] = append(n.keySpans[j], sp...)
			}
			transfer(t.kids[i], n.kids[j])
		}
	}
}

// tokenSpan returns the span of a string's text, n bytes as decoded, that
// the bytes [a, b) of its JSON token tok cover, when tok has no escape:
// decoded byte i is at tok[i+1], and the quotes cover none.
func tokenSpan(tok []byte, a, b, n int) [2]int {
	if lo, hi := max(a-1, 0), min(b-1, n); lo < hi {
		return [2]int{lo, hi}
	}
	return [2]int{0, 0}
}

// tokenIndex keeps the tokenOffsets of each escaped string token that
// masking looks into, by reading and start, built once however many
// secrets the token holds.
type tokenIndex map[[2]int]*tokenOffsets

// tokenOffsets maps an escaped token's byte offsets to its decoded bytes:
// first[o] is the first decoded byte whose spelling, a character or an
// escape, ends after offset o, and upto[o] the number of decoded bytes
// whose spelling starts before o. whole is set when the token does not
// decode to the text's length, which a token with invalid UTF-8 may not.
type tokenOffsets struct {
	first, upto []int
	whole       bool
}

// span returns the span of a string's text, n bytes as decoded, that the
// bytes [a, b) of buf cover in the token at [lo, hi) of buf, read compact
// or not: each decoded byte whose spelling overlaps them. Each lookup is
// one index into the token's offsets.
func (idx tokenIndex) span(buf []byte, lo, hi int, compact bool, a, b, n int) [2]int {
	tok := buf[lo:hi]
	if len(tok) == n+2 {
		return tokenSpan(tok, a-lo, b-lo, n)
	}
	key := [2]int{lo, 0}
	if compact {
		key[1] = 1
	}
	t, ok := idx[key]
	if !ok {
		t = offsetsOf(tok, n)
		idx[key] = t
	}
	if t.whole {
		return [2]int{0, n}
	}
	a, b = min(max(a-lo, 0), len(tok)), min(max(b-lo, 0), len(tok))
	if f, u := t.first[a], t.upto[b]; f < u {
		return [2]int{f, u}
	}
	return [2]int{0, 0}
}

// offsetsOf builds the tokenOffsets of tok, n bytes as decoded, in one pass
// over its map (tokenMap), whose starts and ends do not decrease.
func offsetsOf(tok []byte, n int) *tokenOffsets {
	st, en := tokenMap(tok)
	if len(st) != n {
		return &tokenOffsets{whole: true}
	}
	t := &tokenOffsets{first: make([]int, len(tok)+1), upto: make([]int, len(tok)+1)}
	i, j := 0, 0
	for o := 0; o <= len(tok); o++ {
		for i < n && en[i] <= o {
			i++
		}
		for j < n && st[j] < o {
			j++
		}
		t.first[o], t.upto[o] = i, j
	}
	return t
}

// tokenMap returns, for each byte of a JSON string token's decoded text,
// where its spelling starts and ends in the token: one byte for a plain
// byte, the whole escape for each byte that an escape decodes to.
func tokenMap(tok []byte) (st, en []int) {
	for i := 1; i < len(tok)-1; {
		if tok[i] != '\\' || i+1 >= len(tok)-1 {
			st, en = append(st, i), append(en, i+1)
			i++
			continue
		}
		size, r := 2, rune(0)
		if tok[i+1] == 'u' && i+6 <= len(tok)-1 {
			r, size = hexRune(tok[i+2:i+6]), 6
			if utf16.IsSurrogate(r) {
				if i+12 <= len(tok)-1 && tok[i+6] == '\\' && tok[i+7] == 'u' {
					if p := utf16.DecodeRune(r, hexRune(tok[i+8:i+12])); p != utf8.RuneError {
						r, size = p, 12
					} else {
						r = utf8.RuneError
					}
				} else {
					r = utf8.RuneError
				}
			}
		}
		k := 1
		if tok[i+1] == 'u' {
			k = utf8.RuneLen(r)
		}
		for range k {
			st, en = append(st, i), append(en, i+size)
		}
		i += size
	}
	return st, en
}

// hexRune is the rune that four hex digits spell, or utf8.RuneError.
func hexRune(h []byte) rune {
	v, err := strconv.ParseUint(string(h), 16, 32)
	if err != nil {
		return utf8.RuneError
	}
	return rune(v)
}
