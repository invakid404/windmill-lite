//go:build linux || darwin

package oracle

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

// TestCLIVersionExact: --version must report exactly the pinned version
// (OR-8); a pre-release or another version that contains it is refused.
func TestCLIVersionExact(t *testing.T) {
	for _, c := range []struct {
		out string
		ok  bool
	}{
		{"CLI version: 1.767.0\nCLI is outdated. Latest version 1.851.0 is available. Run `wmill upgrade` to update.", true},
		{"1.767.0", true},
		{"v1.767.0", true},
		{"wmill 1.767.0\n", true},
		{"1.767.0-beta", false},
		{"CLI version: 1.767.0-rc1", false},
		{"11.767.0", false},
		{"1.767.01", false},
		{"1.767.0.1", false},
		{"CLI version: 1.766.0\nLatest version 1.767.0 is available.", false},
		{"no version here", false},
		{"", false},
	} {
		// The CLI's environment is fixed, so the output is in the script.
		script := "printf '%b' '" + strings.ReplaceAll(c.out, "\n", `\n`) + "'"
		cli := CLI{Command: []string{"/bin/sh", "-c", script}, Home: t.TempDir()}
		v, err := cli.Version(context.Background())
		var r *Refusal
		switch {
		case c.ok && err != nil:
			t.Errorf("%q refused: %v", c.out, err)
		case !c.ok && (!errors.As(err, &r) || r.Code != "cli_version_mismatch"):
			t.Errorf("%q accepted as %q (%v)", c.out, v, err)
		}
	}
}

// TestCLICancelKillsTheGroup: canceling a CLI command kills its children
// too, and returns promptly, for commands and for --version (OR-18, OR-30).
func TestCLICancelKillsTheGroup(t *testing.T) {
	for _, version := range []bool{false, true} {
		dir := t.TempDir()
		pidFile := filepath.Join(dir, "child.pid")
		script := "/bin/sleep 30 & echo $! > " + pidFile + "; wait"
		cli := CLI{Command: []string{"/bin/sh", "-c", script}, Home: dir}
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		start := time.Now()
		var err error
		if version {
			_, err = cli.Version(ctx)
		} else {
			err = cli.run(ctx, dir, cli.Command[1:])
		}
		cancel()
		if elapsed := time.Since(start); elapsed > 3*time.Second || err == nil {
			t.Fatalf("version %v: returned after %s with %v", version, elapsed, err)
		}
		data, err := os.ReadFile(pidFile)
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for processAlive(pid) {
			if time.Now().After(deadline) {
				t.Fatalf("version %v: child %d survived the cancellation", version, pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func processAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }
