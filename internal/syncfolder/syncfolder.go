// Package syncfolder reads a `wmill sync` folder: it finds the root, reads
// wmill.yaml, walks the tree, maps files to Windmill paths, applies the
// filters, and reads scripts, flows, resources, resource types and
// variables as the wmill CLI would push them (SY-1 to SY-15, SY-V1 to
// SY-V4). It is step 1 of the loader pipeline of ADR 0002.
//
// Loading is in two stages. Load and Open classify the files and fail only
// when the folder as a whole cannot be read: no wmill.yaml, an invalid
// wmill.yaml, or an I/O error during the walk. Everything about one item is
// read, checked and reported when the item is asked for (Script, Flow,
// Resource, Variable, ResourceType, Target), so a build that loads only
// the items its roots reach fails only for those (ADR 0002, host
// contract). Check reads every item, as `wmill sync push` would.
package syncfolder

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
)

// ItemKind is the kind of a workspace item. Each kind has its own paths: a
// script and a flow may share one.
type ItemKind string

const (
	KindScript       ItemKind = "script"
	KindFlow         ItemKind = "flow"
	KindResource     ItemKind = "resource"
	KindVariable     ItemKind = "variable"
	KindResourceType ItemKind = "resource_type"
)

var itemKinds = []ItemKind{KindScript, KindFlow, KindResource, KindVariable, KindResourceType}

// Folder is a classified sync folder. Its methods may be called
// concurrently; Close must not run concurrently with them. Each read goes
// to the files again, so a read reflects the folder at that time.
type Folder struct {
	// Root is the sync root as an OS path, for a folder from Open; it is
	// empty for a folder from Load.
	Root   string
	Config *Config
	// Skipped lists the files that windmill-lite recognises and does not
	// load: schedules, triggers, apps, users, groups, settings, workspace
	// dependencies and the like (SY-12), and the CLI's lock cache (SY-14).
	Skipped []Skipped
	// Warnings are diagnostics that do not fail the load: defaults applied,
	// files ignored, and the problems of items that are read only on
	// demand.
	Warnings []string

	fsys    fs.FS
	closer  io.Closer
	entries map[ItemKind]map[string]*entry
	// dotted maps the path a refused dotted script file would have
	// without the CLI's truncation to that file, for hints.
	dotted map[string]string
}

// Skipped is a file recognised and not loaded.
type Skipped struct {
	File string `json:"file"`
	// Kind is what the file is: schedule, trigger, native_trigger,
	// folder, user, group, settings, encryption_key, app, raw_app,
	// workspace_dependency, migration, ui, override, flow_json,
	// wmill_lock or wmillignore.
	Kind string `json:"kind"`
	Note string `json:"note,omitempty"`
}

// entry is one item of the index: the files that define it, and whether
// it is filtered out or has a problem that reading it reports.
type entry struct {
	kind ItemKind
	path string
	// file is the item's main file: a script's source, a flow folder, a
	// resource, variable or resource-type file. Filters apply to it.
	file string
	// meta is a script's metadata file, "" when it has none.
	meta     string
	filtered string
	err      *Error
}

// Find returns the sync root for a working directory: the nearest
// directory, from dir upwards, that holds wmill.yaml. The search stops
// after the git root (a directory holding `.git`) or the file system root
// (SY-1).
func Find(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", &Error{Kind: ErrIO, Msg: err.Error(), Err: err}
	}
	for d := abs; ; {
		if st, err := os.Stat(filepath.Join(d, ConfigFile)); err == nil && st.Mode().IsRegular() {
			return d, nil
		}
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return "", &Error{Kind: ErrNoConfig, Msg: fmt.Sprintf("no %s in %s or its parents up to the git root", ConfigFile, abs)}
}

// Open finds the sync root from dir and loads it. Reads are confined to
// the root: a symbolic link that leads outside it fails to read. The root
// is always the directory of wmill.yaml, also when wmill.yaml sets
// noCdToRoot, under which the CLI would walk dir instead (SY-V4); Open
// then adds a warning. Close the folder when done.
func Open(dir string) (*Folder, error) {
	root, err := Find(dir)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, &Error{Kind: ErrIO, Msg: err.Error(), Err: err}
	}
	f, err := Load(r.FS())
	if err != nil {
		r.Close()
		return nil, err
	}
	f.Root, f.closer = root, r
	if abs, _ := filepath.Abs(dir); f.Config.NoCdToRoot && abs != root {
		f.Warnings = append(f.Warnings, fmt.Sprintf(
			"wmill.yaml sets noCdToRoot, under which the CLI walks %s; windmill-lite walks the directory of wmill.yaml, %s (SY-V4)", abs, root))
	}
	return f, nil
}

// Load classifies the sync folder whose root is fsys. wmill.yaml must be
// at the root of fsys.
func Load(fsys fs.FS) (*Folder, error) {
	src, err := fs.ReadFile(fsys, ConfigFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, &Error{Kind: ErrNoConfig, Msg: "no " + ConfigFile + " at the root"}
	}
	if err != nil {
		return nil, ioError(ConfigFile, err)
	}
	cfg, warnings, err := parseConfig(src)
	if err != nil {
		return nil, err
	}
	f := &Folder{
		Config:   cfg,
		Warnings: warnings,
		fsys:     fsys,
		entries:  map[ItemKind]map[string]*entry{},
		dotted:   map[string]string{},
	}
	for _, k := range itemKinds {
		f.entries[k] = map[string]*entry{}
	}
	w := &walker{f: f, scripts: map[string]*scriptFiles{}, groups: map[ItemKind]map[string][]string{}}
	if err := w.walk(); err != nil {
		return nil, err
	}
	w.index()
	return f, nil
}

// Close releases the folder's root, for a folder from Open.
func (f *Folder) Close() error {
	if f.closer == nil {
		return nil
	}
	return f.closer.Close()
}

// FS returns the folder's file system, rooted at the sync root, for
// builders that read further files (relative imports, codebase sources).
func (f *Folder) FS() fs.FS { return f.fsys }

// Paths returns the sorted paths of the items of a kind, filtered ones
// excluded. Items that have a problem are listed; reading them reports it.
func (f *Folder) Paths(kind ItemKind) []string {
	var out []string
	for p, e := range f.entries[kind] {
		if e.filtered == "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// Filtered returns the sorted paths of the items of a kind that wmill.yaml
// filters out.
func (f *Folder) Filtered(kind ItemKind) []string {
	var out []string
	for p, e := range f.entries[kind] {
		if e.filtered != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// Check reads every item that is not filtered out and returns the errors
// joined, in kind and path order, or nil. It refuses what `wmill sync
// push` would refuse, and a few things more (the restrictions of ADR 0001
// §5a); a build that loads only what its roots reach need not call it.
func (f *Folder) Check() error {
	var errs []error
	for _, k := range itemKinds {
		for _, p := range f.Paths(k) {
			if _, err := f.read(k, p); err != nil {
				var e *Error
				if errors.As(err, &e) && e.Kind == ErrFiltered {
					continue // a secret under skipSecrets
				}
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (f *Folder) read(k ItemKind, p string) (any, error) {
	switch k {
	case KindScript:
		return f.Script(p)
	case KindFlow:
		return f.Flow(p)
	case KindResource:
		return f.Resource(p)
	case KindVariable:
		return f.Variable(p)
	}
	return f.ResourceType(p)
}

// lookup returns the entry of an item that may be read, or the error that
// reading it reports: not in the folder, filtered out, or its problem.
func (f *Folder) lookup(k ItemKind, p string) (*entry, error) {
	item := string(k) + " " + p
	e := f.entries[k][p]
	if e == nil {
		msg := "no " + string(k) + " has this path in the folder"
		if k == KindScript {
			if file, ok := f.dotted[p]; ok {
				msg += fmt.Sprintf("; %s would have it, but the CLI cuts that file's path at its first dot, which windmill-lite refuses", file)
			}
		}
		return nil, &Error{Kind: ErrNotInFolder, Item: item, Msg: msg}
	}
	if e.filtered != "" {
		return nil, &Error{Kind: ErrFiltered, Item: item, File: e.file, Msg: "filtered out: " + e.filtered}
	}
	if e.err != nil {
		return nil, e.err.withItem(item)
	}
	return e, nil
}

func ioError(file string, err error) *Error {
	return &Error{Kind: ErrIO, File: file, Msg: err.Error(), Err: err}
}

// readFile reads a file of the folder. found is false when the file does
// not exist or is a directory, which the CLI's reads treat as missing.
func (f *Folder) readFile(p string) (data []byte, found bool, err error) {
	st, err := fs.Stat(f.fsys, p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, false, nil
	case err != nil:
		return nil, false, ioError(p, err)
	case st.IsDir():
		return nil, false, nil
	}
	data, err = fs.ReadFile(f.fsys, p)
	if err != nil {
		return nil, false, ioError(p, err)
	}
	return data, true, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
