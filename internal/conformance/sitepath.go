package conformance

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// SegmentKind is the kind of one site-path segment (ADR 0011).
type SegmentKind byte

// The segment kinds, written as the first character of a segment.
const (
	// KindModule names a module in a module list by its id.
	KindModule SegmentKind = 'm'
	// KindIteration is a loop iteration index.
	KindIteration SegmentKind = 'i'
	// KindBranch is a branch: an index, or default for branchone.
	KindBranch SegmentKind = 'b'
	// KindSlot is a special slot: the failure or the preprocessor module.
	KindSlot SegmentKind = 's'
	// KindAttempt is an attempt number of a retried module, from 0.
	KindAttempt SegmentKind = 'r'
)

const (
	slotFailure      = "failure"
	slotPreprocessor = "preprocessor"
	branchDefault    = "default"
	noOccurrence     = -1
)

// ErrInvalidSitePath is wrapped by every error ParseSitePath returns.
var ErrInvalidSitePath = errors.New("invalid site path")

// Segment is one typed segment of a site path. Build segments with the
// constructors below; the zero Segment is not a segment, and SitePath.Child
// panics on it. Segments are comparable with ==.
type Segment struct {
	kind  SegmentKind
	value string // module id, canonical decimal, "default", or slot name
	occ   int    // KindModule only: occurrence k, or noOccurrence for a unique id
}

// Module is the segment of a module whose id occurs once in its module list.
// The id may be any string: Windmill does not restrict module ids (OF-2).
func Module(id string) Segment {
	return Segment{kind: KindModule, value: id, occ: noOccurrence}
}

// RepeatedModule is the segment of the occurrence k (from 0, in list order)
// of a module id that occurs more than once in its module list. It panics if
// k is negative.
func RepeatedModule(id string, k int) Segment {
	if k < 0 {
		panic("conformance: negative module occurrence")
	}
	return Segment{kind: KindModule, value: id, occ: k}
}

// ModuleSegments returns the segment of each module of one module list, given
// the module ids in list order: an id that occurs once gets Module, an id
// that occurs more than once gets RepeatedModule with its occurrence.
func ModuleSegments(ids []string) []Segment {
	count := make(map[string]int, len(ids))
	for _, id := range ids {
		count[id]++
	}
	seen := make(map[string]int)
	segs := make([]Segment, len(ids))
	for i, id := range ids {
		if count[id] == 1 {
			segs[i] = Module(id)
			continue
		}
		segs[i] = RepeatedModule(id, seen[id])
		seen[id]++
	}
	return segs
}

// Iteration is the segment of loop iteration n. It panics if n is negative.
func Iteration(n int) Segment { return indexSegment(KindIteration, n) }

// Branch is the segment of branch n of a branchone or branchall module. It
// panics if n is negative.
func Branch(n int) Segment { return indexSegment(KindBranch, n) }

// DefaultBranch is the segment of a branchone module's default branch.
func DefaultBranch() Segment {
	return Segment{kind: KindBranch, value: branchDefault, occ: noOccurrence}
}

// FailureSlot is the segment of a flow's failure module, whatever its id.
func FailureSlot() Segment {
	return Segment{kind: KindSlot, value: slotFailure, occ: noOccurrence}
}

// PreprocessorSlot is the segment of a flow's preprocessor module, whatever
// its id.
func PreprocessorSlot() Segment {
	return Segment{kind: KindSlot, value: slotPreprocessor, occ: noOccurrence}
}

// Attempt is the segment of attempt n of a retried module, counted from 0.
// It panics if n is negative.
func Attempt(n int) Segment { return indexSegment(KindAttempt, n) }

func indexSegment(kind SegmentKind, n int) Segment {
	if n < 0 {
		panic("conformance: negative site-path index")
	}
	return Segment{kind: kind, value: strconv.Itoa(n), occ: noOccurrence}
}

// Kind returns the segment's kind.
func (s Segment) Kind() SegmentKind { return s.kind }

// Value returns the segment's decoded value: the module id for KindModule,
// the decimal index for KindIteration, KindAttempt and an indexed
// KindBranch, "default" for the default branch, and "failure" or
// "preprocessor" for KindSlot.
func (s Segment) Value() string { return s.value }

// Occurrence returns the occurrence of a repeated module id, and false for
// a unique id and for every other kind.
func (s Segment) Occurrence() (int, bool) {
	if s.kind != KindModule || s.occ == noOccurrence {
		return 0, false
	}
	return s.occ, true
}

// Index returns the number of an iteration, an attempt or an indexed branch,
// and false for every other segment.
func (s Segment) Index() (int, bool) {
	switch s.kind {
	case KindIteration, KindAttempt, KindBranch:
		n, err := strconv.Atoi(s.value)
		return n, err == nil
	}
	return 0, false
}

// String returns the segment as it appears in a site path, for example
// "m:a~1" or "i:2".
func (s Segment) String() string {
	var b strings.Builder
	s.appendTo(&b)
	return b.String()
}

func (s Segment) appendTo(b *strings.Builder) {
	b.WriteByte(byte(s.kind))
	b.WriteByte(':')
	if s.kind != KindModule {
		b.WriteString(s.value)
		return
	}
	escapeValue(b, s.value)
	if s.occ != noOccurrence {
		b.WriteByte('~')
		b.WriteString(strconv.Itoa(s.occ))
	}
}

// escapeValue writes v with the four escapes of ADR 0011. The escaped bytes
// are ASCII, so working byte by byte never splits a UTF-8 sequence.
func escapeValue(b *strings.Builder, v string) {
	for i := 0; i < len(v); i++ {
		switch c := v[i]; c {
		case '%':
			b.WriteString("%25")
		case '/':
			b.WriteString("%2F")
		case ':':
			b.WriteString("%3A")
		case '~':
			b.WriteString("%7E")
		default:
			b.WriteByte(c)
		}
	}
}

// SitePath is the identity of a call site in a run: a sequence of typed
// segments (ADR 0011). The zero SitePath is the root, the run itself.
//
// A SitePath holds its canonical text, so two paths are equal (==) exactly
// when their segments are, and a SitePath can key a map. Every value of the
// type is valid: it can only be built by Child or by ParseSitePath.
type SitePath struct {
	text string
}

// Child returns p extended by segs. It panics if a segment is the zero
// Segment.
func (p SitePath) Child(segs ...Segment) SitePath {
	var b strings.Builder
	b.WriteString(p.text)
	for _, s := range segs {
		if s.kind == 0 {
			panic("conformance: zero Segment in site path")
		}
		if b.Len() > 0 {
			b.WriteByte('/')
		}
		s.appendTo(&b)
	}
	return SitePath{text: b.String()}
}

// IsRoot reports whether p is the root (no segments).
func (p SitePath) IsRoot() bool { return p.text == "" }

// String returns the canonical text of p, for example "m:outer/i:1/m:s".
// The root is "".
func (p SitePath) String() string { return p.text }

// Segments returns the segments of p.
func (p SitePath) Segments() []Segment {
	segs, err := parseSegments(p.text)
	if err != nil {
		// Unreachable: a SitePath is built only from valid segments.
		panic(err)
	}
	return segs
}

// Placeholder returns the job-id placeholder for p, "<job:" + p + ">", with
// every ">" inside p written as "%3E". p's text never contains "%3E" (a
// literal "%" is escaped as "%25"), so the replacement keeps placeholders
// distinct, and a placeholder always ends at its first ">" even when a
// module id contains one.
func (p SitePath) Placeholder() string {
	return "<job:" + strings.ReplaceAll(p.text, ">", "%3E") + ">"
}

// MarshalText returns p's canonical text, so a SitePath can be a JSON
// object key. It fails if the text is not valid UTF-8, because JSON would
// replace the invalid bytes and two distinct paths could then collide.
// Module ids decoded from YAML or JSON are always valid UTF-8.
func (p SitePath) MarshalText() ([]byte, error) {
	if !utf8.ValidString(p.text) {
		return nil, fmt.Errorf("conformance: site path %q is not valid UTF-8", p.text)
	}
	return []byte(p.text), nil
}

// UnmarshalText parses canonical site-path text, as ParseSitePath does.
func (p *SitePath) UnmarshalText(text []byte) error {
	q, err := ParseSitePath(string(text))
	if err != nil {
		return err
	}
	*p = q
	return nil
}

// ParseSitePath parses the canonical text of a site path. It accepts exactly
// the texts that Child produces, so parsing and re-encoding a path gives
// back the same text, and rejects every other spelling: unknown kinds, empty
// segments, lower-case or unknown escapes, an unescaped ":" or "~" in a
// module id, a malformed occurrence, and numbers with a sign or a leading
// zero. The empty text is the root.
func ParseSitePath(text string) (SitePath, error) {
	if _, err := parseSegments(text); err != nil {
		return SitePath{}, err
	}
	return SitePath{text: text}, nil
}

func parseSegments(text string) ([]Segment, error) {
	if text == "" {
		return nil, nil
	}
	parts := strings.Split(text, "/")
	segs := make([]Segment, len(parts))
	for i, part := range parts {
		s, reason := parseSegment(part)
		if reason != "" {
			return nil, fmt.Errorf("%w %q: segment %d (%q): %s", ErrInvalidSitePath, text, i, part, reason)
		}
		segs[i] = s
	}
	return segs, nil
}

// parseSegment parses one segment and returns a non-empty reason when it is
// not canonical.
func parseSegment(part string) (Segment, string) {
	if part == "" {
		return Segment{}, "empty segment"
	}
	if len(part) < 2 || part[1] != ':' {
		return Segment{}, "expected <kind>:<value>"
	}
	kind, raw := SegmentKind(part[0]), part[2:]
	switch kind {
	case KindModule:
		idText, occText, repeated := strings.Cut(raw, "~")
		id, reason := unescapeValue(idText)
		if reason != "" {
			return Segment{}, reason
		}
		if !repeated {
			return Module(id), ""
		}
		k, ok := parseIndex(occText)
		if !ok {
			return Segment{}, "occurrence must be a decimal number without sign or leading zero"
		}
		return RepeatedModule(id, k), ""
	case KindIteration, KindAttempt:
		n, ok := parseIndex(raw)
		if !ok {
			return Segment{}, "index must be a decimal number without sign or leading zero"
		}
		return indexSegment(kind, n), ""
	case KindBranch:
		if raw == branchDefault {
			return DefaultBranch(), ""
		}
		n, ok := parseIndex(raw)
		if !ok {
			return Segment{}, `branch must be "default" or a decimal number without sign or leading zero`
		}
		return Branch(n), ""
	case KindSlot:
		switch raw {
		case slotFailure:
			return FailureSlot(), ""
		case slotPreprocessor:
			return PreprocessorSlot(), ""
		}
		return Segment{}, `slot must be "failure" or "preprocessor"`
	}
	return Segment{}, "unknown segment kind"
}

// parseIndex parses a canonical non-negative decimal number.
func parseIndex(s string) (int, bool) {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// unescapeValue reverses escapeValue, accepting only its output.
func unescapeValue(s string) (string, string) {
	if !strings.ContainsAny(s, "%:") {
		return s, ""
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case ':':
			return "", `unescaped ":" in module id`
		case '%':
			if i+3 > len(s) {
				return "", "truncated escape"
			}
			switch s[i+1 : i+3] {
			case "25":
				b.WriteByte('%')
			case "2F":
				b.WriteByte('/')
			case "3A":
				b.WriteByte(':')
			case "7E":
				b.WriteByte('~')
			default:
				return "", "escape must be %25, %2F, %3A or %7E"
			}
			i += 2
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), ""
}
