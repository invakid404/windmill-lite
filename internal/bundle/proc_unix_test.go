//go:build unix

package bundle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestBuildCancelsInstallTree times a build out while a lifecycle script
// of its install runs, and checks that the script's process is gone when
// BuildBun returns (c2a review 3).
func TestBuildCancelsInstallTree(t *testing.T) {
	bun := bunPath(t)
	reg := newRegistry(t, map[string]map[string]string{"fixture-pkg@1.2.3": {"index.js": "export const tag = 1;\n"}})
	lock := reg.lock(t, bun, map[string]string{"fixture-pkg": "1.2.3"})
	pidFile := filepath.Join(t.TempDir(), "pid")
	pj, rest, ok := strings.Cut(lock, "\n//bun.lock\n")
	if !ok {
		t.Fatalf("lock: %q", lock)
	}
	// The same dependencies, with a root preinstall script that records its
	// pid and then outlives the build.
	pj = strings.Replace(pj, `{"dependencies"`, `{"scripts":{"preinstall":"echo $$ > `+pidFile+`; exec sleep 30"},"dependencies"`, 1)
	files := map[string]string{"f/a/main.ts": "import { tag } from \"fixture-pkg\";\nexport function main() { return tag; }\n"}
	start := time.Now()
	_, err := BuildBun(context.Background(), BunConfig{
		Bun: bun, Helper: repoPath(t, "harnesses/bun/build/main.ts"),
		Folder: writeFolder(t, files), Out: t.TempDir(), Scripts: scriptsOf(files), Env: reg.env,
		Timeout: 3 * time.Second,
		Entries: []BunScript{{ID: "main", Path: "f/a/main", File: "f/a/main.ts", Lock: pj + "\n//bun.lock\n" + rest}},
	})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the build did not time out: %v", err)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("the build took %v to time out", d)
	}
	b, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		t.Fatalf("the preinstall script did not run: %v (%v)", rerr, err)
	}
	pid, perr := strconv.Atoi(strings.TrimSpace(string(b)))
	if perr != nil {
		t.Fatal(perr)
	}
	if kerr := syscall.Kill(pid, 0); !errors.Is(kerr, syscall.ESRCH) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("the install's lifecycle process %d outlived the build (kill: %v)", pid, kerr)
	}
}
