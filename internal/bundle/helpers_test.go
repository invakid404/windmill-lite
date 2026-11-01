package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/harness"
)

// The build tests run the real build helper on the pinned Bun. WL_BUN names
// the bun executable; without it they skip, unless WL_REQUIRE_BUN is set,
// as CI sets it. The helper's own dependency (its TypeScript parser) must
// be installed: `bun install --frozen-lockfile` in harnesses/bun/build.
func bunPath(t testing.TB) string {
	t.Helper()
	p := os.Getenv("WL_BUN")
	if p == "" {
		if os.Getenv("WL_REQUIRE_BUN") != "" {
			t.Fatal("WL_REQUIRE_BUN is set but WL_BUN is not")
		}
		t.Skip("WL_BUN is not set; it names the pinned Bun " + harness.PinnedBun)
	}
	if _, err := os.Stat(repoPath(t, "harnesses/bun/build/node_modules/@babel/parser/package.json")); err != nil {
		msg := "the build helper's dependencies are not installed: run `bun install --frozen-lockfile` in harnesses/bun/build"
		if os.Getenv("WL_REQUIRE_BUN") != "" {
			t.Fatal(msg)
		}
		t.Skip(msg)
	}
	return p
}

func repoPath(t testing.TB, rel string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// registry is an npm registry for synthetic packages, so that installs from
// a lock run offline, and an install cache of its own, so that no synthetic
// package reaches the machine's Bun cache.
type registry struct {
	url string
	env []string
}

// newRegistry serves packages, each "name@version" with its files (without
// the tarball's package/ prefix; a package.json is written when absent).
func newRegistry(t testing.TB, packages map[string]map[string]string) *registry {
	t.Helper()
	type version struct {
		tarball []byte
		meta    map[string]any
	}
	byName := map[string]map[string]version{}
	srv := httptest.NewUnstartedServer(nil)
	base := "http://" + srv.Listener.Addr().String()
	for spec, files := range packages {
		at := strings.LastIndex(spec, "@")
		if at <= 0 {
			t.Fatalf("package %q is not name@version", spec)
		}
		name, ver := spec[:at], spec[at+1:]
		if _, ok := files["package.json"]; !ok {
			files = withFile(files, "package.json", fmt.Sprintf(`{"name":%q,"version":%q,"main":"index.js","type":"module"}`, name, ver))
		}
		tgz := tarball(t, files)
		sum512 := sha512.Sum512(tgz)
		sum1 := sha1.Sum(tgz)
		if byName[name] == nil {
			byName[name] = map[string]version{}
		}
		meta := map[string]any{
			"name": name, "version": ver,
			"dist": map[string]any{
				"tarball":   base + "/" + name + "/-/" + name + "-" + ver + ".tgz",
				"integrity": "sha512-" + base64.StdEncoding.EncodeToString(sum512[:]),
				"shasum":    hex.EncodeToString(sum1[:]),
			},
		}
		// An installer reads a package's dependencies from the registry's
		// metadata, not from its tarball, so the metadata repeats them.
		var pj struct {
			Dependencies map[string]string `json:"dependencies"`
		}
		if err := json.Unmarshal([]byte(files["package.json"]), &pj); err != nil {
			t.Fatalf("package %s: package.json: %v", spec, err)
		}
		if len(pj.Dependencies) > 0 {
			meta["dependencies"] = pj.Dependencies
		}
		byName[name][ver] = version{tgz, meta}
	}
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if name, file, ok := strings.Cut(p, "/-/"); ok {
			for ver, v := range byName[name] {
				if file == name+"-"+ver+".tgz" {
					w.Write(v.tarball)
					return
				}
			}
			http.NotFound(w, r)
			return
		}
		vs := byName[strings.ReplaceAll(p, "%2f", "/")]
		if vs == nil {
			http.NotFound(w, r)
			return
		}
		versions := map[string]any{}
		var latest string
		for ver, v := range vs {
			versions[ver] = v.meta
			latest = ver
		}
		json.NewEncoder(w).Encode(map[string]any{"name": p, "dist-tags": map[string]string{"latest": latest}, "versions": versions})
	})
	srv.Start()
	t.Cleanup(srv.Close)
	cache := t.TempDir()
	env := append(os.Environ(), "BUN_CONFIG_REGISTRY="+srv.URL+"/", "BUN_INSTALL_CACHE_DIR="+cache, "NO_COLOR=1")
	return &registry{url: srv.URL, env: env}
}

func withFile(m map[string]string, k, v string) map[string]string {
	out := map[string]string{k: v}
	for a, b := range m {
		out[a] = b
	}
	return out
}

// tarball is a package tarball: stored, not compressed, with fixed headers,
// so that its integrity hash does not depend on the Go version.
func tarball(t testing.TB, files map[string]string) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		hdr := &tar.Header{Name: "package/" + n, Mode: 0o644, Size: int64(len(files[n])), ModTime: time.Unix(0, 0), Format: tar.FormatPAX}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(files[n]))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	zw, err := gzip.NewWriterLevel(&out, gzip.NoCompression)
	if err != nil {
		t.Fatal(err)
	}
	zw.ModTime = time.Unix(0, 0)
	zw.Write(raw.Bytes())
	zw.Close()
	return out.Bytes()
}

// lock is a lock in the format of WR-25 for these dependencies, made by
// installing them from the registry.
func (r *registry) lock(t testing.TB, bun string, deps map[string]string) string {
	t.Helper()
	pj, err := json.Marshal(map[string]any{"dependencies": deps})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), pj, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bun, "install", "--save-text-lockfile")
	cmd.Dir, cmd.Env = dir, r.env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bun install: %v\n%s", err, out)
	}
	lock, err := os.ReadFile(filepath.Join(dir, "bun.lock"))
	if err != nil {
		t.Fatal(err)
	}
	return string(pj) + "\n//bun.lock\n" + string(lock)
}

// writeFolder writes a sync folder's files into a new directory.
func writeFolder(t testing.TB, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for p, text := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// scriptsOf maps every .ts file of a folder to its Windmill path.
func scriptsOf(files map[string]string) map[string]string {
	out := map[string]string{}
	for p := range files {
		if strings.HasSuffix(p, ".ts") {
			out[strings.TrimSuffix(p, ".ts")] = p
		}
	}
	return out
}

// call is one call of a bundled module's export in runModules.
type call struct {
	Module string `json:"module"`
	Export string `json:"export"`
	Args   []any  `json:"args"`
}

// callResult is a call's JSON result, or the error it threw, as
// "<name>: <message>".
type callResult struct {
	Result json.RawMessage `json:"result"`
	Error  *string         `json:"error"`
}

const runnerSource = `const spec = JSON.parse(await Bun.file(process.argv[2]).text());
if (spec.env !== null) process.env = spec.env;
// A registration that fails stops the registration, as a harness's start
// fails; the calls still run.
let registerError = null;
if (spec.register !== null) {
  const reg = new Map();
  globalThis[Symbol.for("windmill-lite.hostModules")] = reg;
  try {
    for (const [key, file] of spec.register) reg.set(key, await import(file));
  } catch (e) {
    registerError = String(e?.name) + ": " + String(e?.message);
  }
}
const out = [];
for (const c of spec.calls ?? []) {
  try {
    const ns = await import(c.module);
    const v = await ns[c.export](...(c.args ?? []));
    out.push({ result: v === undefined ? null : v });
  } catch (e) {
    out.push({ error: String(e?.name) + ": " + String(e?.message) });
  }
}
process.stdout.write("\n@@" + JSON.stringify({ register_error: registerError, calls: out }) + "\n");
`

// runModules imports bundled modules in one Bun process and calls their
// exports in order, after replacing process.env with env (as a harness's
// per-invocation view does, WA-31) and registering host-provided modules
// (key and module file, in order) when register is not nil.
func runModules(t testing.TB, bun string, env map[string]string, register [][2]string, calls []call) []callResult {
	t.Helper()
	return mustRegister(t, runModulesAt(t, bun, "", env, register, calls))
}

// runModulesRegistering runs calls as runModules does, and returns the
// error that ended the registration ("<name>: <message>"), if one did,
// instead of failing the test.
func runModulesRegistering(t testing.TB, bun string, env map[string]string, register [][2]string, calls []call) (*string, []callResult) {
	t.Helper()
	r := runModulesAt(t, bun, "", env, register, calls)
	return r.RegisterError, r.Calls
}

// mustRegister fails the test when the registration failed.
func mustRegister(t testing.TB, r runnerOutput) []callResult {
	t.Helper()
	if r.RegisterError != nil {
		t.Fatalf("registering the host-provided modules: %s", *r.RegisterError)
	}
	return r.Calls
}

// runnerOutput is what the runner prints: the error that ended the
// registration, if one did, and each call's result.
type runnerOutput struct {
	RegisterError *string      `json:"register_error"`
	Calls         []callResult `json:"calls"`
}

// runModulesIn runs calls as runModules does, in the working directory
// cwd, where a harness extracts a bundle's assets.
func runModulesIn(t testing.TB, bun, cwd string, calls []call) []callResult {
	t.Helper()
	return mustRegister(t, runModulesAt(t, bun, cwd, nil, nil, calls))
}

func runModulesAt(t testing.TB, bun, cwd string, env map[string]string, register [][2]string, calls []call) runnerOutput {
	t.Helper()
	dir := t.TempDir()
	if cwd == "" {
		cwd = dir
	}
	runner := filepath.Join(dir, "runner.mjs")
	specPath := filepath.Join(dir, "spec.json")
	spec, err := json.Marshal(map[string]any{"env": env, "register": register, "calls": calls})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runner, []byte(runnerSource), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, spec, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bun, runner, specPath)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running the bundles: %v\n%s", err, out)
	}
	_, last, ok := bytes.Cut(out, []byte("\n@@"))
	if !ok {
		t.Fatalf("the runner printed no results:\n%s", out)
	}
	var res runnerOutput
	if err := json.Unmarshal(bytes.TrimSpace(last), &res); err != nil {
		t.Fatalf("runner output: %v\n%s", err, out)
	}
	return res
}

// jsonEqual compares two JSON texts as values.
func jsonEqual(t testing.TB, got, want json.RawMessage) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("bad JSON %s: %v", got, err)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("bad JSON %s: %v", want, err)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	return bytes.Equal(gb, wb)
}
