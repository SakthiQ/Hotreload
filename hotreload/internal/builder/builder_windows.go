//go:build windows

package builder

import (
	"os/exec"
	"strconv"
)

// configureBuildCmd is a no-op on Windows; the process tree is discovered by
// taskkill at cancellation time rather than being grouped up front.
func configureBuildCmd(cmd *exec.Cmd) {}

// killBuildTree terminates the build and everything it spawned.
//
// The build runs as `cmd /c <command>`, so killing only the process we started
// would leave the actual compiler running: it keeps the working directory open
// and can still be holding the output binary when the next build tries to
// write it.
func killBuildTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err != nil {
		// Fall back to Go's TerminateProcess, which at least stops the wrapper.
		return cmd.Process.Kill()
	}
	return nil
}
