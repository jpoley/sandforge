//go:build !windows

package prd

import (
	"os/exec"
	"syscall"
)

// setProcGroupKill runs cmd in its own process group and, when the command's context is cancelled
// (timeout), SIGKILLs the whole group so child processes (curl/sleep) can't outlive the killed
// shell and hang the CLI on an open output pipe.
func setProcGroupKill(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// ESRCH = the group already exited; that's success, not a failure. Returning it would
		// replace the real deadline/cancellation outcome with a confusing "no such process" error.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			return err
		}
		return nil
	}
}
