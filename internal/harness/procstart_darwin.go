package harness

import "golang.org/x/sys/unix"

// processStart returns when the process pid started, in microseconds since
// the epoch (the kernel's p_starttime), and whether a process, a zombie
// included, has the pid.
func processStart(pid int) (uint64, bool) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(k.Proc.P_pid) != pid {
		return 0, false
	}
	t := k.Proc.P_starttime
	return uint64(t.Sec)*1_000_000 + uint64(t.Usec), true
}
