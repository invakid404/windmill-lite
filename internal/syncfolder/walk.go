package syncfolder

import (
	"fmt"
	"io/fs"
	"path"
	"strings"
	"unicode/utf8"
)

// walker classifies the files of the folder (SY-1, SY-2, SY-6, SY-10 to
// SY-12, SY-18) and then builds the index.
type walker struct {
	f *Folder
	// scripts groups script files by the path the CLI gives them.
	scripts map[string]*scriptFiles
	// groups holds the main files of flows (their folders), resources,
	// variables and resource types, by item path.
	groups map[ItemKind]map[string][]string
	// flowFiles lists the files inside each flow folder, at any depth.
	flowFiles map[string][]string
	// candidates lists the other files of the loaded kinds that the
	// CLI's walk keeps, before the filter and the skip switches (SY-18).
	candidates []candidate
}

// candidate is a file the CLI's walk keeps, and the kind of its item.
type candidate struct {
	file string
	kind ItemKind
}

type scriptFiles struct {
	sources []string
	metas   []string
	mods    []string // `__mod` folders (SY-2, later scope)
}

// The top-level directories that hold workspace items (SY-1).
var itemRoots = map[string]bool{"u": true, "f": true, "g": true}

// triggerKinds are the `<kind>_trigger.yaml` kinds (SY-12).
var triggerKinds = map[string]bool{
	"http": true, "websocket": true, "kafka": true, "nats": true, "postgres": true, "mqtt": true,
	"amqp": true, "sqs": true, "gcp": true, "azure": true, "email": true,
}

func (w *walker) skip(file, kind, note string) {
	w.f.Skipped = append(w.f.Skipped, Skipped{File: file, Kind: kind, Note: note})
}

func (w *walker) warn(format string, args ...any) {
	w.f.Warnings = append(w.f.Warnings, fmt.Sprintf(format, args...))
}

func (w *walker) candidate(file string, k ItemKind) {
	w.candidates = append(w.candidates, candidate{file, k})
}

func (w *walker) add(k ItemKind, p, file string) {
	if w.groups[k] == nil {
		w.groups[k] = map[string][]string{}
	}
	w.groups[k][p] = append(w.groups[k][p], file)
}

func (w *walker) script(p string) *scriptFiles {
	s := w.scripts[p]
	if s == nil {
		s = &scriptFiles{}
		w.scripts[p] = s
	}
	return s
}

// list reads a directory. A symbolic link to a directory is not followed;
// any other symbolic link is kept as a file, and reading it is confined to
// the root.
func (w *walker) list(dir string) (dirs, files []string, err error) {
	ents, err := fs.ReadDir(w.f.fsys, dir)
	if err != nil {
		return nil, nil, ioError(dir, err)
	}
	for _, e := range ents {
		p := path.Join(dir, e.Name())
		if !utf8.ValidString(e.Name()) {
			// The CLI reads names as UTF-8 with replacement characters,
			// under which it cannot open the file again.
			w.warn("%q is ignored: its name is not valid UTF-8", p)
			continue
		}
		isDir := e.IsDir()
		if e.Type()&fs.ModeSymlink != 0 {
			st, err := fs.Stat(w.f.fsys, p)
			switch {
			case err != nil:
				w.warn("%s is a symbolic link that cannot be followed: %v", p, err)
			case st.IsDir():
				w.warn("%s is a symbolic link to a directory, which is not followed", p)
				continue
			}
		}
		if isDir {
			dirs = append(dirs, p)
		} else {
			files = append(files, p)
		}
	}
	return dirs, files, nil
}

func (w *walker) walk() error {
	dirs, files, err := w.list(".")
	if err != nil {
		return err
	}
	for _, p := range files {
		switch name := p; {
		case name == ConfigFile:
		case name == "wmill-lock.yaml":
			w.skip(p, "wmill_lock", "the CLI's lock cache, which windmill-lite does not read (SY-14)")
		case name == ".wmillignore":
			w.skip(p, "wmillignore", "not supported; the CLI ignores it too (SY-1)")
		case name == "settings.yaml" || strings.HasPrefix(name, "settings.") && strings.HasSuffix(name, ".yaml"):
			w.skip(p, "settings", "")
		case name == "encryption_key.yaml":
			w.skip(p, "encryption_key", "")
		case strings.HasSuffix(name, ".resource-type.yaml"):
			w.add(KindResourceType, strings.TrimSuffix(name, ".resource-type.yaml"), p)
			w.candidate(p, KindResourceType)
		case strings.HasSuffix(name, ".resource-type.json"):
			w.dropJSON(p)
		}
	}
	for _, d := range dirs {
		var err error
		switch d {
		case "u", "f", "g":
			err = w.items(d)
		case "users":
			err = w.special(d, func(p string) string { return kindIf(strings.HasSuffix(p, ".user.yaml"), "user") })
		case "groups":
			err = w.special(d, func(p string) string { return kindIf(strings.HasSuffix(p, ".group.yaml"), "group") })
		case "dependencies":
			err = w.special(d, func(p string) string { return kindIf(isWorkspaceDependency(path.Base(p)), "workspace_dependency") })
		case "migrations":
			err = w.special(d, func(p string) string { return kindIf(strings.HasPrefix(p, "migrations/datatable/"), "migration") })
		case "ui":
			w.skip(d, "ui", "synced separately (SY-1)")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func kindIf(ok bool, kind string) string {
	if ok {
		return kind
	}
	return ""
}

func isWorkspaceDependency(name string) bool {
	for _, f := range []string{"package.json", "requirements.in", "composer.json", "go.mod", "modules.json"} {
		if name == f || strings.HasSuffix(name, "."+f) {
			return true
		}
	}
	return false
}

// special lists the recognised files of a top-level directory of other
// workspace objects (SY-12).
func (w *walker) special(dir string, kind func(string) string) error {
	dirs, files, err := w.list(dir)
	if err != nil {
		return err
	}
	for _, p := range files {
		if k := kind(p); k != "" {
			w.skip(p, k, "")
		}
	}
	for _, d := range dirs {
		if skippedDir(path.Base(d)) {
			continue
		}
		if err := w.special(d, kind); err != nil {
			return err
		}
	}
	return nil
}

// skippedDir reports the directories the walk never enters (SY-1).
func skippedDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules"
}

// items walks a directory under u/, f/ or g/.
func (w *walker) items(dir string) error {
	dirs, files, err := w.list(dir)
	if err != nil {
		return err
	}
	for _, d := range dirs {
		name := path.Base(d)
		switch {
		case skippedDir(name):
		case strings.HasSuffix(name, ".flow"), strings.HasSuffix(name, "__flow"):
			// Every file inside is part of the flow (SY-18), nested
			// folders included.
			p := strings.TrimSuffix(strings.TrimSuffix(d, ".flow"), "__flow")
			w.add(KindFlow, p, d)
			if err := w.tree(d, d); err != nil {
				return err
			}
		case strings.HasSuffix(name, ".raw_app"), strings.HasSuffix(name, "__raw_app"):
			w.skip(d, "raw_app", "")
		case strings.HasSuffix(name, ".app"), strings.HasSuffix(name, "__app"):
			w.skip(d, "app", "")
		case strings.HasSuffix(name, "__mod"):
			s := w.script(strings.TrimSuffix(d, "__mod"))
			s.mods = append(s.mods, d)
		case strings.HasSuffix(name, ".fileset"):
			// The files of a fileset resource (SY-10).
		default:
			if err := w.items(d); err != nil {
				return err
			}
		}
	}
	for _, p := range files {
		w.file(p)
	}
	return nil
}

// tree lists the files under dir, at any depth, as files of the flow
// folder flow. It skips the directories the walk never enters.
func (w *walker) tree(flow, dir string) error {
	dirs, files, err := w.list(dir)
	if err != nil {
		return err
	}
	w.flowFiles[flow] = append(w.flowFiles[flow], files...)
	for _, d := range dirs {
		if skippedDir(path.Base(d)) {
			continue
		}
		if err := w.tree(flow, d); err != nil {
			return err
		}
	}
	return nil
}

// file classifies one file under u/, f/ or g/ (SY-18). Script sources are
// tested before the type part of a name, so `x.script.ts` is a source.
func (w *walker) file(p string) {
	name := path.Base(p)
	switch {
	case strings.Contains(name, ".resource.file."):
		// The text of a file resource (SY-10), which the extension list
		// does not limit.
		w.candidate(p, KindResource)
	case scriptExtension(name) != "":
		s := w.script(cliScriptPath(p))
		s.sources = append(s.sources, p)
		w.candidate(p, KindScript)
	case strings.HasSuffix(name, ".script.lock"):
		w.candidate(p, KindScript)
	case strings.HasSuffix(name, ".yaml"):
		w.metadata(p, ".yaml")
	case strings.HasSuffix(name, ".json"):
		w.metadata(p, ".json")
	}
}

// listedExtensions are the last extensions of the files the CLI's walk
// keeps (SY-18). `.json` is among them only in JSON mode, which
// windmill-lite refuses.
var listedExtensions = map[string]bool{
	"yaml": true, "go": true, "sh": true, "ts": true, "py": true, "sql": true, "gql": true,
	"ps1": true, "php": true, "js": true, "lock": true, "rs": true, "cs": true, "yml": true,
	"nu": true, "java": true, "rb": true, "r": true,
}

// listed reports whether the walk keeps a file by its name: the text after
// its last dot, or the whole name when it has none, is a listed extension.
func listed(name string) bool {
	return listedExtensions[name[strings.LastIndexByte(name, '.')+1:]]
}

// neverSelects reports the files that never select an item when they are
// new, as every file is in a fresh deploy: metadata, lock and resource
// content files (SY-17).
func neverSelects(name string) bool {
	return strings.HasSuffix(name, ".script.yaml") || strings.HasSuffix(name, ".script.json") ||
		strings.HasSuffix(name, ".lock") || strings.Contains(name, ".resource.file.")
}

// dropJSON notes a .json item file, which the walk drops in YAML mode
// (SY-18).
func (w *walker) dropJSON(p string) {
	w.warn("%s is ignored: the CLI drops .json files unless wmill.yaml sets json: true (SY-18)", p)
}

// metadata classifies a `<p>.<type>.yaml` or `.json` file.
func (w *walker) metadata(p, ext string) {
	dir, name := path.Split(p)
	stem := strings.TrimSuffix(name, ext)
	dot := strings.LastIndexByte(stem, '.')
	if dot < 0 {
		return // not a `<p>.<type>` file
	}
	typ, base := stem[dot+1:], path.Join(dir, stem[:dot])
	if typ == "meta" && (stem[:dot] == "folder" || strings.HasPrefix(stem[:dot], "folder.")) {
		w.skip(p, "folder", "")
		return
	}
	if ext == ".json" && (typ == "resource" || typ == "variable" || typ == "resource-type") {
		// A script's .json metadata is still paired with it, and refused
		// (SY-4).
		w.dropJSON(p)
		return
	}
	var kind ItemKind
	switch {
	case typ == "script":
		kind = KindScript
	case typ == "resource":
		kind = KindResource
	case typ == "variable":
		kind = KindVariable
	case typ == "schedule":
		w.skip(p, "schedule", "")
		return
	case strings.HasSuffix(typ, "_native_trigger"):
		w.skip(p, "native_trigger", "")
		return
	case strings.HasSuffix(typ, "_trigger") && triggerKinds[strings.TrimSuffix(typ, "_trigger")]:
		w.skip(p, "trigger", "")
		return
	case typ == "flow" && ext == ".json":
		w.skip(p, "flow_json", "the CLI does not push a flat .flow.json; flows are folders holding flow.yaml (PA-1)")
		w.warn("%s is ignored: the CLI does not push a flat .flow.json (PA-1)", p)
		return
	case typ == "resource-type":
		w.warn("%s is ignored: resource types are read only at the root (SY-10)", p)
		return
	default:
		if ext == ".yaml" {
			w.warn("%s is ignored: %q is not a type of file the loader reads", p, typ)
		}
		return
	}
	if ws, ok := w.f.Config.overrideWorkspace(base); ok {
		w.skip(p, "override", fmt.Sprintf("the %s override of workspace %q (SY-12)", kind, ws))
		return
	}
	if kind == KindScript {
		s := w.script(base)
		s.metas = append(s.metas, p)
		if ext == ".yaml" {
			w.candidate(p, KindScript)
		}
		return
	}
	w.add(kind, base, p)
	w.candidate(p, kind)
}

// index builds the folder's entries from the classified files: it applies
// the skip switches and the path filter to the files that select each item
// (SY-13, SY-17), records the files the CLI's walk reads (SY-18), then the
// problems that reading an item reports (SY-2, SY-6).
func (w *walker) index() {
	cfg := w.f.Config
	for _, k := range w.candidates {
		// Resource types bypass the path filter (SY-13).
		if !cfg.skips(k.kind) && (k.kind == KindResourceType || cfg.pathFilter(k.file) == "") {
			w.f.walked[k.file] = true
		}
	}
	for _, files := range w.flowFiles {
		for _, p := range files {
			if !cfg.SkipFlows && listed(path.Base(p)) && cfg.pathFilter(p) == "" {
				w.f.walked[p] = true
			}
		}
	}
	for _, p := range sortedKeys(w.scripts) {
		w.indexScript(p, w.scripts[p])
	}

	flows := w.groups[KindFlow]
	reasons := map[string]string{} // flow folder → why no file selects it, "" when one does
	var wrong []string
	for _, p := range sortedKeys(flows) {
		for _, d := range flows[p] {
			reason, none := w.selectFlow(d)
			if none {
				w.warn("%s is ignored: no file in it selects the flow, as no file there is one the walk keeps that is not a metadata, lock or resource content file (SY-17)", d)
				continue
			}
			reasons[d] = reason
			if reason == "" && strings.HasSuffix(d, "__flow") != cfg.NonDottedPaths {
				wrong = append(wrong, d)
			}
		}
	}
	for _, p := range sortedKeys(flows) {
		e := &entry{kind: KindFlow, path: p}
		var kept []string
		for _, d := range flows[p] {
			reason, ok := reasons[d]
			switch {
			case !ok:
			case reason == "":
				kept = append(kept, d)
			case e.filtered == "":
				e.filtered, e.file = reason, d
			}
		}
		switch {
		case len(kept) == 0 && e.filtered == "":
			continue // no folder of this path is a flow
		case cfg.SkipFlows:
			e.filtered = "wmill.yaml sets skipFlows"
			if e.file == "" {
				e.file = kept[0]
			}
		case len(kept) == 0:
		case len(kept) > 1:
			e.filtered, e.file = "", kept[0]
			e.err = duplicate(kept, "flow")
		default:
			e.filtered, e.file = "", kept[0]
			if strings.HasSuffix(kept[0], "__flow") != cfg.NonDottedPaths {
				e.err = layoutError(kept[0], wrong, cfg.NonDottedPaths)
			}
		}
		w.put(e)
	}

	for _, k := range []ItemKind{KindResource, KindVariable, KindResourceType} {
		for _, p := range sortedKeys(w.groups[k]) {
			files := w.groups[k][p]
			e := &entry{kind: k, path: p, file: files[0]}
			var kept []string
			for _, file := range files {
				reason := ""
				if k != KindResourceType {
					reason = cfg.pathFilter(file) // resource types bypass it (SY-13)
				}
				if reason == "" {
					kept = append(kept, file)
				} else if e.filtered == "" {
					e.filtered = reason
				}
			}
			switch {
			case k == KindResource && cfg.SkipResources:
				e.filtered = "wmill.yaml sets skipResources"
			case k == KindVariable && cfg.SkipVariables:
				e.filtered = "wmill.yaml sets skipVariables"
			case k == KindResourceType && cfg.SkipResourceTypes:
				e.filtered = "wmill.yaml sets skipResourceTypes"
			case len(kept) == 0:
			case len(kept) > 1:
				e.filtered, e.file = "", kept[0]
				e.err = duplicate(kept, string(k))
			default:
				e.filtered, e.file = "", kept[0]
			}
			w.put(e)
		}
	}
}

// selectFlow applies SY-17 to a flow folder. In a fresh deploy every file
// is new, and a new file selects its flow when the walk keeps it (SY-18),
// it passes the path filter, and it is not a metadata, lock or resource
// content file. It returns "" when a file selects the folder, and
// otherwise why none does; none is true when no file of the folder could
// select it, whatever the filter.
func (w *walker) selectFlow(d string) (reason string, none bool) {
	for _, p := range w.flowFiles[d] {
		name := path.Base(p)
		if !listed(name) || neverSelects(name) {
			continue
		}
		r := w.f.Config.pathFilter(p)
		if r == "" {
			return "", false
		}
		if reason == "" {
			reason = fmt.Sprintf("no file of the flow folder passes the path filter (SY-17); %s does not: %s", p, r)
		}
	}
	return reason, reason == ""
}

func (w *walker) indexScript(p string, s *scriptFiles) {
	cfg := w.f.Config
	e := &entry{kind: KindScript, path: p}
	var kept []string
	for _, src := range s.sources {
		if reason := cfg.pathFilter(src); reason == "" {
			kept = append(kept, src)
		} else if e.filtered == "" {
			e.filtered, e.file = reason, src
		}
	}
	for _, m := range s.mods {
		if reason := cfg.pathFilter(m + "/script.yaml"); reason == "" {
			kept = append(kept, m)
		} else if e.filtered == "" {
			e.filtered, e.file = reason, m
		}
	}
	if len(s.metas) > 0 {
		e.meta = s.metas[0]
	}
	switch {
	case cfg.SkipScripts:
		e.filtered = "wmill.yaml sets skipScripts"
		switch {
		case e.file != "":
		case len(s.sources) > 0:
			e.file = s.sources[0]
		case len(s.mods) > 0:
			e.file = s.mods[0]
		}
	case len(kept) == 0 && e.filtered != "":
	case len(kept) == 0:
		e.file = s.metas[0]
		e.err = &Error{Kind: ErrMissingSource, File: e.file,
			Msg: "script metadata without a source file " + p + ".<ext>"}
	case len(kept) > 1:
		e.filtered, e.file = "", kept[0]
		e.err = duplicate(kept, "script")
	default:
		e.filtered, e.file = "", kept[0]
		switch {
		case strings.HasSuffix(e.file, "__mod"):
			e.err = &Error{Kind: ErrUnsupported, File: e.file,
				Msg: "unsupported: multi-file script (the __mod folder layout of SY-2)"}
		case len(s.metas) > 1:
			e.err = duplicate(s.metas, "script metadata")
		case strings.HasSuffix(e.meta, ".json"):
			e.err = jsonModeError(e.meta)
		default:
			if logical := logicalScriptPath(e.file); logical != p {
				w.f.dotted[logical] = e.file
				e.err = &Error{Kind: ErrDottedPath, File: e.file, Msg: fmt.Sprintf(
					"the CLI maps this file to %s by cutting its path at the first dot; windmill-lite refuses dotted script paths such as %s instead of renaming the script (ADR 0002)",
					p, logical)}
			}
		}
	}
	w.put(e)
}

func (w *walker) put(e *entry) {
	w.f.entries[e.kind][e.path] = e
	if e.err != nil && e.filtered == "" {
		w.warn("%s", e.err.withItem(string(e.kind)+" "+e.path).Error())
	}
}

func duplicate(files []string, what string) *Error {
	return &Error{Kind: ErrDuplicate, File: files[0],
		Msg: fmt.Sprintf("%d files define this %s: %s", len(files), what, strings.Join(files, ", "))}
}

func jsonModeError(file string) *Error {
	return &Error{Kind: ErrUnsupported, File: file,
		Msg: "unsupported: JSON-mode metadata (a tree pulled with json: true; SY-2, SY-13)"}
}

// layoutError is the CLI's refusal of a tree that mixes flow folder styles
// (SY-6), which windmill-lite reports for each flow in the other style.
func layoutError(dir string, wrong []string, nonDotted bool) *Error {
	style, want := ".flow", "nonDottedPaths: true selects __flow"
	if !nonDotted {
		style, want = "__flow", "nonDottedPaths is false, which selects .flow"
	}
	return &Error{Kind: ErrFlowLayout, File: dir, Msg: fmt.Sprintf(
		"found %d flow folder(s) using the %s format, but %s: %s", len(wrong), style, want, strings.Join(wrong, ", "))}
}
