package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The agents router can run as an always-on background daemon (a detached host process) so it
// survives the terminal closing and is brought back by `sandforge init` — emulating an always-on
// Copilot. It is a HOST process, not a compose service, because the dispatcher needs host git +
// the host agent CLIs. State: a pidfile (the running child) + an "enabled" marker (the desired
// run config) so init knows to restart it. `down` stops it but keeps the marker; an explicit
// `agents stop` clears the marker (the user turned it off).

func (a *App) daemonDir() string  { return filepath.Join(a.Cfg.StateDir, "agents") }
func (a *App) pidPath() string    { return filepath.Join(a.daemonDir(), "daemon.pid") }
func (a *App) markerPath() string { return filepath.Join(a.daemonDir(), "daemon.json") }
func (a *App) daemonLog() string  { return filepath.Join(a.daemonDir(), "daemon.log") }

// daemonMarker records the desired daemon run config (so init restarts it identically).
type daemonMarker struct {
	Port int    `json:"port"`
	Repo string `json:"repo"`
}

func (a *App) readPid() int {
	b, err := os.ReadFile(a.pidPath())
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}

func (a *App) daemonRunning() (int, bool) {
	pid := a.readPid()
	return pid, processAlive(pid)
}

func (a *App) writeMarker(m daemonMarker) error {
	if err := os.MkdirAll(a.daemonDir(), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(a.markerPath(), b, 0o600)
}

func (a *App) readMarker() (daemonMarker, bool) {
	var m daemonMarker
	b, err := os.ReadFile(a.markerPath())
	if err != nil {
		return m, false
	}
	if json.Unmarshal(b, &m) != nil {
		return m, false
	}
	return m, true
}

// AgentsStart launches the router as a detached background daemon and waits for it to answer, then
// records it as enabled so `init` brings it back. Idempotent: a no-op if already running.
func (a *App) AgentsStart(port int, repo string) error {
	if a.Client == nil {
		return fmt.Errorf("not initialized — run `sandforge init` first")
	}
	if port == 0 {
		port = defaultAgentsPort
	}
	if pid, ok := a.daemonRunning(); ok {
		p := a.daemonPort(port)
		if a.waitDaemonReady(p, 1*time.Second) == nil {
			a.Log.Infof("  agents daemon already running (pid %d) — http://127.0.0.1:%d", pid, p)
			return nil
		}
		// Stale pidfile (or reused pid): fall through and spawn a new daemon.
		_ = os.Remove(a.pidPath())
	}
	if err := os.MkdirAll(a.daemonDir(), 0o700); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate sandforge binary to spawn daemon: %w", err)
	}
	args := []string{"agents", "serve", "--port", strconv.Itoa(port)}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	logf, err := os.OpenFile(a.daemonLog(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer logf.Close()
	cmd := exec.Command(exe, args...)
	cmd.Env = os.Environ()
	cmd.Stdout = logf
	cmd.Stderr = logf
	detachProc(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn agents daemon: %w", err)
	}
	pid := cmd.Process.Pid
	// Release the child so it is reparented to init and keeps running after this CLI exits.
	_ = cmd.Process.Release()
	if err := os.WriteFile(a.pidPath(), []byte(strconv.Itoa(pid)), 0o600); err != nil {
		return err
	}
	if err := a.writeMarker(daemonMarker{Port: port, Repo: repo}); err != nil {
		return err
	}
	// Wait for it to actually answer — never report "started" for a process that died on launch.
	if err := a.waitDaemonReady(port, 8*time.Second); err != nil {
		tail := a.daemonLogTail(20)
		return fmt.Errorf("agents daemon did not come up on :%d: %w\n  --- daemon.log tail ---\n%s", port, err, tail)
	}
	a.Log.Decision("D-AGENTS-DAEMON", "Agents router started as background daemon",
		fmt.Sprintf("pid %d on :%d (survives terminal; restarted by init)", pid, port), "docs/goal.md")
	a.Log.Infof("  agents daemon started (pid %d)\n    UI:      http://127.0.0.1:%d\n    log:     %s\n    stop:    sandforge agents stop",
		pid, port, a.daemonLog())
	return nil
}

func (a *App) waitDaemonReady(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	hc := &http.Client{Timeout: 1 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/api/status", port)
	for time.Now().Before(deadline) {
		if pid := a.readPid(); pid != 0 && !processAlive(pid) {
			return fmt.Errorf("daemon process exited during startup")
		}
		if resp, err := hc.Get(url); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for /api/status")
}

// AgentsStop stops the daemon and clears the enabled marker (explicit user-off; init won't restart).
func (a *App) AgentsStop() error {
	pid, running := a.daemonRunning()
	_ = os.Remove(a.markerPath())
	if !running {
		_ = os.Remove(a.pidPath())
		a.Log.Infof("  agents daemon not running")
		return nil
	}
	if err := terminateProcess(pid); err != nil {
		return fmt.Errorf("stop agents daemon (pid %d): %w", pid, err)
	}
	_ = os.Remove(a.pidPath())
	a.Log.Infof("  agents daemon stopped (pid %d)", pid)
	return nil
}

// AgentsDaemonStatus prints whether the daemon is running and where.
func (a *App) AgentsDaemonStatus() error {
	pid, running := a.daemonRunning()
	m, hasMarker := a.readMarker()
	port := a.daemonPort(0)
	if hasMarker && m.Port != 0 {
		port = m.Port
	}
	if running {
		reach := "unreachable"
		if a.waitDaemonReady(port, 1*time.Second) == nil {
			reach = "ready"
		}
		fmt.Printf("agents daemon: running (pid %d) · http://127.0.0.1:%d · %s\n", pid, port, reach)
		if m.Repo != "" {
			fmt.Printf("  repo filter: %s\n", m.Repo)
		}
		return nil
	}
	if hasMarker {
		fmt.Printf("agents daemon: enabled but NOT running (init or `agents start` will launch it on :%d)\n", port)
	} else {
		fmt.Printf("agents daemon: stopped (run `sandforge agents start` to enable always-on routing)\n")
	}
	return nil
}

// maybeStartDaemon is called at the end of init: if the user previously enabled the daemon (marker
// present) and it isn't running, bring it back — the "always-on, restarted by init" behavior.
func (a *App) maybeStartDaemon() {
	m, ok := a.readMarker()
	if !ok {
		return
	}
	if _, running := a.daemonRunning(); running {
		return
	}
	a.Log.Step("init: restarting enabled agents daemon")
	if err := a.AgentsStart(m.Port, m.Repo); err != nil {
		// Best-effort: a daemon hiccup must not fail the control-plane bootstrap. Surface it loud.
		a.Log.Event("agents", "daemon restart skipped", map[string]any{"reason": err.Error()})
	}
}

// stopDaemonKeepMarker stops a running daemon but leaves the enabled marker so the next init brings
// it back. Used by `down` (teardown shouldn't permanently disable the user's configured daemon).
func (a *App) stopDaemonKeepMarker() {
	pid, running := a.daemonRunning()
	if running {
		_ = terminateProcess(pid)
		a.Log.Event("agents", "daemon stopped (teardown)", map[string]any{"pid": pid})
	}
	_ = os.Remove(a.pidPath())
}

func (a *App) daemonPort(fallback int) int {
	if m, ok := a.readMarker(); ok && m.Port != 0 {
		return m.Port
	}
	if fallback != 0 {
		return fallback
	}
	return defaultAgentsPort
}

func (a *App) daemonLogTail(n int) string {
	b, err := os.ReadFile(a.daemonLog())
	if err != nil {
		return "(no daemon log)"
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return indent(strings.Join(lines, "\n"), "    ")
}
