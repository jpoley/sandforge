package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	sandforge "github.com/jpoley/sandforge"
	"github.com/jpoley/sandforge/internal/agents"
)

// defaultAgentsPort is the host port the router/UI binds and Forgejo posts deliveries to.
const defaultAgentsPort = 3999

// agentDispatcher is the host-side executor for the agents router: it clones the repo at the right
// ref, runs the configured agent command with the event context in its env, posts the agent's
// output back as a PR/issue comment (the "review"), and pushes any commits the agent made (the
// "fix" handoff). It needs host git + the forge client, which is why it lives in the app layer.
type agentDispatcher struct{ a *App }

func (d agentDispatcher) Dispatch(j agents.Job) agents.Result {
	start := time.Now()
	res := agents.Result{}
	fail := func(format string, args ...any) agents.Result {
		res.Err = fmt.Errorf(format, args...)
		res.Duration = time.Since(start)
		// Surface the failure to the human on the PR too — never fail silently (resilience rule).
		_ = d.comment(j, "⚠️ agent @"+j.Agent.Handle+" failed: "+res.Err.Error())
		return res
	}
	if d.a.Client == nil || d.a.Creds == nil {
		return fail("agents router not initialized (run `sandforge init` first)")
	}

	// 1. resolve the branch to act on (PR head, or default branch for a plain issue).
	branch := j.Branch
	if j.IsPull && branch == "" {
		pr, err := d.a.Client.GetPull(j.Owner, j.Repo, j.IssueNumber)
		if err != nil {
			return fail("resolve PR head: %w", err)
		}
		branch = pr.Head.Ref
	}

	// 2. clone the repo into a throwaway dir at that branch.
	tmp, err := os.MkdirTemp("", "sf-agent-")
	if err != nil {
		return fail("mktemp: %w", err)
	}
	defer os.RemoveAll(tmp)
	url := d.a.authedRepoURLFor(j.Owner, j.Repo)
	cloneArgs := []string{"clone", "--depth", "50"}
	if branch != "" {
		cloneArgs = append(cloneArgs, "-b", branch)
	}
	cloneArgs = append(cloneArgs, url, "repo")
	if out, err := runGit(tmp, cloneArgs...); err != nil {
		return fail("clone %s/%s@%s: %w\n%s", j.Owner, j.Repo, branch, err, out)
	}
	wd := filepath.Join(tmp, "repo")
	beforeSHA, _ := runGit(wd, "rev-parse", "HEAD")

	// 3. run the agent command with the event context in its env.
	if len(j.Agent.Command) == 0 {
		return fail("agent @%s has no command configured (nothing to invoke)", j.Agent.Handle)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, j.Agent.Command[0], j.Agent.Command[1:]...)
	cmd.Dir = wd
	cmd.Env = append(os.Environ(),
		"SANDFORGE_HANDLE="+j.Agent.Handle,
		"SANDFORGE_KIND="+j.Agent.Kind,
		"SANDFORGE_REPO="+j.Owner+"/"+j.Repo,
		"SANDFORGE_OWNER="+j.Owner,
		"SANDFORGE_NAME="+j.Repo,
		fmt.Sprintf("SANDFORGE_ISSUE=%d", j.IssueNumber),
		fmt.Sprintf("SANDFORGE_IS_PULL=%t", j.IsPull),
		"SANDFORGE_BRANCH="+branch,
		"SANDFORGE_TRIGGER="+j.Trigger,
		"SANDFORGE_COMMENT="+j.Comment,
		"SANDFORGE_FORGE_URL="+d.a.Cfg.RepoBase(),
		"SANDFORGE_TOKEN="+d.a.Creds.Token, // local-Forgejo-only token (never an upstream cred, AC-5)
	)
	outB, runErr := cmd.CombinedOutput()
	out := strings.TrimRight(string(outB), "\n")
	res.Output = truncate(out, 8000)
	if runErr != nil {
		res.Output = truncate(out, 8000)
		return fail("command failed: %v\n%s", runErr, truncate(out, 2000))
	}

	// 4. if the agent made commits, push them back to the branch (the "fix" handoff). Only for PRs
	// (a plain issue has no branch to push). Loud on failure.
	afterSHA, _ := runGit(wd, "rev-parse", "HEAD")
	if j.IsPull && branch != "" && strings.TrimSpace(beforeSHA) != strings.TrimSpace(afterSHA) {
		if pout, perr := runGit(wd, "push", "origin", "HEAD:"+branch); perr != nil {
			return fail("push agent commits to %s: %w\n%s", branch, perr, pout)
		}
		res.Pushed = true
	}

	// 5. post the agent's output back as a comment (the review/handoff people actually see).
	body := out
	if body == "" {
		body = "_(no output)_"
	}
	if res.Pushed {
		body += "\n\n✅ pushed commit(s) to `" + branch + "`."
	}
	if err := d.comment(j, body); err != nil {
		return fail("post comment: %w", err)
	}
	res.Duration = time.Since(start)
	return res
}

// comment posts an agent message to the issue/PR with the footer + hidden loop-guard marker.
func (d agentDispatcher) comment(j agents.Job, body string) error {
	full := truncate(body, 60000) +
		fmt.Sprintf("\n\n<sub>🤖 @%s · sandforge local agent · %s</sub>\n%s",
			j.Agent.Handle, j.Trigger, agents.AgentMarker)
	return d.a.Client.Comment(j.Owner, j.Repo, j.IssueNumber, full)
}

// authedRepoURLFor builds a token-authed clone URL for an arbitrary owner (the per-instance
// authedRepoURL assumes the admin org; agents may act on repos under any owner the token can reach).
// It honors RepoBase so the containerized router clones via the in-network forge address.
func (a *App) authedRepoURLFor(owner, repo string) string {
	base := a.Cfg.RepoBase() // scheme://host:port
	creds := a.Cfg.Admin.User + ":" + a.Creds.Token + "@"
	if i := strings.Index(base, "://"); i >= 0 {
		base = base[:i+3] + creds + base[i+3:]
	}
	return fmt.Sprintf("%s/%s/%s.git", strings.TrimRight(base, "/"), owner, repo)
}

// newRouter loads the file-backed config + event store and wires the host dispatcher.
func (a *App) newRouter() (*agents.Router, *agents.Config, error) {
	cfg, err := agents.LoadConfig(a.Cfg.StateDir)
	if err != nil {
		return nil, nil, err
	}
	// NOTE: we deliberately do NOT default BotLogin to the admin user. In the default single-user
	// setup the human IS the admin account, so guarding on that login would drop the human's own
	// @mentions. The hidden AgentMarker the dispatcher stamps on every agent comment is the robust
	// loop guard (an agent never re-triggers itself). BotLogin stays opt-in for a dedicated bot user.
	store := agents.NewStore(a.Cfg.StateDir)
	r := &agents.Router{Cfg: cfg, Store: store, Dispatcher: agentDispatcher{a}, Async: true}
	return r, cfg, nil
}

// AgentsServe registers Forgejo webhooks (idempotently) on the target repos and runs the listener +
// UI until interrupted. repoFilter ("owner/name" or "") limits which repos get a hook; empty means
// every repo the token can see.
func (a *App) AgentsServe(port int, repoFilter string) error {
	if a.Client == nil {
		return fmt.Errorf("not initialized — run `sandforge init` first")
	}
	if port == 0 {
		port = defaultAgentsPort
	}
	// The containerized router gets its repo filter via env (its compose command is fixed argv).
	if repoFilter == "" {
		repoFilter = os.Getenv("SANDFORGE_AGENTS_REPO")
	}
	r, cfg, err := a.newRouter()
	if err != nil {
		return err
	}

	host := agentsHost()
	webhookURL := fmt.Sprintf("http://%s:%d/webhook", host, port)
	// Host process → guard the command-executing routes to loopback (it binds all interfaces).
	// Container → the host-side 127.0.0.1 port publish is the boundary; the in-container guard would
	// wrongly 403 local traffic that arrives via the docker bridge gateway. See Server.LoopbackGuard.
	srv := &agents.Server{
		Router: r, WebhookURL: webhookURL, Logf: a.Log.Infof,
		LoopbackGuard: a.Cfg.ForgeInternalURL == "",
		// Human-facing forge URL (always loopback, reachable from the browser — never the in-network
		// forgejo:3000) so the UI can link out to the Forgejo web UI.
		ForgeURL:     a.Cfg.CloneURL(),
		DocsMarkdown: sandforge.AgentsDoc,
	}

	// Register the webhook on each target repo (idempotent reuse if it already exists).
	repos, err := a.targetRepos(repoFilter)
	if err != nil {
		return err
	}
	if len(repos) == 0 {
		return fmt.Errorf("no repos to wire (filter %q matched nothing) — `sandforge import` one first", repoFilter)
	}
	events := []string{"issue_comment", "pull_request"}
	for _, full := range repos {
		owner, name, ok := splitRepo(full)
		if !ok {
			continue
		}
		h, err := a.Client.EnsureHook(owner, name, webhookURL, cfg.Secret, events)
		if err != nil {
			return fmt.Errorf("register webhook on %s: %w", full, err)
		}
		// Prune any OTHER router webhook (a stale /webhook hook from a previous run in a different
		// mode — e.g. host vs container — whose host now also resolves to us). Two live hooks would
		// double-dispatch every mention. Only our exact target survives.
		if hooks, herr := a.Client.ListHooks(owner, name); herr == nil {
			for _, old := range hooks {
				if old.ID != h.ID && strings.HasSuffix(old.Config["url"], "/webhook") {
					if derr := a.Client.DeleteHook(owner, name, old.ID); derr == nil {
						a.Log.Event("agents", "pruned stale webhook", map[string]any{"repo": full, "url": old.Config["url"]})
					}
				}
			}
		}
		a.Log.Event("agents", "webhook ready", map[string]any{"repo": full, "hook_id": h.ID, "url": webhookURL})
	}

	a.Log.Decision("D-AGENTS", "Local agent router online",
		fmt.Sprintf("webhook→agent routing on %d repo(s); UI on :%d", len(repos), port), "docs/goal.md")
	a.Log.Infof("\n  Sandforge local agents are live.\n    UI:       http://127.0.0.1:%d\n    webhook:  %s\n    agents:   %d configured (@%s)\n    repos:    %s\n  @mention an agent on a PR/issue, or use the UI to invoke a handoff. Ctrl-C to stop.\n",
		port, webhookURL, len(cfg.Snapshot()), strings.Join(handles(cfg), " @"), strings.Join(repos, ", "))

	httpSrv := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: srv.Handler()}
	// Bind explicitly so we fail loud if the port is taken (rather than a late ListenAndServe error).
	ln, err := net.Listen("tcp", httpSrv.Addr)
	if err != nil {
		return fmt.Errorf("bind agents listener on :%d: %w (set a different --port)", port, err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Serve(ln) }()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sig:
		a.Log.Infof("  shutting down agents router…")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpSrv.Shutdown(ctx)
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

// AgentsList prints the configured agents.
func (a *App) AgentsList() error {
	cfg, err := agents.LoadConfig(a.Cfg.StateDir)
	if err != nil {
		return err
	}
	list := cfg.Snapshot()
	if len(list) == 0 {
		fmt.Println("no agents configured")
		return nil
	}
	for _, g := range list {
		state := "enabled"
		if !g.Enabled {
			state = "disabled"
		}
		onopen := ""
		if g.OnOpen {
			onopen = " on-open"
		}
		fmt.Printf("  @%-10s %-9s [%s%s] %s\n", g.Handle, g.Kind, state, onopen, strings.Join(g.Command, " "))
	}
	return nil
}

// AgentsAdd upserts an agent from the CLI.
func (a *App) AgentsAdd(handle, name, kind string, onOpen bool, command []string) error {
	cfg, err := agents.LoadConfig(a.Cfg.StateDir)
	if err != nil {
		return err
	}
	if err := cfg.Upsert(agents.Agent{
		Handle: handle, Name: name, Kind: kind, OnOpen: onOpen, Enabled: true, Command: command,
	}); err != nil {
		return err
	}
	fmt.Printf("agent @%s saved\n", strings.TrimPrefix(handle, "@"))
	return nil
}

// AgentsTrigger fires an agent against an issue/PR without going through a webhook (the manual
// handoff path). It dispatches synchronously and prints the result.
func (a *App) AgentsTrigger(repoArg string, issue int, handle string, isPull bool, comment string) error {
	if a.Client == nil {
		return fmt.Errorf("not initialized — run `sandforge init` first")
	}
	owner, name, ok := splitRepo(repoArg)
	if !ok {
		// allow a bare repo name under the admin org
		owner, name = a.Cfg.Admin.Org, repoArg
	}
	r, _, err := a.newRouter()
	if err != nil {
		return err
	}
	r.Async = false
	ev, err := r.Trigger(owner, name, issue, isPull, handle, comment)
	if err != nil {
		return err
	}
	fmt.Printf("[%s] @%s on %s/%s#%d — %s (%.1fs)\n", ev.Status, handle, owner, name, issue, ev.Detail, ev.Duration)
	if ev.Output != "" {
		fmt.Printf("--- agent output ---\n%s\n", ev.Output)
	}
	if ev.Status == agents.StatusFailed {
		return fmt.Errorf("agent run failed")
	}
	return nil
}

// targetRepos returns the repo full-names to wire. A filter selects one; otherwise all repos the
// token can list.
func (a *App) targetRepos(filter string) ([]string, error) {
	if filter != "" {
		return []string{filter}, nil
	}
	repos, err := a.Client.ListRepos()
	if err != nil {
		return nil, err
	}
	return repos, nil
}

func splitRepo(full string) (owner, name string, ok bool) {
	parts := strings.SplitN(full, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), true
}

func handles(cfg *agents.Config) []string {
	var hs []string
	for _, g := range cfg.Snapshot() {
		hs = append(hs, g.Handle)
	}
	return hs
}

// agentsHost is the address Forgejo (in a container) uses to reach this host process. Docker
// Desktop (the validated platform) provides host.docker.internal; override for Linux bridge setups.
func agentsHost() string {
	if v := os.Getenv("SANDFORGE_AGENTS_HOST"); v != "" {
		return v
	}
	return "host.docker.internal"
}
