//go:build unix

package bundle

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// ownProcessGroup starts cmd in a process group of its own, which a
// cancellation kills as a whole.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}

// endProcessGroup kills what is left of cmd's process group after cmd has
// exited (a lifecycle script's background process, or the tree a
// cancellation signalled), and waits until no process of it remains. The
// group is signalled only while it still has members, so that its number,
// once free, is never signalled again.
func endProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pgid := cmd.Process.Pid
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		}
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		if time.Now().After(deadline) {
			return fmt.Errorf("processes of the helper's group %d did not exit", pgid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
