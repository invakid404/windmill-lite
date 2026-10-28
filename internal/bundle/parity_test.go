package bundle

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/invakid404/windmill-lite/internal/oracle"
	"github.com/invakid404/windmill-lite/internal/syncfolder"
)

// The CLI parity check (ADR 0002, decision 4; milestone C2b; Q-WR-24): the
// pinned windmill-cli, run unmodified as a black-box tool against a
// recording HTTP stub, produces the codebase bundle it would upload, and
// the esbuild reproduction of its settings (CodebaseSettings.CLIBundle)
// must produce the same bytes. The one normalisation: a single-file bundle
// travels as a multipart string field, whose line breaks the form-data
// encoding writes as CRLF, so the upload must equal the reproduction with
// each LF written as CRLF; a tar travels as bytes and must equal it as it
// is. The CLI runs only where WL_WMILL_CLI names it, the command line that
// starts it ("npx --yes windmill-cli@1.767.0" in CI): it is used as a
// tool, as the oracle uses it (OR-8), never a dependency, and nothing of
// it is read. Without WL_WMILL_CLI the check skips, and the documented
// fallback, TestCLIOptionsFollowWR47, stands in.

// parityFixture is one codebase entry and its scripts, pushed one script at
// a time.
type parityFixture struct {
	name    string
	entry   string // the codebases entry's fields beyond relative_path and includes
	files   map[string]string
	scripts []string // script files to push, relative to the sync root
}

// parityFixtures are the WR-47 cases' folders, one per setting, plus an
// inlined package and an asset (WR-49/a).
var parityFixtures = []parityFixture{
	{name: "cjs-default", scripts: []string{"f/cb/runtime.ts"}, files: map[string]string{
		"f/cb/runtime.ts": "export function main() { return typeof (globalThis as any).Bun; }\n"}},
	{name: "esm", entry: "    format: esm\n", scripts: []string{"f/cb/runtime.ts"}, files: map[string]string{
		"f/cb/runtime.ts": "export function main() { return typeof (globalThis as any).Bun; }\n"}},
	{name: "define", entry: "    define: {\"__MODE__\": \"\\\"lite\\\"\"}\n", scripts: []string{"f/cb/mode.ts"}, files: map[string]string{
		"f/cb/mode.ts": "declare const __MODE__: string;\nexport function main() { return __MODE__; }\n"}},
	{name: "inject", entry: "    inject: [\"f/cb/shim.ts\"]\n", scripts: []string{"f/cb/inject.ts"}, files: map[string]string{
		"f/cb/shim.ts":   "export const answer = 42;\n",
		"f/cb/inject.ts": "declare const answer: number;\nexport function main() { return answer; }\n"}},
	{name: "loader", entry: "    loader: {\".txt\": \"text\"}\n", scripts: []string{"f/cb/text.ts"}, files: map[string]string{
		"f/cb/note.txt": "hi",
		"f/cb/text.ts":  "import note from \"./note.txt\";\nexport function main() { return note; }\n"}},
	{name: "banner", entry: "    banner: {js: \"globalThis.__banner = 1;\"}\n", scripts: []string{"f/cb/banner.ts"}, files: map[string]string{
		"f/cb/banner.ts": "export function main() { return (globalThis as any).__banner; }\n"}},
	{name: "package-and-asset", entry: "    external: [\"electron\"]\n    assets: [{from: f/cb/data/a.txt, to: data/a.txt}]\n", scripts: []string{"f/cb/pkg.ts"}, files: map[string]string{
		"node_modules/fixture-cb/package.json": `{"name":"fixture-cb","version":"1.0.0","main":"index.js"}`,
		"node_modules/fixture-cb/index.js":     "exports.tag = \"inlined\";\n",
		"f/cb/data/a.txt":                      "asset",
		"f/cb/pkg.ts":                          "import { tag } from \"fixture-cb\";\nimport { readFileSync } from \"node:fs\";\nexport function main() { return [tag, readFileSync(\"data/a.txt\", \"utf8\")]; }\n"}},
}

// stubMetadata is the script metadata a push sends, written beside each
// script so that the CLI needs no server to generate it: a codebase script
// has no lock (SY-8).
const stubMetadata = `summary: ''
description: ''
lock: ''
kind: script
schema:
  $schema: 'https://json-schema.org/draft/2020-12/schema'
  type: object
  properties: {}
  required: []
`

// upload is one create_snapshot request the stub recorded: its multipart
// fields, by name, in order.
type upload struct {
	path   string
	fields []uploadField
}

type uploadField struct {
	name, filename, contentType string
	data                        []byte
}

// cliStub answers what a script push asks of a server, records every
// request, and keeps each upload. Requests it does not know get 404 and
// are logged, so that a CLI that needs more of a server shows what.
type cliStub struct {
	mu       sync.Mutex
	requests []string
	uploads  []upload
}

func (s *cliStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.requests = append(s.requests, fmt.Sprintf("%s %s (%s, %d bytes)", r.Method, r.URL.RequestURI(), r.Header.Get("Content-Type"), len(body)))
	s.mu.Unlock()
	p := r.URL.Path
	switch {
	case r.Method == http.MethodGet && p == "/api/version":
		io.WriteString(w, "v"+oracle.CLIVersion)
	case r.Method == http.MethodGet && p == "/api/users/whoami":
		json.NewEncoder(w).Encode(map[string]any{"email": "parity@windmill-lite.invalid", "login_type": "password", "super_admin": true, "verified": true, "name": "parity"})
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/users/whoami"):
		json.NewEncoder(w).Encode(map[string]any{"email": "parity@windmill-lite.invalid", "username": "parity", "is_admin": true, "is_super_admin": true,
			"created_at": "2026-01-01T00:00:00Z", "operator": false, "disabled": false, "folders": []string{}, "folders_read": []string{}, "folders_owners": []string{}, "groups": []string{}})
	case r.Method == http.MethodGet && strings.Contains(p, "/scripts/get/p/"):
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, "Not found: script not found at path "+p[strings.Index(p, "/get/p/")+7:])
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/scripts/create_snapshot"):
		up, err := readUpload(r.Header.Get("Content-Type"), body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, err.Error())
			return
		}
		up.path = r.URL.RequestURI()
		s.mu.Lock()
		s.uploads = append(s.uploads, up)
		s.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, "c2b0000000000001")
	default:
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, "the parity stub does not answer this request")
	}
}

func readUpload(contentType string, body []byte) (upload, error) {
	mt, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(mt, "multipart/") {
		return upload{}, fmt.Errorf("not a multipart request: %q", contentType)
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	var up upload
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return up, nil
		}
		if err != nil {
			return upload{}, err
		}
		data, err := io.ReadAll(part)
		if err != nil {
			return upload{}, err
		}
		up.fields = append(up.fields, uploadField{part.FormName(), part.FileName(), part.Header.Get("Content-Type"), data})
	}
}

func (s *cliStub) log(t *testing.T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t.Logf("requests to the stub:\n  %s", strings.Join(s.requests, "\n  "))
	for _, u := range s.uploads {
		var names []string
		for _, f := range u.fields {
			names = append(names, fmt.Sprintf("%s (file %q, %s, %d bytes)", f.name, f.filename, f.contentType, len(f.data)))
		}
		t.Logf("upload %s: %s", u.path, strings.Join(names, ", "))
	}
}

// TestCLIParity is the parity check: for each fixture, the bundle the
// pinned CLI uploads equals CLIBundle's output, and an upload with assets
// is a tar of main.js and the assets (WR-49).
func TestCLIParity(t *testing.T) {
	command := strings.Fields(os.Getenv("WL_WMILL_CLI"))
	if len(command) == 0 {
		t.Skip("WL_WMILL_CLI is not set; it names the command that runs " + oracle.CLIPackage + " (CI only)")
	}
	home := t.TempDir()
	cli := oracle.CLI{Command: command, Home: home, ConfigDir: filepath.Join(home, "config")}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	version, err := cli.Version(ctx)
	if err != nil {
		t.Fatalf("the CLI: %v (%s)", err, version)
	}
	t.Logf("CLI: %s", version)
	for _, fx := range parityFixtures {
		t.Run(fx.name, func(t *testing.T) {
			stub := &cliStub{}
			srv := httptest.NewServer(stub)
			defer srv.Close()
			defer stub.log(t)
			files := map[string]string{
				"wmill.yaml":            "defaultTs: bun\ncodebases:\n  - relative_path: f/cb\n    includes: [\"f/cb/**\"]\n" + fx.entry,
				"f/cb/folder.meta.yaml": "summary: ''\ndisplay_name: cb\nowners: []\nextra_perms: {}\n",
			}
			for p, text := range fx.files {
				files[p] = text
			}
			for _, s := range fx.scripts {
				files[strings.TrimSuffix(s, ".ts")+".script.yaml"] = stubMetadata
			}
			root := writeFolder(t, files)
			for _, script := range fx.scripts {
				before := len(stub.uploads)
				args := append(slices.Clone(command[1:]), "--base-url", srv.URL, "--token", "parity", "--workspace", "parity",
					"--config-dir", cli.ConfigDir, "script", "push", script)
				cmd := exec.CommandContext(ctx, command[0], args...)
				cmd.Dir = root
				cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "npm_config_cache=" + filepath.Join(home, "npm-cache"),
					"npm_config_update_notifier=false", "WMILL_CONFIG_DIR=" + cli.ConfigDir, "NO_COLOR=1"}
				out, err := cmd.CombinedOutput()
				t.Logf("wmill script push %s:\n%s", script, out)
				if err != nil {
					t.Fatalf("wmill script push %s: %v", script, err)
				}
				stub.mu.Lock()
				ups := slices.Clone(stub.uploads[before:])
				stub.mu.Unlock()
				if len(ups) != 1 {
					t.Fatalf("%d uploads for %s, want 1", len(ups), script)
				}
				got := uploadedBundle(t, ups[0])
				settings := parseEntrySettings(t, root)
				want, err := settings.CLIBundle(root, script)
				if err != nil {
					t.Fatal(err)
				}
				if len(want) != 1 {
					t.Fatalf("CLIBundle gave %d files", len(want))
				}
				var main []byte
				for _, b := range want {
					main = b
				}
				// A bundle sent as text is a multipart string field, which
				// the form-data encoding writes with CRLF line breaks; a tar
				// is sent as bytes.
				if !got.tar {
					main = bytes.ReplaceAll(main, []byte("\n"), []byte("\r\n"))
				}
				if !bytes.Equal(got.files["main.js"], main) {
					t.Errorf("%s: the CLI's bundle differs from windmill-lite's reproduction\n--- CLI ---\n%q\n--- windmill-lite ---\n%q", script, got.files["main.js"], main)
				}
				for _, a := range settings.Assets {
					data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(a.From)))
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(got.files[a.To], data) {
						t.Errorf("%s: the upload's asset %s is %q, want %q", script, a.To, got.files[a.To], data)
					}
				}
				if got.tar != (len(settings.Assets) > 0) {
					t.Errorf("%s: the upload is a tar: %v; the entry has %d assets (WR-49)", script, got.tar, len(settings.Assets))
				}
				if extra := len(got.files) - 1 - len(settings.Assets); extra != 0 {
					t.Errorf("%s: the upload has %d entries beyond main.js and the assets: %v", script, extra, sortedKeys(got.files))
				}
				t.Logf("%s: the CLI's upload equals windmill-lite's reproduction (%d bytes, tar: %v)", script, len(got.files["main.js"]), got.tar)
			}
		})
	}
}

// uploadedFiles are the files of an upload's bundle: main.js alone for a
// plain bundle, every entry of a tar (WR-49).
type uploadedFiles struct {
	files map[string][]byte
	tar   bool
}

func uploadedBundle(t *testing.T, up upload) uploadedFiles {
	t.Helper()
	var file *uploadField
	for i := range up.fields {
		if up.fields[i].name == "file" {
			file = &up.fields[i]
		}
	}
	if file == nil {
		t.Fatalf("the upload has no file field")
	}
	if !isTar(file.data) {
		return uploadedFiles{files: map[string][]byte{"main.js": file.data}}
	}
	out := uploadedFiles{files: map[string][]byte{}, tar: true}
	tr := tar.NewReader(bytes.NewReader(file.data))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("the uploaded tar: %v", err)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		out.files[strings.TrimPrefix(h.Name, "./")] = b
	}
}

// isTar reports whether data starts with a tar header: "ustar" at offset
// 257.
func isTar(data []byte) bool {
	return len(data) > 262 && string(data[257:262]) == "ustar"
}

// parseEntrySettings reads the one codebases entry of a fixture's
// wmill.yaml, as the loader gives it to the build.
func parseEntrySettings(t *testing.T, root string) *CodebaseSettings {
	t.Helper()
	cfg, err := syncfolderConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseCodebaseSettings(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestCLIOptionsFollowWR47 is the parity check's documented fallback (ADR
// 0002, decision 4): the esbuild options windmill-lite derives from a
// codebases entry are those WR-47's table gives for the pinned CLI, read
// option by option, and the esbuild that applies them is the CLI's
// version.
func TestCLIOptionsFollowWR47(t *testing.T) {
	gomod, err := os.ReadFile(repoPath(t, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gomod), "github.com/evanw/esbuild v"+EsbuildVersion+"\n") {
		t.Fatalf("go.mod does not pin github.com/evanw/esbuild at v%s, the pinned CLI's esbuild (WR-47)", EsbuildVersion)
	}
	root := t.TempDir()
	for _, c := range []struct {
		name     string
		settings string
		check    func(t *testing.T, o api.BuildOptions)
	}{
		{"an absent format is cjs, target node20.15.1", `{"relative_path":"f/cb"}`, func(t *testing.T, o api.BuildOptions) {
			if o.Format != api.FormatCommonJS || o.Target != api.DefaultTarget ||
				!slices.Equal(o.Engines, []api.Engine{{Name: api.EngineNode, Version: "20.15.1"}}) {
				t.Errorf("format %v, target %v, engines %v", o.Format, o.Target, o.Engines)
			}
		}},
		{"format esm, target esnext", `{"format":"esm"}`, func(t *testing.T, o api.BuildOptions) {
			if o.Format != api.FormatESModule || o.Target != api.ESNext || len(o.Engines) != 0 {
				t.Errorf("format %v, target %v, engines %v", o.Format, o.Target, o.Engines)
			}
		}},
		{"the fixed options", `{}`, func(t *testing.T, o api.BuildOptions) {
			if !o.Bundle || o.Platform != api.PlatformNode || o.Packages != api.PackagesBundle || o.Outdir != "/" || o.Write ||
				!slices.Equal(o.EntryPoints, []string{"f/cb/x.ts"}) || o.AbsWorkingDir != root {
				t.Errorf("%+v", o)
			}
			if o.MinifySyntax || o.MinifyWhitespace || o.MinifyIdentifiers || o.Sourcemap != api.SourceMapNone || o.Splitting || o.Metafile {
				t.Errorf("minification, source maps, splitting or the metafile are set: %+v", o)
			}
			if o.External != nil || o.Inject != nil || o.Define != nil || o.Banner != nil {
				t.Errorf("absent settings must leave esbuild's defaults: %+v", o)
			}
			if !mapsEqual(o.Loader, map[string]api.Loader{".node": api.LoaderFile}) {
				t.Errorf("the default loader map is %v", o.Loader)
			}
		}},
		{"external, inject, define and banner pass through", `{"external":["pg"],"inject":["f/cb/shim.ts"],"define":{"__X__":"\"x\""},"banner":{"js":"// b"}}`, func(t *testing.T, o api.BuildOptions) {
			if !slices.Equal(o.External, []string{"pg"}) || !slices.Equal(o.Inject, []string{"f/cb/shim.ts"}) ||
				!mapsEqual(o.Define, map[string]string{"__X__": `"x"`}) || !mapsEqual(o.Banner, map[string]string{"js": "// b"}) {
				t.Errorf("%+v", o)
			}
		}},
		{"a loader map replaces the default", `{"loader":{".txt":"text"}}`, func(t *testing.T, o api.BuildOptions) {
			if !mapsEqual(o.Loader, map[string]api.Loader{".txt": api.LoaderText}) {
				t.Errorf("loader %v", o.Loader)
			}
		}},
	} {
		t.Run(strings.ReplaceAll(c.name, " ", "_"), func(t *testing.T) {
			s, err := ParseCodebaseSettings(json.RawMessage(c.settings))
			if err != nil {
				t.Fatal(err)
			}
			o, err := s.CLIOptions(root, "f/cb/x.ts")
			if err != nil {
				t.Fatal(err)
			}
			c.check(t, o)
		})
	}
	for _, bad := range []string{`{"format":"iife"}`, `{"define":{"X":1}}`, `{"loader":{".txt":"nope"}}`} {
		s, err := ParseCodebaseSettings(json.RawMessage(bad))
		if err == nil {
			_, err = s.CLIOptions(root, "f/cb/x.ts")
		}
		if err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}

// syncfolderConfig returns the first codebases entry of the folder's
// wmill.yaml, as the loader reads it.
func syncfolderConfig(root string) (json.RawMessage, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	f, err := syncfolder.Load(r.FS())
	if err != nil {
		return nil, err
	}
	if len(f.Config.Codebases) == 0 {
		return nil, errors.New("wmill.yaml has no codebases entry")
	}
	return f.Config.Codebases[0].Settings, nil
}

func mapsEqual[V comparable](a, b map[string]V) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}
