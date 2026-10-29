package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// The Q-WA-4 test, W1's exit gate "the Q-WA-4 env-view test 16/16": does
// the per-invocation isolation of WA-31 hold on the pinned Bun 1.3.10 with
// the stock pinned windmill-client? It runs the experiment design of WA-31
// and Q-WA-4 on the real harness:
//
//   - the stock windmill-client@1.767.0 (WL_WINDMILL_CLIENT names a
//     directory whose node_modules holds it; CI fetches it by its npm
//     integrity hash), through a namespace import and a default import;
//   - each step bundled three ways: not at all, by Bun.build with
//     Windmill's settings (WR-27), and by Bun.build with the define rewrite
//     of WA-31 (Bun.env and import.meta.env to process.env);
//   - one unbundled node_modules dependency that reads Bun.env, kept
//     external in every build;
//   - 16 concurrent invocations per variant, each with its own WM_*
//     values, every one making a client call that reads WM_WORKSPACE and
//     WM_JOB_ID at call time and goes to the facade by fetch.
//
// Every channel must read the invocation's own value, 16 of 16: direct
// process.env, Bun.env and import.meta.env reads before and after awaits,
// a copy of process.env ({...process.env}), a variable the step writes to
// process.env and reads back after an await, a process variable that half
// of the steps delete (gone for them, still there for the others), the
// dependency's Bun.env
// read, and the client call, whose platform frame must be attributed to
// the invocation that made it and carry its variables, and whose answer
// must reach that invocation.
func TestQWA4EnvView(t *testing.T) {
	bun := bunPath(t)
	client := os.Getenv("WL_WINDMILL_CLIENT")
	if client == "" {
		if os.Getenv("WL_REQUIRE_BUN") != "" {
			t.Fatal("WL_REQUIRE_BUN is set but WL_WINDMILL_CLIENT is not; it names a directory whose node_modules holds windmill-client@1.767.0")
		}
		t.Skip("WL_WINDMILL_CLIENT is not set")
	}
	pkg, err := os.ReadFile(filepath.Join(client, "node_modules", "windmill-client", "package.json"))
	if err != nil || !strings.Contains(string(pkg), `"version": "1.767.0"`) {
		t.Fatalf("WL_WINDMILL_CLIENT must hold windmill-client 1.767.0: %v", err)
	}
	bundle, variants := qwa4Bundle(t, bun, client)
	digest := digestOf(t, bundle)
	facade := closedOrigin(t)

	for _, proc := range []struct {
		name string
		env  []string
	}{
		{"process without WM_*", nil},
		// The host process of host mode may itself have the variables:
		// the harness turns each into an accessor (path 3 of the view).
		{"process with WM_*", []string{"WM_JOB_ID=process", "WM_WORKSPACE=process", "WM_TOKEN=process"}},
	} {
		t.Run(proc.name, func(t *testing.T) {
			f := &qwa4Facade{calls: map[string]qwa4Call{}}
			main := repoPath(t, "harnesses/bun/runtime/main.ts")
			path, args := BunCommand(bun, main, bundle)
			diag := &syncBuffer{}
			p := spawn(t, ProcessConfig{
				Path: path, Args: args, WorkRoot: t.TempDir(), Kind: "bun", Runtime: PinnedBun, Bundle: digest, Diagnostics: diag,
				Env:    append(append(BaseEnv(), "BASE_URL="+facade, "WL_QWA4_PROCESS=process"), proc.env...),
				Facade: facade, Platform: f.handle,
			})
			var report []string
			for _, v := range variants {
				counts := qwa4Run(t, p, f, v)
				line := v + ":"
				for _, ch := range qwa4Channels {
					line += fmt.Sprintf(" %s %d/16", ch, counts[ch])
					if counts[ch] != 16 {
						t.Errorf("%s: channel %s isolated %d of 16", v, ch, counts[ch])
					}
				}
				report = append(report, line)
			}
			t.Logf("Q-WA-4 on Bun %s, %s:\n%s", PinnedBun, proc.name, strings.Join(report, "\n"))
			if t.Failed() {
				t.Logf("diagnostics:\n%s", diag)
			}
		})
	}
}

var qwa4Channels = []string{"process.env", "Bun.env", "import.meta.env", "process.env copy", "process.env write", "process.env delete", "dependency Bun.env", "client call", "after await"}

type qwa4Call struct {
	workspace, job, token string
}

// qwa4Facade answers getRootJobId calls and records each by the invocation
// the harness attributed it to.
type qwa4Facade struct {
	mu    sync.Mutex
	calls map[string]qwa4Call
}

func (f *qwa4Facade) handle(req PlatformRequest) func(context.Context) PlatformReply {
	rep := f.answer(req)
	return func(context.Context) PlatformReply { return rep }
}

func (f *qwa4Facade) answer(req PlatformRequest) PlatformReply {
	u, err := url.Parse(req.URL)
	// /api/w/<workspace>/jobs_u/get_root_job_id/<id>
	parts := strings.Split(strings.TrimPrefix(u.Path, "/api/w/"), "/")
	if err != nil || len(parts) != 4 || parts[1] != "jobs_u" || parts[2] != "get_root_job_id" || req.Method != "GET" {
		return PlatformReply{Status: 404, Headers: map[string]string{"content-type": "text/plain"}, Body: []byte("unexpected " + req.Method + " " + req.URL)}
	}
	f.mu.Lock()
	f.calls[req.Inv] = qwa4Call{workspace: parts[0], job: parts[3], token: req.Headers["authorization"]}
	f.mu.Unlock()
	// The answer names the invocation the call was attributed to, so the
	// step can check that its own call came back to it.
	body, _ := json.Marshal("root-of-" + req.Inv)
	return PlatformReply{Status: 200, Headers: map[string]string{"content-type": "application/json"}, Body: body}
}

func qwa4Run(t *testing.T, p *Process, f *qwa4Facade, variant string) map[string]int {
	t.Helper()
	type res struct {
		i   int
		out engine.StepOutcome
		err error
	}
	results := make(chan res, 16)
	for i := range 16 {
		go func() {
			inv := engine.Invocation{
				ID:     fmt.Sprintf("inv-%s-%d", variant, i),
				Run:    "run-" + variant,
				Script: "qwa4/" + variant,
				Entry:  "main",
				Args:   jsonval.FromProducer(fmt.Appendf(nil, `{"delay":%d,"del":%t}`, (i*7)%16, i%2 == 0)),
				Env: map[string]string{
					"WM_JOB_ID":    fmt.Sprintf("job-%s-%d", variant, i),
					"WM_WORKSPACE": fmt.Sprintf("ws-%d", i),
					"WM_TOKEN":     fmt.Sprintf("token-%d", i),
				},
				Boundary: engine.BoundaryBun,
				Report:   engine.NopReporter{},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			out, err := p.Execute(ctx, inv)
			results <- res{i, out, err}
		}()
	}
	counts := map[string]int{}
	for range 16 {
		r := <-results
		if r.err != nil || r.out.Kind != engine.OutcomeOK {
			t.Errorf("%s invocation %d: %+v, %v", variant, r.i, r.out, r.err)
			continue
		}
		var got struct {
			Process, Bun, Meta, Copy, Written, Dep, Root, After, AfterBun string
			Kept                                                          *string
		}
		if err := json.Unmarshal(r.out.Body.Producer(), &got); err != nil {
			t.Errorf("%s invocation %d: %s: %v", variant, r.i, r.out.Body, err)
			continue
		}
		inv, job, ws := fmt.Sprintf("inv-%s-%d", variant, r.i), fmt.Sprintf("job-%s-%d", variant, r.i), fmt.Sprintf("ws-%d", r.i)
		f.mu.Lock()
		call, called := f.calls[inv]
		f.mu.Unlock()
		for ch, ok := range map[string]bool{
			"process.env":        got.Process == job,
			"Bun.env":            got.Bun == job,
			"import.meta.env":    got.Meta == job,
			"process.env copy":   got.Copy == job,
			"process.env write":  got.Written == "wrote-"+job,
			"process.env delete": (r.i%2 == 0) == (got.Kept == nil) && (got.Kept == nil || *got.Kept == "process"),
			"dependency Bun.env": got.Dep == job,
			// Attributed to this invocation, read its variables at call
			// time, and the answer came back to it.
			"client call": called && call.workspace == ws && call.job == job && got.Root == "root-of-"+inv,
			"after await": got.After == job && got.AfterBun == job,
		} {
			if ok {
				counts[ch]++
			}
		}
	}
	return counts
}

// qwa4Bundle builds the bundle of the test: the step, a synthetic package
// that reads Bun.env, and the variants, built with the pinned Bun.
func qwa4Bundle(t *testing.T, bun, client string) (string, []string) {
	t.Helper()
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle")
	write := func(rel, text string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("bundle/manifest.json", `{"note":"the Q-WA-4 test's bundle (milestone W1)"}`)
	// The client resolves from the bundle's node_modules, for the unbundled
	// variants at run time and for Bun.build from src/ at build time.
	for _, dir := range []string{filepath.Join(bundle, "node_modules"), filepath.Join(root, "node_modules")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(client, "node_modules", "windmill-client"), filepath.Join(dir, "windmill-client")); err != nil {
			t.Fatal(err)
		}
	}
	write("bundle/node_modules/wl-env-reader/package.json", `{"name":"wl-env-reader","version":"0.0.0","type":"module","main":"index.js"}`)
	write("bundle/node_modules/wl-env-reader/index.js", "// A synthetic dependency that reads Bun.env at call time, unbundled.\nexport function jobId() {\n  return Bun.env.WM_JOB_ID;\n}\n")
	step := func(imp string) string {
		return imp + `
import { jobId } from "wl-env-reader";

export async function main(delay, del) {
  await Bun.sleep(delay);
  const process_ = process.env.WM_JOB_ID;
  const bun = Bun.env.WM_JOB_ID;
  const meta = import.meta.env.WM_JOB_ID;
  const copy = { ...process.env }.WM_JOB_ID;
  process.env.WL_WRITTEN = "wrote-" + process.env.WM_JOB_ID;
  if (del) delete process.env.WL_QWA4_PROCESS;
  const dep = jobId();
  const root = await wmill.getRootJobId();
  await Bun.sleep(delay);
  return { process: process_, bun, meta, copy, written: process.env.WL_WRITTEN, kept: Bun.env.WL_QWA4_PROCESS ?? null, dep, root, after: process.env.WM_JOB_ID, afterbun: Bun.env.WM_JOB_ID };
}
`
	}
	srcs := map[string]string{
		"namespace": step(`import * as wmill from "windmill-client";`),
		"default":   step(`import wmill from "windmill-client";`),
	}
	out := filepath.Join(bundle, "scripts", "bun", "qwa4")
	var variants []string
	for name, src := range srcs {
		write("src/"+name+".mjs", src)
		// Unbundled: the module and its imports load from node_modules.
		write("bundle/scripts/bun/qwa4/source-"+name+".mjs", src)
		for _, b := range []struct {
			kind   string
			define []string
		}{
			{"bundled", nil},
			{"define", []string{"--define", "Bun.env=process.env", "--define", "import.meta.env=process.env"}},
		} {
			// WR-27's settings: target bun, ESM, syntax-only minification,
			// electron external; the synthetic dependency stays external.
			args := append([]string{"build", filepath.Join(root, "src", name+".mjs"), "--target=bun", "--format=esm", "--minify-syntax",
				"--external", "electron", "--external", "wl-env-reader", "--outfile", filepath.Join(out, b.kind+"-"+name+".mjs")}, b.define...)
			cmd := exec.Command(bun, args...)
			cmd.Dir = root
			if msg, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("bun %s: %v\n%s", strings.Join(args, " "), err, msg)
			}
			variants = append(variants, b.kind+"-"+name)
		}
		variants = append(variants, "source-"+name)
	}
	slices.Sort(variants)
	// The signature the build helper would record for main (ADR 0002).
	for _, v := range variants {
		write("bundle/scripts/bun/qwa4/"+v+".meta.json", `{"signatures":{"main":{"params":[{"name":"delay","date":false},{"name":"del","date":false}]}}}`)
	}
	// The bundled variants must have inlined the client and kept the
	// dependency external, and the define rewrite must have applied.
	for _, v := range variants {
		b, err := os.ReadFile(filepath.Join(out, v+".mjs"))
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		switch {
		case strings.HasPrefix(v, "source-"):
		case strings.Contains(text, `from "windmill-client"`) || !strings.Contains(text, `from "wl-env-reader"`):
			t.Fatalf("%s is not bundled as the test needs", v)
		case strings.HasPrefix(v, "define-") && (strings.Contains(text, "Bun.env.WM_JOB_ID") || strings.Contains(text, "import.meta.env.WM_JOB_ID")):
			t.Fatalf("%s: the define rewrite did not apply", v)
		case strings.HasPrefix(v, "bundled-") && !strings.Contains(text, "Bun.env.WM_JOB_ID"):
			t.Fatalf("%s: Bun.build rewrote Bun.env by itself", v)
		}
	}
	return bundle, variants
}

func digestOf(t *testing.T, bundle string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// closedOrigin is a loopback origin where nothing listens, so a facade
// request that escaped interception would be refused, not answered.
func closedOrigin(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return "http://" + addr
}
