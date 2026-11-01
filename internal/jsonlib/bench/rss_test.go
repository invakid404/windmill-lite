package bench

import (
	"github.com/invakid404/windmill-lite/internal/jsonlib/shapes"

	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	wlbench "github.com/invakid404/windmill-lite/internal/bench"
)

// TestWriteFixtures writes each family's payload at each size of
// JSONLIB_SIZES to JSONLIB_FIXTURES_OUT as <family>-<size>.json, for
// TestPeakRSS to read; with JSONLIB_BRIEF (tiers such as worst,p90) it
// writes the brief's families instead, as <family>-<tier>.json.
func TestWriteFixtures(t *testing.T) {
	dir := os.Getenv("JSONLIB_FIXTURES_OUT")
	if dir == "" {
		t.Skip("JSONLIB_FIXTURES_OUT is not set")
	}
	if os.Getenv("JSONLIB_BRIEF") != "" {
		// The brief's families, at the tiers JSONLIB_BRIEF lists.
		for _, f := range shapes.Brief {
			for _, tier := range f.Tiers {
				if !strings.Contains(","+os.Getenv("JSONLIB_BRIEF")+",", ","+tier.Name+",") {
					continue
				}
				p := filepath.Join(dir, f.Name+"-"+tier.Name+".json")
				if err := os.WriteFile(p, tier.Payload(), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		return
	}
	for _, n := range sizes(t) {
		for _, f := range shapes.Families {
			p := filepath.Join(dir, f+"-"+wlbench.SizeLabel(n)+".json")
			if err := os.WriteFile(p, shapes.Make(f, n), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestPeakRSS runs one operation once, for one candidate on one fixture,
// in a process of its own, and prints the process's resident memory as one
// JSON line: JSONLIB_RSS is cand,op,fixture-path. The fixture is read and
// the operation's input prepared first; then the garbage collector runs,
// memory is returned to the system, and on Linux the peak is reset
// (/proc/self/clear_refs), so peak_kib is the operation's own high-water
// mark over base_kib. Elsewhere the peak is getrusage's, which includes
// the fixture's reading.
func TestPeakRSS(t *testing.T) {
	spec := os.Getenv("JSONLIB_RSS")
	if spec == "" {
		t.Skip("JSONLIB_RSS is not set")
	}
	parts := strings.Split(spec, ",")
	if len(parts) != 3 {
		t.Fatalf("JSONLIB_RSS %q is not cand,op,path", spec)
	}
	c, ok := CandNamed(parts[0])
	if !ok {
		t.Fatalf("no candidate %q", parts[0])
	}
	op, ok := OpNamed(parts[1])
	if !ok || !op.Has(c) {
		t.Fatalf("no operation %q for %s", parts[1], c.Name)
	}
	payload, err := os.ReadFile(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	in, err := op.Prepare(c, payload)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	runtime.GC()
	debug.FreeOSMemory()
	reset := resetPeak()
	base := currentRSS()
	start := time.Now()
	n, err := op.Do(c, in)
	elapsed := time.Since(start)
	peak := peakRSS(reset)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	runtime.KeepAlive(in)
	runtime.KeepAlive(payload)
	line, _ := json.Marshal(map[string]any{
		"cand": c.Name, "op": op.Name, "fixture": filepath.Base(parts[2]), "bytes": len(payload),
		"base_kib": base, "peak_kib": peak, "extra_kib": peak - base, "peak_reset": reset,
		"out_bytes": n, "err": errText(err), "ns": elapsed.Nanoseconds(), "gc": ms.NumGC, "total_alloc": ms.TotalAlloc,
		"goos": runtime.GOOS, "goarch": runtime.GOARCH,
	})
	fmt.Printf("RSS %s\n", line)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// resetPeak resets the process's peak resident size, on Linux; it reports
// whether it could.
func resetPeak() bool {
	return runtime.GOOS == "linux" && os.WriteFile("/proc/self/clear_refs", []byte("5"), 0) == nil
}

// currentRSS is the resident size in KiB: VmRSS on Linux, the peak
// elsewhere.
func currentRSS() int64 {
	if v, ok := procStatus("VmRSS:"); ok {
		return v
	}
	return maxRSS()
}

// peakRSS is the peak resident size in KiB since it was reset, or since
// the process started.
func peakRSS(reset bool) int64 {
	if v, ok := procStatus("VmHWM:"); ok && reset {
		return v
	}
	return maxRSS()
}

func procStatus(field string) (int64, bool) {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, false
	}
	for _, l := range bytes.Split(b, []byte("\n")) {
		if v, ok := bytes.CutPrefix(l, []byte(field)); ok {
			n, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(string(v)), " kB"), 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}

// maxRSS is getrusage's peak in KiB (it reports bytes on macOS).
func maxRSS() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return ru.Maxrss / 1024
	}
	return ru.Maxrss
}
