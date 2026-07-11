//go:build windows

package prd

import "os/exec"

// setProcGroupKill is a no-op on Windows (no Setpgid-style process groups). The context deadline
// still cancels the direct process; Sandforge's runtime target is macOS/Linux Docker hosts anyway.
func setProcGroupKill(cmd *exec.Cmd) {}
