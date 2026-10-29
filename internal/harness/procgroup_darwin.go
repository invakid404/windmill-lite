package harness

import (
	"bytes"
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

// groupMembersOf returns the members of each process group of pgids, a
// zombie included, with their start times in microseconds since the epoch
// (the kernel's p_starttime), from kern.proc.pgrp.
func groupMembersOf(pgids []int) map[int][]groupMember {
	out := map[int][]groupMember{}
	for _, g := range pgids {
		var ks []unix.KinfoProc
		var err error
		// The group may grow between the size query and the read.
		for range 3 {
			if ks, err = unix.SysctlKinfoProcSlice("kern.proc.pgrp", g); !errors.Is(err, syscall.ENOMEM) {
				break
			}
		}
		if err != nil {
			continue
		}
		for _, k := range ks {
			if int(k.Eproc.Pgid) != g {
				continue
			}
			t := k.Proc.P_starttime
			out[g] = append(out[g], groupMember{pid: int(k.Proc.P_pid), start: uint64(t.Sec)*1_000_000 + uint64(t.Usec)})
		}
	}
	return out
}

// processHasGuard reports whether the process pid is the guardian of the
// token: an argument of its is guardArg(token), from kern.procargs2, which
// gives a process of the engine's user its executable's path and its
// arguments, each ending in a NUL.
func processHasGuard(pid int, token string) bool {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	return err == nil && bytes.Contains(b, []byte("\x00"+guardArg(token)+"\x00"))
}
