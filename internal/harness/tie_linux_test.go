package harness

import (
	"os"
	"strconv"
	"syscall"
)

// processGone reports whether the process pid has ended: it is no longer
// listed, or it is a zombie that waits to be reaped (statOf).
func processGone(pid int) bool {
	info, ok := statOf(pid)
	return !ok || info.zombie
}

// currentThread is the calling thread's id.
func currentThread() int { return syscall.Gettid() }

// threadGone reports whether the thread tid of this process has ended.
func threadGone(tid int) bool {
	_, err := os.Stat("/proc/self/task/" + strconv.Itoa(tid))
	return os.IsNotExist(err)
}
