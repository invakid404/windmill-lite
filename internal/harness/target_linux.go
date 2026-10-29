//go:build linux

package harness

import (
	"golang.org/x/sys/unix"
)

// parentTarget is the watchdog's target in host mode on Linux: a pidfd of
// the host, opened once the handshake has checked that pid is the engine's
// parent (ADR 0004 host mode item 4). A pidfd keeps naming that process
// after it exits, so the signal can never reach a process that took its
// pid later: the gap between the watchdog's getppid() check and its signal
// is closed. The pidfd is kept only when the host is still the engine's
// parent after it was opened, which shows that it names the host; without
// pidfd_open (Linux before 5.3), or otherwise, the target falls back to
// kill(2) with the watchdog's check before it.
func parentTarget(pid int, getppid func() int) hostTarget {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return killTarget(pid)
	}
	if getppid() != pid {
		unix.Close(fd)
		return killTarget(pid)
	}
	return pidfdTarget(fd)
}

// pidfdTarget signals the process a pidfd names.
type pidfdTarget int

func (t pidfdTarget) kill() error { return unix.PidfdSendSignal(int(t), unix.SIGKILL, nil, 0) }
func (t pidfdTarget) close()      { unix.Close(int(t)) }
