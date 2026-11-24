//go:build unix

package main

import "syscall"

// privateUmask sets the process's umask to 0077 and returns the umask that
// it replaced.
func privateUmask() (uint32, error) {
	return uint32(syscall.Umask(0o077)), nil
}
