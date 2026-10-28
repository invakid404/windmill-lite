//go:build !unix

package bundle

import "os/exec"

// ownProcessGroup leaves cmd as it is: only Unix systems are supported
// build hosts, and elsewhere a cancellation kills the helper alone.
func ownProcessGroup(*exec.Cmd) {}

func endProcessGroup(*exec.Cmd) error { return nil }
