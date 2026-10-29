//go:build !linux

package harness

// parentTarget is the watchdog's target in host mode where the system has
// no pidfd: kill(2) on the handshake's pid, which the watchdog sends only
// while getppid() still names it (ADR 0004 host mode item 4). The host
// could exit and its pid be taken in the moment between that check and
// the signal; Linux closes that gap with a pidfd.
func parentTarget(pid int, _ func() int) hostTarget {
	return killTarget(pid)
}
