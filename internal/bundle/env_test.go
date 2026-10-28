package bundle

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// golden compares got with testdata/<name>, or rewrites it under -update.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := writeFile(path, got); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/bundle -run %s -args -update to write it)", err, t.Name())
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the golden file:\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}

// TestEnvRewriteGolden pins the one define table and support module both
// bundlers receive (ADR 0010 step 5), per output format.
func TestEnvRewriteGolden(t *testing.T) {
	var first string
	for _, f := range []Format{ESM, CJS} {
		define, support, err := EnvRewrite(f)
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(define); err != nil {
			t.Fatal(err)
		}
		golden(t, filepath.Join("env", "define-"+string(f)+".json"), b.Bytes())
		if first == "" {
			first = support
			golden(t, filepath.Join("env", "support.js"), []byte(support))
		} else if support != first {
			t.Errorf("the %s support module differs from the ESM one", f)
		}
	}
}

func TestEnvRewriteFormats(t *testing.T) {
	esm, _, _ := EnvRewrite(ESM)
	cjs, _, _ := EnvRewrite(CJS)
	if esm["import.meta.env"] != EnvBinding+".env" {
		t.Errorf("ESM output rewrites import.meta.env: %q", esm["import.meta.env"])
	}
	if _, ok := cjs["import.meta.env"]; ok {
		t.Error("CommonJS output must leave import.meta.env alone (it stays empty, as in the CLI's esbuild bundles)")
	}
	for _, k := range []string{"Bun", "Bun.env", "globalThis.Bun", "global.Bun", "self.Bun"} {
		if esm[k] == "" || esm[k] != cjs[k] {
			t.Errorf("%s: ESM %q, CJS %q; both formats rewrite it alike", k, esm[k], cjs[k])
		}
	}
	if _, _, err := EnvRewrite("iife"); err == nil {
		t.Error("an unknown format is accepted")
	}
}
