package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// benchFreshEnv selects G1's per-process mode: with WL_BENCH_FRESH=1, each
// shape of BenchmarkHostShapes and each configuration of
// BenchmarkStepBoundary runs in a test process of its own. In one process,
// a shape runs on the heap the earlier ones left, so the Go collector's
// pace, and with it the engine's CPU, depends on which shapes ran before.
// The child, marked by benchFreshChildEnv, runs the benchmark as usual.
const (
	benchFreshEnv      = "WL_BENCH_FRESH"
	benchFreshChildEnv = "WL_BENCH_FRESH_CHILD"
)

// benchFresh reports whether this process hands its benchmarks' leaves to
// child processes.
func benchFresh() bool {
	return os.Getenv(benchFreshEnv) == "1" && os.Getenv(benchFreshChildEnv) == ""
}

// benchFreshResults holds each leaf's figures from its child process: the
// testing package calls a leaf's function more than once (first with b.N
// of 1) on one *testing.B, and the child runs once for it. With -count,
// each count is a new *testing.B, and a child of its own.
var benchFreshResults = map[*testing.B][]benchMetric{}

type benchMetric struct {
	value float64
	unit  string
}

// benchInChild runs the leaf benchmark b in a child test process, with this
// process's -benchtime and -timeout, and reports the child's figures as
// b's, ns/op included. It logs the child's process ID. The iteration count
// printed is this process's: with -benchtime Nx it is N, as the child's;
// with a duration it is 1.
func benchInChild(b *testing.B) {
	name := b.Name()
	metrics, ok := benchFreshResults[b]
	if !ok {
		exe, err := os.Executable()
		if err != nil {
			b.Fatal(err)
		}
		args := []string{"-test.run=^$", "-test.bench=" + benchPattern(name), "-test.count=1"}
		for _, f := range []string{"test.benchtime", "test.timeout"} {
			if fl := flag.Lookup(f); fl != nil {
				args = append(args, "-"+f+"="+fl.Value.String())
			}
		}
		cmd := exec.Command(exe, args...)
		cmd.Env = append(os.Environ(), benchFreshChildEnv+"=1")
		out, err := cmd.CombinedOutput()
		if err == nil {
			metrics, err = benchLine(out, name)
		}
		if err != nil {
			b.Fatalf("%s in its own process: %v\n%s", name, err, out)
		}
		b.Logf("ran in process %d", cmd.Process.Pid)
		benchFreshResults[b] = metrics
	}
	for _, m := range metrics {
		b.ReportMetric(m.value, m.unit)
	}
}

// benchPattern is the -test.bench pattern that selects exactly the
// benchmark named name: each level of the name, anchored and quoted.
func benchPattern(name string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = "^" + regexp.QuoteMeta(p) + "$"
	}
	return strings.Join(parts, "/")
}

// benchLine finds the result line of the benchmark named name in a test
// binary's output and returns its figures.
func benchLine(out []byte, name string) ([]benchMetric, error) {
	for _, line := range bytes.Split(out, []byte("\n")) {
		f := strings.Fields(string(line))
		if len(f) < 4 || (f[0] != name && !strings.HasPrefix(f[0], name+"-")) {
			continue
		}
		if _, err := strconv.Atoi(f[1]); err != nil {
			continue
		}
		var ms []benchMetric
		for i := 2; i+1 < len(f); i += 2 {
			v, err := strconv.ParseFloat(f[i], 64)
			if err != nil {
				return nil, fmt.Errorf("figure %q of %q", f[i], line)
			}
			ms = append(ms, benchMetric{v, f[i+1]})
		}
		return ms, nil
	}
	return nil, fmt.Errorf("no result line for %s", name)
}

// TestBenchFresh checks the per-process mode end to end: one boundary
// configuration run with WL_BENCH_FRESH=1 ran in a child of the process
// the test started, and reports the figures it reports in one process. It
// needs the pinned Bun.
func TestBenchFresh(t *testing.T) {
	if got, want := benchPattern("BenchmarkHostShapes/shape=s200-mixed-conc8"), `^BenchmarkHostShapes$/^shape=s200-mixed-conc8$`; got != want {
		t.Fatalf("pattern %q, want %q", got, want)
	}
	ms, err := benchLine([]byte("goos: darwin\nBenchmarkX/a=1-10   \t       2\t  12.5 step-p50-ns\t 3 load1\n"), "BenchmarkX/a=1")
	if err != nil || len(ms) != 2 || ms[0] != (benchMetric{12.5, "step-p50-ns"}) {
		t.Fatalf("benchLine: %v, %v", ms, err)
	}
	benchSetup(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "BenchmarkStepBoundary/topology=subprocess/conc=1/size=26B"
	cmd := exec.Command(exe, "-test.run=^$", "-test.bench="+benchPattern(name), "-test.benchtime=2x", "-test.v")
	cmd.Env = append(os.Environ(), benchFreshEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	ms, err = benchLine(out, name)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	m := regexp.MustCompile(`ran in process (\d+)`).FindSubmatch(out)
	if m == nil || string(m[1]) == strconv.Itoa(cmd.Process.Pid) {
		t.Fatalf("the configuration did not run in a child process\n%s", out)
	}
	units := map[string]float64{}
	for _, m := range ms {
		units[m.unit] = m.value
	}
	for _, u := range []string{"ns/op", "step-p50-ns", "engine-cpu-ns/step", "host-cpu-ns/step"} {
		if units[u] <= 0 {
			t.Fatalf("%s missing or zero in %v\n%s", u, ms, out)
		}
	}
}
