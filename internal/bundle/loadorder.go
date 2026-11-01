package bundle

import (
	"fmt"
	"slices"
	"strings"
)

// The order in which host-provided modules are registered (ADR 0010, item
// 1). A shim takes its module's exports from the registry when it is
// evaluated, so a listed module that imports another listed module while it
// loads must find that module registered. An import() of a listed module
// inside a function runs only when the function is called, normally after
// every listed module has registered, so it puts no constraint on the order.
//
// The analysis here tells the two apart for both bundlers. Each bundler
// reports every import of a listed module in a listed module's bundle
// (hostImport). The build helper's parse reports, for each module that
// holds an import() of one, the specifiers it imports only from inside
// functions.

// hostImport is one import of a listed module in a listed module's bundle,
// as a bundler's metafile and plugin record it.
type hostImport struct {
	// From is the module of the bundle that holds the import, as the bundle
	// names its input.
	From string `json:"from"`
	// Kind is the bundler's kind of import: "import-statement",
	// "dynamic-import", "require-call" and so on. Only "dynamic-import" can
	// be lazy.
	Kind string `json:"kind"`
	// Specifier is the import's specifier as From spells it.
	Specifier string `json:"specifier"`
	// Host is the key of the listed module the import resolves to.
	Host string `json:"host"`
}

// importGraph is what the load-order analysis reads of one listed module's
// bundle.
type importGraph struct {
	Imports []hostImport `json:"imports"`
	// Lazy gives, for a module that holds an import() of a listed module,
	// the specifiers it imports only through import() calls inside functions
	// (the build helper's lazyImports). A module that is absent, or whose
	// value is null because it does not parse, has none.
	Lazy map[string][]string `json:"lazy"`
}

// loadTimeHostModules returns those of keys, the listed modules that a
// listed module's bundle imports, that it imports at load time, sorted. g
// is the bundle's import graph.
//
// An import of a listed module is lazy when it is an import() of that
// module inside a function: an import() whose specifier its module imports
// only from inside functions (functions, methods, arrows and class methods,
// whose bodies run when they are called). Its module may be the listed
// module or any module of its bundle. A key is lazy when g holds an import
// of it and every import of it is lazy.
//
// Every other import is a load-time one, wherever its module sits in the
// bundle: a static import or re-export, a require() call, and an import()
// evaluated at module top level.
//
// That includes the static import of a listed module by a module that the
// bundle loads only through an import() inside a function. Such a module
// reads what it imports while it is initialised, and both bundlers
// initialise a module once: a read of an unregistered module there would
// leave that module's exports undefined for every later import of it.
//
// Where the analysis cannot tell, it takes the reading that orders more: a
// key of which g holds no import is a load-time one, since the bundler's
// records did not show how it is imported, and an import() whose module
// does not parse is a load-time one.
func loadTimeHostModules(g importGraph, keys []string) []string {
	seen, atLoad := map[string]bool{}, map[string]bool{}
	for _, im := range g.Imports {
		seen[im.Host] = true
		if im.Kind != "dynamic-import" || !slices.Contains(g.Lazy[im.From], im.Specifier) {
			atLoad[im.Host] = true
		}
	}
	out := []string{}
	for _, k := range keys {
		if (atLoad[k] || !seen[k]) && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// hostModuleOrder orders host-provided modules for registration. Each
// module comes after every listed module it imports at load time
// (HostModuleMeta.LoadHostModules), and after every one it imports only
// lazily too, unless that would close a cycle; otherwise the modules keep
// the order of mods. A list in which no module imports another lazily, or
// whose lazy imports close no cycle, is therefore ordered as if every import
// ordered it.
//
// The order is a depth-first walk of mods, in their order, that visits a
// module's imports of listed modules in the order of HostModules:
//
//   - a lazy import of a module already on the walk's path is set aside:
//     that module cannot come first;
//   - when a load-time import closes a cycle through a lazy import on the
//     path, the first such lazy import on the cycle is set aside and the
//     walk starts again. Each pass sets one more aside, so the walk ends;
//   - a cycle of load-time imports alone fails, naming it by ID:
//     "host-provided modules import each other: a → b → a".
//
// late gives, by module ID, the keys of the listed modules it imports lazily
// that come after it in the order (sorted): the imports set aside. Every
// other import of a listed module, load-time or lazy, finds that module
// registered before the importer loads.
func hostModuleOrder(mods []HostModule, meta map[string]*HostModuleMeta) (order []string, late map[string][]string, err error) {
	byKey := map[string]string{}
	for _, m := range mods {
		byKey[m.Key] = m.ID
	}
	aside := map[[2]string]bool{}
	for {
		var conflict *[2]string
		order, conflict, err = orderPass(mods, meta, byKey, aside)
		if err != nil {
			return nil, nil, err
		}
		if conflict == nil {
			break
		}
		aside[*conflict] = true
	}
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	late = map[string][]string{}
	for _, id := range order {
		for _, k := range meta[id].HostModules {
			if dep, ok := byKey[k]; ok && pos[dep] > pos[id] {
				late[id] = append(late[id], k)
			}
		}
		slices.Sort(late[id])
	}
	return order, late, nil
}

// orderPass is one depth-first walk of hostModuleOrder with the lazy
// imports in aside (importer ID, imported ID) left out. It returns the
// order, or the lazy import to set aside before the next pass, or the error
// of a cycle of load-time imports.
func orderPass(mods []HostModule, meta map[string]*HostModuleMeta, byKey map[string]string, aside map[[2]string]bool) ([]string, *[2]string, error) {
	const (
		visiting = 1
		done     = 2
	)
	state := map[string]int{}
	var order, path []string
	// lazyIn[i] says that path[i] was reached from path[i-1] by a lazy
	// import.
	var lazyIn []bool
	var visit func(id string, lazy bool) (*[2]string, error)
	visit = func(id string, lazy bool) (*[2]string, error) {
		state[id] = visiting
		path, lazyIn = append(path, id), append(lazyIn, lazy)
		for _, k := range meta[id].HostModules {
			dep, ok := byKey[k]
			if !ok {
				continue
			}
			soft := !slices.Contains(meta[id].LoadHostModules, k)
			if soft && aside[[2]string{id, dep}] {
				continue
			}
			switch state[dep] {
			case done:
				continue
			case visiting:
				if soft {
					continue
				}
				start := slices.Index(path, dep)
				for j := start + 1; j < len(path); j++ {
					if lazyIn[j] {
						return &[2]string{path[j-1], path[j]}, nil
					}
				}
				cycle := append(slices.Clone(path[start:]), dep)
				return nil, fmt.Errorf("host-provided modules import each other: %s", strings.Join(cycle, " → "))
			}
			if c, err := visit(dep, soft); c != nil || err != nil {
				return c, err
			}
		}
		state[id] = done
		path, lazyIn = path[:len(path)-1], lazyIn[:len(lazyIn)-1]
		order = append(order, id)
		return nil, nil
	}
	for _, m := range mods {
		if state[m.ID] == 0 {
			if c, err := visit(m.ID, false); c != nil || err != nil {
				return nil, c, err
			}
		}
	}
	return order, nil, nil
}
