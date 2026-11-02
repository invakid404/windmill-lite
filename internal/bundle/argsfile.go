package bundle

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// ArgsFileName is the file in which Windmill's worker leaves a job's
// arguments in its working directory (WR-5).
const ArgsFileName = "args.json"

// noArgsFileReader reports whether no script of the bundle in dir can
// read args.json (Manifest.NoArgsFileReader). Host mode's args.json is one
// file in the host's working directory, which every in-flight script of
// the bundle could read, so the answer is the bundle's, never a script's.
// Whether code reads a file cannot be decided in general: a script counts
// as no reader only when its built module gives its code no way to a
// file, a process or code made at run time (argsFileFree), and it calls no
// code the build cannot read: no host-provided module, no shared closure,
// no codebase bundle with assets, no native boundary. One script that does
// not qualify makes the bundle a reader.
func noArgsFileReader(dir string, files map[string]string) (bool, error) {
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		id, ok := strings.CutPrefix(rel, "scripts/bun/")
		if !ok {
			continue
		}
		id, ok = strings.CutSuffix(id, ".meta.json")
		if !ok || strings.HasPrefix(id, "provided/") || strings.HasPrefix(id, "closures/") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return false, err
		}
		var m ScriptMeta
		if err := json.Unmarshal(b, &m); err != nil {
			return false, err
		}
		if len(m.HostModules) > 0 || m.Shared != nil || m.Native != nil || m.Bundler != "" && m.Bundler != "bun" {
			return false, nil
		}
		ext := ".mjs"
		if m.Format == CJS {
			ext = ".cjs"
		}
		module := "scripts/bun/" + id + ext
		if _, ok := files[module]; !ok {
			return false, nil
		}
		code, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(module)))
		if err != nil {
			return false, err
		}
		if !argsFileFree(code) {
			return false, nil
		}
	}
	return true, nil
}

// argsCapabilities are the texts through which a script's code could read
// args.json, or reach what could: the file's name, a module or global that
// reads files or starts processes (fs, Bun.file, fetch of a file: URL,
// import(), require), code made at run time (eval, Function, a
// constructor), the global object, and reflection that reads a property by
// a name built at run time. A script whose code holds none of them, as a
// substring anywhere, comments and strings included, has no way to the
// file. process.env is removed before the search: it only reads the
// invocation's environment.
var argsCapabilities = []string{
	ArgsFileName, "Bun", "require", "fetch", "import", "eval", "Function", "constructor", "global", "window",
	"Worker", "WebAssembly", "process", "Reflect", "Proxy", "getOwnProperty", "__proto__", "__lookup", "__define",
	"prepareStackTrace", "captureStackTrace", "getThis", "getFunction", "Deno", "XMLHttpRequest", "WebSocket",
	"EventSource", "navigator", "node:", "child_process", "worker_threads",
}

// argsCapabilityWords are capabilities that are common words, searched as
// whole words: the global object through this, self or with, a sloppy
// function's arguments, and CommonJS's module and exports.
var argsCapabilityWords = regexp.MustCompile(`\b(this|self|with|arguments|module|exports|fs)\b`)

// generatedSection is the header Bun prints for a module of windmill-lite's
// own build plugin (wl/...), such as the Bun.env rewrite each script
// imports first.
var (
	sectionHeader   = regexp.MustCompile(`^// \S+$`)
	generatedHeader = regexp.MustCompile(`^// wl/\S+$`)
)

// argsFileFree reports whether a script's built module code gives it no
// way to read args.json: no capability (argsCapabilities), and no [ other
// than [] and [<digits>], since a property read by a computed name can
// reach any of them (["con" + "structor"]). The modules windmill-lite's
// build prints first, before the script's own code, are left out: they
// are the build's, and a capability they hold reaches the script only
// through a name the search then finds. The search is a conservative
// one: a script it does not clear keeps its args.json.
func argsFileFree(code []byte) bool {
	lines := strings.Split(string(code), "\n")
	i := 0
	if i < len(lines) && strings.HasPrefix(lines[i], "// @bun") {
		i++
	}
	for i < len(lines) && generatedHeader.MatchString(lines[i]) {
		i++
		for i < len(lines) && !sectionHeader.MatchString(lines[i]) {
			i++
		}
	}
	text := strings.ReplaceAll(strings.Join(lines[i:], "\n"), "process.env", "")
	for _, c := range argsCapabilities {
		if strings.Contains(text, c) {
			return false
		}
	}
	if argsCapabilityWords.MatchString(text) {
		return false
	}
	for j := strings.IndexByte(text, '['); j >= 0; {
		k := j + 1
		for k < len(text) && '0' <= text[k] && text[k] <= '9' {
			k++
		}
		if k >= len(text) || text[k] != ']' {
			return false
		}
		next := strings.IndexByte(text[k:], '[')
		if next < 0 {
			break
		}
		j = k + next
	}
	return !bytes.Contains(code, []byte(ArgsFileName))
}
