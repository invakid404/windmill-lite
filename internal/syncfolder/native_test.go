package syncfolder

import "testing"

// spec: SY-V3
func TestHasNativeAnnotation(t *testing.T) {
	for _, c := range []struct {
		src  string
		want bool
	}{
		{"// native\n", true},
		{"//native", true},
		{"//native=false\n", true},
		{"//native=\n", true},
		{"//   native   \n", true},
		{"// native\r\nexport {}\r\n", true},
		{"\n\n  \t\n// native\n", true},
		{"// @ts-nocheck\n//nobundling\n// native\n", true},
		{"// a\n\n// native\n", true},
		{"//\u00a0native\u2003\n", true}, // Unicode white space is trimmed
		{" //native\n", false},
		{"\t// native\n", false},
		{"//Native\n", false},
		{"//NATIVE\n", false},
		{"//native =true\n", false},
		{"// nativex\n", false},
		{"/// native\n", false},
		{"/* native */\n", false},
		{"import x from 'y';\n// native\n", false},
		{"export {}\n//native\n", false},
		{"\ufeff// native\n", false},      // a byte order mark is not white space here
		{"// native\rexport {}\n", false}, // a lone \r does not end a line
		{"", false},
	} {
		if got := HasNativeAnnotation(c.src); got != c.want {
			t.Errorf("HasNativeAnnotation(%q) = %v, want %v", c.src, got, c.want)
		}
	}
}

// spec: WR-31, SY-V3 — every annotation key follows the native rule.
func TestHasAnnotation(t *testing.T) {
	for _, c := range []struct {
		src, key string
		want     bool
	}{
		{"\n// nobundling\nexport function main(){return 1}\n", "nobundling", true},             // WR-31/a
		{"const x = 1;\n//nobundling\nexport function main(){return 1}\n", "nobundling", false}, // WR-31/b
		{"//npm\n//nodejs\n", "nodejs", true},
		{"//nobundling=yes\n", "nobundling", true},
		{"//Nobundling\n", "nobundling", false},
		{"  //nobundling\n", "nobundling", false},
		{"// native\n", "nobundling", false},
	} {
		if got := HasAnnotation(c.src, c.key); got != c.want {
			t.Errorf("HasAnnotation(%q, %q) = %v, want %v", c.src, c.key, got, c.want)
		}
	}
}
