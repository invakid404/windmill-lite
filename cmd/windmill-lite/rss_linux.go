package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strconv"
)

// currentRSS is the engine's current resident set size, in bytes: the
// VmRSS line of /proc/self/status, not its peak (VmHWM).
func currentRSS() (int64, error) {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, err
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		rest, ok := bytes.CutPrefix(sc.Bytes(), []byte("VmRSS:"))
		if !ok {
			continue
		}
		fields := bytes.Fields(rest)
		if len(fields) != 2 || string(fields[1]) != "kB" {
			return 0, fmt.Errorf("/proc/self/status: a VmRSS line of %q", sc.Text())
		}
		kib, err := strconv.ParseInt(string(fields[0]), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("/proc/self/status: VmRSS: %w", err)
		}
		return kib * 1024, nil
	}
	return 0, fmt.Errorf("/proc/self/status: no VmRSS line")
}
