//go:build !linux && !darwin

package oracle

import (
	"os/exec"
	"time"
)

// ownProcessGroup bounds the wait for a canceled command's pipes; process
// groups are used on Linux and macOS only.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.WaitDelay = 5 * time.Second
}
