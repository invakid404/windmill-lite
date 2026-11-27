//go:build !linux

package harness

// The tie's tests skip on this system (tieSkip), so these are never called.

func processGone(int) bool { return false }

func currentThread() int { return 0 }

func threadGone(int) bool { return false }
