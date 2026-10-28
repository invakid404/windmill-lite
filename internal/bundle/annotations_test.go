package bundle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/syncfolder"
)

// The leading-comment annotation cases of conformance/build/annotations.json
// are read by both implementations of the rule (WR-31, SY-V3): the Go
// loader's syncfolder.HasAnnotation, which internal/ir uses, and the Bun
// build helper, whose metadata records them. bun test runs the same file
// through the helper's parser.

type annotationCases struct {
	ReliesOn []string `json:"relies_on"`
	Note     string   `json:"note"`
	Cases    []struct {
		Spec       string   `json:"spec"`
		Provenance string   `json:"provenance"`
		Variant    string   `json:"variant"`
		Title      string   `json:"title"`
		Source     string   `json:"source"`
		Flags      []string `json:"flags"`
	} `json:"cases"`
}

var annotationFlags = []string{"native", "nobundling", "nodejs", "npm", "sandbox"}

func TestAnnotationCases(t *testing.T) {
	var f annotationCases
	readStrict(t, filepath.Join(casesDir, "annotations.json"), &f)
	data, err := os.ReadFile(catalogueFile)
	if err != nil {
		t.Fatal(err)
	}
	catalogue := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^- \*\*((?:WR|SY)-[0-9V]+/[a-z])\*\* · ([a-z-]+)`).FindAllSubmatch(data, -1) {
		catalogue[string(m[1])] = string(m[2])
	}
	for _, c := range f.Cases {
		if prov, ok := catalogue[c.Spec]; strings.Contains(c.Spec, "/") && (!ok || prov != c.Provenance) {
			t.Errorf("%s: provenance %q, catalogue %q", c.Spec, c.Provenance, prov)
		}
		if !slices.IsSorted(c.Flags) {
			t.Errorf("%s %s: flags must be sorted", c.Spec, c.Variant)
		}
	}

	t.Run("go", func(t *testing.T) {
		for _, c := range f.Cases {
			var got []string
			for _, flag := range annotationFlags {
				if syncfolder.HasAnnotation(c.Source, flag) {
					got = append(got, flag)
				}
			}
			if !slices.Equal(nonNil(got), nonNil(c.Flags)) {
				t.Errorf("%s %s (%s): HasAnnotation gives %q, want %q", c.Spec, c.Variant, c.Title, got, c.Flags)
			}
		}
	})

	t.Run("helper", func(t *testing.T) {
		bun := bunPath(t)
		var entries []BunScript
		for i, c := range f.Cases {
			entries = append(entries, BunScript{ID: fmt.Sprintf("c%d", i), Path: fmt.Sprintf("f/annotations/c%d", i), Content: c.Source})
		}
		res, err := BuildBun(context.Background(), BunConfig{
			Bun: bun, Helper: repoPath(t, "harnesses/bun/build/main.ts"),
			Folder: t.TempDir(), Out: t.TempDir(), Entries: entries,
		})
		var be *BuildError
		if err != nil && !errors.As(err, &be) {
			t.Fatal(err)
		}
		for i, c := range f.Cases {
			m := res.Scripts[fmt.Sprintf("c%d", i)]
			if m == nil {
				t.Errorf("%s %s: no metadata", c.Spec, c.Variant)
				continue
			}
			a := m.Annotations
			var got []string
			for flag, on := range map[string]bool{"native": a.Native, "nobundling": a.Nobundling, "nodejs": a.Nodejs, "npm": a.Npm, "sandbox": a.Sandbox} {
				if on {
					got = append(got, flag)
				}
			}
			slices.Sort(got)
			if !slices.Equal(nonNil(got), nonNil(c.Flags)) {
				t.Errorf("%s %s (%s): the helper records %q, want %q", c.Spec, c.Variant, c.Title, got, c.Flags)
			}
		}
	})
}
