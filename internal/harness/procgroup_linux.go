package harness

import (
	"bytes"
	"os"
	"strconv"
	"strings"
)

// groupMembersOf returns the members of each process group of pgids, a
// zombie included, with their start times in clock ticks since boot: one
// pass over /proc, reading each process's group (field 5 of
// /proc/<pid>/stat) and start time (field 22).
func groupMembersOf(pgids []int) map[int][]groupMember {
	want := map[int]bool{}
	for _, g := range pgids {
		want[g] = true
	}
	out := map[int][]groupMember{}
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return out
	}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue // it exited meanwhile
		}
		// The command name, in parentheses, may hold spaces and
		// parentheses: the fields after it start at the last ')', with
		// the state, field 3.
		i := bytes.LastIndexByte(b, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(string(b[i+1:]))
		if len(f) < 20 {
			continue
		}
		pgrp, err1 := strconv.Atoi(f[5-3])
		start, err2 := strconv.ParseUint(f[22-3], 10, 64)
		if err1 != nil || err2 != nil || !want[pgrp] {
			continue
		}
		out[pgrp] = append(out[pgrp], groupMember{pid: pid, start: start})
	}
	return out
}

// processHasGuard reports whether the process pid is the guardian of the
// token: an argument of its is guardArg(token) (/proc/<pid>/cmdline).
func processHasGuard(pid int, token string) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	return err == nil && bytes.Contains(append([]byte{0}, b...), []byte("\x00"+guardArg(token)+"\x00"))
}
