package yamlx_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/invakid404/windmill-lite/internal/yamlx"
)

// FuzzToJSON checks that any input either fails with a *yamlx.Error or
// gives valid JSON, the same text from ToJSON and from Parse, on every
// call. The case file seeds it.
func FuzzToJSON(f *testing.F) {
	for _, c := range loadCases(f) {
		f.Add([]byte(c.YAML))
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		out, err := yamlx.ToJSON(src)
		if err != nil {
			var e *yamlx.Error
			if !errors.As(err, &e) {
				t.Fatalf("%q: error %v is a %T", src, err, err)
			}
			return
		}
		if !json.Valid(out) {
			t.Fatalf("%q: invalid JSON %q", src, out)
		}
		n, err := yamlx.Parse(src)
		if err != nil {
			t.Fatalf("%q: ToJSON succeeds, Parse fails: %v", src, err)
		}
		if again := n.AppendJSON(nil); !bytes.Equal(again, out) {
			t.Fatalf("%q: ToJSON gives %s, Parse then AppendJSON gives %s", src, out, again)
		}
	})
}
