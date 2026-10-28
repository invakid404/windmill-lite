package conformance

import (
	"errors"
	"fmt"
	"strings"
)

// The markers a normaliser writes into strings, besides job placeholders:
// a masked runtime stack (OR-V3), the masked location suffix of the one
// recognised internal error (OR-27), and a value masked by a fixture path
// (OR-14 item 8).
const (
	MarkerStack  = "<stack>"
	MarkerLoc    = "<loc>"
	MarkerMasked = "<masked>"
)

// markerHeads are the texts that follow the "<" of a marker: a job
// placeholder ("<job:" + path + ">") and the three fixed markers.
var markerHeads = [...]string{"job:", "stack>", "loc>", "masked>"}

// ErrInvalidText is wrapped by every error ParseText returns.
var ErrInvalidText = errors.New("invalid normalised text")

// TextPart is one part of a normalised string: literal text, the
// placeholder of a walked job, or a marker. Exactly one field is set.
type TextPart struct {
	// Literal is user text, kept as it was.
	Literal string
	// Job is the site of a walked job whose id stood here (ADR 0011).
	Job *SitePath
	// Marker is MarkerStack, MarkerLoc or MarkerMasked.
	Marker string
}

// Literal returns a literal part.
func Literal(s string) TextPart { return TextPart{Literal: s} }

// JobPart returns the part of a walked job at site p.
func JobPart(p SitePath) TextPart { return TextPart{Job: &p} }

// MarkerPart returns a marker part. It panics on anything but the three
// markers.
func MarkerPart(m string) TextPart {
	if !isMarker(m) {
		panic("conformance: unknown marker " + m)
	}
	return TextPart{Marker: m}
}

func isMarker(m string) bool {
	return m == MarkerStack || m == MarkerLoc || m == MarkerMasked
}

// SplitJobIDs splits raw text at every occurrence of a walked job id and
// returns the parts: literal text between the occurrences, and a job part
// for each id. Ids are matched exactly, so other UUIDs stay literal (OR-14
// item 1, OR-V3/a). At each position the longest matching id wins.
func SplitJobIDs(raw string, ids map[string]SitePath) []TextPart {
	if len(ids) == 0 || raw == "" {
		return []TextPart{Literal(raw)}
	}
	lengths := make(map[int]bool)
	for id := range ids {
		if id != "" {
			lengths[len(id)] = true
		}
	}
	var parts []TextPart
	start := 0
	for i := 0; i < len(raw); {
		best := 0
		for n := range lengths {
			if n > best && i+n <= len(raw) {
				if _, ok := ids[raw[i:i+n]]; ok {
					best = n
				}
			}
		}
		if best == 0 {
			i++
			continue
		}
		if start < i {
			parts = append(parts, Literal(raw[start:i]))
		}
		parts = append(parts, JobPart(ids[raw[i:i+best]]))
		i += best
		start = i
	}
	if start < len(raw) || len(parts) == 0 {
		parts = append(parts, Literal(raw[start:]))
	}
	return parts
}

// EncodeText writes parts as one normalised string. Placeholders and
// markers are written as they are; literal text is escaped so that the
// result parses back to the same parts (ParseText), which makes the
// normalised string injective in the raw text and the walked ids.
//
// The escape: every maximal run of "<" in literal text that is followed by
// a placeholder, a marker, or literal text that begins like one ("job:",
// "stack>", "loc>", "masked>") is written twice as long. Every other "<"
// is written once, so text without such a run is unchanged. A reader counts
// the run of "<" before such a head: an odd count ends in a placeholder or
// marker, an even count is literal. This replaces the drafted "<<job:"
// rule of OR-14 item 2, which a user "<" before a walked id defeats
// (ADR 0011).
func EncodeText(parts []TextPart) string {
	parts = mergeLiterals(parts)
	var b strings.Builder
	for i, p := range parts {
		switch {
		case p.Job != nil:
			b.WriteString(p.Job.Placeholder())
		case p.Marker != "":
			if !isMarker(p.Marker) {
				panic("conformance: unknown marker " + p.Marker)
			}
			b.WriteString(p.Marker)
		default:
			nextIsMarker := i+1 < len(parts)
			writeLiteral(&b, p.Literal, nextIsMarker)
		}
	}
	return b.String()
}

// mergeLiterals joins adjacent literal parts and drops empty ones, so runs
// of "<" are maximal.
func mergeLiterals(parts []TextPart) []TextPart {
	out := make([]TextPart, 0, len(parts))
	for _, p := range parts {
		if p.Job == nil && p.Marker == "" {
			if p.Literal == "" {
				continue
			}
			if n := len(out); n > 0 && out[n-1].Job == nil && out[n-1].Marker == "" {
				out[n-1].Literal += p.Literal
				continue
			}
		}
		out = append(out, p)
	}
	return out
}

// writeLiteral writes s, doubling each run of "<" that a head follows. A
// run at the end of s is followed by the next part, which doubles it when
// that part is a placeholder or marker.
func writeLiteral(b *strings.Builder, s string, nextIsMarker bool) {
	for i := 0; i < len(s); {
		if s[i] != '<' {
			b.WriteByte(s[i])
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] == '<' {
			j++
		}
		run := j - i
		if (j == len(s) && nextIsMarker) || hasHead(s[j:]) {
			run *= 2
		}
		b.WriteString(strings.Repeat("<", run))
		i = j
	}
}

// hasHead reports whether s begins with a marker head.
func hasHead(s string) bool {
	for _, h := range markerHeads {
		if strings.HasPrefix(s, h) {
			return true
		}
	}
	return false
}

// ParseText reads a normalised string back into parts. It is the inverse
// of EncodeText: EncodeText(ParseText(s)) == s for every s it accepts, and
// ParseText(EncodeText(parts)) gives parts back with adjacent literals
// merged. It fails on a placeholder whose path is not a canonical site path
// or that has no closing ">".
func ParseText(s string) ([]TextPart, error) {
	var parts []TextPart
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			parts = append(parts, Literal(lit.String()))
			lit.Reset()
		}
	}
	for i := 0; i < len(s); {
		if s[i] != '<' {
			lit.WriteByte(s[i])
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] == '<' {
			j++
		}
		run := j - i
		if !hasHead(s[j:]) {
			lit.WriteString(s[i:j])
			i = j
			continue
		}
		lit.WriteString(strings.Repeat("<", run/2))
		if run%2 == 0 {
			i = j
			continue
		}
		flush()
		rest := s[j:]
		if strings.HasPrefix(rest, "job:") {
			end := strings.IndexByte(rest, '>')
			if end < 0 {
				return nil, fmt.Errorf("%w: placeholder at byte %d has no closing \">\"", ErrInvalidText, j-1)
			}
			p, err := ParseSitePath(strings.ReplaceAll(rest[len("job:"):end], "%3E", ">"))
			if err != nil {
				return nil, fmt.Errorf("%w: placeholder at byte %d: %v", ErrInvalidText, j-1, err)
			}
			parts = append(parts, JobPart(p))
			i = j + end + 1
			continue
		}
		for _, m := range []string{MarkerStack, MarkerLoc, MarkerMasked} {
			if strings.HasPrefix(rest, m[1:]) {
				parts = append(parts, MarkerPart(m))
				i = j + len(m) - 1
				break
			}
		}
	}
	flush()
	if len(parts) == 0 {
		parts = append(parts, Literal(""))
	}
	return parts, nil
}
