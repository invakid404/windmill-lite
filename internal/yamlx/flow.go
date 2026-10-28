package yamlx

import (
	"strings"

	"go.yaml.in/yaml/v3"
)

// checkFlow scans the source of the flow collection n for the indicators
// that the CLI's parser (YAML 1.2) and the YAML 1.1 parser read
// differently, and refuses them:
//
//   - a '?' that starts a token and is followed by a plain character: the
//     YAML 1.1 parser reads an explicit key, the CLI a plain scalar
//     ([?v] is ["?v"]);
//   - a ':' that starts a token and is followed by a plain character,
//     unless it follows a quoted scalar or a flow collection: the YAML 1.1
//     parser reads a value indicator, the CLI a plain scalar ([a :b] is
//     ["a :b"]);
//   - a ':' after a plain scalar and followed by ',', ']' or '}': the
//     CLI reads a value indicator, the YAML 1.1 parser part of the scalar
//     ([v:] is [{v: null}]);
//   - a '?' with nothing but whitespace before a ',': the two parsers
//     pair the entries differently.
//
// Quoted scalars and comments are skipped. Both parsers read every other
// use of the two characters alike.
func (p *parser) checkFlow(n *yaml.Node) error {
	_, s, err := p.properties(n)
	if err != nil {
		return err
	}
	if len(s) == 0 || (s[0] != '[' && s[0] != '{') {
		return errorAt(Unsupported, n, "cannot locate the flow collection in the source")
	}
	const (
		space  = iota // whitespace, or the start of the collection
		opener        // '[', '{', ',' or an indicator
		json          // the end of a quoted scalar or a flow collection
		plain         // a character of a plain scalar
	)
	// raw classifies the previous character, last the previous one that
	// is not whitespace.
	raw, last, depth := space, opener, 0
	plainNext := func(i int) bool {
		return i < len(s) && !isSeparator(s[i]) && !strings.ContainsRune(",[]{}", rune(s[i]))
	}
	endNext := func(i int) bool { return i < len(s) && strings.ContainsRune(",]}", rune(s[i])) }
	base := len(p.src) - len(s) // s is the source from the collection on
	refuse := func(i int, msg string) error {
		e := &Error{Kind: Unsupported, Msg: msg}
		e.Line, e.Column = p.position(base + i)
		return e
	}
	set := func(c int) { raw, last = c, c }
	for i := 0; i < len(s); i++ {
		c := s[i]
		start := raw == space || raw == opener // c starts a token
		switch {
		case (c == '"' || c == '\'') && start && last != plain:
			// A quote after a space inside a plain scalar ([a 'b']) is
			// part of the scalar.
			i = skipQuoted(s, i)
			set(json)
		case c == '#' && raw == space:
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '[' || c == '{':
			depth++
			set(opener)
		case c == ']' || c == '}':
			if depth--; depth == 0 {
				return nil
			}
			set(json)
		case c == ',':
			set(opener)
		case isSeparator(c):
			raw = space
		case c == '?' && raw != plain && plainNext(i+1):
			return refuse(i, "a '?' followed by a plain character in a flow collection: the CLI reads it as part of a plain scalar")
		case c == ':' && raw == plain && plainNext(i+1), c == '?' && raw == plain:
			set(plain) // inside a plain scalar
		case c == ':' && start && last != json && plainNext(i+1):
			return refuse(i, "a ':' followed by a plain character in a flow collection: the CLI reads it as part of a plain scalar")
		case c == ':' && last == plain && endNext(i+1):
			return refuse(i, "a ':' before a flow indicator after a plain scalar in a flow collection: the CLI reads it as a value indicator")
		case c == '?' && emptyKey(s, i+1):
			return refuse(i, "a '?' with no key before ',' in a flow collection: the two parsers pair the entries differently")
		case c == '?' || c == ':':
			set(opener)
		default:
			set(plain)
		}
	}
	return nil
}

// emptyKey reports whether only whitespace separates s[i:] from a ','.
func emptyKey(s []byte, i int) bool {
	for i < len(s) && isSeparator(s[i]) {
		i++
	}
	return i < len(s) && s[i] == ','
}

// skipQuoted returns the index of the quote that closes the quoted scalar
// opening at s[i], or the last index of s.
func skipQuoted(s []byte, i int) int {
	q := s[i]
	for i++; i < len(s); i++ {
		switch {
		case q == '"' && s[i] == '\\':
			i++
		case s[i] == q && q == '\'' && i+1 < len(s) && s[i+1] == '\'':
			i++
		case s[i] == q:
			return i
		}
	}
	return len(s) - 1
}
