// Package app wires Sandforge's commands together: the control-plane bootstrap and the
// inner-loop / curation pipeline. It is a thin orchestrator over docker, git and the Forgejo API.
package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	sandforge "github.com/jpoley/sandforge"
	"github.com/jpoley/sandforge/internal/compose"
	"github.com/jpoley/sandforge/internal/config"
	"github.com/jpoley/sandforge/internal/forge"
	"github.com/jpoley/sandforge/internal/logx"
)

// App holds resolved config + dependencies for a single CLI invocation.
type App struct {
	Cfg         *config.Config
	Log         *logx.Logger
	ComposeFile string // control-plane compose path
	Client      *forge.Client
	Creds       *forge.Credentials
}

// New constructs an App, locating the repo root (where deploy/ assets live).
func New(quiet bool) (*App, error) {
	root, err := findRepoRoot()
	if err != nil {
		// No source tree on disk (the common case: a user ran the installed binary anywhere).
		// Materialize the embedded deploy assets and use them as the root — this is what makes
		// the binary standalone: users need the binary, not this repo.
		home, herr := os.UserHomeDir()
		if herr != nil {
			return nil, fmt.Errorf("locate home dir for embedded assets: %w", herr)
		}
		mat, merr := sandforge.Materialize(filepath.Join(home, ".sandforge"))
		if merr != nil {
			return nil, fmt.Errorf("could not use embedded assets (%w); also no source repo found (%v)", merr, err)
		}
		root = mat
	}
	cfg, err := config.Load(root)
	if err != nil {
		return nil, err
	}
	cfg.RepoRoot = root
	a := &App{
		Cfg: cfg,
		// Logs go under the per-instance state dir (~/.sandforge/<project>), NOT the repo root or
		// the content-addressed materialized assets dir: keeps generated logs (which can embed
		// local tokens in clone URLs) out of version control and out of the immutable asset cache.
		Log:         logx.New(cfg.StateDir, quiet),
		ComposeFile: filepath.Join(root, "deploy", "control-plane", "control-plane.compose.yml"),
	}
	if cr, err := forge.LoadCredentials(cfg.StateDir); err == nil {
		a.Creds = cr
		// In-container (ForgeInternalURL set) the loopback creds URL (127.0.0.1:port) is unreachable;
		// talk to the forge by its in-network address instead. Same token. Host path is unchanged.
		base := cr.URL
		if cfg.ForgeInternalURL != "" {
			base = cfg.RepoBase()
		}
		a.Client = forge.NewClient(base, cr.Token)
	}
	// Reuse the gid detected at init so non-init compose commands (down/status/logs) can supply the
	// REQUIRED ${SANDFORGE_DOCKER_GID} without re-probing.
	if gid := forge.LoadDockerGID(cfg.StateDir); gid != "" {
		cfg.DockerGID = gid
	}
	// Fill the runner mode from the value resolved at init, so non-init commands select the SAME
	// overlay (socket vs tcp) and endpoint the instance runs with. Precedence: an EXPLICIT
	// SANDFORGE_RUNNER_MODE / sandforge.yaml value (socket|tcp) wins and is left untouched here;
	// persisted only fills the gap when the mode is still `auto` (the unset default). Otherwise a
	// user's explicit override would be silently ignored on an already-initialized instance — exactly
	// the silent-default the resilience rule forbids. `init` with an explicit mode re-resolves and
	// re-persists, so switching modes never needs a full `reset`.
	if mode, dh := forge.LoadRunnerMode(cfg.StateDir); mode != "" && cfg.RunnerMode == "auto" {
		cfg.RunnerMode = mode
		cfg.RunnerDockerHost = dh
	}
	return a, nil
}

// cpFiles returns the control-plane compose file set: the base file plus the runner-mode overlay
// (socket|tcp). The overlay carries how act_runner reaches Docker (mounted socket vs dialed TCP).
// An unresolved/auto/legacy mode defaults to socket, matching pre-runner-mode behavior.
func (a *App) cpFiles() []string {
	mode := a.Cfg.RunnerMode
	if mode != "socket" && mode != "tcp" {
		mode = "socket"
	}
	dir := filepath.Dir(a.ComposeFile)
	return []string{a.ComposeFile, filepath.Join(dir, "control-plane."+mode+".compose.yml")}
}

// findRepoRoot walks up from cwd until it finds go.mod + deploy/control-plane.
func findRepoRoot() (string, error) {
	if v := os.Getenv("SANDFORGE_HOME"); v != "" {
		return v, nil
	}
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "deploy", "control-plane")); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not locate sandforge repo root (no go.mod + deploy/); set SANDFORGE_HOME")
		}
		dir = parent
	}
}

var hex40 = regexp.MustCompile(`\b[0-9a-f]{40}\b`)

// credInURL matches the userinfo (user:token@) of a URL so it can be redacted before logging —
// authed clone URLs embed the Forgejo token and must never reach a log file.
var credInURL = regexp.MustCompile(`://[^/@\s]+@`)

func redactCreds(s string) string { return credInURL.ReplaceAllString(s, "://***@") }

// ----- init / bootstrap (design §14) -------------------------------------------------------

// Init brings up the control-plane stack and seeds it, idempotently and resumably (re-runnable).
func (a *App) Init() error {
	if err := compose.MustDocker(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(a.Cfg.StateDir, "runner"), 0o700); err != nil {
		return err
	}

	// Resolve HOW the warm runner connects to Docker — a mounted unix socket, or a dialed TCP
	// endpoint — never Docker-in-Docker (design §5.1). auto inspects the effective DOCKER_HOST/
	// context the user's own `docker` CLI uses; an explicit runner_mode is honored. The daemon is
	// the SAME one `docker compose` targets, so the host image cache is reused either way.
	mode, runnerHost, err := config.ResolveRunnerMode(a.Cfg.RunnerMode)
	if err != nil {
		return fmt.Errorf("init: %w", err)
	}
	a.Cfg.RunnerMode = mode
	a.Cfg.RunnerDockerHost = runnerHost

	if mode == "socket" {
		// Detect the docker-socket gid FIRST — before any compose call — so the socket overlay can
		// hard-REQUIRE it (${SANDFORGE_DOCKER_GID:?}) and never silently guess 1001. Persist it so
		// later commands (down/status/logs) supply the same value without re-probing.
		a.Log.Step("init: detecting docker socket gid (in-container probe)")
		gid, gerr := config.DetectDockerGID(a.Cfg.Images.Runner, a.Cfg.DockerSocket)
		if gerr != nil {
			return fmt.Errorf("init: %w\n  the act_runner needs group access to the docker socket; without "+
				"the correct gid it crash-loops on 'permission denied'. fix docker access and retry\n"+
				"  (if your Docker is a remote/TCP daemon, set DOCKER_HOST=tcp://… or runner_mode=tcp to skip the socket entirely)", gerr)
		}
		a.Cfg.DockerGID = gid
		if err := forge.SaveDockerGID(a.Cfg.StateDir, gid); err != nil {
			return err
		}
		a.Log.Event("docker", "socket gid detected", map[string]any{"gid": gid, "socket": a.Cfg.DockerSocketOrDefault()})
		a.Log.Decision("D-RUNNER-MODE", "Runner connects to Docker via the mounted host socket",
			fmt.Sprintf("effective endpoint is a local unix socket (%s, gid %s); no DinD", a.Cfg.DockerSocketOrDefault(), gid),
			"docs/design.md#51-engine")
	} else {
		a.Log.Decision("D-RUNNER-MODE", "Runner connects to Docker over TCP (no socket mount, no gid)",
			fmt.Sprintf("effective DOCKER_HOST is %s; runner dials the same daemon compose uses; no DinD", runnerHost),
			"docs/design.md#51-engine")
	}
	if err := forge.SaveRunnerMode(a.Cfg.StateDir, mode, runnerHost); err != nil {
		return err
	}
	env := a.Cfg.ComposeEnv()

	a.Log.Step("init: ensuring CI job image %s", a.Cfg.Images.CI)
	if err := a.ensureCIImage(); err != nil {
		return err
	}

	a.Log.Step("init: bringing up control plane (postgres + forgejo + lb)")
	if out, err := compose.ComposeFiles(env, a.cpFiles(), "up", "-d", "--wait", "postgres", "forgejo", "lb"); err != nil {
		if strings.Contains(out, "already allocated") || strings.Contains(out, "address already in use") {
			return fmt.Errorf("compose up control plane: the forge port %d is already in use by another process.\n"+
				"  free it, or set SANDFORGE_HTTP_PORT to a different port, then retry.\n%s\n%w",
				a.Cfg.HTTPPort, out, err)
		}
		return fmt.Errorf("compose up control plane: %w\n%s", err, out)
	}

	a.Log.Step("init: health-gating forge at %s", a.Cfg.CloneURL())
	if err := forge.WaitHealthy(a.Cfg.CloneURL(), 90*time.Second); err != nil {
		return err
	}
	a.Log.Event("health", "forge healthy", map[string]any{"url": a.Cfg.CloneURL()})

	a.Log.Step("init: resolving admin credentials")
	if err := a.resolveAdminPassword(); err != nil {
		return err
	}

	a.Log.Step("init: seeding admin user (idempotent)")
	if err := a.ensureAdmin(env); err != nil {
		return err
	}

	a.Log.Step("init: ensuring API token")
	if err := a.ensureToken(env); err != nil {
		return err
	}

	// Cosmetic, so best-effort: give the sandforge admin the robot avatar. A failure is logged
	// (never silent) but must not block the bootstrap.
	a.Log.Step("init: setting the sandforge robot avatar")
	if err := a.Client.SetUserAvatar(); err != nil {
		a.Log.Event("avatar", "skipped", map[string]any{"reason": err.Error()})
	}

	a.Log.Step("init: registering act_runner (chicken-and-egg sequenced)")
	if err := a.ensureRunner(env); err != nil {
		return err
	}

	a.Log.Step("init: starting warm runner (docker gid=%s)", a.Cfg.DockerGID)
	if out, err := compose.ComposeFiles(env, a.cpFiles(), "up", "-d", "act_runner"); err != nil {
		return fmt.Errorf("compose up runner: %w\n%s", err, out)
	}
	if err := a.waitRunnerOnline(env, 60*time.Second); err != nil {
		return err
	}

	// Warm the runner so the FIRST real user push is fast (docs/goal.md AC-1/AC-2). The one-time cost of
	// a cold loop is fetching the `actions/checkout` action (~10-12s) and the initial job-container
	// create — both excluded from the loop-time contract ("startup is a one-time, excluded cost").
	// Best-effort: a warmup hiccup must not fail init.
	a.Log.Step("init: warming the runner (priming the actions cache; one-time, excluded from loop time)")
	if err := a.warmupRunner(); err != nil {
		a.Log.Event("warmup", "skipped", map[string]any{"reason": err.Error()})
	}

	a.Log.Decision("D-INIT", "Control plane up and runner online", "bootstrap completed idempotently", "docs/design.md#14")
	a.Log.Infof("\n  Sandforge is up.\n    forge:  %s\n    login:  %s / %s\n    token:  %s…\n",
		a.Cfg.CloneURL(), a.Creds.User, a.Creds.Password, safePrefix(a.Creds.Token))

	// If the user enabled the always-on agents router (Copilot emulation), bring it back now so it
	// survives forge restarts. No-op unless `sandforge agents start` was run before.
	a.maybeStartDaemon()
	return nil
}

func safePrefix(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// ensureCIImage builds the warm CI job image if it's missing OR stale. Staleness matters on
// UPGRADE: a user with an old `sandforge/ci:...` tag (e.g. built before the arm64 toolchain fix)
// must not keep running it. We stamp the image with a hash of its build context and rebuild when
// the embedded Dockerfile changes — a plain "tag exists → skip" would ship the fix to nobody.
func (a *App) ensureCIImage() error {
	ctx := filepath.Join(a.Cfg.RepoRoot, "deploy", "ci-image")
	want := ciContextHash(ctx)
	if compose.ImagePresent(a.Cfg.Images.CI) {
		if want == "" || a.ciImageLabel() == want {
			return nil
		}
		a.Log.Infof("  CI image is stale (build context changed) — rebuilding %s", a.Cfg.Images.CI)
	} else {
		a.Log.Infof("  building %s (one-time) …", a.Cfg.Images.CI)
	}
	args := []string{"build", "-t", a.Cfg.Images.CI}
	if want != "" {
		args = append(args, "--label", "sandforge.ci.hash="+want)
	}
	args = append(args, ctx)
	return compose.RunStream(os.Environ(), "docker", args...)
}

// ciImageLabel reads the staleness hash baked into the current CI image (empty if absent).
func (a *App) ciImageLabel() string {
	out, err := compose.Run(os.Environ(), "docker", "inspect", "--format",
		`{{index .Config.Labels "sandforge.ci.hash"}}`, a.Cfg.Images.CI)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// ciContextHash is a stable digest of every file in the CI image build context, so any change to
// the Dockerfile (or a helper it copies) yields a new hash and forces a rebuild. Empty on error
// (callers then fall back to a presence check rather than rebuilding on every init).
func ciContextHash(ctxDir string) string {
	var files []string
	if err := filepath.WalkDir(ctxDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	}); err != nil {
		return ""
	}
	sort.Strings(files)
	h := sha256.New()
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return ""
		}
		rel, _ := filepath.Rel(ctxDir, f)
		// Length-delimit each file's bytes so the encoding is unambiguous — otherwise different
		// file sets/contents could hash to the same byte stream and a changed Dockerfile would be
		// wrongly treated as "not stale". (Same framing as assetsHash.)
		fmt.Fprintf(h, "%s\x00%d\x00", rel, len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// resolveAdminPassword fills Cfg.Admin.Password when nothing explicit was configured (yaml/env):
// reuse the password already persisted for this instance, else generate a fresh random one. The
// old behavior — every instance sharing the hardcoded "sandforge-dev" — is gone: the forge listens
// on loopback only, but a well-known credential is still a well-known credential.
func (a *App) resolveAdminPassword() error {
	if a.Cfg.Admin.Password != "" {
		return nil // explicit sandforge.yaml / SANDFORGE_ADMIN_PASSWORD wins
	}
	// Reuse the instance's persisted password — EXCEPT the retired hardcoded default, which is a
	// publicly known credential: instances still carrying it get rotated to a random one (the
	// change-password in ensureAdmin + the credentials re-save make the rotation complete).
	const retiredDefault = "sandforge-dev"
	if a.Creds != nil && a.Creds.Password != "" && a.Creds.Password != retiredDefault {
		a.Cfg.Admin.Password = a.Creds.Password // existing instance: keep its password
		return nil
	}
	pw, err := config.GeneratePassword(20)
	if err != nil {
		return err
	}
	a.Cfg.Admin.Password = pw
	reason := "no password configured and none persisted; random-per-instance replaces the shared default"
	if a.Creds != nil && a.Creds.Password == retiredDefault {
		reason = "instance still used the retired well-known default; rotated to a random password"
	}
	a.Log.Decision("D-ADMIN-PW", "Generated a random admin password for this instance", reason, "docs/design.md#12")
	return nil
}

func (a *App) ensureAdmin(env []string) error {
	out, err := compose.ExecUserFiles(env, a.cpFiles(), "forgejo", "git",
		"forgejo", "admin", "user", "create",
		"--admin", "--username", a.Cfg.Admin.User, "--password", a.Cfg.Admin.Password,
		"--email", a.Cfg.Admin.Email, "--must-change-password=false")
	if err != nil {
		// Only the specific "already exists" outcome is success-for-idempotency; any other failure
		// containing "already" must not be masked by proceeding to change-password.
		if !strings.Contains(strings.ToLower(out), "already exists") {
			return fmt.Errorf("admin create: %w\n%s", err, out)
		}
		// "already exists" is success for idempotency — but the existing user's password may not be
		// the one we resolved (e.g. the credentials file was deleted, or an old instance still has
		// the retired hardcoded default). Set it authoritatively so the login we print always works.
		out, err = compose.ExecUserFiles(env, a.cpFiles(), "forgejo", "git",
			"forgejo", "admin", "user", "change-password",
			"--username", a.Cfg.Admin.User, "--password", a.Cfg.Admin.Password,
			"--must-change-password=false")
		if err != nil {
			return fmt.Errorf("admin change-password (align existing user with resolved password): %w\n%s", err, out)
		}
	}
	return nil
}

// apiBase is the URL the API client dials: in-network forge address when containerized
// (ForgeInternalURL set — the loopback creds URL is unreachable there), else the creds URL.
// Same rule New() applies when it first builds the client.
func (a *App) apiBase(credsURL string) string {
	if a.Cfg.ForgeInternalURL != "" {
		return a.Cfg.RepoBase()
	}
	return credsURL
}

func (a *App) ensureToken(env []string) error {
	if a.Creds != nil && a.Creds.Token != "" {
		a.Client = forge.NewClient(a.apiBase(a.Creds.URL), a.Creds.Token)
		// verify still valid
		if a.Client.GetRepoOK() {
			// keep the persisted password in step with the resolved one (ensureAdmin made the
			// resolved value authoritative in the forge; the file must say the same thing).
			if a.Creds.Password != a.Cfg.Admin.Password {
				a.Creds.Password = a.Cfg.Admin.Password
				if err := forge.SaveCredentials(a.Cfg.StateDir, a.Creds); err != nil {
					return err
				}
			}
			return nil
		}
	}
	name := "sandforge-cli"
	out, err := compose.ExecUserFiles(env, a.cpFiles(), "forgejo", "git",
		"forgejo", "admin", "user", "generate-access-token",
		"--username", a.Cfg.Admin.User, "--scopes", "all", "--token-name", name, "--raw")
	tok := hex40.FindString(out)
	if tok == "" {
		// duplicate name or non-raw output: retry with a unique name
		name = fmt.Sprintf("sandforge-cli-%d", time.Now().UnixNano())
		out, err = compose.ExecUserFiles(env, a.cpFiles(), "forgejo", "git",
			"forgejo", "admin", "user", "generate-access-token",
			"--username", a.Cfg.Admin.User, "--scopes", "all", "--token-name", name, "--raw")
		tok = hex40.FindString(out)
	}
	if tok == "" {
		return fmt.Errorf("could not obtain API token: %v\n%s", err, out)
	}
	a.Creds = &forge.Credentials{
		URL: a.Cfg.CloneURL(), User: a.Cfg.Admin.User, Password: a.Cfg.Admin.Password, Token: tok,
	}
	if err := forge.SaveCredentials(a.Cfg.StateDir, a.Creds); err != nil {
		return err
	}
	a.Client = forge.NewClient(a.apiBase(a.Creds.URL), a.Creds.Token)
	return nil
}

func (a *App) ensureRunner(env []string) error {
	runnerDir := filepath.Join(a.Cfg.StateDir, "runner")
	if _, err := os.Stat(filepath.Join(runnerDir, ".runner")); err == nil {
		return nil // already registered
	}
	// 1. mint a runner registration token from forgejo
	out, err := compose.ExecUserFiles(env, a.cpFiles(), "forgejo", "git",
		"forgejo", "actions", "generate-runner-token")
	tok := strings.TrimSpace(lastLine(out))
	if err != nil || len(tok) < 30 {
		return fmt.Errorf("generate runner token: %w\n%s", err, out)
	}
	// 2. write config.yml
	cfgPath := filepath.Join(runnerDir, "config.yml")
	if err := os.WriteFile(cfgPath, []byte(runnerConfigYAML(a.Cfg)), 0o644); err != nil {
		return err
	}
	// 3. register via a one-shot runner container on the compose network
	regArgs := []string{
		"run", "--rm", "--network", a.Cfg.Network,
		"-v", runnerDir + ":/data", "-w", "/data",
		"--entrypoint", "forgejo-runner", a.Cfg.Images.Runner,
		"register", "--no-interactive",
		"--instance", "http://forgejo:3000",
		"--token", tok, "--name", a.Cfg.Project + "-runner",
		"--labels", a.Cfg.CILabel(),
		"--config", "/data/config.yml",
	}
	if out, err := compose.Run(os.Environ(), "docker", regArgs...); err != nil {
		return fmt.Errorf("runner register: %w\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(runnerDir, ".runner")); err != nil {
		return fmt.Errorf("runner registration did not produce .runner")
	}
	return nil
}

// waitRunnerOnline blocks until the act_runner daemon is genuinely connected to Forgejo, or fails
// LOUD. Readiness is the positive daemon signal in the container's own logs; failure is detected
// fast (a crash-looping container) and reported with the real underlying error — never the old
// silent "did not come online" with no cause.
// runnerContainerID resolves the act_runner container's real ID via compose, so we don't assume
// the `<project>-act_runner-1` naming (Compose normalizes project names, lowercasing/stripping
// punctuation). Falls back to the conventional name if compose can't answer.
func (a *App) runnerContainerID(env []string) string {
	out, err := compose.ComposeFiles(env, a.cpFiles(), "ps", "-q", "act_runner")
	if id := strings.TrimSpace(out); err == nil && id != "" {
		return strings.Split(id, "\n")[0] // first line = the (single) runner container id
	}
	return a.Cfg.Project + "-act_runner-1"
}

func (a *App) waitRunnerOnline(env []string, timeout time.Duration) error {
	container := a.runnerContainerID(env)
	deadline := time.Now().Add(timeout)
	// Positive signals forgejo-runner emits once the daemon has connected to the Forgejo instance.
	online := []string{"declared successfully", "poller", "Successfully pinged the Forgejo instance server", "is online"}
	for time.Now().Before(deadline) {
		// Fail fast on a crash-loop: a restarting container will never come online, and the real
		// cause (e.g. socket permission denied) is in its logs — surface it instead of waiting out
		// the whole timeout with a generic message.
		if rc, restarting := a.containerRestartState(container); restarting || rc >= 3 {
			return a.runnerFailure(env, container, fmt.Sprintf(
				"act_runner is crash-looping (restarting=%v, restartCount=%d) — it is not staying up", restarting, rc))
		}
		out, _ := compose.ComposeFiles(env, a.cpFiles(), "logs", "--no-color", "--tail", "60", "act_runner")
		// Require the container to be Up AND the MOST RECENT online signal to come after the most
		// recent fatal ping error — so a stale early error (or a stale earlier "online" before a
		// later crash) in the tail can't produce a false verdict either way.
		if a.containerUp(container) && runnerLogReady(out, online) {
			a.Log.Event("runner", "online", map[string]any{"gid": a.Cfg.DockerGID})
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	return a.runnerFailure(env, container, fmt.Sprintf("runner did not come online within %s", timeout))
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// runnerLogReady reports whether the runner is connected: a positive online signal exists in the
// logs AND its most-recent occurrence is later than the most-recent fatal "cannot ping the docker
// daemon" line. Comparing positions (not mere presence) means a stale early error — or a stale
// earlier "online" before a later crash — in the tailed window can't flip the verdict either way.
func runnerLogReady(logs string, online []string) bool {
	lastErr := strings.LastIndex(logs, "cannot ping the docker daemon")
	lastOK := -1
	for _, sig := range online {
		if i := strings.LastIndex(logs, sig); i > lastOK {
			lastOK = i
		}
	}
	return lastOK >= 0 && lastOK > lastErr
}

// runnerFailure builds a rich, actionable error: the symptom + the GID we injected + the last log
// lines from the runner container (where the real cause — e.g. socket permission denied — lives).
func (a *App) runnerFailure(env []string, container, symptom string) error {
	logs, _ := compose.ComposeFiles(env, a.cpFiles(), "logs", "--no-color", "--tail", "15", "act_runner")
	logs = strings.TrimSpace(logs)
	if logs == "" {
		logs = "(no logs captured from " + container + ")"
	}
	return fmt.Errorf("%s\n  injected docker socket gid: %s (socket %s)\n  --- last act_runner logs ---\n%s\n  --- end logs ---\n  hint: 'permission denied ... docker.sock' means the gid is wrong for this host",
		symptom, a.Cfg.DockerGID, a.Cfg.DockerSocketOrDefault(), indent(logs, "    "))
}

func indent(s, pad string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n")
}

// containerRestartState returns the container's RestartCount and whether it is currently Restarting.
func (a *App) containerRestartState(container string) (int, bool) {
	out, err := compose.Run(os.Environ(), "docker", "inspect", "--format",
		"{{.RestartCount}} {{.State.Restarting}}", container)
	if err != nil {
		return 0, false
	}
	var rc int
	var restarting bool
	fmt.Sscanf(strings.TrimSpace(out), "%d %t", &rc, &restarting)
	return rc, restarting
}

// containerUp reports whether the container's State.Running is true.
func (a *App) containerUp(container string) bool {
	out, err := compose.Run(os.Environ(), "docker", "inspect", "--format", "{{.State.Running}}", container)
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) == "true"
}

// warmupRunner pushes a tiny throwaway workflow that uses actions/checkout, so the action is
// fetched+cached and the first job container is created once — making the first *real* user loop
// warm. The repo is deleted afterwards. Best-effort and bounded; never fatal to init.
func (a *App) warmupRunner() error {
	if a.Client == nil {
		return fmt.Errorf("no forge client")
	}
	const name = "_sandforge_warmup"
	if _, err := a.Client.CreateRepo(name, "main", true); err != nil {
		return fmt.Errorf("create warmup repo: %w", err)
	}
	defer func() { _ = a.Client.DeleteRepo(a.Cfg.Admin.Org, name) }()

	tmp, err := os.MkdirTemp("", "sf-warmup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	url := a.authedRepoURL(name)
	if out, err := runGit(tmp, "clone", url, "r"); err != nil {
		return fmt.Errorf("clone warmup: %w\n%s", err, out)
	}
	wd := filepath.Join(tmp, "r")
	wf := filepath.Join(wd, ".github", "workflows", "warmup.yml")
	if err := os.MkdirAll(filepath.Dir(wf), 0o755); err != nil {
		return err
	}
	content := "name: warmup\non: [push]\njobs:\n  warm:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n      - run: echo warm\n"
	if err := os.WriteFile(wf, []byte(content), 0o644); err != nil {
		return err
	}
	runGit(wd, "add", "-A")
	if out, err := runGit(wd, "commit", "-m", "warmup"); err != nil {
		return fmt.Errorf("commit warmup: %w\n%s", err, out)
	}
	shaOut, err := runGit(wd, "rev-parse", "HEAD")
	head := strings.TrimSpace(shaOut)
	if err != nil || head == "" {
		// An empty head would wildcard-match an arbitrary run in WaitRunComplete — the false-match
		// class fixed elsewhere. Warmup is best-effort, so bail loudly instead of waiting on a bogus SHA.
		return fmt.Errorf("warmup: resolve HEAD: %v\n%s", err, shaOut)
	}
	if out, err := runGit(wd, "push", "origin", "HEAD:main"); err != nil {
		return fmt.Errorf("push warmup: %w\n%s", err, out)
	}
	// Wait for the warmup run to finish (pass or fail — we only care that the cache is primed).
	ok, elapsed, status, _ := a.Client.WaitRunComplete(a.Cfg.Admin.Org, name, head, 90*time.Second)
	a.Log.Event("warmup", "done", map[string]any{"green": ok, "status": status, "elapsed_s": elapsed.Seconds()})
	return nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

// runnerConfigYAML renders the act_runner config: job containers attach to the compose network
// (so they resolve `forgejo`) and ubuntu-latest maps to the warm Sandforge CI image (D-009).
func runnerConfigYAML(c *config.Config) string {
	return fmt.Sprintf(`log:
  level: info
runner:
  file: .runner
  capacity: 4
  timeout: 3h
  fetch_timeout: 5s
  fetch_interval: 2s
  labels:
    - "%s"
cache:
  enabled: true
container:
  network: "%s"
  privileged: false
  valid_volumes: []
  force_pull: false
host:
  workdir_parent: ""
`, c.CILabel(), c.Network)
}

// ----- down / reset / status / logs --------------------------------------------------------

// Down tears down the control-plane stack (optionally keeping volumes).
func (a *App) Down(keepVolumes bool) error {
	// Stop the agents daemon first (it's a host process, not part of the compose stack) but keep its
	// enabled marker so the next `init` brings it back. An explicit `agents stop` is how you disable it.
	a.stopDaemonKeepMarker()
	// Also tear down the containerized router (if running) so removing the control-plane network
	// doesn't orphan it. Best-effort.
	a.agentsContainerDown()
	env := a.Cfg.ComposeEnv()
	args := []string{"down"}
	if !keepVolumes {
		args = append(args, "-v")
	}
	a.Log.Step("down: tearing down control plane (keepVolumes=%v)", keepVolumes)
	out, err := compose.ComposeFiles(env, a.cpFiles(), args...)
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	// also remove any orphaned job containers
	a.cleanupJobContainers()
	return nil
}

func (a *App) cleanupJobContainers() {
	out, _ := compose.Run(os.Environ(), "docker", "ps", "-aq", "--filter", "name=FORGEJO-ACTIONS")
	for _, id := range strings.Fields(out) {
		compose.Run(os.Environ(), "docker", "rm", "-f", id)
	}
}

// Reset wipes sandbox state (whole instance) and credentials.
func (a *App) Reset() error {
	if err := a.Down(false); err != nil {
		return err
	}
	a.Log.Step("reset: wiping state dir %s", a.Cfg.StateDir)
	if err := os.RemoveAll(a.Cfg.StateDir); err != nil {
		return err
	}
	a.Log.Decision("D-RESET", "Wiped instance state + rotated credentials", "user-requested reset", "docs/design.md#12")
	return nil
}

// Status prints a one-screen summary (design §12).
func (a *App) Status() error {
	env := a.Cfg.ComposeEnv()
	fmt.Printf("Sandforge instance: %s\n", a.Cfg.Project)
	fmt.Printf("  forge URL:   %s\n", a.Cfg.CloneURL())
	ps, _ := compose.ComposeFiles(env, a.cpFiles(), "ps", "--format", "table {{.Service}}\t{{.Status}}")
	fmt.Printf("  control plane:\n")
	for _, l := range strings.Split(strings.TrimSpace(ps), "\n") {
		fmt.Printf("    %s\n", l)
	}
	if a.Client != nil {
		fmt.Printf("  health:      %s\n", healthString(a.Cfg.CloneURL()))
	}
	return nil
}

func healthString(url string) string {
	if err := forge.WaitHealthy(url, 2*time.Second); err == nil {
		return "pass"
	}
	return "unreachable"
}

// Logs tails control-plane (or a specific service) logs.
func (a *App) Logs(service string, follow bool) error {
	env := a.Cfg.ComposeEnv()
	args := []string{"logs", "--tail", "100"}
	if follow {
		args = append(args, "-f")
	}
	if service != "" {
		args = append(args, service)
	}
	return compose.ComposeFilesStream(env, a.cpFiles(), args...)
}

// gitEnv returns env for git operations with the forge token embedded auth helper disabled.
func (a *App) authedRepoURL(repo string) string {
	return fmt.Sprintf("http://%s:%s@127.0.0.1:%d/%s/%s.git",
		a.Cfg.Admin.User, a.Creds.Token, a.Cfg.HTTPPort, a.Cfg.Admin.Org, repo)
}

// runGit runs a git command in dir, returning combined output.
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=sandforge", "GIT_AUTHOR_EMAIL=agent@sandforge.local",
		"GIT_COMMITTER_NAME=sandforge", "GIT_COMMITTER_EMAIL=agent@sandforge.local",
	)
	out, err := cmd.CombinedOutput()
	// Redact credentials from git's output before it can reach an error message or a log: git
	// echoes the authed clone URL (http://user:token@…) on clone/push failures. redactCreds only
	// rewrites "://user@" — never a SHA/ref/path — so it's safe for the callers that parse output.
	return redactCreds(string(out)), err
}
