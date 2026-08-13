//go:build !windows

package builder

import (
	"os/exec"
	"syscall"
)

// configureBuildCmd puts the build in its own process group so that cancelling
// it takes down the whole tree, not just the `sh -c` wrapper.
func configureBuildCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killBuildTree signals the build's entire process group. Without this a
// superseded compiler keeps running and can still be holding the output binary
// when the next build tries to write it.
func killBuildTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
