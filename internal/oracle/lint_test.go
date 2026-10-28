package oracle

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/fstest"
)

type guardCase struct {
	Name       string            `json:"name"`
	Spec       []string          `json:"spec"`
	Provenance string            `json:"provenance"`
	Check      string            `json:"check"`
	Files      map[string]string `json:"files"`
	Findings   []struct {
		File string `json:"file"`
		Rule string `json:"rule"`
	} `json:"findings"`
}

// TestGuardCases runs testdata/guard/cases.json: the image lint (OR-3) and
// the manifest guard (LIC-1/a, LIC-1/b) on small repository trees.
func TestGuardCases(t *testing.T) {
	data, err := os.ReadFile("testdata/guard/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		About string      `json:"about"`
		Cases []guardCase `json:"cases"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		t.Fatal(err)
	}
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if len(c.Spec) == 0 || c.Provenance == "" {
				t.Fatal("case needs spec IDs and a provenance")
			}
			fsys := fstest.MapFS{}
			for name, body := range c.Files {
				fsys[name] = &fstest.MapFile{Data: []byte(body)}
			}
			var findings []Finding
			switch c.Check {
			case "images":
				findings, err = LintImages(fsys)
			case "manifests":
				findings, err = GuardManifests(fsys)
			default:
				t.Fatalf("unknown check %q", c.Check)
			}
			if err != nil {
				t.Fatal(err)
			}
			type fr struct{ File, Rule string }
			got := []fr{}
			for _, f := range findings {
				got = append(got, fr{f.File, f.Rule})
			}
			want := []fr{}
			for _, f := range c.Findings {
				want = append(want, fr{f.File, f.Rule})
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("findings %v, want %v (%v)", got, want, findings)
			}
		})
	}
}

// repoRoot returns the repository root: the directory holding go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = parent
	}
}

// TestRepositoryPassesTheChecks runs both checks on this repository: no
// derived or re-pushed Windmill image, and no Windmill-derived input other
// than the allowed ones (ADR 0011, CI item 1).
func TestRepositoryPassesTheChecks(t *testing.T) {
	fsys := os.DirFS(repoRoot(t))
	for name, check := range map[string]func(fs.FS) ([]Finding, error){"image lint": LintImages, "manifest guard": GuardManifests} {
		findings, err := check(fsys)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, f := range findings {
			t.Errorf("%s: %s", name, f)
		}
	}
	// The guard sees the two vendored Windmill directories; a stray file in
	// either would be reported.
	for _, dir := range []string{"internal/openflow/schema", "internal/oracle/openapi"} {
		probe := fstest.MapFS{}
		entries, err := fs.ReadDir(fsys, dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			data, err := fs.ReadFile(fsys, dir+"/"+e.Name())
			if err != nil {
				t.Fatal(err)
			}
			probe[dir+"/"+e.Name()] = &fstest.MapFile{Data: data}
		}
		probe[dir+"/stray.rs"] = &fstest.MapFile{Data: []byte("x")}
		findings, err := GuardManifests(probe)
		if err != nil || len(findings) != 1 || findings[0].Rule != RuleVendoredFile {
			t.Errorf("%s: a stray file gave %v, %v", dir, findings, err)
		}
	}
}
