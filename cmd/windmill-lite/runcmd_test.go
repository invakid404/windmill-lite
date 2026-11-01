package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
