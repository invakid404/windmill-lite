package jsonval

import "strings"

// StoredBound is a bound above the size in bytes that PostgreSQL's jsonb
// stores text in, text being one JSON value, before any compression, so
// above its pg_column_size however the value is stored: 8 bytes of
// headers, and for each value, key and container 4 bytes of entry, up to
// 3 of alignment and its content: a string's or a key's bytes, at most its
// text's between the quotes; a number's, at most 16 more than its text's
// (a numeric's headers and its base-10000 digits); 4 of a container's
// header. A byte of text takes at most 12 bytes stored (a one-digit number
// and its comma take 24), and the headers and a last value without its
// comma at most 32 more, so text shorter than 7000 bytes is bounded
// without the walk. TestStoredBoundAbovePostgreSQL checks the bound
// against PostgreSQL itself.
func StoredBound(text []byte) int {
	if len(text) < 7000 {
		return 12*len(text) + 32
	}
	return storedWalk(text)
}

// storedWalk is StoredBound's walk over text.
func storedWalk(text []byte) int {
	n := 8
	for i := 0; i < len(text); {
		switch c := text[i]; {
		case c == '"':
			j := i + 1
			for j < len(text) && text[j] != '"' {
				if text[j] == '\\' {
					j++
				}
				j++
			}
			n += 7 + j - i - 1
			i = j + 1
		case c == '-' || c >= '0' && c <= '9':
			j := i + 1
			for j < len(text) && strings.IndexByte("+-.0123456789eE", text[j]) >= 0 {
				j++
			}
			n += 7 + 16 + j - i
			i = j
		case c == 't' || c == 'f' || c == 'n':
			n += 7
			for i < len(text) && text[i] >= 'a' && text[i] <= 'z' {
				i++
			}
		case c == '[' || c == '{':
			n += 7 + 4
			i++
		default:
			i++
		}
	}
	return n
}

// ToastedSize is pg_column_size of text, one JSON value, stored as jsonb in
// a table column that PostgreSQL 16 moves out of line, as it does with any
// value of a few KiB or more in its row: when pglz's default strategy
// compresses the datum by more than its 8-byte compressed header and 2
// bytes, the compressed length and its 4-byte compression header; else the
// datum's length less its 4-byte varlena header. It needs the datum's exact
// bytes (jsonbDatum) and pglz's exact output (pglzCompressedLen), and
// assumes pglz, PostgreSQL's default compression, which the oracle uses
// (WA-19/o); TestToastedSizeMatchesPostgreSQL checks it against PostgreSQL.
func ToastedSize(text []byte) (int, error) {
	d, err := jsonbDatum(text)
	if err != nil {
		return 0, err
	}
	raw := len(d) - 4
	if c := pglzCompressedLen(d[4:]); c >= 0 && c+8 < raw-2 {
		return c + 4, nil
	}
	return raw, nil
}
