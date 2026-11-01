package bench

import (
	"bytes"
	"encoding/json"
	"testing"
	"unicode/utf8"
)

// TestFamilies checks that each family makes one valid JSON object of
// exactly the size asked for, in valid UTF-8 and without \u0000.
func TestFamilies(t *testing.T) {
	for _, f := range Families {
		for _, n := range []int{1 << 10, 16 << 10, 256 << 10, 1 << 20, 3 << 20} {
			b := Make(f, n)
			if len(b) != n || !json.Valid(b) || !utf8.Valid(b) || bytes.Contains(b, []byte(`\u0000`)) || b[0] != '{' {
				t.Errorf("%s at %d: %d bytes, valid %v", f, n, len(b), json.Valid(b))
			}
			if !bytes.Equal(b, Make(f, n)) {
				t.Errorf("%s at %d: not deterministic", f, n)
			}
		}
	}
}
