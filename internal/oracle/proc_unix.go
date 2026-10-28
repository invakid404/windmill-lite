//go:build linux || darwin

package oracle

import (
	"os/exec"
	"syscall"
	"time"
)

// ownProcessGroup starts cmd in a process group of its own and makes the
// cancellation of its context kill the whole group. A launcher such as npx
// runs the CLI as a child: killing only the launcher would leave the CLI
// running, still holding the output pipes, and block the session's
// teardown (OR-18, OR-30). WaitDelay bounds the wait for those pipes after
// the kill.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
}
