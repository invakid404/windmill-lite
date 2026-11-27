package harness

import (
	"os/exec"
	"runtime"
	"sync"
	"syscall"
)

// startTied starts cmd so that its process ends when the engine's does,
// also while its event loop is stuck: the kernel sends it SIGKILL when its
// parent dies (prctl's parent-death signal).
//
// The kernel sends that signal when the thread that forked the process
// exits, not only when its process does, and the Go runtime ends a thread
// whose goroutine exits while locked to it, which any goroutine of the
// engine, or of the program that embeds it, may do. So no spawn runs on
// the calling goroutine's thread: each runs on the thread of one goroutine
// that locks it and never leaves it (spawner), which ends only with the
// engine's process.
func startTied(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
	spawner.once.Do(func() {
		spawner.starts = make(chan spawnStart)
		go func() {
			// Locked for good: the thread exits only with the process.
			runtime.LockOSThread()
			for s := range spawner.starts {
				s.done <- s.cmd.Start()
			}
		}()
	})
	done := make(chan error, 1)
	spawner.starts <- spawnStart{cmd: cmd, done: done}
	return <-done
}

// spawner is the goroutine whose thread forks every harness process
// (startTied). Starts are rare and short, so one thread serves them all.
var spawner struct {
	once   sync.Once
	starts chan spawnStart
}

// spawnStart asks the spawner to start cmd and to send Start's error on done.
type spawnStart struct {
	cmd  *exec.Cmd
	done chan error
}
