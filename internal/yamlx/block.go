package yamlx

import (
	"bytes"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Block scalars are read from the source, not from the library: the CLI's
// parser and the YAML 1.1 parser disagree on whitespace-only lines, on
// trailing lines, and on a body that ends the file. The rules below were
// pinned against the CLI's parser (testdata/sy9.json, rule "block").

// blockLine is one line of a block scalar's body: its leading spaces and
// the rest, without the line break.
type blockLine struct {
	spaces  int
	content string
}

// blockScalar returns the value of the literal or folded scalar n, whose
// enclosing block collection starts at column indent (0-based; -1 at the
// document root, where text may start at column 0 but an indentation
// indicator counts from column 0).
func (p *parser) blockScalar(n *yaml.Node, indent int) (string, error) {
	_, s, err := p.properties(n)
	if err != nil {
		return "", err
	}
	if len(s) == 0 || (s[0] != '|' && s[0] != '>') {
		return "", errorAt(Unsupported, n, "cannot locate the block scalar header in the source")
	}
	folded := s[0] == '>'
	explicit, chomp := 0, byte(0)
header:
	for _, c := range s[1:] {
		switch {
		case c >= '1' && c <= '9' && explicit == 0:
			explicit = int(c - '0')
		case (c == '-' || c == '+') && chomp == 0:
			chomp = c
		default:
			break header
		}
	}
	nl := bytes.IndexByte(s, '\n')
	if nl < 0 {
		return "", nil // the header ends the file
	}
	lines := blockBody(s[nl+1:], indent, explicit, p.restoreText)

	// Everything from chompStart on is trailing empty lines.
	chompStart := len(lines)
	for i := len(lines) - 1; i >= 0 && lines[i].content == ""; i-- {
		chompStart = i
	}
	if chompStart == 0 {
		if chomp == '+' && len(lines) > 0 {
			return strings.Repeat("\n", max(1, len(lines)-1)), nil
		}
		return "", nil
	}

	trim := max(indent, 0) + explicit
	contentStart := 0
	for i, l := range lines[:chompStart] {
		if l.content == "" {
			if explicit == 0 && l.spaces > trim {
				trim = l.spaces
			}
			continue
		}
		if l.spaces < trim {
			return "", errorAt(Syntax, n, "a leading empty line of a block scalar is more indented than its first line; use an indentation indicator")
		}
		if explicit == 0 {
			trim = l.spaces
		}
		contentStart = i
		break
	}
	// Trailing empty lines indented beyond the first line with content
	// hold content.
	for i := len(lines) - 1; i >= chompStart; i-- {
		if lines[i].spaces > lines[contentStart].spaces {
			chompStart = i + 1
			break
		}
	}

	var b strings.Builder
	extra := func(l blockLine) string { return strings.Repeat(" ", max(0, l.spaces-trim)) }
	for _, l := range lines[:contentStart] {
		b.WriteString(extra(l))
		b.WriteByte('\n')
	}
	sep, moreIndented := "", false
	for _, l := range lines[contentStart:chompStart] {
		switch {
		case !folded:
			b.WriteString(sep + extra(l) + l.content)
			sep = "\n"
		case l.spaces > trim || strings.HasPrefix(l.content, "\t"):
			if sep == " " {
				sep = "\n"
			} else if !moreIndented && sep == "\n" {
				sep = "\n\n"
			}
			b.WriteString(sep + extra(l) + l.content)
			sep, moreIndented = "\n", true
		case l.content == "":
			if sep == "\n" {
				b.WriteByte('\n')
			} else {
				sep = "\n"
			}
		default:
			b.WriteString(sep + l.content)
			sep, moreIndented = " ", false
		}
	}
	switch chomp {
	case '-':
	case '+':
		for _, l := range lines[chompStart:] {
			b.WriteString("\n" + extra(l))
		}
		if !strings.HasSuffix(b.String(), "\n") {
			b.WriteByte('\n')
		}
	default:
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// blockBody splits the source after a block scalar's header into the
// scalar's body lines. The body ends before the first line with content
// that is indented less than the scalar's content, or that marks a
// document. When the last line ends with a line break, an empty line
// follows it, as when the CLI's parser splits the body text.
func blockBody(s []byte, indent, explicit int, restore func(string) string) []blockLine {
	var lines []blockLine
	firstContent := -1 // spaces of the first line with content
	end := 0           // bytes of s in the body
	for start := 0; start < len(s); start = end {
		stop := bytes.IndexByte(s[start:], '\n')
		next := len(s)
		if stop >= 0 {
			stop += start
			next = stop + 1
		} else {
			stop = len(s)
		}
		raw := strings.TrimSuffix(restore(string(s[start:stop])), "\r")
		content := strings.TrimLeft(raw, " ")
		spaces := len(raw) - len(content)
		if content != "" {
			var ends bool
			switch {
			case spaces == 0 && isDocumentMarker(content):
				ends = true
			case explicit > 0:
				ends = spaces < max(indent, 0)+explicit
			case firstContent < 0:
				ends = spaces <= indent
			default:
				ends = spaces < firstContent
			}
			if ends {
				break
			}
			if firstContent < 0 {
				firstContent = spaces
			}
		} else if stop == len(s) {
			// A last line of spaces that ends the file without a line
			// break belongs to the body only when it reaches the body's
			// indentation.
			need := indent + 1
			switch {
			case explicit > 0:
				need = max(indent, 0) + explicit
			case firstContent >= 0:
				need = firstContent
			}
			if spaces < need {
				break
			}
		}
		lines = append(lines, blockLine{spaces, content})
		end = next
	}
	if end > 0 && s[end-1] == '\n' {
		lines = append(lines, blockLine{})
	}
	return lines
}

func isDocumentMarker(s string) bool {
	return (strings.HasPrefix(s, "---") || strings.HasPrefix(s, "...")) &&
		(len(s) == 3 || s[3] == ' ' || s[3] == '\t')
}
