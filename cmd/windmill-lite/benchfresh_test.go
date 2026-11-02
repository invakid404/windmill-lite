package main

import (
	"bytes"
	"flag"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
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

// benchFreshResults holds each leaf's figures from its child process, by
// the leaf's *testing.B and GOMAXPROCS: the testing package calls a leaf's
// function more than once (first with b.N of 1) on one *testing.B, and the
// child runs once for it. With -count, each count is a new *testing.B, and
// a child of its own. The first call on the first count's *testing.B comes
// before the testing package sets GOMAXPROCS to the first -cpu value, so
// with more than one value it starts a child at another, whose figures go
// unused.
var benchFreshResults = map[benchFreshKey][]benchMetric{}

type benchFreshKey struct {
	b     *testing.B
	procs int
}

// benchFreshChildren counts the child processes this process has started,
// which numbers their profiles.
var benchFreshChildren int

type benchMetric struct {
	value float64
	unit  string
}

// benchInChild runs the leaf benchmark b in a child test process, with this
// process's test flags (freshChildArgs), and reports the child's figures as
// b's, ns/op included. It logs the child's process ID. The iteration count
// printed is this process's: with -benchtime Nx it is N, as the child's;
// with a duration it is 1.
func benchInChild(b *testing.B) {
	name := b.Name()
	key := benchFreshKey{b, runtime.GOMAXPROCS(0)}
	metrics, ok := benchFreshResults[key]
	if !ok {
		exe, err := os.Executable()
		if err != nil {
			b.Fatal(err)
		}
		set := map[string]string{}
		flag.Visit(func(f *flag.Flag) { set[f.Name] = f.Value.String() })
		for _, f := range []string{"test.benchtime", "test.timeout"} {
			if fl := flag.Lookup(f); fl != nil {
				set[f] = fl.Value.String()
			}
		}
		benchFreshChildren++
		args := freshChildArgs(name, key.procs, benchFreshChildren, set)
		cmd := exec.Command(exe, args...)
		cmd.Env = append(os.Environ(), benchFreshChildEnv+"=1")
		out, err := cmd.CombinedOutput()
		if err == nil {
			metrics, err = benchLine(out, name)
		}
		if err != nil {
			b.Fatalf("%s in its own process: %v\n%s", name, err, out)
		}
		b.Logf("ran in process %d: %s", cmd.Process.Pid, strings.Join(args, " "))
		benchFreshResults[key] = metrics
	}
	for _, m := range metrics {
		b.ReportMetric(m.value, m.unit)
	}
}

// freshChildArgs are the arguments of the child process that runs the
// benchmark named name alone: its -test.bench pattern, one count, and
// -test.cpu at procs, the GOMAXPROCS the testing package set for this run
// of it (one per -cpu value); then this process's test flags that set, as
// set holds them (benchtime and timeout always), each as it was given, but
// for the ones this process acts on itself (run, bench, count, cpu, list,
// skip, shuffle, fuzzing, coverage, the test log). A profile or trace
// goes to a file of the child's own: the name, then the benchmark's, its
// -cpu and the child's number n, so that no child writes over another,
// whether for another -cpu value or another count.
func freshChildArgs(name string, procs, n int, set map[string]string) []string {
	args := []string{"-test.run=^$", "-test.bench=" + benchPattern(name), "-test.count=1", "-test.cpu=" + strconv.Itoa(procs)}
	own := map[string]bool{
		"test.run": true, "test.bench": true, "test.count": true, "test.cpu": true, "test.list": true, "test.skip": true,
		"test.shuffle": true, "test.paniconexit0": true, "test.testlogfile": true, "test.gocoverdir": true, "test.coverprofile": true,
		"test.fuzz": true, "test.fuzztime": true, "test.fuzzminimizetime": true, "test.fuzzcachedir": true, "test.fuzzworker": true,
		"test.artifacts": true,
	}
	profiles := map[string]bool{"test.cpuprofile": true, "test.memprofile": true, "test.blockprofile": true, "test.mutexprofile": true, "test.trace": true}
	leaf := strings.NewReplacer("/", "_", "=", "-", " ", "_").Replace(name)
	for _, f := range slices.Sorted(maps.Keys(set)) {
		v := set[f]
		switch {
		case !strings.HasPrefix(f, "test.") || own[f]:
			continue
		case profiles[f] && v != "":
			v += fmt.Sprintf(".%s.cpu%d.%d", leaf, procs, n)
		}
		args = append(args, "-"+f+"="+v)
	}
	return args
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
	// The child gets the flags this process was given, -cpu at the
	// GOMAXPROCS of this run of the benchmark, and a profile of its own.
	set := map[string]string{
		"test.benchtime": "10x", "test.timeout": "1h0m0s", "test.benchmem": "true", "test.short": "true", "test.cpu": "1,4",
		"test.run": "^$", "test.bench": "X", "test.count": "3", "test.cpuprofile": "cpu.out", "test.v": "true", "test.memprofilerate": "1",
		"test.trace": "trace.out",
	}
	got := freshChildArgs("BenchmarkX/a=1", 4, 2, set)
	want := []string{"-test.run=^$", "-test.bench=^BenchmarkX$/^a=1$", "-test.count=1", "-test.cpu=4", "-test.benchmem=true",
		"-test.benchtime=10x", "-test.cpuprofile=cpu.out.BenchmarkX_a-1.cpu4.2", "-test.memprofilerate=1", "-test.short=true",
		"-test.timeout=1h0m0s", "-test.trace=trace.out.BenchmarkX_a-1.cpu4.2", "-test.v=true"}
	if !slices.Equal(got, want) {
		t.Fatalf("freshChildArgs:\n%q\nwant\n%q", got, want)
	}
	// -cpu 1,4 with -count 3 starts six children, with six profiles, in
	// the order the testing package runs them.
	files := map[string]bool{}
	n := 0
	for range 3 {
		for _, procs := range []int{1, 4} {
			n++
			for _, a := range freshChildArgs("BenchmarkX/a=1", procs, n, set) {
				if v, ok := strings.CutPrefix(a, "-test.cpuprofile="); ok {
					files[v] = true
				}
			}
		}
	}
	if len(files) != 6 {
		t.Fatalf("six children write %d profiles: %v", len(files), slices.Sorted(maps.Keys(files)))
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
	// With -cpu 1,2 and -count 2, each of the four results comes from a
	// child at its own -cpu, each child writing a profile of its own.
	name := "BenchmarkStepBoundary/topology=subprocess/conc=1/size=26B"
	dir := t.TempDir()
	cmd := exec.Command(exe, "-test.run=^$", "-test.bench="+benchPattern(name), "-test.benchtime=2x", "-test.cpu=1,2", "-test.count=2",
		"-test.cpuprofile="+filepath.Join(dir, "cpu.out"), "-test.v")
	cmd.Env = append(os.Environ(), benchFreshEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	ms, err = benchLine(out, name)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	m := regexp.MustCompile(`ran in process (\d+): (.*)`).FindSubmatch(out)
	if m == nil || string(m[1]) == strconv.Itoa(cmd.Process.Pid) {
		t.Fatalf("the configuration did not run in a child process\n%s", out)
	}
	if !strings.Contains(string(m[2]), "-test.benchtime=2x") {
		t.Fatalf("the child did not get -benchtime: %s", m[2])
	}
	child, results := "", map[string]int{}
	for _, line := range strings.Split(string(out), "\n") {
		if m := regexp.MustCompile(`ran in process \d+: .*-test\.cpu=(\d+)`).FindStringSubmatch(line); m != nil {
			child = m[1]
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 || (f[0] != name && f[0] != name+"-2") {
			continue
		}
		procs := "1"
		if f[0] != name {
			procs = "2"
		}
		if child != procs {
			t.Fatalf("a result at -cpu %s came from a child at -cpu %q\n%s", procs, child, out)
		}
		results[procs]++
		child = ""
	}
	if results["1"] != 2 || results["2"] != 2 {
		t.Fatalf("results by -cpu: %v\n%s", results, out)
	}
	profiles, err := filepath.Glob(filepath.Join(dir, "cpu.out.*"))
	if err != nil || len(profiles) < 4 {
		t.Fatalf("profiles %v, %v", profiles, err)
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
