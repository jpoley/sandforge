// Package config holds Sandforge's embedded defaults and the optional sandforge.yaml overrides.
// Principle (docs/goal.md): one command, zero required tinkering — every value has a sensible default;
// config is optional and most users never touch it.
package config

import (
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the full, resolved Sandforge configuration. Defaults() fills every field; an optional
// sandforge.yaml and SANDFORGE_* env vars override.
type Config struct {
	Project  string `yaml:"project"`   // compose project name / instance id
	HTTPPort int    `yaml:"http_port"` // fixed loopback port for the forge (design §9)
	Network  string `yaml:"network"`   // docker network name (job containers attach here)

	Admin struct {
		User     string `yaml:"user"`
		Password string `yaml:"password"`
		Email    string `yaml:"email"`
		Org      string `yaml:"org"` // owner namespace for imported repos
	} `yaml:"admin"`

	Images struct {
		Forgejo  string `yaml:"forgejo"`
		Runner   string `yaml:"runner"`
		Postgres string `yaml:"postgres"`
		LB       string `yaml:"lb"`
		CI       string `yaml:"ci"` // job image act_runner launches for ubuntu-latest
	} `yaml:"images"`

	History string `yaml:"history"` // squash|rebase|merge (design §7)

	Ephemeral bool `yaml:"ephemeral"` // anonymous volumes, nuked on down (design §10)

	// RunnerMode is HOW the warm act_runner reaches the Docker daemon that runs CI jobs. It is NOT
	// Docker-in-Docker (design §5.1 forbids DinD): both modes share the ONE daemon the user's own
	// `docker` CLI already talks to (so `docker compose` and the runner land on the same daemon and
	// the host image cache is reused), differing only in how the runner connects to it:
	//   socket — bind-mount the (auto-detected) host unix socket. Standard/Desktop/rootless/Colima.
	//   tcp    — dial DOCKER_HOST over TCP; nothing is mounted, no gid/group_add. For remote or
	//            TCP daemons where there is no local socket to mount (many locked-down setups).
	//   auto   — at init, inspect the effective DOCKER_HOST/context: unix→socket, tcp→tcp.
	// Default auto so "one command" works regardless of the host's docker posture; the resolved mode
	// is logged (Log.Decision) and persisted — never a silent guess (docs/premortem resilience rule).
	RunnerMode string `yaml:"runner_mode"` // socket|tcp|auto

	// Derived / runtime (not usually in yaml):
	DockerGID    string `yaml:"docker_gid"`    // gid of the docker socket as a container sees it (detected; socket mode)
	DockerSocket string `yaml:"docker_socket"` // host path to the docker socket (default /var/run/docker.sock)
	// RunnerDockerHost is the DOCKER_HOST string handed to the act_runner in tcp mode — the user's
	// TCP endpoint with a loopback host rewritten to host.docker.internal so it is reachable from
	// INSIDE the runner container. Empty in socket mode. Persisted so non-init commands reuse it.
	RunnerDockerHost string `yaml:"-"`
	RunnerVer        string `yaml:"-"`
	StateDir         string `yaml:"-"` // ~/.sandforge/<project>
	RepoRoot         string `yaml:"-"` // sandforge source root (for embedded deploy assets)
	// ForgeInternalURL, when set (SANDFORGE_FORGE_INTERNAL_URL), is the forge base URL as seen from
	// INSIDE the compose network — e.g. http://forgejo:3000. The containerized agents router uses it
	// instead of the loopback CloneURL (127.0.0.1:port) for API + clone/push, since the container
	// can't reach the host's loopback. Empty on the host (the default), where loopback is correct.
	ForgeInternalURL string `yaml:"-"`
}

// RepoBase returns the scheme://host:port base used to build clone/push URLs: the in-network forge
// address when running containerized (ForgeInternalURL set), else the loopback CloneURL.
func (c *Config) RepoBase() string {
	if c.ForgeInternalURL != "" {
		return strings.TrimRight(c.ForgeInternalURL, "/")
	}
	return c.CloneURL()
}

// DockerSocketOrDefault returns the configured docker socket path or the conventional default.
func (c *Config) DockerSocketOrDefault() string {
	if c.DockerSocket != "" {
		return c.DockerSocket
	}
	return "/var/run/docker.sock"
}

// CILabel is the act_runner label mapping ubuntu-latest -> our CI image.
func (c *Config) CILabel() string {
	return "ubuntu-latest:docker://" + c.Images.CI
}

// CloneURL is the stable loopback clone URL (design §9).
func (c *Config) CloneURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", c.HTTPPort)
}

// Defaults returns the embedded opinionated defaults (design §13).
func Defaults() *Config {
	c := &Config{
		Project:    "sandforge",
		HTTPPort:   3000,
		Network:    "sandforge-net",
		History:    "squash",
		RunnerMode: "auto",
	}
	c.Admin.User = "sandforge"
	// Password intentionally empty: init generates a random one per instance (or reuses the one
	// already persisted in ~/.sandforge/<project>/credentials). A yaml/env value still overrides.
	c.Admin.Password = ""
	c.Admin.Email = "admin@sandforge.local"
	c.Admin.Org = "sandforge"
	c.Images.Forgejo = "codeberg.org/forgejo/forgejo:11"
	c.Images.Runner = "code.forgejo.org/forgejo/runner:11.0.0"
	c.Images.Postgres = "postgres:16-alpine"
	c.Images.LB = "caddy:2-alpine"
	c.Images.CI = "sandforge/ci:ubuntu-22.04"
	c.RunnerVer = "11.0.0"
	return c
}

// Load resolves config: defaults <- sandforge.yaml (if present in cwd or repoRoot) <- env.
func Load(repoRoot string) (*Config, error) {
	c := Defaults()
	c.RepoRoot = repoRoot
	for _, p := range []string{"sandforge.yaml", filepath.Join(repoRoot, "sandforge.yaml")} {
		if b, err := os.ReadFile(p); err == nil {
			if err := yaml.Unmarshal(b, c); err != nil {
				return nil, fmt.Errorf("parse %s: %w", p, err)
			}
			break
		}
	}
	// env overrides (the "CAN be changed" knobs)
	if v := os.Getenv("SANDFORGE_PROJECT"); v != "" {
		c.Project = v
	}
	if v := os.Getenv("SANDFORGE_HTTP_PORT"); v != "" {
		fmt.Sscanf(v, "%d", &c.HTTPPort)
	}
	if v := os.Getenv("SANDFORGE_CI_IMAGE"); v != "" {
		c.Images.CI = v
	}
	if v := os.Getenv("SANDFORGE_NETWORK"); v != "" {
		c.Network = v
	}
	if os.Getenv("SANDFORGE_EPHEMERAL") == "1" {
		c.Ephemeral = true
	}
	if v := os.Getenv("SANDFORGE_DOCKER_SOCKET"); v != "" {
		c.DockerSocket = v
	}
	if v := os.Getenv("SANDFORGE_RUNNER_MODE"); v != "" {
		c.RunnerMode = v
	}
	if v := os.Getenv("SANDFORGE_ADMIN_PASSWORD"); v != "" {
		c.Admin.Password = v
	}
	if v := os.Getenv("SANDFORGE_FORGE_INTERNAL_URL"); v != "" {
		c.ForgeInternalURL = v
	}
	if c.DockerSocket == "" {
		c.DockerSocket = detectDockerSocket()
	}
	// network defaults off project name if customized
	if c.Network == "sandforge-net" && c.Project != "sandforge" {
		c.Network = c.Project + "-net"
	}
	// DockerGID is detected lazily (DetectDockerGID) right before the runner starts — it requires
	// docker and the only correct vantage point is INSIDE a container, so it cannot be a cheap
	// host stat at config-load time. Left empty here on purpose.
	home, _ := os.UserHomeDir()
	c.StateDir = filepath.Join(home, ".sandforge", c.Project)
	return c, nil
}

// detectDockerSocket finds the host docker socket without assuming the default path, so rootless
// Docker (socket under $XDG_RUNTIME_DIR) and custom DOCKER_HOST setups work without manual config.
// Order: $DOCKER_HOST (unix:// only) → /var/run/docker.sock if present → $XDG_RUNTIME_DIR/docker.sock
// → conventional default (so behavior is unchanged on a standard host).
func detectDockerSocket() string {
	if dh := os.Getenv("DOCKER_HOST"); strings.HasPrefix(dh, "unix://") {
		return strings.TrimPrefix(dh, "unix://")
	}
	const def = "/var/run/docker.sock"
	if _, err := os.Stat(def); err == nil {
		return def
	}
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		if p := filepath.Join(xdg, "docker.sock"); fileExists(p) {
			return p
		}
	}
	return def
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// GeneratePassword returns a crypto/rand password of n characters over an unambiguous base62-ish
// alphabet (no 0/O/l/1/I). Used at init when no admin password is configured, so every instance
// gets its own secret instead of a shared hardcoded default. Fails loud on entropy errors — never
// a guessed/constant fallback (resilience rule).
func GeneratePassword(n int) (string, error) {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate admin password: %w", err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b), nil
}

// effectiveDockerHost returns the Docker endpoint the user's own `docker` CLI resolves to: the
// $DOCKER_HOST env if set, else the active docker context's endpoint, else "" (caller treats empty
// as the conventional local unix socket). This is the single source of truth so `docker compose`
// and the act_runner always target the SAME daemon — the reason we never need Docker-in-Docker.
func effectiveDockerHost() string {
	if dh := strings.TrimSpace(os.Getenv("DOCKER_HOST")); dh != "" {
		return dh
	}
	// No env: ask the CLI what the active context points at (e.g. `docker context use remote` sets a
	// tcp endpoint without exporting DOCKER_HOST). Best-effort — empty on any error → local socket.
	out, err := exec.Command("docker", "context", "inspect", "-f", "{{.Endpoints.docker.Host}}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ResolveRunnerMode decides how the act_runner connects to Docker, honoring an explicit mode
// (socket|tcp) or auto-detecting from the effective DOCKER_HOST/context. It returns the resolved
// mode plus, for tcp mode, the DOCKER_HOST string to hand the runner (loopback rewritten to
// host.docker.internal so a TCP daemon on the developer's machine is reachable from inside the
// runner container). It performs NO docker daemon calls beyond `docker context inspect`, so it is
// cheap and deterministic; the socket-gid probe stays separate (socket mode only).
func ResolveRunnerMode(explicit string) (mode, runnerDockerHost string, err error) {
	endpoint := ""
	switch explicit {
	case "socket":
		return "socket", "", nil
	case "tcp":
		endpoint = effectiveDockerHost()
		if !strings.HasPrefix(endpoint, "tcp://") {
			return "", "", fmt.Errorf("runner_mode=tcp but no tcp DOCKER_HOST is set (effective endpoint %q); "+
				"export DOCKER_HOST=tcp://host:port or use runner_mode=auto", endpointOrLocal(endpoint))
		}
		return "tcp", rewriteLoopbackHost(endpoint), nil
	case "", "auto":
		endpoint = effectiveDockerHost()
		// auto: classify the effective endpoint.
		switch {
		case strings.HasPrefix(endpoint, "tcp://"):
			return "tcp", rewriteLoopbackHost(endpoint), nil
		case strings.HasPrefix(endpoint, "unix://"), endpoint == "":
			return "socket", "", nil
		case strings.HasPrefix(endpoint, "ssh://"):
			// ssh:// needs an ssh agent inside the runner — out of scope here. Fail loud with the fix.
			return "", "", fmt.Errorf("DOCKER_HOST=%s (ssh) is not supported for the runner; expose the daemon over "+
				"tcp:// (then set DOCKER_HOST) or use a local unix socket", endpoint)
		default:
			return "", "", fmt.Errorf("unrecognized DOCKER_HOST scheme %q; expected unix://, tcp:// or ssh://", endpoint)
		}
	default:
		return "", "", fmt.Errorf("invalid runner_mode %q (want socket|tcp|auto)", explicit)
	}
}

func endpointOrLocal(e string) string {
	if e == "" {
		return "unix:///var/run/docker.sock"
	}
	return e
}

// rewriteLoopbackHost rewrites a tcp:// URL whose host is loopback (localhost/127.0.0.1/::1/0.0.0.0)
// to host.docker.internal, which resolves to the host from inside a container (Docker Desktop
// natively; Linux via the extra_hosts:host-gateway mapping the tcp overlay adds). A non-loopback
// host (a real remote daemon) is returned unchanged.
func rewriteLoopbackHost(dockerHost string) string {
	rest := strings.TrimPrefix(dockerHost, "tcp://")
	host, port := rest, ""

	if strings.HasPrefix(rest, "[") {
		// Bracketed IPv6, optionally with port ("[::1]:2375").
		if i := strings.LastIndex(rest, "]:"); i >= 0 {
			host, port = rest[:i+1], rest[i+2:]
		} else {
			host = rest
		}
	} else if strings.Count(rest, ":") > 1 {
		// Likely unbracketed IPv6 without port; don't split on ':'.
		host = rest
	} else if i := strings.LastIndex(rest, ":"); i >= 0 {
		host, port = rest[:i], rest[i+1:]
	}

	switch host {
	case "localhost", "127.0.0.1", "::1", "[::1]", "0.0.0.0":
		host = "host.docker.internal"
	default:
		return dockerHost
	}
	if port != "" {
		return "tcp://" + host + ":" + port
	}
	return "tcp://" + host
}

// DetectDockerGID returns the gid that owns the docker socket *as a container sees it* — the only
// vantage point that matters, because the runner accesses the bind-mounted socket from inside a
// container, not from the host.
//
// Why not host `stat`: (1) the host symlink/path GID is meaningless to the container; Docker
// Desktop (macOS/Windows) proxies the socket so it appears as gid 0 in-container while the host
// path is something else entirely. (2) macOS `stat` has no `-c` flag (that is GNU/Linux syntax),
// so the old host probe silently errored and fell back to a hardcoded 1001 — the exact silent
// default that caused the runner to crash-loop on "permission denied".
//
// It probes with the runner image itself (already needed, so no extra pull) via `--entrypoint
// stat`. On any failure it returns a LOUD error rather than guessing a default.
func DetectDockerGID(runnerImage, socketPath string) (string, error) {
	if socketPath == "" {
		socketPath = "/var/run/docker.sock"
	}
	// Validate the host path BEFORE bind-mounting it: a missing or non-socket path would otherwise
	// get mounted as a directory and yield a misleading gid. Fail loud with the remedy instead.
	fi, err := os.Stat(socketPath)
	if err != nil {
		return "", fmt.Errorf("docker socket %q not found: %w (set SANDFORGE_DOCKER_SOCKET to the right path)", socketPath, err)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return "", fmt.Errorf("docker socket %q is not a unix socket (mode %s) — set SANDFORGE_DOCKER_SOCKET", socketPath, fi.Mode())
	}
	cmd := exec.Command("docker", "run", "--rm",
		"-v", socketPath+":/var/run/docker.sock",
		"--entrypoint", "stat", runnerImage,
		"-c", "%g", "/var/run/docker.sock")
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("detect docker socket gid (in-container probe with %s failed): %w\n%s",
			runnerImage, err, strings.TrimSpace(buf.String()))
	}
	// stat prints the gid on its own line; tolerate any leading image-pull chatter.
	gid := ""
	for _, ln := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		ln = strings.TrimSpace(ln)
		if isAllDigits(ln) {
			gid = ln
		}
	}
	if gid == "" {
		return "", fmt.Errorf("detect docker socket gid: no numeric gid in probe output:\n%s",
			strings.TrimSpace(buf.String()))
	}
	return gid, nil
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ComposeEnv returns the environment passed to `docker compose` so the embedded compose file
// interpolates correctly.
func (c *Config) ComposeEnv() []string {
	env := os.Environ()
	add := func(k, v string) { env = append(env, k+"="+v) }
	add("SANDFORGE_PROJECT", c.Project)
	add("SANDFORGE_NETWORK", c.Network)
	add("SANDFORGE_HTTP_PORT", fmt.Sprintf("%d", c.HTTPPort))
	// The compose file REQUIRES this (${SANDFORGE_DOCKER_GID:?}) so a direct `docker compose` with
	// no value fails loud instead of guessing. init detects + persists the REAL gid before any
	// compose call, so the runner always gets the right one. For non-runner commands (down/status/
	// logs) on an instance with no persisted gid, supply "0" purely to satisfy interpolation of a
	// service we aren't even starting — it never reaches a running runner.
	gid := c.DockerGID
	if gid == "" {
		gid = "0"
	}
	add("SANDFORGE_DOCKER_GID", gid)
	add("SANDFORGE_DOCKER_SOCKET", c.DockerSocketOrDefault())
	// tcp mode: the runner dials this endpoint instead of mounting a socket. Default to the local
	// socket URL so the value is always defined for interpolation even in socket mode (where the tcp
	// overlay isn't loaded and this is inert).
	rh := c.RunnerDockerHost
	if rh == "" {
		rh = "unix:///var/run/docker.sock"
	}
	add("SANDFORGE_DOCKER_HOST", rh)
	add("SANDFORGE_RUNNER_DIR", filepath.Join(c.StateDir, "runner"))
	add("SANDFORGE_RUNNER_VERSION", c.RunnerVer)
	add("SANDFORGE_FORGEJO_IMAGE", c.Images.Forgejo)
	add("SANDFORGE_POSTGRES_IMAGE", c.Images.Postgres)
	add("SANDFORGE_LB_IMAGE", c.Images.LB)
	return env
}
