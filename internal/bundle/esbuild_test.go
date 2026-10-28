package bundle

import "testing"

// spells decides whether a codebase bundle's inputs spell the env
// rewrite's binding, as written or through an identifier's Unicode
// escapes, as the Bun helper's spells does.
func TestSpells(t *testing.T) {
	const name = "__windmill_lite_Bun"
	for _, c := range []struct {
		text string
		want bool
	}{
		{"const __windmill_lite_Bun = 1;", true},
		{"const __windmill_lite_B\\u0075n = 1;", true},
		{"const __windmill_lite_B\\u{75}n = 1;", true},
		{"const __windmill_lite_B\\u{0000075}n = 1;", true},
		{"const \\u005f_windmill_lite_Bun = 1;", true},
		{"const __windmill_lite_Bu = 1; // __windmill_lite_Bun in a comment", true},
		{"const __windmill_lite_Bunny = 1;", true},
		{"const __windmill_lite_B = 1; Bun.env.X", false},
		{"const s = '\\u{110000}__windmill_lite_B';", false},
		{"const s = '\\u00zz';", false},
	} {
		if got := spells(c.text, name); got != c.want {
			t.Errorf("spells(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}
