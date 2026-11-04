package main

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// threadCPU reads a process's CPU time by thread name: on Linux from
// /proc/<pid>/task/*/stat and comm, on macOS from proc_pidinfo through
// testdata/benchhost/threadcpu_darwin.c: what a JavaScript
// profile, which samples one thread, cannot show (the JIT's and the
// collector's threads, the I/O threads). Names are grouped by dropping
// trailing digits and spaces. Threads that exited between two readings are
// lost from the second; the difference counts only live ones.
func threadCPU(pid int) map[string]time.Duration {
	if pid <= 0 {
		return nil
	}
	if runtime.GOOS == "darwin" {
		out := map[string]time.Duration{}
		for _, t := range darwinThreads(pid) {
			out[threadGroup(t.name)] += t.cpu
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	dir := filepath.Join("/proc", strconv.Itoa(pid), "task")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := map[string]time.Duration{}
	for _, e := range ents {
		stat, err := os.ReadFile(filepath.Join(dir, e.Name(), "stat"))
		if err != nil {
			continue
		}
		comm, _ := os.ReadFile(filepath.Join(dir, e.Name(), "comm"))
		// The fields after the command, which is in parentheses and may hold
		// spaces: utime and stime are the 14th and 15th fields of the line.
		s := string(stat)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+1:])
		if len(f) < 13 {
			continue
		}
		ut, _ := strconv.ParseInt(f[11], 10, 64)
		st, _ := strconv.ParseInt(f[12], 10, 64)
		name := threadGroup(strings.TrimSpace(string(comm)))
		// Clock ticks, 100 per second on Linux.
		out[name] += time.Duration(ut+st) * 10 * time.Millisecond
	}
	return out
}

var trailingDigits = regexp.MustCompile(`[\s#_-]*\d+$`)

func threadGroup(comm string) string {
	g := trailingDigits.ReplaceAllString(comm, "")
	if g == "" {
		g = "thread"
	}
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '/' || r == ':' {
			return '_'
		}
		return r
	}, g)
}

// threadDelta is after minus before, by group.
func threadDelta(before, after map[string]time.Duration) map[string]time.Duration {
	d := map[string]time.Duration{}
	for k, v := range after {
		if x := v - before[k]; x > 0 {
			d[k] = x
		}
	}
	return d
}

// darwinThread is one thread of a process on macOS: its 64-bit id (the
// Thread_<id> of /usr/bin/sample), name and CPU time.
type darwinThread struct {
	id   string
	name string
	cpu  time.Duration
}

var (
	threadcpuOnce sync.Once
	threadcpuBin  string
)

// darwinThreads lists pid's threads with their CPU time, through the
// threadcpu helper, built once with the system's C compiler.
func darwinThreads(pid int) []darwinThread {
	threadcpuOnce.Do(func() {
		dir := embDir
		if dir == "" {
			dir = os.TempDir()
		}
		bin := filepath.Join(dir, "threadcpu")
		if out, err := exec.Command("cc", "-O2", "-o", bin, "testdata/benchhost/threadcpu_darwin.c").CombinedOutput(); err == nil {
			threadcpuBin = bin
		} else {
			os.Stderr.Write(out)
		}
	})
	if threadcpuBin == "" {
		return nil
	}
	out, err := exec.Command(threadcpuBin, strconv.Itoa(pid)).Output()
	if err != nil {
		return nil
	}
	var ts []darwinThread
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.SplitN(sc.Text(), " ", 4)
		if len(f) < 4 {
			continue
		}
		u, _ := strconv.ParseInt(f[1], 10, 64)
		st, _ := strconv.ParseInt(f[2], 10, 64)
		ts = append(ts, darwinThread{id: f[0], name: f[3], cpu: time.Duration(u + st)})
	}
	return ts
}
