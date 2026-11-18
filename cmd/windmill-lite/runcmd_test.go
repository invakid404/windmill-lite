package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRunCommand builds a synthetic folder and runs its flow as the command
// line does, in both module states: the step reads a host's secret through
// argument resolution ($var:, WA-12). A successful run exits 0 and prints
// its outcome, with the trace on request; a failed one exits 1.
func TestRunCommand(t *testing.T) {
	bun := bunForTest(t)
	dir := t.TempDir()
	folder := filepath.Join(dir, "windmill")
	for p, s := range map[string]string{
		"wmill.yaml": "defaultTs: bun\n",
		"f/t/hello.ts": `export async function main(greeting: string, name: string) {
  if (name === "fail") throw new Error("no " + name);
  return greeting + ", " + name;
}
`,
		"f/t/greet.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/t/hello","input_transforms":{` +
			`"greeting":{"type":"static","value":"$var:u/t/greeting"},"name":{"type":"javascript","expr":"flow_input.name"}}}}]}}`,
		// A state that step a creates through the job API and step b
		// reads through a $res: argument: a run keeps it, and the next run
		// creates it again (D24).
		"f/t/state.ts": `export async function main() {
  const r = await fetch(` + "`${process.env.BASE_URL}/api/w/${process.env.WM_WORKSPACE}/resources/create`" + `, {
    method: "POST",
    headers: { Authorization: ` + "`Bearer ${process.env.WM_TOKEN}`" + `, "Content-Type": "application/json" },
    body: JSON.stringify({ path: "f/t/kept", value: { k: 1 }, resource_type: "state" }),
  });
  return await r.text();
}
`,
		"f/t/keep.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/t/state","input_transforms":{}}},` +
			`{"id":"b","value":{"type":"rawscript","language":"bun","content":"export async function main(created: string, x: unknown) { return { created, x } }",` +
			`"input_transforms":{"created":{"type":"javascript","expr":"results.a"},"x":{"type":"static","value":"$res:f/t/kept"}}}}]}}`,
	} {
		f := filepath.Join(folder, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	vars := filepath.Join(dir, "variables.json")
	if err := os.WriteFile(vars, []byte(`{"u/t/greeting":{"value":"hi","is_secret":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	args := filepath.Join(dir, "args.json")
	if err := os.WriteFile(args, []byte(`{"name":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join("..", "..", "harnesses", "bun", "build", "main.ts")
	runtime := filepath.Join("..", "..", "harnesses", "bun", "runtime")
	b := filepath.Join(dir, "b.wlb")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"build", folder, "-o", b, "-bun", bun, "-helper", helper}, &stdout, &stderr); code != 0 {
		t.Fatalf("build exit code %d: %s", code, stderr.String())
	}
	for _, mode := range []string{"warm", "fresh"} {
		stdout.Reset()
		stderr.Reset()
		prof := filepath.Join(dir, mode+".pprof")
		code := run([]string{"run", "f/t/greet", "--bundle", b, "--args", "@" + args, "-bun", bun, "-harness-runtime", runtime,
			"-module-state", mode, "-variables", vars, "-trace", "-cpuprofile", prof}, &stdout, &stderr)
		var out runOutput
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil || code != 0 {
			t.Fatalf("%s: exit code %d, stdout %s, stderr %s", mode, code, stdout.String(), stderr.String())
		}
		if out.Status != "success" || string(out.Result) != `"hi, x"` || out.Trace == nil || out.Trace.ModuleState != mode ||
			len(out.Trace.Steps) != 1 || out.Trace.Steps[0].Site.String() != "m:a" || out.Trace.Steps[0].Job == "" {
			t.Fatalf("%s: %s", mode, stdout.String())
		}
		// -cpuprofile (milestone G1) wrote the engine's CPU profile.
		if err := checkCPUProfile(prof); err != nil {
			t.Fatalf("%s: -cpuprofile: %v", mode, err)
		}

		stdout.Reset()
		code = run([]string{"run", "f/t/greet", "--bundle", b, "--args", `{"name":"fail"}`, "-bun", bun, "-harness-runtime", runtime,
			"-module-state", mode, "-variables", vars}, &stdout, &stderr)
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil || code != 1 || out.Status != "failure" || !strings.Contains(string(out.Result), "no fail") {
			t.Fatalf("%s: a failing run: exit code %d, stdout %s", mode, code, stdout.String())
		}
	}
	// -resource-store memory: a run keeps the state its step writes, and
	// the next run, a new runtime, starts without it (D24); a store other
	// than memory is refused before the bundle opens.
	for i := range 2 {
		stdout.Reset()
		stderr.Reset()
		code := run([]string{"run", "f/t/keep", "--bundle", b, "-bun", bun, "-harness-runtime", runtime, "-resource-store", "memory"}, &stdout, &stderr)
		var out runOutput
		var result bytes.Buffer
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil || code != 0 || json.Compact(&result, out.Result) != nil ||
			result.String() != `{"x":{"k":1},"created":"resource f/t/kept created"}` {
			t.Fatalf("-resource-store memory, run %d: exit code %d, stdout %s, stderr %s", i+1, code, stdout.String(), stderr.String())
		}
	}
	// Each refusal runs in an engine process of its own, so that its whole
	// standard error, go-redis's log included, is checked for the URL's
	// marker.
	for _, c := range []struct {
		kind, url, want string
	}{
		{"bogus", "", `-resource-store "bogus": the resource store must be memory or redis`},
		{"redis", "", "-resource-store redis: WL_REDIS_URL is not set; it holds the Redis URL, which no flag takes"},
		// No server listens on port 1: the run does not start, and the
		// error names the address and not the password.
		{"redis", "redis://u:s3cr3t-pw@127.0.0.1:1/0", "-resource-store redis: redisstore: cannot reach Redis at 127.0.0.1:1 (database 0)"},
		// No Sentinel listens either; go-redis logs its failed lookups of
		// the master by name.
		{"redis", "redis://u:s3cr3t-pw@127.0.0.1:1/0?master_name=main&password=s3cr3t-pw", "-resource-store redis: redisstore: cannot reach Redis at the master of the Sentinels 127.0.0.1:1 (database 0)"},
		// URLs that do not parse: the error names no part of them, here a
		// credential pasted as a Sentinel address and as the master's name.
		{"redis", "redis://127.0.0.1:26379/0?master_name=m&addr=redis://u:s3cr3t-pw@host2:26379", "-resource-store redis: redisstore: the Redis URL does not parse"},
		{"redis", "redis://127.0.0.1:26379/0?master_name=redis://u:s3cr3t-pw@host2:6379", "-resource-store redis: redisstore: the Redis URL does not parse"},
	} {
		cmd := exec.Command(os.Args[0], "run", "f/t/keep", "--bundle", b, "-bun", bun, "-harness-runtime", runtime, "-resource-store", c.kind)
		cmd.Env = append(os.Environ(), "WL_TEST_ENGINE=1", "WL_REDIS_URL="+c.url)
		var out, errOut bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errOut
		err := cmd.Run()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), c.want) || strings.Contains(errOut.String(), "s3cr3t") {
			t.Errorf("-resource-store %s (case %q): %v, stdout %d bytes, stderr holds the marker %v, want %q",
				c.kind, c.want, err, out.Len(), strings.Contains(errOut.String(), "s3cr3t"), c.want)
		}
	}
	if url := os.Getenv("WL_TEST_REDIS_URL"); url != "" {
		// -resource-store redis, cross-process: two engine processes, each
		// with its own runtime, on one Redis and one prefix. The second sees
		// the state the first created: its create is refused as taken and
		// its $res: argument reads the first's value. A third, on another
		// prefix, sees none of it.
		id := time.Now().UnixNano()
		prefix, other := fmt.Sprintf("wl-test-run-%d:", id), fmt.Sprintf("wl-test-run-%d-other:", id)
		for i, c := range []struct{ prefix, want string }{
			{prefix, `{"x":{"k":1},"created":"resource f/t/kept created"}`},
			{prefix, `{"x":{"k":1},"created":"Bad request: Resource f/t/kept already exists"}`},
			{other, `{"x":{"k":1},"created":"resource f/t/kept created"}`},
		} {
			want := c.want
			cmd := exec.Command(os.Args[0], "run", "f/t/keep", "--bundle", b, "-bun", bun, "-harness-runtime", runtime, "-resource-store", "redis", "-redis-prefix", c.prefix)
			cmd.Env = append(os.Environ(), "WL_TEST_ENGINE=1", "WL_REDIS_URL="+url)
			var errOut bytes.Buffer
			cmd.Stderr = &errOut
			got, err := cmd.Output()
			var out runOutput
			var result bytes.Buffer
			if err != nil || json.Unmarshal(got, &out) != nil || json.Compact(&result, out.Result) != nil || result.String() != want {
				t.Fatalf("-resource-store redis, process %d: %v, stdout %s, stderr %s; want the result %s", i+1, err, got, errOut.String(), want)
			}
		}
	}
	// Without the harness runtime the bundle was built for, nothing runs.
	stdout.Reset()
	stderr.Reset()
	t.Setenv("WL_HARNESS_RUNTIME", "")
	if code := run([]string{"run", "f/t/greet", "--bundle", b, "-bun", bun}, &stdout, &stderr); code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "harness runtime") {
		t.Errorf("run without the harness runtime: exit code %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

func TestRunCommandUsage(t *testing.T) {
	for _, args := range [][]string{{"run"}, {"run", "f/t/a"}, {"run", "f/t/a", "f/t/b", "--bundle", "b"}, {"run", "f/t/a", "--bundle", "b", "-nope"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Errorf("run(%q) exit code = %d, want 2 (%s)", args, code, stderr.String())
		}
	}
	for _, c := range []struct{ args []string }{
		{[]string{"run", "f/t/a", "--bundle", "b", "--args", "{"}},
		{[]string{"run", "f/t/a", "--bundle", "b", "--args", "@" + filepath.Join(t.TempDir(), "none.json")}},
		{[]string{"run", "f/t/a", "--bundle", filepath.Join(t.TempDir(), "none")}},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(c.args, &stdout, &stderr); code != 1 || stdout.Len() != 0 {
			t.Errorf("run(%q) exit code = %d, stdout %q, want 1 and nothing", c.args, code, stdout.String())
		}
	}
	// A CPU profile that cannot be written fails the command before it
	// opens the bundle.
	var stdout, stderr bytes.Buffer
	if code := run([]string{"run", "f/t/a", "--bundle", "b", "-cpuprofile", filepath.Join(t.TempDir(), "none", "cpu.pprof")}, &stdout, &stderr); code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "the CPU profile") {
		t.Errorf("run -cpuprofile into a missing directory: exit code %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

// checkCPUProfile checks that file holds a whole CPU profile in Go's pprof
// format: a gzip stream, complete, whose profile names CPU time in
// nanoseconds among its sample types.
func checkCPUProfile(file string) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("not gzip: %w", err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		return fmt.Errorf("an incomplete profile: %w", err)
	}
	if !bytes.Contains(raw, []byte("cpu")) || !bytes.Contains(raw, []byte("nanoseconds")) {
		return fmt.Errorf("not a CPU profile (%d bytes)", len(raw))
	}
	return nil
}
