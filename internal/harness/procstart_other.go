//go:build !linux && !darwin

package harness

import "syscall"

// processStart reports whether a process has the pid; where the engine
// knows no start time, every process reads as started at 1, so a child
// group's identity is its leader's existence only.
func processStart(pid int) (uint64, bool) {
	err := syscall.Kill(pid, 0)
	return 1, err == nil || err == syscall.EPERM
}
