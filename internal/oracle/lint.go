package oracle

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Finding is one violation of a repository check.
type Finding struct {
	File   string `json:"file"`
	Line   int    `json:"line,omitempty"`
	Rule   string `json:"rule"`
	Detail string `json:"detail"`
}

func (f Finding) String() string {
	if f.Line > 0 {
		return fmt.Sprintf("%s:%d: %s: %s", f.File, f.Line, f.Rule, f.Detail)
	}
	return fmt.Sprintf("%s: %s: %s", f.File, f.Rule, f.Detail)
}

// The rules the checks report.
const (
	RuleImageFrom     = "image-from"
	RuleImageRepush   = "image-repush"
	RuleManifestDep   = "manifest-dependency"
	RuleVendoredPath  = "vendored-path"
	RuleVendoredFile  = "vendored-file"
	RuleVendoredInput = "vendored-input"
)

// skipDirs are never scanned: VCS state, installed dependencies, and test
// data, which holds deliberate counterexamples.
var skipDirs = map[string]bool{".git": true, ".jj": true, "node_modules": true, "testdata": true}

func walkRepo(fsys fs.FS, visit func(p string) error) error {
	return fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != "." && skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return visit(p)
	})
}

// windmillImageRef matches a reference to a Windmill image.
var windmillImageRef = regexp.MustCompile(`(?i)(ghcr\.io/windmill-labs/|(^|[^a-z0-9./-])((index\.|registry-1\.)?docker\.io/)?windmilllabs/)`)

// repush matches a command that re-tags, re-pushes or copies an image.
var repush = regexp.MustCompile(`\bdocker\s+(image\s+)?(push|tag)\b|\bdocker\s+buildx\s+imagetools\s+create\b|\bskopeo\s+copy\b|\bcrane\s+(copy|cp|push|tag|mutate|append|rebase)\b|\bregctl\s+image\s+(copy|mod)\b`)

// isDockerfile reports whether a file name is a Dockerfile or Containerfile.
func isDockerfile(name string) bool {
	l := strings.ToLower(name)
	return l == "dockerfile" || l == "containerfile" ||
		strings.HasPrefix(l, "dockerfile.") || strings.HasPrefix(l, "containerfile.") ||
		strings.HasSuffix(l, ".dockerfile") || strings.HasSuffix(l, ".containerfile")
}

// isScript reports whether a file holds shell commands the lint reads: a
// workflow or composite action (any YAML under .github/), a shell script, a
// Makefile or a justfile.
func isScript(p string) bool {
	switch path.Base(p) {
	case "Makefile", "makefile", "GNUmakefile", "justfile", "Justfile", ".justfile":
		return true
	}
	return (strings.HasPrefix(p, ".github/") && (strings.HasSuffix(p, ".yml") || strings.HasSuffix(p, ".yaml"))) ||
		strings.HasSuffix(p, ".sh") || strings.HasSuffix(p, ".mk")
}

// logicalLines joins backslash-continued lines and reports each with the
// number of its first line.
func logicalLines(data []byte) (lines []string, nums []int) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var cur strings.Builder
	start, n := 0, 0
	for sc.Scan() {
		n++
		l := sc.Text()
		if cur.Len() == 0 {
			start = n
		}
		if strings.HasSuffix(l, "\\") {
			cur.WriteString(strings.TrimSuffix(l, "\\"))
			cur.WriteByte(' ')
			continue
		}
		cur.WriteString(l)
		lines, nums = append(lines, cur.String()), append(nums, start)
		cur.Reset()
	}
	if cur.Len() > 0 {
		lines, nums = append(lines, cur.String()), append(nums, start)
	}
	return lines, nums
}

// LintImages is the image lint of OR-3: no Dockerfile in the repository
// builds FROM a Windmill image, and no workflow, composite action or shell
// script that names a Windmill image re-tags, re-pushes or copies an image. Pulling and
// running the unmodified image is allowed; deriving, mirroring or
// re-pushing it is not (licensing.md, Decision).
func LintImages(fsys fs.FS) ([]Finding, error) {
	var out []Finding
	err := walkRepo(fsys, func(p string) error {
		name := path.Base(p)
		dockerfile, script := isDockerfile(name), isScript(p)
		if !dockerfile && !script {
			return nil
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		lines, nums := logicalLines(data)
		if dockerfile {
			// A Dockerfile may not name a Windmill image at all: not in
			// FROM, nor COPY --from, RUN --mount=from=, or an ARG that a
			// later FROM ${…} expands, so none of those can bake it in.
			for i, l := range lines {
				if m := windmillImageRef.FindString(l); m != "" {
					f := strings.Fields(l)
					out = append(out, Finding{File: p, Line: nums[i], Rule: RuleImageFrom, Detail: "a Dockerfile names a Windmill image (" + strings.ToUpper(f[0]) + "): " + strings.TrimSpace(l)})
				}
			}
			return nil
		}
		if !windmillImageRef.Match(data) {
			return nil
		}
		for i, l := range lines {
			if m := repush.FindString(l); m != "" {
				out = append(out, Finding{File: p, Line: nums[i], Rule: RuleImageRepush, Detail: "names a Windmill image and runs " + strings.Join(strings.Fields(m), " ")})
			}
		}
		return nil
	})
	return out, err
}

// The Windmill-derived inputs the build team may use (LIC-1): the Apache
// Go client module and the npm client package. Everything else Windmill
// publishes is excluded or, until Q-OF-7(a) said no, metadata-only Apache.
var (
	allowedGoModules = map[string]bool{"github.com/windmill-labs/windmill-go-client": true}
	// deniedPackage matches the excluded npm packages as a token: the
	// AGPL-derived windmill-parser-wasm-* packages and the metadata-only
	// windmill-yaml-validator, windmill-utils-internal and windmill-cli
	// (decisions.md, Q-OF-7(a)). windmill-client is allowed.
	deniedPackage = regexp.MustCompile(`(^|[^A-Za-z0-9._/-])(windmill-parser-wasm[A-Za-z0-9._-]*|windmill-yaml-validator|windmill-utils-internal|windmill-cli)([^A-Za-z0-9._-]|$)`)
	// windmillRepoRef matches a git or tarball dependency on the Windmill
	// repository itself, which is AGPL by default.
	windmillRepoRef = regexp.MustCompile(`(?i)(github\.com[/:]|github:)windmill-labs/windmill(\.git)?([#/@"'\s]|$)`)
	// deniedCrate matches a Cargo dependency on the Rust client or a
	// Windmill crate: by key, as a [dependencies.<name>] table, or through
	// package = "<name>".
	deniedCrate = regexp.MustCompile(`^\s*(?:(wmill|windmill[A-Za-z0-9_-]*)\s*=|\[[A-Za-z.-]*dependencies\.(wmill|windmill[A-Za-z0-9_-]*)\]|.*\bpackage\s*=\s*"(wmill|windmill[A-Za-z0-9_-]*)")`)
	// deniedTarball matches an npm registry tarball of an excluded package.
	deniedTarball = regexp.MustCompile(`/(windmill-parser-wasm[A-Za-z0-9._-]*|windmill-yaml-validator|windmill-utils-internal|windmill-cli)/-/`)
	// backendPath matches a path into Windmill's backend/.
	backendPath = regexp.MustCompile(`\bbackend/[A-Za-z0-9_./-]+`)
)

// allowedBackendPath is the one backend/ file the build team may use: the
// Apache-2.0 REST schema (LIC-1).
const allowedBackendPath = "backend/windmill-api/openapi.yaml"

// allowedVendoredFiles are the files a vendored Windmill directory may
// hold: the two Apache-2.0 schemas, their licence and the notice.
var allowedVendoredFiles = map[string]bool{"openflow.openapi.yaml": true, "openapi.yaml": true, "LICENSE-APACHE": true, "NOTICE": true}

// textManifests are read as text for excluded package names: lockfiles and
// import maps, whose resolved entries name every transitive package.
var textManifests = map[string]bool{
	"bun.lock": true, "bun.lockb": true, "package-lock.json": true, "npm-shrinkwrap.json": true,
	"pnpm-lock.yaml": true, "yarn.lock": true, "deno.json": true, "deno.jsonc": true, "deno.lock": true,
	"import_map.json": true, "jsr.json": true,
}

// GuardManifests is the manifest guard of LIC-1 (cases LIC-1/a, LIC-1/b).
// It reads windmill-lite's own manifests and vendored Windmill files and
// reports any Windmill-derived input other than the allowed ones:
//
//   - go.mod and go.sum: a github.com/windmill-labs/ module other than the
//     Go client;
//   - package.json (every dependency map, overrides and resolutions, by name
//     and by npm:, git and GitHub spec), lockfiles and import maps: an
//     excluded npm package, or a dependency on the Windmill repository;
//   - Cargo.toml: the Rust client or a Windmill crate;
//   - a directory whose NOTICE names the Windmill repository: a file other
//     than the Apache schemas, their licence and the notice, a backend/
//     path other than backend/windmill-api/openapi.yaml, or a reference to
//     an excluded package.
//
// Unrelated third-party dependencies are outside the check.
func GuardManifests(fsys fs.FS) ([]Finding, error) {
	var out []Finding
	add := func(f Finding) { out = append(out, f) }
	err := walkRepo(fsys, func(p string) error {
		name := path.Base(p)
		switch {
		case name == "go.mod" || name == "go.sum":
			return guardGo(fsys, p, add)
		case name == "package.json":
			return guardPackageJSON(fsys, p, add)
		case textManifests[name]:
			return guardText(fsys, p, add)
		case name == "Cargo.toml":
			return guardCargo(fsys, p, add)
		case name == "NOTICE":
			return guardVendored(fsys, p, add)
		}
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out, err
}

func guardGo(fsys fs.FS, p string, add func(Finding)) error {
	data, err := fs.ReadFile(fsys, p)
	if err != nil {
		return err
	}
	for i, l := range strings.Split(string(data), "\n") {
		l, _, _ = strings.Cut(l, "//") // a comment is not a requirement
		for _, f := range strings.Fields(l) {
			f = strings.Trim(f, `"`)
			if !strings.HasPrefix(f, "github.com/windmill-labs/") {
				continue
			}
			mod := f
			if parts := strings.SplitN(f, "/", 4); len(parts) >= 3 {
				mod = strings.Join(parts[:3], "/")
			}
			if !allowedGoModules[mod] {
				add(Finding{File: p, Line: i + 1, Rule: RuleManifestDep, Detail: "Windmill Go module " + mod + " is not an allowed input"})
			}
		}
	}
	return nil
}

func guardPackageJSON(fsys fs.FS, p string, add func(Finding)) error {
	data, err := fs.ReadFile(fsys, p)
	if err != nil {
		return err
	}
	var pkg map[string]json.RawMessage
	if err := json.Unmarshal(data, &pkg); err != nil {
		return fmt.Errorf("%s: %w", p, err)
	}
	check := func(where, name, spec string) {
		for _, s := range []string{name, strings.TrimPrefix(spec, "npm:")} {
			if deniedPackage.MatchString(s) {
				add(Finding{File: p, Rule: RuleManifestDep, Detail: fmt.Sprintf("%s %q: %q is an excluded Windmill package", where, name, s)})
				return
			}
		}
		if windmillRepoRef.MatchString(spec) {
			add(Finding{File: p, Rule: RuleManifestDep, Detail: fmt.Sprintf("%s %q depends on the Windmill repository: %q", where, name, spec)})
		} else if m := deniedTarball.FindStringSubmatch(spec); m != nil {
			add(Finding{File: p, Rule: RuleManifestDep, Detail: fmt.Sprintf("%s %q: %q is a tarball of the excluded package %s", where, name, spec, m[1])})
		}
	}
	var walk func(where string, raw json.RawMessage)
	walk = func(where string, raw json.RawMessage) {
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) == nil {
			for k, v := range m {
				var s string
				if json.Unmarshal(v, &s) == nil {
					check(where, k, s)
					continue
				}
				check(where, k, "")
				walk(where, v)
			}
			return
		}
		var a []string
		if json.Unmarshal(raw, &a) == nil {
			for _, s := range a {
				check(where, s, "")
			}
		}
	}
	for _, k := range []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies", "bundleDependencies", "bundledDependencies", "overrides", "resolutions"} {
		if raw, ok := pkg[k]; ok {
			walk(k, raw)
		}
	}
	return nil
}

func guardText(fsys fs.FS, p string, add func(Finding)) error {
	data, err := fs.ReadFile(fsys, p)
	if err != nil {
		return err
	}
	for i, l := range strings.Split(string(data), "\n") {
		if m := deniedPackage.FindStringSubmatch(l); m != nil {
			add(Finding{File: p, Line: i + 1, Rule: RuleManifestDep, Detail: m[2] + " is an excluded Windmill package"})
		} else if windmillRepoRef.MatchString(l) {
			add(Finding{File: p, Line: i + 1, Rule: RuleManifestDep, Detail: "depends on the Windmill repository"})
		}
	}
	return nil
}

func guardCargo(fsys fs.FS, p string, add func(Finding)) error {
	data, err := fs.ReadFile(fsys, p)
	if err != nil {
		return err
	}
	for i, l := range strings.Split(string(data), "\n") {
		if m := deniedCrate.FindStringSubmatch(l); m != nil {
			add(Finding{File: p, Line: i + 1, Rule: RuleManifestDep, Detail: "Cargo dependency " + m[1] + m[2] + m[3] + " is a Windmill crate"})
		}
	}
	return nil
}

// guardVendored checks a directory whose NOTICE names the Windmill
// repository: the files vendored from Windmill must be allowed inputs.
func guardVendored(fsys fs.FS, p string, add func(Finding)) error {
	data, err := fs.ReadFile(fsys, p)
	if err != nil {
		return err
	}
	if !bytes.Contains(data, []byte("github.com/windmill-labs/windmill")) {
		return nil
	}
	for i, l := range strings.Split(string(data), "\n") {
		for _, bp := range backendPath.FindAllString(l, -1) {
			if strings.TrimRight(bp, ".") != allowedBackendPath {
				add(Finding{File: p, Line: i + 1, Rule: RuleVendoredPath, Detail: bp + " is Windmill backend source, not an allowed input"})
			}
		}
		if m := deniedPackage.FindStringSubmatch(l); m != nil {
			add(Finding{File: p, Line: i + 1, Rule: RuleVendoredInput, Detail: m[2] + " is an excluded Windmill package"})
		}
	}
	dir := path.Dir(p)
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !allowedVendoredFiles[e.Name()] {
			add(Finding{File: path.Join(dir, e.Name()), Rule: RuleVendoredFile, Detail: "a vendored Windmill directory may hold only the Apache-2.0 schemas, their licence and NOTICE"})
		}
	}
	return nil
}
