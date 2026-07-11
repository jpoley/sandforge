package app

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jpoley/sandforge/internal/compose"
)

// The agents router can also run as a CONTAINER joined to the control-plane network — the
// "completely local in container" path. Unlike the host daemon, the build context needs the Go
// source, so this is available from a source checkout (or `mage`), not the standalone embedded
// binary (which uses `agents start`). `agents up` builds + runs it; `agents down` tears it down.

func (a *App) agentsComposeFile() string {
	return filepath.Join(a.Cfg.RepoRoot, "deploy", "agents-router", "agents.compose.yml")
}

// agentsComposeEnv is the focused env the agents compose file interpolates.
func (a *App) agentsComposeEnv(port int, repo string) []string {
	if port == 0 {
		port = defaultAgentsPort
	}
	env := os.Environ()
	env = append(env,
		"SANDFORGE_PROJECT="+a.Cfg.Project,
		"SANDFORGE_NETWORK="+a.Cfg.Network,
		fmt.Sprintf("SANDFORGE_AGENTS_PORT=%d", port),
		"SANDFORGE_STATE_DIR="+a.Cfg.StateDir,
		"SANDFORGE_AGENTS_REPO="+repo,
	)
	return env
}

// AgentsUp builds and starts the containerized router on the control-plane network. The container's
// own `agents serve` registers the webhook (target http://agents:3999/webhook, in-network).
func (a *App) AgentsUp(port int, repo string) error {
	if a.Client == nil {
		return fmt.Errorf("not initialized — run `sandforge init` first")
	}
	if err := compose.MustDocker(); err != nil {
		return err
	}
	// The image builds from the Go module; the standalone embedded binary has no source tree.
	if _, err := os.Stat(filepath.Join(a.Cfg.RepoRoot, "go.mod")); err != nil {
		return fmt.Errorf("containerized router needs the sandforge source (go.mod not found at %s).\n"+
			"  run it from a source checkout, or use the host daemon instead: sandforge agents start", a.Cfg.RepoRoot)
	}
	if port == 0 {
		port = defaultAgentsPort
	}
	file := a.agentsComposeFile()
	env := a.agentsComposeEnv(port, repo)
	a.Log.Step("agents up: building the router image (one-time; cached after)")
	if err := compose.ComposeStream(env, file, "build"); err != nil {
		return fmt.Errorf("build agents image: %w", err)
	}
	a.Log.Step("agents up: starting the router container on network %s", a.Cfg.Network)
	if out, err := compose.Compose(env, file, "up", "-d"); err != nil {
		return fmt.Errorf("agents up: %w\n%s", err, out)
	}
	// Wait until the container's UI answers on the published loopback port — never report "up" for a
	// container that died on launch (resilience rule); dump its logs if it doesn't come ready.
	if err := a.waitDaemonReady(port, 30*time.Second); err != nil {
		logs, _ := compose.Compose(env, file, "logs", "--no-color", "--tail", "40", "agents")
		return fmt.Errorf("agents container did not come up on :%d: %w\n  --- agents logs ---\n%s", port, err, indent(logs, "    "))
	}
	a.Log.Decision("D-AGENTS-CONTAINER", "Agents router running as a container",
		fmt.Sprintf("on %s; webhook container-to-container; UI :%d", a.Cfg.Network, port), "docs/goal.md")
	a.Log.Infof("\n  Agents router is up (containerized).\n    UI:       http://127.0.0.1:%d\n    webhook:  http://agents:3999/webhook (in-network)\n    logs:     sandforge logs (or docker compose -f %s logs -f)\n    stop:     sandforge agents down\n",
		port, file)
	return nil
}

// AgentsDown tears down the containerized router stack.
func (a *App) AgentsDown() error {
	file := a.agentsComposeFile()
	if _, err := os.Stat(file); err != nil {
		a.Log.Infof("  no agents compose file (%s) — nothing to tear down", file)
		return nil
	}
	a.Log.Step("agents down: stopping the router container")
	out, err := compose.Compose(a.agentsComposeEnv(0, ""), file, "down")
	if err != nil {
		return fmt.Errorf("agents down: %w\n%s", err, out)
	}
	return nil
}

// agentsContainerDown is the best-effort teardown the control-plane `down` calls so removing the
// control-plane network doesn't orphan the agents container. Silent if the stack isn't present.
func (a *App) agentsContainerDown() {
	file := a.agentsComposeFile()
	if _, err := os.Stat(file); err != nil {
		return
	}
	compose.Compose(a.agentsComposeEnv(0, ""), file, "down")
}
