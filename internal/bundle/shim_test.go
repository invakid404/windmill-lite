package bundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var shimCases = []struct {
	name    string
	key     string
	exports []string
}{
	{"ctx", "f/shared/ctx.ts", []string{"store", "default", "label", "store"}},
	{"package", "@acme/ctx", []string{"als", "register"}},
	{"no-exports", "f/shared/side_effect.ts", nil},
	{"names", `f/q"uote.ts`, []string{"a-b", "if", "ünï", "$ok", "_x1", "default"}},
}

// TestShimGolden pins the generated shim source (ADR 0010): sorted, unique
// export names, string export names where a name is not an identifier, and
// the key escaped as a JavaScript string.
func TestShimGolden(t *testing.T) {
	for _, c := range shimCases {
		t.Run(c.name, func(t *testing.T) {
			src, err := Shim(c.key, c.exports)
			if err != nil {
				t.Fatal(err)
			}
			golden(t, filepath.Join("shim", c.name+".mjs"), src)
			again, _ := Shim(c.key, append([]string{}, c.exports...))
			if string(again) != string(src) {
				t.Error("the shim is not deterministic")
			}
		})
	}
}

// TestLateShimGolden pins the generated late shim (ADR 0010): an ES
// module that re-exports each name, sorted and unique, with then among
// them, from the CommonJS module named by the late specifier and the key,
// whose getters read the registry. A module without exports gets one too.
func TestLateShimGolden(t *testing.T) {
	for _, c := range shimCases {
		t.Run(c.name, func(t *testing.T) {
			esm, cjs, err := LateShim(c.key, c.exports)
			if err != nil {
				t.Fatal(err)
			}
			golden(t, filepath.Join("shim", c.name+".late.mjs"), esm)
			golden(t, filepath.Join("shim", c.name+".late.cjs"), cjs)
			esm2, cjs2, _ := LateShim(c.key, append([]string{}, c.exports...))
			if string(esm2) != string(esm) || string(cjs2) != string(cjs) {
				t.Error("the late shim is not deterministic")
			}
		})
	}
	for _, c := range []struct {
		key     string
		exports []string
	}{
		{"", []string{"a"}},
		{"bad\xff", []string{"a"}},
		{"k", []string{""}},
		{"k", []string{"bad\xff"}},
	} {
		if _, _, err := LateShim(c.key, c.exports); err == nil {
			t.Errorf("LateShim(%q, %q) succeeded", c.key, c.exports)
		}
	}
}

func TestShimRefusals(t *testing.T) {
	for _, c := range []struct {
		key     string
		exports []string
	}{
		{"", []string{"a"}},
		{"bad\xff", nil},
		{"k", []string{""}},
		{"k", []string{"bad\xff"}},
	} {
		if _, err := Shim(c.key, c.exports); err == nil {
			t.Errorf("Shim(%q, %q) succeeded", c.key, c.exports)
		}
	}
}

// TestShimBehaviour runs generated shims on the pinned Bun: a registered
// module's exports come through by name (string names and default
// included); a missing module, a missing registry and a registry that is
// not a Map all fail the load with the not-registered message.
func TestShimBehaviour(t *testing.T) {
	bun := bunPath(t)
	dir := t.TempDir()
	write := func(name string, text string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	src, err := Shim(`f/q"uote.ts`, []string{"a-b", "if", "default", "ünï"})
	if err != nil {
		t.Fatal(err)
	}
	shim := write("shim.mjs", string(src))
	user := write("user.mjs", `import d, { "a-b" as ab, if as kw, ünï } from "./shim.mjs";
export const main = () => [d, ab, kw, ünï];
`)
	impl := write("impl.mjs", `export default "D"; const ab = "AB", kw = "IF", u = "U"; export { ab as "a-b", kw as if, u as ünï };
`)
	got := runModules(t, bun, nil, [][2]string{{`f/q"uote.ts`, impl}}, []call{{Module: user, Export: "main"}})
	if got[0].Error != nil || !jsonEqual(t, got[0].Result, []byte(`["D","AB","IF","U"]`)) {
		t.Errorf("registered: %s %v", got[0].Result, got[0].Error)
	}

	want := `Error: host-provided module not registered: f/q"uote.ts`
	got = runModules(t, bun, nil, [][2]string{{"another", impl}}, []call{{Module: shim, Export: "default"}})
	if got[0].Error == nil || *got[0].Error != want {
		t.Errorf("not registered: %s %v, want %q", got[0].Result, got[0].Error, want)
	}
	got = runModules(t, bun, nil, nil, []call{{Module: shim, Export: "default"}})
	if got[0].Error == nil || *got[0].Error != want {
		t.Errorf("no registry: %v, want %q", got[0].Error, want)
	}
	notMap := write("notmap.mjs", `globalThis[Symbol.for("windmill-lite.hostModules")] = { 'f/q"uote.ts': {} };
export const main = async () => (await import("./shim.mjs")).default;
`)
	got = runModules(t, bun, nil, nil, []call{{Module: notMap, Export: "main"}})
	if got[0].Error == nil || !strings.Contains(*got[0].Error, "not registered") {
		t.Errorf("registry not a Map: %v", got[0].Error)
	}
}
