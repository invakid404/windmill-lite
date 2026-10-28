package yamlx

import (
	"bytes"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// prepare checks that the source is UTF-8, drops a leading byte order
// mark, hides from the YAML 1.1 parser the characters it reads differently
// from the CLI, and blanks %YAML 1.2 directives, which the YAML 1.1 parser
// refuses. The replacer it returns, when not nil, puts the hidden
// characters back into parsed text.
func prepare(src []byte) ([]byte, *strings.Replacer, error) {
	src = bytes.TrimPrefix(src, []byte{0xEF, 0xBB, 0xBF})
	line, col := 1, 1
	hide := map[rune]bool{}
	used := map[rune]bool{} // private-use characters the source holds or escapes
	for i := 0; i < len(src); {
		r, size := utf8.DecodeRune(src[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			return nil, nil, &Error{Kind: Unsupported, Line: line, Column: col, Msg: "the source is not valid UTF-8"}
		case r == '\r' && (i+1 == len(src) || src[i+1] != '\n'):
			// The CLI's parser reads a lone CR as an ordinary character in
			// most places but as a separator after an indicator such as
			// ':'; the YAML 1.1 parser reads it as a line break. The CLI's
			// writer escapes it.
			return nil, nil, &Error{Kind: Unsupported, Line: line, Column: col, Msg: "a carriage return without a line feed"}
		case hidden(r):
			hide[r] = true
		case isPrivateUse(r):
			used[r] = true
		case r == '\\':
			if e, ok := escaped(src[i:]); ok && isPrivateUse(e) {
				used[e] = true
			}
		}
		if r == '\n' {
			line, col = line+1, 1
		} else if r != '\r' {
			col++
		}
		i += size
	}
	var restore *strings.Replacer
	if len(hide) > 0 {
		var err error
		if src, restore, err = hideCharacters(src, hide, used); err != nil {
			return nil, nil, err
		}
	}
	src, err := directives(src)
	return src, restore, err
}

// hidden reports whether the YAML 1.1 parser refuses r or reads it as a
// line break, where the CLI's parser reads an ordinary character: the C0
// controls other than tab, LF and CR; DEL; the C1 controls, NEL among
// them; LS and PS; U+FEFF after the start; U+FFFE and U+FFFF. The CLI's
// writer emits DEL, LS, PS and U+FEFF unescaped.
func hidden(r rune) bool {
	switch {
	case r < 0x20:
		return r != '\t' && r != '\n' && r != '\r'
	case r >= 0x7F && r <= 0x9F:
		return true
	}
	return r == '\u2028' || r == '\u2029' || r == 0xFEFF || r == 0xFFFE || r == 0xFFFF
}

func isPrivateUse(r rune) bool {
	return r >= 0xE000 && r <= 0xF8FF
}

// escaped decodes a \uXXXX or \UXXXXXXXX escape at the start of s, as a
// double-quoted scalar would.
func escaped(s []byte) (rune, bool) {
	n := 0
	switch {
	case len(s) >= 6 && s[1] == 'u':
		n = 4
	case len(s) >= 10 && s[1] == 'U':
		n = 8
	default:
		return 0, false
	}
	v, err := strconv.ParseUint(string(s[2:2+n]), 16, 32)
	return rune(v), err == nil
}

// hideCharacters replaces each character of hide with a private-use
// character that the source neither holds nor escapes, which the YAML 1.1
// parser reads as an ordinary character. The returned replacer puts the
// originals back.
func hideCharacters(src []byte, hide, used map[rune]bool) ([]byte, *strings.Replacer, error) {
	stand := map[rune]rune{}
	var restore []string
	next := rune(0xE000)
	for _, r := range slices.Sorted(maps.Keys(hide)) {
		for isPrivateUse(next) && used[next] {
			next++
		}
		if !isPrivateUse(next) {
			return nil, nil, &Error{Kind: Unsupported, Msg: "no free private-use character to stand for a control or line-break character"}
		}
		stand[r] = next
		restore = append(restore, string(next), string(r))
		next++
	}
	out := make([]byte, 0, len(src)+len(src)/16)
	for i := 0; i < len(src); {
		r, size := utf8.DecodeRune(src[i:])
		if h, ok := stand[r]; ok {
			out = utf8.AppendRune(out, h)
		} else {
			out = append(out, src[i:i+size]...)
		}
		i += size
	}
	return out, strings.NewReplacer(restore...), nil
}

// directives scans the lines before the first document's content. A
// %YAML 1.2 directive is replaced by blanks, so positions do not move;
// any other directive is refused. The CLI's parser reads %YAML 1.1 with
// the YAML 1.1 schema, applies %TAG to tags, and ignores other
// directives with a warning.
func directives(src []byte) ([]byte, error) {
	var out []byte
	for start, line := 0, 1; start < len(src); line++ {
		end := bytes.IndexByte(src[start:], '\n')
		if end < 0 {
			end = len(src)
		} else {
			end += start
		}
		text := bytes.TrimRight(src[start:end], "\r")
		trimmed := bytes.TrimLeft(text, " \t")
		switch {
		case len(trimmed) == 0 || trimmed[0] == '#':
		case text[0] == '%':
			if !isYAML12Directive(text) {
				return nil, &Error{Kind: Unsupported, Line: line, Column: 1, Msg: "directive " + string(firstField(text)) + ": only %YAML 1.2 is supported"}
			}
			if out == nil {
				out = bytes.Clone(src)
			}
			for i := start; i < start+len(text); i++ {
				out[i] = ' '
			}
		default:
			if out == nil {
				return src, nil
			}
			return out, nil
		}
		start = end + 1
	}
	if out == nil {
		return src, nil
	}
	return out, nil
}

func isYAML12Directive(text []byte) bool {
	f := bytes.Fields(text)
	return len(f) >= 2 && string(f[0]) == "%YAML" && string(f[1]) == "1.2" && (len(f) == 2 || f[2][0] == '#')
}

func firstField(text []byte) []byte {
	if f := bytes.Fields(text); len(f) > 0 {
		return f[0]
	}
	return text
}

// lineStarts returns the byte offset of the start of each line. The
// source has no line breaks other than LF and CRLF (prepare refuses the
// others), so these are the lines the parser counts.
func lineStarts(src []byte) []int {
	starts := []int{0}
	for i, c := range src {
		if c == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// offset converts the parser's 1-based line and column, which count
// characters, to a byte offset into the source.
func (p *parser) offset(line, col int) (int, bool) {
	if line == len(p.lines)+1 && col == 1 {
		// The parser places an empty node at the end of a source that
		// does not end with a line break on the line after the last.
		return len(p.src), true
	}
	if line < 1 || line > len(p.lines) || col < 1 {
		return 0, false
	}
	off := p.lines[line-1]
	for i := 1; i < col; i++ {
		if off >= len(p.src) || p.src[off] == '\n' {
			return 0, false
		}
		_, size := utf8.DecodeRune(p.src[off:])
		off += size
	}
	return off, true
}
