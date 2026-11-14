//go:build windows

package youtube

import (
	"os/exec"
	"strconv"
)

// prepareCommand configures process tree termination for Windows.
func prepareCommand(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// taskkill /F /T terminates the process and all child processes
		killCmd := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid))
		return killCmd.Run()
	}
}
