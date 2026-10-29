package expr

import (
	"encoding/json"
	"math"
	"os"
	"strconv"
	"testing"
)

// TestDebugFormMatchesRust holds debugFloat and debugString to Rust's debug
// notation, as rquickjs prints a thrown value's payload (JE-21), on the
// expected texts of testdata/debugform.json, which rustc printed. The
// floats include values whose two shortest digit strings tie, which Rust
// breaks upward and Go's own shortest formatting does not (review round 1,
// R1-2); the strings include characters escaped for every reason Rust has.
func TestDebugFormMatchesRust(t *testing.T) {
	data, err := os.ReadFile("testdata/debugform.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Floats []struct {
			Bits string `json:"bits"`
			Text string `json:"text"`
		} `json:"floats"`
		Strings []struct {
			Text  string `json:"text"`
			Debug string `json:"debug"`
		} `json:"strings"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Floats) < 200 || len(f.Strings) < 500 {
		t.Fatalf("%d floats and %d strings; the table has gone missing", len(f.Floats), len(f.Strings))
	}
	ties := 0
	for _, c := range f.Floats {
		b, err := strconv.ParseUint(c.Bits, 16, 64)
		if err != nil {
			t.Fatal(err)
		}
		x := math.Float64frombits(b)
		if got := debugFloat(x); got != c.Text {
			t.Errorf("%s (%v): %s, Rust prints %s", c.Bits, x, got, c.Text)
		}
		if goText := strconv.FormatFloat(math.Abs(x), 'e', -1, 64); x != 0 && !math.IsInf(x, 0) && !math.IsNaN(x) {
			if d, _ := shortestDigits(math.Abs(x)); d != digitsOf(goText) {
				ties++
			}
		}
	}
	if ties < 50 {
		t.Errorf("only %d floats where the tie decides the digits; the table should hold more", ties)
	}
	for _, c := range f.Strings {
		if got := debugString(c.Text); got != c.Debug {
			t.Errorf("%q: %s, Rust prints %s", c.Text, got, c.Debug)
		}
	}
}

// digitsOf returns the significant digits of Go's 'e' formatting.
func digitsOf(e string) string {
	var out []byte
	for i := 0; i < len(e) && e[i] != 'e'; i++ {
		if e[i] != '.' {
			out = append(out, e[i])
		}
	}
	return string(out)
}

// TestDebugFloat pins the presentation rules on named values: decimal
// notation with at least one fractional digit between 1e-4 and 1e16,
// exponent notation outside, ties broken upward in both, and the sign kept
// on a negative tie and on negative zero.
func TestDebugFloat(t *testing.T) {
	for x, want := range map[float64]string{
		1000000000000000.25:  "1000000000000000.3",
		-1000000000000000.25: "-1000000000000000.3",
		1234567890123456.25:  "1234567890123456.3",
		math.Ldexp(1, -25):   "2.9802322387695313e-8",
		-math.Ldexp(1, -25):  "-2.9802322387695313e-8",
		1e16:                 "1e16",
		9999999999999998:     "9999999999999998.0",
		1e-4:                 "0.0001",
		9.999999999999999e-5: "9.999999999999999e-5",
		1:                    "1.0",
		1.5:                  "1.5",
		1e300:                "1e300",
		math.Copysign(0, -1): "-0.0",
		0:                    "0.0",
		math.Inf(1):          "inf",
		math.Inf(-1):         "-inf",
	} {
		if got := debugFloat(x); got != want {
			t.Errorf("debugFloat(%v) = %s, want %s", x, got, want)
		}
	}
	if got := debugFloat(math.NaN()); got != "NaN" {
		t.Errorf("debugFloat(NaN) = %s", got)
	}
}
