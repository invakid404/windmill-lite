package ir

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/invakid404/windmill-lite/internal/syncfolder"
)

// FuzzCompile checks that compiling any flow.yaml never panics, and that a
// program that compiles has a JSON form that decodes to itself. The seeds
// are the main flows of the compile cases, plus a contract that provides
// one site.
func FuzzCompile(f *testing.F) {
	paths, _ := filepath.Glob(filepath.Join(casesDir, "*-*.json"))
	for _, p := range paths {
		var cf caseFile
		data, err := os.ReadFile(p)
		if err != nil {
			f.Fatal(err)
		}
		if err := json.Unmarshal(data, &cf); err != nil {
			f.Fatal(err)
		}
		for _, c := range cf.Cases {
			if src, ok := c.Given.Files["f/x/main.flow/flow.yaml"]; ok {
				f.Add(src, false)
				f.Add(src, true)
			}
		}
	}
	contract := &Contract{
		Roots:    []Root{{Flow: "f/x/main"}},
		Provided: []ProvidedSite{{Selector: Selector{Flow: "f/x/main", Module: new("p")}, Provider: "x", Mandatory: true}},
	}
	f.Fuzz(func(t *testing.T, flow string, provide bool) {
		fsys := fstest.MapFS{
			"wmill.yaml":              {Data: []byte("defaultTs: bun\n")},
			"f/x/main.flow/flow.yaml": {Data: []byte(flow)},
			"f/x/s.ts":                {Data: []byte("export async function main() { return 1 }\n")},
			"f/x/sub.flow/flow.yaml":  {Data: []byte("value:\n  modules: []\n")},
		}
		folder, err := syncfolder.Load(fsys)
		if err != nil {
			return
		}
		c := &Contract{Roots: []Root{{Flow: "f/x/main"}}}
		if provide {
			c = contract
		}
		p, err := Compile(folder, c, Options{Strict: provide})
		if err != nil {
			return
		}
		data, err := p.MarshalIndent()
		if err != nil {
			t.Fatalf("MarshalIndent: %v", err)
		}
		var back Program
		if err := json.Unmarshal(data, &back); err != nil {
			t.Fatalf("decoding the program: %v", err)
		}
		again, err := back.MarshalIndent()
		if err != nil || !bytes.Equal(again, data) {
			t.Fatalf("the program does not round-trip (%v)", err)
		}
	})
}
