package wl

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/invakid404/windmill-lite/internal/bundle"
)

// bunOrSkip returns the Bun of WL_BUN, the pinned 1.3.10; without it the
// test is skipped, unless WL_REQUIRE_BUN is set, as CI sets it.
func bunOrSkip(t *testing.T) string {
	t.Helper()
	bun := os.Getenv("WL_BUN")
	if bun == "" {
		if os.Getenv("WL_REQUIRE_BUN") != "" {
			t.Fatal("WL_REQUIRE_BUN is set but WL_BUN is not")
		}
		t.Skip("WL_BUN is not set")
	}
	return bun
}

// setScript reads the variable v through the job API, sets it to "second"
// and returns what it read (WA-11).
const setScript = `export async function main(v: string) {
  const base = ` + "`${process.env.BASE_URL}/api/w/${process.env.WM_WORKSPACE}/variables`" + `;
  const headers = { Authorization: ` + "`Bearer ${process.env.WM_TOKEN}`" + `, "Content-Type": "application/json" };
  const r = await fetch(` + "`${base}/get_value/${v}`" + `, { headers });
  if (!r.ok) throw new Error("read: " + r.status);
  const old = await r.json();
  const u = await fetch(` + "`${base}/update/${v}`" + `, { method: "POST", headers, body: JSON.stringify({ value: "second" }) });
  if (!u.ok) throw new Error("update: " + u.status);
  return old;
}
`

// buildBundle builds a real bundle (bundle.Build) of a folder with the
// script f/t/set and the flow f/t/flow, which runs it on u/t/v.
func buildBundle(t *testing.T, bun string) (out, runtimeDir string) {
	t.Helper()
	repo, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	for p, s := range map[string]string{
		"wmill.yaml":              "defaultTs: bun\n",
		"f/t/set.ts":              setScript,
		"f/t/flow.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/t/set","input_transforms":{"v":{"type":"static","value":"u/t/v"}}}}]}}`,
	} {
		f := filepath.Join(folder, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runtimeDir = filepath.Join(repo, "harnesses", "bun", "runtime")
	out = filepath.Join(t.TempDir(), "bundle")
	if _, err := bundle.Build(context.Background(), bundle.Config{
		Folder:         folder,
		EngineVersion:  Version(),
		Bun:            bun,
		Helper:         filepath.Join(repo, "harnesses", "bun", "build", "main.ts"),
		HarnessRuntime: runtimeDir,
		Out:            out,
		Work:           filepath.Join(t.TempDir(), "work"),
	}); err != nil {
		t.Fatal(err)
	}
	return out, runtimeDir
}

// A real bundle runs its step on the Bun harness in warm and in fresh
// mode. The step reads and updates a host's variable through the job API
// (WA-11): the run's trace records both platform calls, attributed to the
// step, and the next run reads the update, which the runtime keeps.
func TestRunOnBun(t *testing.T) {
	bun := bunOrSkip(t)
	out, runtimeDir := buildBundle(t, bun)
	for _, mode := range []string{ModuleStateWarm, ModuleStateFresh} {
		t.Run(mode, func(t *testing.T) {
			rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, ModuleState: mode, Variables: map[string]Variable{"u/t/v": {Value: "first"}}})
			if err != nil {
				t.Fatal(err)
			}
			defer rt.Close()
			for i, want := range []string{`"first"`, `"second"`} {
				res, err := rt.Run(context.Background(), "f/t/flow", json.RawMessage(`{}`), RunOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if res.Status != StatusSuccess || string(res.Result) != want {
					t.Fatalf("run %d: %s %s", i, res.Status, res.Result)
				}
				calls := res.Trace.PlatformCalls
				if len(calls) != 2 || calls[0].Route != "GET /w/{ws}/variables/get_value/{path}" || calls[1].Route != "POST /w/{ws}/variables/update/{path}" ||
					calls[0].Status != 200 || calls[1].Status != 200 || calls[0].Site.String() != "m:a" || calls[0].Job != res.Trace.Steps[0].Job {
					t.Fatalf("run %d: platform calls %+v", i, calls)
				}
			}
			// The script is a root too: it runs as a script job (WA-4).
			res, err := rt.Run(context.Background(), "f/t/set", json.RawMessage(`{"v":"u/t/v"}`), RunOptions{Kind: KindScript})
			if err != nil || res.Status != StatusSuccess || string(res.Result) != `"second"` || res.Trace != nil {
				t.Fatalf("script run: %+v %v", res, err)
			}
		})
	}
	// The harness runtime must be the one the bundle was built for.
	if _, err := Open(out, Options{Bun: bun}); err == nil {
		t.Error("opened a bundle with steps and no harness runtime")
	}
	if _, err := Open(out, Options{Bun: bun, HarnessRuntime: t.TempDir()}); err == nil {
		t.Error("opened a bundle with another harness runtime")
	}
}
