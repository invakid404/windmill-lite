package harness

import (
	"bytes"
	"os"
	"strconv"
	"strings"
)

// groupMembersOf returns the members of each process group of pgids, a
// zombie included, with their start times in clock ticks since boot: one
// pass over /proc, reading each process's group and start time
// (statOf).
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
		info, ok := statOf(pid)
		if !ok || !want[info.pgid] {
			continue // it exited meanwhile, or is in another group
		}
		out[info.pgid] = append(out[info.pgid], groupMember{pid: pid, start: info.start})
	}
	return out
}

// procInfoOf reads what reaping needs of the process pid (statOf).
func procInfoOf(pid int) (procInfo, bool) { return statOf(pid) }

// statOf reads the process pid's state, parent, group and start time
// (fields 3, 4, 5 and 22 of /proc/<pid>/stat).
func statOf(pid int) (procInfo, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procInfo{}, false
	}
	// The command name, in parentheses, may hold spaces and parentheses:
	// the fields after it start at the last ')', with the state, field 3.
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return procInfo{}, false
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 20 {
		return procInfo{}, false
	}
	ppid, err1 := strconv.Atoi(f[4-3])
	pgid, err2 := strconv.Atoi(f[5-3])
	start, err3 := strconv.ParseUint(f[22-3], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return procInfo{}, false
	}
	return procInfo{start: start, ppid: ppid, pgid: pgid, zombie: f[0] == "Z"}, true
}

// processHasGuard reports whether the process pid is the guardian of the
// token: an argument of its is guardArg(token) (/proc/<pid>/cmdline).
func processHasGuard(pid int, token string) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	return err == nil && bytes.Contains(append([]byte{0}, b...), []byte("\x00"+guardArg(token)+"\x00"))
}
