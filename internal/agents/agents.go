// Package agents is Sandforge's local "Copilot": it subscribes to Forgejo webhook events,
// routes @mention triggers (e.g. @claude / @codex) to configured coding agents, invokes the
// review/handoff, and persists everything to files under the instance state dir. A small embedded
// web UI views the timeline and creates/edits agents. It is a host process (it needs host git,
// the bare clones, and the host agent CLIs) that the forge containers reach via host.docker.internal.
//
// Design note: docs/goal.md lists "agent orchestration" as a non-goal of the core inner-loop
// forge. This package is a deliberately opt-in, separable feature (only the new `sandforge agents`
// command touches it) layered on top — it does not change the e2e contract.
package agents

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// AgentMarker is appended (hidden) to every comment an agent posts back. The router ignores any
// inbound comment carrying it, which breaks the feedback loop (agent comment → new issue_comment
// event → re-match) regardless of which user the bot posts as. This is the GitHub-bot convention.
const AgentMarker = "<!-- sandforge-agent -->"

// Agent is one routable coding agent. Handle is the mention token without '@' (case-insensitive).
// Command is the argv invoked on a trigger; it runs with cwd = a fresh clone of the repo at the
// PR head (or default branch) and an env carrying the event context (see app dispatcher).
type Agent struct {
	Handle  string   `json:"handle"`
	Name    string   `json:"name"`
	Command []string `json:"command"`
	Kind    string   `json:"kind"`    // "review" | "implement" (informational)
	OnOpen  bool     `json:"on_open"` // also fire on pull_request opened/synchronize
	Enabled bool     `json:"enabled"`
}

// Config is the persisted router configuration (agents + the webhook secret + the loop-guard
// bot login). Stored at <state>/agents/config.json (0600) — it holds the HMAC secret.
type Config struct {
	Secret   string  `json:"secret"`    // webhook HMAC secret (generated on first init)
	BotLogin string  `json:"bot_login"` // comments whose sender is this login are ignored (optional)
	Agents   []Agent `json:"agents"`

	mu   sync.Mutex
	path string
}

// FindAgent returns the enabled agent for a handle (case-insensitive), or nil.
func (c *Config) FindAgent(handle string) *Agent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.findLocked(handle)
}

func (c *Config) findLocked(handle string) *Agent {
	h := strings.ToLower(strings.TrimPrefix(handle, "@"))
	for i := range c.Agents {
		if strings.ToLower(c.Agents[i].Handle) == h && c.Agents[i].Enabled {
			a := c.Agents[i]
			return &a
		}
	}
	return nil
}

// Snapshot returns a copy of the configured agents (for the UI / list command).
func (c *Config) Snapshot() []Agent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Agent, len(c.Agents))
	copy(out, c.Agents)
	return out
}

// Upsert adds or replaces an agent by handle and persists. Validates at the boundary.
func (c *Config) Upsert(a Agent) error {
	a.Handle = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(a.Handle), "@"))
	if a.Handle == "" {
		return fmt.Errorf("agent handle is required")
	}
	if len(a.Command) == 0 {
		return fmt.Errorf("agent %q needs a command to invoke (the trigger has nothing to run)", a.Handle)
	}
	if a.Name == "" {
		a.Name = a.Handle
	}
	if a.Kind == "" {
		a.Kind = "review"
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	replaced := false
	for i := range c.Agents {
		if strings.EqualFold(c.Agents[i].Handle, a.Handle) {
			c.Agents[i] = a
			replaced = true
			break
		}
	}
	if !replaced {
		c.Agents = append(c.Agents, a)
	}
	return c.saveLocked()
}

// Remove deletes an agent by handle and persists.
func (c *Config) Remove(handle string) error {
	h := strings.ToLower(strings.TrimPrefix(handle, "@"))
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.Agents[:0]
	found := false
	for _, a := range c.Agents {
		if strings.EqualFold(a.Handle, h) {
			found = true
			continue
		}
		out = append(out, a)
	}
	c.Agents = out
	if !found {
		return fmt.Errorf("no agent with handle %q", h)
	}
	return c.saveLocked()
}

func (c *Config) saveLocked() error {
	if c.path == "" {
		return fmt.Errorf("config has no path (load via LoadConfig)")
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.path, b, 0o600)
}

// LoadConfig reads <dir>/agents/config.json, creating a default (generated secret + a seed
// echo-agent for each common handle) on first use. The seed commands are safe no-ops (`echo`) so
// the loop works out of the box; users point Command at a real CLI (claude/codex) via the UI.
func LoadConfig(stateDir string) (*Config, error) {
	path := filepath.Join(stateDir, "agents", "config.json")
	c := &Config{path: path}
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, c); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		c.path = path
		return c, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	// first run: generate secret + seed agents
	secret, err := randHex(24)
	if err != nil {
		return nil, err
	}
	c.Secret = secret
	c.Agents = []Agent{
		seedAgent("claude", "Claude", "review"),
		seedAgent("codex", "Codex", "implement"),
	}
	if err := c.saveLocked(); err != nil {
		return nil, err
	}
	return c, nil
}

// seedAgent returns a working out-of-the-box agent that echoes the request back as a review
// comment. It proves the whole path end-to-end with no external CLI installed; replace Command
// with the real agent invocation (e.g. ["claude","-p","..."] or ["codex","exec"]) to go live.
func seedAgent(handle, name, kind string) Agent {
	return Agent{
		Handle: handle, Name: name, Kind: kind, Enabled: true,
		Command: []string{"sh", "-c",
			`printf '**%s** (local %s agent) acting on: %s\n\n> %s\n' "$SANDFORGE_HANDLE" "$SANDFORGE_KIND" "$SANDFORGE_REPO#$SANDFORGE_ISSUE" "$SANDFORGE_COMMENT"`},
	}
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate webhook secret: %w", err)
	}
	return hex.EncodeToString(b), nil
}
