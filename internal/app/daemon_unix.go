//go:build !windows

package app

import (
	"os/exec"
	"syscall"
)

// detachProc makes cmd a session leader so the spawned agents daemon survives the parent CLI
// exiting / the terminal closing (the "always-on" requirement) — it is not killed by the parent's
// process-group teardown or a controlling-terminal SIGHUP.
func detachProc(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// processAlive reports whether a pid names a live process (signal 0 probes without delivering).
// EPERM means it exists but we can't signal it — still alive.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// terminateProcess asks a pid to shut down gracefully (SIGTERM; the daemon traps it for clean exit).
func terminateProcess(pid int) error {
	if pid <= 0 {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}
