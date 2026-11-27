//go:build !linux

package harness

import "os/exec"

// startTied starts cmd as it is: only Linux gives a process a signal when
// its parent dies, so elsewhere, as on macOS, a harness process outlives
// an engine that is killed while its event loop is stuck (ADR 0004
// §Lifecycle).
func startTied(cmd *exec.Cmd) error { return cmd.Start() }
