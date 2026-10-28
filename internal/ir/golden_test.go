package ir

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/invakid404/windmill-lite/internal/syncfolder"
)

var update = flag.Bool("update", false, "rewrite the golden IR files")

// TestGolden compiles each folder under testdata/golden/<case>/windmill
// with the case's contract.yaml, when it has one, and compares the
// program's JSON form with <case>/ir.json byte for byte. Run with -update
// to rewrite the files, then review the diff: the golden files are the
// whole compiled program, so any change to the IR shows here.
func TestGolden(t *testing.T) {
	dirs, err := filepath.Glob("testdata/golden/*")
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no golden cases (%v)", err)
	}
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			got := compileGolden(t, dir)
			want := filepath.Join(dir, "ir.json")
			if *update {
				if err := os.WriteFile(want, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			data, err := os.ReadFile(want)
			if err != nil {
				t.Fatalf("%v (run with -update to write it)", err)
			}
			if !bytes.Equal(got, data) {
				t.Errorf("%s differs from the compiled program; run with -update and review the diff:\n%s", want, got)
			}
			// ir.json decodes to the same program.
			var back Program
			if err := json.Unmarshal(data, &back); err != nil {
				t.Fatal(err)
			}
			again, err := back.MarshalIndent()
			if err != nil || !bytes.Equal(again, data) {
				t.Errorf("%s does not round-trip (%v)", want, err)
			}
		})
	}
}

func compileGolden(t *testing.T, dir string) []byte {
	t.Helper()
	f, err := syncfolder.Open(filepath.Join(dir, "windmill"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var c *Contract
	if src, err := os.ReadFile(filepath.Join(dir, "contract.yaml")); err == nil {
		if c, err = ParseContract(src); err != nil {
			t.Fatal(err)
		}
	}
	p, err := Compile(f, c, Options{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.MarshalIndent()
	if err != nil {
		t.Fatal(err)
	}
	return out
}
