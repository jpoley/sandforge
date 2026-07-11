//go:build windows

package app

import "os/exec"

// On Windows there is no Setsid; the daemon still detaches enough for the CLI's purpose. Sandforge's
// runtime target is macOS/Linux Docker hosts (design §13), so this path is a best-effort stub.
func detachProc(cmd *exec.Cmd) {}

func processAlive(pid int) bool {
	// Without a cheap signal-0 probe, assume a recorded pid is alive; AgentsStart re-checks via the
	// pidfile + the UI port, so a stale assumption self-corrects on next start.
	return pid > 0
}

func terminateProcess(pid int) error { return nil }
