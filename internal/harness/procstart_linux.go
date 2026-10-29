package harness

import (
	"bytes"
	"os"
	"strconv"
	"strings"
)

// processStart returns when the process pid started, in clock ticks since
// boot (field 22 of /proc/<pid>/stat), and whether a process, a zombie
// included, has the pid.
func processStart(pid int) (uint64, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	// The command name, in parentheses, may hold spaces and parentheses:
	// the fields after it start at the last ')', with the state, field 3.
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 20 {
		return 0, false
	}
	start, err := strconv.ParseUint(f[22-3], 10, 64)
	return start, err == nil
}
