package bundle

import (
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestLoadTimeHostModules: an import of a listed module counts when the
// module that holds it loads at load time and the import is not an
// import() its module makes only from inside functions. Where the analysis
// cannot tell, it counts.
func TestLoadTimeHostModules(t *testing.T) {
	static := func(from, to string) moduleImport {
		return moduleImport{From: from, Kind: "import-statement", Specifier: "./" + to, To: to}
	}
	dynamic := func(from, to string) moduleImport {
		return moduleImport{From: from, Kind: "dynamic-import", Specifier: "./" + to, To: to}
	}
	host := func(from, kind, key string) moduleImport {
		return moduleImport{From: from, Kind: kind, Specifier: "./" + key, Host: key}
	}
	for _, c := range []struct {
		name string
		g    importGraph
		want []string
	}{
		{"a static import of a listed module", importGraph{Entry: "e", Imports: []moduleImport{
			host("e", "import-statement", "k"),
		}}, []string{"k"}},
		{"an import() of a listed module inside a function", importGraph{Entry: "e", Imports: []moduleImport{
			host("e", "dynamic-import", "k"),
		}, Lazy: map[string][]string{"e": {"./k"}}}, []string{}},
		{"an import() of a listed module at top level", importGraph{Entry: "e", Imports: []moduleImport{
			host("e", "dynamic-import", "k"),
		}, Lazy: map[string][]string{"e": {}}}, []string{"k"}},
		{"an import() in a module that does not parse", importGraph{Entry: "e", Imports: []moduleImport{
			host("e", "dynamic-import", "k"),
		}, Lazy: map[string][]string{"e": nil}}, []string{"k"}},
		{"a require() of a listed module", importGraph{Entry: "e", Imports: []moduleImport{
			host("e", "require-call", "k"),
		}, Lazy: map[string][]string{"e": {"./k"}}}, []string{"k"}},
		{"through a module loaded at load time", importGraph{Entry: "e", Imports: []moduleImport{
			static("e", "m"), static("m", "n"), host("n", "import-statement", "k"),
		}}, []string{"k"}},
		{"through a module loaded by a top-level import()", importGraph{Entry: "e", Imports: []moduleImport{
			dynamic("e", "m"), host("m", "import-statement", "k"),
		}, Lazy: map[string][]string{"e": {}}}, []string{"k"}},
		{"through a module loaded only by an import() inside a function", importGraph{Entry: "e", Imports: []moduleImport{
			dynamic("e", "m"), static("m", "n"), host("n", "import-statement", "k"),
		}, Lazy: map[string][]string{"e": {"./m"}}}, []string{}},
		{"a module loaded both ways loads at load time", importGraph{Entry: "e", Imports: []moduleImport{
			dynamic("e", "m"), static("e", "n"), static("n", "m"), host("m", "import-statement", "k"),
		}, Lazy: map[string][]string{"e": {"./m"}}}, []string{"k"}},
		{"a lazy import inside a lazily loaded module stays lazy", importGraph{Entry: "e", Imports: []moduleImport{
			dynamic("e", "m"), host("m", "dynamic-import", "k"),
		}, Lazy: map[string][]string{"e": {"./m"}, "m": {"./k"}}}, []string{}},
		{"an unknown module makes every module load at load time", importGraph{Entry: "e", Imports: []moduleImport{
			{From: "e", Kind: "import-statement", Specifier: "./x"},
			dynamic("e", "m"), host("m", "import-statement", "k"), host("m", "dynamic-import", "j"),
		}, Lazy: map[string][]string{"e": {"./m"}, "m": {"./j"}}}, []string{"k"}},
		{"an unknown module behind a lazy import changes nothing", importGraph{Entry: "e", Imports: []moduleImport{
			dynamic("e", "m"), {From: "m", Kind: "import-statement", Specifier: "./x"}, host("m", "import-statement", "k"),
		}, Lazy: map[string][]string{"e": {"./m"}}}, []string{}},
		{"an entry the graph does not name makes every module load at load time", importGraph{Entry: "src/e.ts", Imports: []moduleImport{
			dynamic("e", "m"), host("m", "import-statement", "k"), host("e", "dynamic-import", "j"),
		}, Lazy: map[string][]string{"e": {"./m", "./j"}}}, []string{"k"}},
		{"several, sorted and once each", importGraph{Entry: "e", Imports: []moduleImport{
			host("e", "import-statement", "z"), static("e", "m"), host("m", "import-statement", "a"), host("m", "require-call", "z"),
		}}, []string{"a", "z"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := loadTimeHostModules(c.g); !slices.Equal(nonNil(got), c.want) {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// metaOf builds module metadata for hostModuleOrder from import lists:
// each "key" is a load-time import, each "~key" a lazy one.
func metaOf(imports map[string][]string) ([]HostModule, map[string]*HostModuleMeta) {
	var mods []HostModule
	meta := map[string]*HostModuleMeta{}
	for _, id := range slices.Sorted(maps.Keys(imports)) {
		m := &HostModuleMeta{Key: id + ".ts", HostModules: []string{}, LoadHostModules: []string{}}
		for _, imp := range imports[id] {
			k := strings.TrimPrefix(imp, "~") + ".ts"
			m.HostModules = append(m.HostModules, k)
			if !strings.HasPrefix(imp, "~") {
				m.LoadHostModules = append(m.LoadHostModules, k)
			}
		}
		slices.Sort(m.HostModules)
		meta[id] = m
		mods = append(mods, HostModule{ID: id, Key: id + ".ts"})
	}
	return mods, meta
}

func reorder(mods []HostModule, ids ...string) []HostModule {
	out := make([]HostModule, len(ids))
	for i, id := range ids {
		out[i] = mods[slices.IndexFunc(mods, func(m HostModule) bool { return m.ID == id })]
	}
	return out
}

// TestHostModuleOrder: load-time imports order modules and their cycles
// fail; lazy imports order them too, except one that closes a cycle,
// which is set aside and reported as late.
func TestHostModuleOrder(t *testing.T) {
	for _, c := range []struct {
		name    string
		imports map[string][]string
		list    []string
		order   []string
		late    map[string][]string
		err     string
	}{
		{name: "a cycle closed by a lazy import", imports: map[string][]string{"a": {"b"}, "b": {"~a"}},
			list: []string{"a", "b"}, order: []string{"b", "a"}, late: map[string][]string{"b": {"a.ts"}}},
		{name: "the same, listed the other way", imports: map[string][]string{"a": {"b"}, "b": {"~a"}},
			list: []string{"b", "a"}, order: []string{"b", "a"}, late: map[string][]string{"b": {"a.ts"}}},
		{name: "a lazy import that closes no cycle orders", imports: map[string][]string{"x": {"~k"}, "k": {}},
			list: []string{"x", "k"}, order: []string{"k", "x"}, late: map[string][]string{}},
		{name: "a cycle of load-time imports", imports: map[string][]string{"p": {"q"}, "q": {"p"}},
			list: []string{"p", "q"}, err: "host-provided modules import each other: p → q → p"},
		{name: "the cycle is named from its first module", imports: map[string][]string{"x": {"~p"}, "p": {"q"}, "q": {"p"}},
			list: []string{"x", "p", "q"}, err: "host-provided modules import each other: p → q → p"},
		{name: "a lazy import set aside on a longer cycle", imports: map[string][]string{"a": {"~b"}, "b": {"c"}, "c": {"a"}},
			list: []string{"a", "b", "c"}, order: []string{"a", "c", "b"}, late: map[string][]string{"a": {"b.ts"}}},
		{name: "two lazy imports on one cycle, the first set aside", imports: map[string][]string{"a": {"~b"}, "b": {"~c"}, "c": {"a"}},
			list: []string{"a", "b", "c"}, order: []string{"a", "c", "b"}, late: map[string][]string{"a": {"b.ts"}}},
		{name: "two lazy cycles", imports: map[string][]string{"a": {"b", "~c"}, "b": {"~a"}, "c": {"a"}},
			list: []string{"a", "b", "c"}, order: []string{"b", "a", "c"}, late: map[string][]string{"a": {"c.ts"}, "b": {"a.ts"}}},
		{name: "modules of another build are skipped", imports: map[string][]string{"a": {"elsewhere"}},
			list: []string{"a"}, order: []string{"a"}, late: map[string][]string{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			mods, meta := metaOf(c.imports)
			order, late, err := hostModuleOrder(reorder(mods, c.list...), meta)
			if c.err != "" {
				if err == nil || err.Error() != c.err {
					t.Fatalf("err %v, want %q", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(order, c.order) {
				t.Errorf("order %q, want %q", order, c.order)
			}
			for id := range late {
				if len(late[id]) == 0 {
					delete(late, id)
				}
			}
			if fmt.Sprint(late) != fmt.Sprint(c.late) {
				t.Errorf("late %v, want %v", late, c.late)
			}
			// Every load-time import comes first.
			for _, id := range order {
				for _, k := range meta[id].LoadHostModules {
					dep := strings.TrimSuffix(k, ".ts")
					if slices.Contains(order, dep) && slices.Index(order, dep) > slices.Index(order, id) {
						t.Errorf("%s imports %s at load time but comes first", id, dep)
					}
				}
			}
		})
	}
}

// everyImportOrder is the order when every import of a listed module
// ordered (a module after each listed module it imports, the list's order
// otherwise), and the cycle it then refuses: the reference for a list
// without a cycle.
func everyImportOrder(mods []HostModule, meta map[string]*HostModuleMeta) ([]string, bool) {
	byKey := map[string]string{}
	for _, m := range mods {
		byKey[m.Key] = m.ID
	}
	state := map[string]int{}
	var order []string
	var visit func(id string) bool
	visit = func(id string) bool {
		switch state[id] {
		case 2:
			return true
		case 1:
			return false
		}
		state[id] = 1
		for _, k := range meta[id].HostModules {
			if dep, ok := byKey[k]; ok && !visit(dep) {
				return false
			}
		}
		state[id] = 2
		order = append(order, id)
		return true
	}
	for _, m := range mods {
		if !visit(m.ID) {
			return nil, false
		}
	}
	return order, true
}

// TestHostModuleOrderWithoutLazyCycles: a list whose imports, lazy ones
// included, close no cycle is ordered exactly as when every import ordered
// it, with nothing late; any list whose load-time imports close no cycle
// is ordered with every load-time import first, and with every lazy import
// first but the late ones. Random lists of up to eight modules.
func TestHostModuleOrderWithoutLazyCycles(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	same, late, refused := 0, 0, 0
	for n := 0; n < 6000; n++ {
		size := 1 + r.IntN(8)
		imports := map[string][]string{}
		names := make([]string, size)
		for i := range names {
			names[i] = fmt.Sprintf("m%d", i)
		}
		acyclic := r.IntN(2) == 0
		for i, id := range names {
			imports[id] = []string{}
			for j, dep := range names {
				if i == j || r.IntN(4) != 0 || (acyclic && j > i) {
					continue
				}
				if r.IntN(3) == 0 {
					dep = "~" + dep
				}
				imports[id] = append(imports[id], dep)
			}
		}
		mods, meta := metaOf(imports)
		list := slices.Clone(names)
		r.Shuffle(len(list), func(i, j int) { list[i], list[j] = list[j], list[i] })
		mods = reorder(mods, list...)
		order, lateOf, err := hostModuleOrder(mods, meta)
		want, ok := everyImportOrder(mods, meta)
		if ok {
			if err != nil || !slices.Equal(order, want) {
				t.Fatalf("%v listed %q: order %q, %v; want %q", imports, list, order, err, want)
			}
			for id, l := range lateOf {
				if len(l) > 0 {
					t.Fatalf("%v: %s has late imports %q", imports, id, l)
				}
			}
			same++
			continue
		}
		if err != nil {
			refused++
			continue
		}
		late++
		pos := map[string]int{}
		for i, id := range order {
			pos[id] = i
		}
		for _, id := range order {
			for _, k := range meta[id].HostModules {
				dep := strings.TrimSuffix(k, ".ts")
				lazy := !slices.Contains(meta[id].LoadHostModules, k)
				switch {
				case !lazy && pos[dep] > pos[id]:
					t.Fatalf("%v listed %q: %s imports %s at load time but comes first in %q", imports, list, id, dep, order)
				case lazy && (pos[dep] > pos[id]) != slices.Contains(lateOf[id], k):
					t.Fatalf("%v listed %q: %s's lazy import of %s: late %q in %q", imports, list, id, dep, lateOf[id], order)
				}
			}
		}
	}
	t.Logf("lists ordered as when every import ordered: %d; with lazy imports set aside: %d; refused: %d", same, late, refused)
	if same < 1000 || late < 400 || refused < 400 {
		t.Errorf("too few lists of a kind: %d, %d, %d", same, late, refused)
	}
}

// TestLazyImports runs the build helper's parse of where a module's
// import() calls sit: a specifier every import() of which is inside a
// function, an arrow or a method body is lazy; one evaluated at module top
// level (a class's static block, a field initialiser, a computed key, a
// decorator) is not, and a module that does not parse has none.
func TestLazyImports(t *testing.T) {
	bun := bunPath(t)
	dir := t.TempDir()
	cases := []struct {
		name, syntax, source string
		want                 []string
	}{
		{"function", "ts", `export async function f() { return import("./a.ts"); }`, []string{"./a.ts"}},
		{"arrow", "ts", "export const f = async () => (await import(`./a.ts`)).a;", []string{"./a.ts"}},
		{"method and accessor", "ts", `export const o = { async m() { return import("./a"); }, get g() { return import("./b"); } };
export class K { async m() { return import("./c"); } static async s() { return import("./d"); } #p() { return import("./e"); } constructor() { import("./f"); } }`,
			[]string{"./a", "./b", "./c", "./d", "./e", "./f"}},
		{"default parameter", "ts", `export function f(m = import("./a")) { return m; }`, []string{"./a"}},
		{"top-level await", "ts", `const m = await import("./a.ts"); export const x = m;`, []string{}},
		{"top level and in a function", "ts", `export const f = () => import("./a"); import("./a");`, []string{}},
		{"class body outside methods", "ts", `export class K { static { import("./a"); } static s = import("./b"); i = import("./c"); [import("./d")]() {} }`, []string{}},
		{"decorator", "ts", `function d(_: unknown) {} export class K { @d(import("./a")) m() { return import("./b"); } }`, []string{"./b"}},
		{"a call at load is not followed", "ts", `export async function later() { return import("./a"); } await later();`, []string{"./a"}},
		{"a computed specifier is no specifier", "ts", `export const f = (n: string) => import("./" + n);`, []string{}},
		{"tsx", "tsx", `export const C = () => <div onClick={() => import("./a")} />;`, []string{"./a"}},
		{"does not parse", "ts", `export function (`, nil},
	}
	var mods []helperLazyModule
	for i, c := range cases {
		file := filepath.Join(dir, fmt.Sprintf("m%d.%s", i, c.syntax))
		if err := os.WriteFile(file, []byte(c.source), 0o644); err != nil {
			t.Fatal(err)
		}
		mods = append(mods, helperLazyModule{File: file, Syntax: c.syntax})
	}
	missing := filepath.Join(dir, "missing.ts")
	mods = append(mods, helperLazyModule{File: missing, Syntax: "ts"})
	bc := BunConfig{Bun: bun, Helper: repoPath(t, "harnesses/bun/build/main.ts"), Folder: dir, Out: dir, Work: t.TempDir()}
	req := helperRequest{Version: 1, Op: "lazy", Folder: dir, Work: bc.Work, Out: dir,
		Scripts: map[string]string{}, Deps: map[string]string{}, Define: map[string]string{},
		HostModules: []helperHostModule{}, Entries: []helperEntry{}, Lazy: mods}
	resp, err := runHelper(context.Background(), &bc, &req)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		got, ok := resp.Lazy[mods[i].File]
		if !ok || (c.want == nil) != (got == nil) || !slices.Equal(got, c.want) {
			t.Errorf("%s: got %q (reported %v), want %q", c.name, got, ok, c.want)
		}
	}
	if got, ok := resp.Lazy[missing]; !ok || got != nil {
		t.Errorf("a file that does not read: got %q (reported %v), want null", got, ok)
	}
}
