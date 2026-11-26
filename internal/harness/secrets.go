package harness

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"strconv"
	"strings"
)

// SecretMask is what replaces a secret's value in the text that the engine
// passes on from a harness process (Secrets).
const SecretMask = "[secret]"

// Secrets are the values of a host's secret environment entries
// (ProcessConfig.Secrets), which the engine masks with SecretMask in the
// text that it takes from a harness process and passes on. It masks each
// value, and each line of a value that spans lines, since the process's
// output reaches the engine one line at a time. It also masks each of them
// as it reads inside a Go quoted string (%q, strconv.Quote), since an
// error text can quote a value; for a quote, a backslash, a newline or a
// tab, a JSON string writes the same. A nil *Secrets masks nothing, and
// its methods return their input.
type Secrets struct {
	// olds are the strings to mask, the longest first, so that a secret
	// that holds another is masked whole: a Replacer tries its old strings
	// in argument order at each position.
	olds    []string
	r       *strings.Replacer
	longest int
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
	}
	for _, v := range values {
		forms(v)
		if strings.Contains(v, "\n") {
			for _, l := range strings.Split(v, "\n") {
				forms(strings.TrimSuffix(l, "\r"))
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
	return &Secrets{olds: olds, r: strings.NewReplacer(pairs...), longest: len(olds[0])}
}

// unquote is a quoted string without its quotes.
func unquote(q string) string {
	if len(q) < 2 {
		return ""
	}
	return q[1 : len(q)-1]
}

// Mask returns text with each occurrence of a secret replaced by
// SecretMask.
func (s *Secrets) Mask(text string) string {
	if s == nil {
		return text
	}
	return s.r.Replace(text)
}

// MaskJSON returns the JSON text with each occurrence of a secret masked in
// its strings, object keys included, as decoded, so that a secret written
// with escapes is masked too. A number or a literal whose text holds a
// secret, such as a secret of digits that a step returned as a number,
// becomes the string SecretMask, so the text stays JSON. The other numbers
// and literals, key order and duplicate keys stay as they were. It returns
// text itself, byte for byte, when nothing holds a secret, and also when
// text is not JSON.
func (s *Secrets) MaskJSON(text []byte) []byte {
	if s == nil || len(text) == 0 {
		return text
	}
	// A secret can appear in a string only as itself or with escapes.
	if bytes.IndexByte(text, '\\') < 0 && !slices.ContainsFunc(s.olds, func(o string) bool { return bytes.Contains(text, []byte(o)) }) {
		return text
	}
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.UseNumber()
	type level struct {
		object bool
		n      int
	}
	var out bytes.Buffer
	var stack []level
	changed := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return text
		}
		if d, ok := tok.(json.Delim); ok && (d == '}' || d == ']') {
			out.WriteByte(byte(d))
			stack = stack[:len(stack)-1]
			continue
		}
		if len(stack) > 0 {
			top := &stack[len(stack)-1]
			switch {
			case top.object && top.n%2 == 1:
				out.WriteByte(':')
			case top.n > 0:
				out.WriteByte(',')
			}
			top.n++
		}
		switch v := tok.(type) {
		case json.Delim:
			out.WriteByte(byte(v))
			stack = append(stack, level{object: v == '{'})
		case string:
			m := s.r.Replace(v)
			if m != v {
				changed = true
			}
			writeJSONString(&out, m)
		default:
			// A number keeps its text; a literal is true, false or null.
			lit := "null"
			switch v := v.(type) {
			case json.Number:
				lit = string(v)
			case bool:
				lit = strconv.FormatBool(v)
			}
			if s.r.Replace(lit) != lit {
				changed = true
				writeJSONString(&out, SecretMask)
			} else {
				out.WriteString(lit)
			}
		}
	}
	if !changed {
		return text
	}
	return out.Bytes()
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
