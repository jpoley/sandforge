package app

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jpoley/sandforge/internal/compose"
	"github.com/jpoley/sandforge/internal/forge"
	"github.com/jpoley/sandforge/internal/prd"
)

// worktreeMu serializes `git worktree add/remove` on a shared bare repo. Concurrent worktree
// registration mutates shared .git/worktrees bookkeeping and takes ref/packed-refs locks that git
// does not retry — a real intermittent-failure source in the 3-agent fan-out (AC-4). The push +
// CI wait stay fully parallel; only the worktree admin step is serialized.
var worktreeMu sync.Mutex

// repoMeta records where an imported repo came from (for graduate rebase / upstream push).
type repoMeta struct {
	Name         string `json:"name"`
	UpstreamURL  string `json:"upstream_url"`  // git URL we rebase onto / push to at upstream
	UpstreamRepo string `json:"upstream_repo"` // forge repo name acting as upstream (local default)
}

func (a *App) metaPath(name string) string {
	return filepath.Join(a.Cfg.StateDir, "repos", name+".meta.json")
}

func (a *App) saveMeta(m *repoMeta) error {
	_ = os.MkdirAll(filepath.Join(a.Cfg.StateDir, "repos"), 0o700)
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(a.metaPath(m.Name), b, 0o600)
}

func (a *App) loadMeta(name string) (*repoMeta, error) {
	b, err := os.ReadFile(a.metaPath(name))
	if err != nil {
		return nil, err
	}
	var m repoMeta
	return &m, json.Unmarshal(b, &m)
}

// Import seeds a writable forge repo <name> from an upstream git URL, and keeps the upstream as
// a remote for later rebasing (design §12). It also creates the on-host bare clone for worktrees.
func (a *App) Import(upstreamURL, name string) error {
	if a.Client == nil {
		return fmt.Errorf("not initialized — run `sandforge init` first")
	}
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(upstreamURL), ".git")
	}
	a.Log.Step("import: seeding %s from %s", name, redactCreds(upstreamURL))

	// 1. create the working forge repo (empty)
	if _, err := a.Client.CreateRepo(name, "main", false); err != nil {
		return err
	}
	// 2. mirror upstream into a temp dir and push to the working forge repo
	tmp, err := os.MkdirTemp("", "sf-import-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if out, err := runGit(tmp, "clone", "--origin", "upstream", upstreamURL, "work"); err != nil {
		return fmt.Errorf("clone upstream: %w\n%s", err, out)
	}
	wd := filepath.Join(tmp, "work")
	target := a.authedRepoURL(name)
	if out, err := runGit(wd, "remote", "add", "origin", target); err != nil {
		return fmt.Errorf("add origin: %w\n%s", err, out)
	}
	// ensure a main branch exists and a staging branch (design §5.2)
	runGit(wd, "branch", "-M", "main")
	if out, err := runGit(wd, "push", "-u", "origin", "main"); err != nil {
		return fmt.Errorf("push main: %w\n%s", err, out)
	}
	runGit(wd, "branch", "staging", "main")
	if out, err := runGit(wd, "push", "origin", "staging"); err != nil {
		return fmt.Errorf("push staging: %w\n%s", err, out)
	}
	// 3. bare clone on host for worktrees (design §8)
	bare := filepath.Join(a.Cfg.StateDir, "repos", name+".git")
	os.RemoveAll(bare)
	if out, err := runGit("", "clone", "--bare", target, bare); err != nil {
		return fmt.Errorf("bare clone: %w\n%s", err, out)
	}
	m := &repoMeta{Name: name, UpstreamURL: upstreamURL}
	if err := a.saveMeta(m); err != nil {
		return err
	}
	a.Log.Decision("D-IMPORT", "Imported "+name, "seeded working repo + bare clone; upstream kept as remote", "docs/design.md#12")
	a.Log.Event("import", "done", map[string]any{"repo": name, "clone": a.Cfg.CloneURL() + "/" + a.Cfg.Admin.Org + "/" + name})
	return nil
}

// AgentResult captures one simulated agent's inner-loop measurement (AC-1/AC-4).
type AgentResult struct {
	Agent   string
	Branch  string
	HeadSHA string
	Green   bool
	Elapsed time.Duration
	Status  string
}

// SimulateAgent acts as an AI agent: it creates a worktree-isolated branch work/<agent>/<slug>,
// makes a change, pushes, and waits for warm CI to go green — measuring the inner-loop round-trip.
func (a *App) SimulateAgent(name, agent, slug, changeFile, changeContent string) (*AgentResult, error) {
	bare := filepath.Join(a.Cfg.StateDir, "repos", name+".git")
	wt := filepath.Join(a.Cfg.StateDir, "worktrees", agent+"-"+slug)
	branch := fmt.Sprintf("work/%s/%s", agent, slug)
	os.RemoveAll(wt)
	_ = os.MkdirAll(filepath.Dir(wt), 0o700)

	// isolated worktree off the shared bare clone (design §8). Serialize the worktree-admin step
	// (the push + CI wait below stay parallel) to avoid concurrent ref/packed-refs lock contention.
	worktreeMu.Lock()
	addOut, addErr := runGit(bare, "worktree", "add", "-f", "-B", branch, wt, "main")
	worktreeMu.Unlock()
	if addErr != nil {
		// Include git's own output (the actual "cannot lock ref"/contention message) plus the
		// current worktree list — both are what you need to diagnose this path.
		list, _ := runGit(bare, "worktree", "list")
		return nil, fmt.Errorf("worktree add %s: %w\n%s\n--- worktrees ---\n%s", branch, addErr, addOut, list)
	}
	defer func() {
		worktreeMu.Lock()
		runGit(bare, "worktree", "remove", "--force", wt)
		worktreeMu.Unlock()
	}()

	// make the change
	target := filepath.Join(wt, changeFile)
	_ = os.MkdirAll(filepath.Dir(target), 0o755)
	if err := os.WriteFile(target, []byte(changeContent), 0o644); err != nil {
		return nil, err
	}
	runGit(wt, "add", "-A")
	if out, err := runGit(wt, "commit", "-m", fmt.Sprintf("%s: %s", agent, slug)); err != nil {
		return nil, fmt.Errorf("commit: %w\n%s", err, out)
	}
	shaOut, err := runGit(wt, "rev-parse", "HEAD")
	head := strings.TrimSpace(shaOut)
	if err != nil || head == "" {
		// An empty head would match an arbitrary run in WaitRunComplete → a false result. Fail loud.
		return nil, fmt.Errorf("resolve HEAD for %s: %v\n%s", branch, err, shaOut)
	}

	// push the work branch to the forge (triggers warm CI)
	start := time.Now()
	if out, err := runGit(wt, "push", "-f", a.authedRepoURL(name), branch); err != nil {
		return nil, fmt.Errorf("push %s: %w\n%s", branch, err, out)
	}
	ok, _, status, err := a.Client.WaitRunComplete(a.Cfg.Admin.Org, name, head, 5*time.Minute)
	elapsed := time.Since(start)
	res := &AgentResult{Agent: agent, Branch: branch, HeadSHA: head, Green: ok, Elapsed: elapsed, Status: status}
	a.Log.Event("agent", "inner-loop", map[string]any{
		"agent": agent, "branch": branch, "green": ok, "elapsed_s": elapsed.Seconds(), "status": status,
	})
	return res, err
}

// Promote merges a green branch into a target (work->staging, staging->main), pushing to the forge.
func (a *App) Promote(name, from, to string) error {
	tmp, err := os.MkdirTemp("", "sf-promote-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	url := a.authedRepoURL(name)
	if out, err := runGit(tmp, "clone", url, "r"); err != nil {
		return fmt.Errorf("clone: %w\n%s", err, out)
	}
	wd := filepath.Join(tmp, "r")
	if out, err := runGit(wd, "checkout", to); err != nil {
		return fmt.Errorf("checkout %s: %w\n%s", to, err, out)
	}
	if out, err := runGit(wd, "merge", "--no-ff", "-m", fmt.Sprintf("promote %s -> %s", from, to), "origin/"+from); err != nil {
		return fmt.Errorf("merge %s->%s: %w\n%s", from, to, err, out)
	}
	if out, err := runGit(wd, "push", "origin", to); err != nil {
		return fmt.Errorf("push %s: %w\n%s", to, err, out)
	}
	a.Log.Event("promote", from+"->"+to, map[string]any{"repo": name})
	return nil
}

// GraduateResult summarizes a graduate run.
type GraduateResult struct {
	Report       *prd.Report
	PRBody       string
	SquashSHA    string
	BranchName   string // the curated squash branch name (sandforge-graduate)
	SourceBranch string // the branch that was graduated (what upstream must actually publish)
}

// Graduate rebases onto fresh upstream, spins the deploy-target stack, runs PRD validation against
// it, tears it down, and (on pass) assembles the curated squash commit + PR body (design §6/§7).
func (a *App) Graduate(name, branch string, prdPath string, onlySCs, skipSCs []string) (*GraduateResult, error) {
	if prdPath == "" {
		prdPath = a.resolvePRDPath()
	}
	m, err := a.loadMeta(name)
	if err != nil {
		return nil, fmt.Errorf("load meta (import first?): %w", err)
	}
	a.Log.Step("graduate: %s @ %s", name, branch)

	// 1. rebase the working branch onto fresh upstream default (design §7 drift handling)
	tmp, err := os.MkdirTemp("", "sf-graduate-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	url := a.authedRepoURL(name)
	if out, err := runGit(tmp, "clone", "-b", branch, url, "r"); err != nil {
		return nil, fmt.Errorf("clone %s: %w\n%s", branch, err, out)
	}
	wd := filepath.Join(tmp, "r")
	runGit(wd, "remote", "add", "upstream", m.UpstreamURL)
	if out, err := runGit(wd, "fetch", "upstream"); err != nil {
		return nil, fmt.Errorf("fetch upstream: %w\n%s", err, out)
	}
	// determine upstream default branch (don't assume main — a repo may use master/trunk)
	upBranch := a.upstreamDefaultBranch(wd)
	if out, err := runGit(wd, "rebase", "upstream/"+upBranch); err != nil {
		return nil, fmt.Errorf("rebase onto upstream/%s failed (resolve conflicts in-loop): %w\n%s", upBranch, err, out)
	}

	// 2. spin the deploy-target stack on demand (design §6)
	dtDir := filepath.Join(a.Cfg.RepoRoot, "deploy", "tasks-app")
	dtCompose := filepath.Join(dtDir, "deploy-target.compose.yml")
	// The deploy-target LB port is ephemeral (the stack is spun on demand and torn down). A fixed
	// 8088 collides with whatever else the developer is running on their daily-driver Docker host,
	// so default to an OS-assigned free port; an explicit TASKS_LB_PORT still wins.
	lbPort := os.Getenv("TASKS_LB_PORT")
	if lbPort == "" {
		p, err := freeTCPPort()
		if err != nil {
			return nil, fmt.Errorf("graduate: could not find a free port for the deploy-target LB: %w", err)
		}
		lbPort = p
	}
	dtProject := a.Cfg.Project + "-tasks"
	dtEnv := append(os.Environ(),
		"TASKS_PROJECT="+dtProject, "TASKS_LB_PORT="+lbPort)
	a.Log.Step("graduate: spinning deploy-target stack on :%s", lbPort)
	// Clear any leftover deploy-target from a previously crashed graduate (same project name) so we
	// never build on top of stale containers/volumes.
	compose.Compose(dtEnv, dtCompose, "down", "-v")
	upStart := time.Now()
	if out, err := compose.Compose(dtEnv, dtCompose, "up", "-d", "--build", "--wait"); err != nil {
		compose.Compose(dtEnv, dtCompose, "logs", "--tail", "50")
		return nil, fmt.Errorf("deploy-target up: %w\n%s", err, out)
	}
	a.Log.Event("deploy-target", "healthy", map[string]any{"up_s": time.Since(upStart).Seconds()})
	defer func() {
		a.Log.Step("graduate: tearing down deploy-target")
		compose.Compose(dtEnv, dtCompose, "down", "-v")
	}()

	// 3. PRD validation against the running env (AC-7/AC-8)
	a.Log.Step("graduate: validating PRD success criteria")
	p, err := prd.Parse(prdPath)
	if err != nil {
		return nil, err
	}
	runner := &prd.Runner{
		VerifyScript: filepath.Join(dtDir, "verify.sh"),
		WorkDir:      dtDir,
		Only:         onlySCs,
		Skip:         skipSCs,
		Timeout:      5 * time.Minute, // explicit per-criterion cap (don't rely on Validate's default)
		Env: append(dtEnv,
			"BASE_URL=http://127.0.0.1:"+lbPort,
			"COMPOSE="+dtCompose,
		),
	}
	rep := runner.Validate(p, func(r prd.Result) {
		st := "pass"
		if r.Skipped {
			st = "skip"
		} else if !r.Pass {
			st = "FAIL"
		}
		a.Log.Event("prd", r.Criterion.ID+" "+st, map[string]any{
			"gate": r.Criterion.Gate, "elapsed_s": r.Elapsed.Seconds(),
		})
	})

	// 4. assemble the curated squash commit + PR body (design §7)
	gr := &GraduateResult{Report: rep, BranchName: "sandforge-graduate", SourceBranch: branch}
	prdReport := rep.Markdown(p)
	if rep.Passed() {
		sha, err := a.assembleSquash(wd, name, branch, prdReport)
		if err != nil {
			return gr, err
		}
		gr.SquashSHA = sha
	}
	gr.PRBody = a.prBody(name, branch, prdReport)
	// persist the report as an artifact
	artDir := filepath.Join(a.Cfg.StateDir, "graduate", name)
	_ = os.MkdirAll(artDir, 0o755)
	os.WriteFile(filepath.Join(artDir, "pr-body.md"), []byte(gr.PRBody), 0o644)
	os.WriteFile(filepath.Join(artDir, "prd-report.md"), []byte(prdReport), 0o644)

	if rep.Passed() {
		a.Log.Decision("D-GRADUATE", "PRD validation passed for "+name, "all staging-main gates green; curated squash assembled", "docs/goal.md#AC-7")
	} else {
		a.Log.Decision("D-GRADUATE-BLOCK", "PRD validation blocked "+name, fmt.Sprintf("%d staging-main gate(s) failed", len(rep.BlockingFails)), "docs/goal.md#AC-7")
	}
	return gr, nil
}

// assembleSquash creates a single squashed commit of the branch vs upstream/main on a graduate
// branch, and pushes it to the upstream forge repo. Returns the squash commit SHA.
func (a *App) assembleSquash(wd, name, branch, prdReport string) (string, error) {
	// squash everything since the upstream merge-base into one commit
	upBranch := a.upstreamDefaultBranch(wd)
	baseOut, err := runGit(wd, "merge-base", "upstream/"+upBranch, "HEAD")
	base := strings.TrimSpace(baseOut)
	if err != nil || base == "" {
		return "", fmt.Errorf("merge-base upstream/%s: %v\n%s", upBranch, err, baseOut)
	}
	if out, err := runGit(wd, "checkout", "-B", "sandforge-graduate", base); err != nil {
		return "", fmt.Errorf("checkout graduate base: %w\n%s", err, out)
	}
	if out, err := runGit(wd, "merge", "--squash", branch); err != nil {
		return "", fmt.Errorf("squash merge: %w\n%s", err, out)
	}
	msg := fmt.Sprintf("Sandforge: graduate %s\n\nValidated via full e2e + PRD success criteria.\n", name)
	if out, err := runGit(wd, "commit", "-m", msg); err != nil {
		// nothing to commit means branch == upstream; treat as no-op error
		return "", fmt.Errorf("commit squash: %w\n%s", err, out)
	}
	shaOut, err := runGit(wd, "rev-parse", "HEAD")
	sha := strings.TrimSpace(shaOut)
	if err != nil || sha == "" {
		return "", fmt.Errorf("resolve squash commit sha: %v\n%s", err, shaOut)
	}
	return sha, nil
}

func (a *App) prBody(name, branch, prdReport string) string {
	return fmt.Sprintf(`# Sandforge graduation: %s

Curated from sandbox branch `+"`%s`"+` — squashed (default) after full e2e against the
on-demand deploy-target stack.

## Specs & design
- Goal: docs/goal.md (acceptance criteria)
- Design: docs/design.md
- PRD: docs/prd.md (machine-checked below)

%s

🤖 Assembled by Sandforge. Human approval required before upstream merge.
`, name, branch, prdReport)
}

// Upstream pushes the graduated branch to the upstream and opens a PR. For the local-default
// upstream (a forge repo), it uses the Forgejo API; the real path would use gh/glab/tea. This is
// the ONLY production-touching command (design §12); upstream creds are used only here (AC-5).
func (a *App) Upstream(name string, gr *GraduateResult) (*forge.PullRequest, error) {
	if gr == nil || gr.Report == nil {
		return nil, fmt.Errorf("nothing graduated for %s", name)
	}
	if !gr.Report.Passed() {
		return nil, fmt.Errorf("refusing upstream: %d staging-main gate(s) failed (docs/goal.md AC-7)", len(gr.Report.BlockingFails))
	}
	m, err := a.loadMeta(name)
	if err != nil {
		return nil, err
	}
	upRepo := m.UpstreamRepo
	// Two upstreams: the local-default forge repo (e2e / repeatable self-test) uses the Forgejo API;
	// a real remote (imported with `sandforge import`) uses its git URL + `gh` to open the PR.
	realRemote := upRepo == ""
	var upURL string
	if realRemote {
		if m.UpstreamURL == "" {
			return nil, fmt.Errorf("no upstream recorded for %s — run `sandforge import` first", name)
		}
		upURL = m.UpstreamURL
	} else {
		upURL = a.authedRepoURL(upRepo)
	}

	// push the squashed graduate branch into the upstream, then open a PR -> default branch
	tmp, err := os.MkdirTemp("", "sf-upstream-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	// re-create the squashed branch: clone the GRADUATED branch (not always main), rebuild squash
	// against upstream, push — so `upstream <repo> <branch>` publishes exactly what it validated.
	srcBranch := gr.SourceBranch
	if srcBranch == "" {
		srcBranch = "main"
	}
	wkURL := a.authedRepoURL(name)
	if out, err := runGit(tmp, "clone", "-b", srcBranch, wkURL, "r"); err != nil {
		return nil, fmt.Errorf("clone working branch %s: %w\n%s", srcBranch, err, out)
	}
	wd := filepath.Join(tmp, "r")
	if out, err := runGit(wd, "remote", "add", "upstream", upURL); err != nil {
		return nil, fmt.Errorf("add upstream remote: %w\n%s", err, out)
	}
	if out, err := runGit(wd, "fetch", "upstream"); err != nil {
		return nil, fmt.Errorf("fetch upstream: %w\n%s", err, out)
	}
	upBranch := a.upstreamDefaultBranch(wd)
	base, err := runGit(wd, "merge-base", "upstream/"+upBranch, "HEAD")
	base = strings.TrimSpace(base)
	if err != nil || base == "" {
		return nil, fmt.Errorf("merge-base upstream/%s: %v\n%s", upBranch, err, base)
	}
	if out, err := runGit(wd, "checkout", "-B", "sandforge-graduate", base); err != nil {
		return nil, fmt.Errorf("checkout graduate base: %w\n%s", err, out)
	}
	if out, err := runGit(wd, "merge", "--squash", "origin/"+srcBranch); err != nil {
		return nil, fmt.Errorf("squash merge origin/%s: %w\n%s", srcBranch, err, out)
	}
	if out, err := runGit(wd, "commit", "-m", fmt.Sprintf("Sandforge: graduate %s", name)); err != nil {
		// "nothing to commit" means there is no delta to upstream — refuse to open an empty PR.
		return nil, fmt.Errorf("graduate produced no changes vs upstream/%s (refusing to open an empty PR): %w\n%s", upBranch, err, out)
	}
	if out, err := runGit(wd, "push", "-f", "upstream", "sandforge-graduate"); err != nil {
		return nil, fmt.Errorf("push to upstream: %w\n%s", err, out)
	}
	title := fmt.Sprintf("Sandforge graduation: %s", name)
	var pr *forge.PullRequest
	if realRemote {
		// Real upstream: open the PR with the host's existing gh/glab/tea auth (used ONLY here —
		// these creds never reach the runner/agents, docs/goal.md AC-5).
		pr, err = a.openRealUpstreamPR(wd, upURL, upBranch, "sandforge-graduate", title, gr.PRBody)
	} else {
		pr, err = a.Client.CreatePR(a.Cfg.Admin.Org, upRepo, "sandforge-graduate", upBranch, title, gr.PRBody)
	}
	if err != nil {
		return nil, err
	}
	a.Log.Decision("D-UPSTREAM", "Opened upstream PR #"+fmt.Sprint(pr.Number)+" for "+name,
		"single squash-by-default PR carrying specs+design+e2e+PRD report", "docs/goal.md#AC-6")
	a.Log.Event("upstream", "PR opened", map[string]any{"repo": redactCreds(upURL), "number": pr.Number, "url": pr.URL})
	return pr, nil
}

// openRealUpstreamPR opens a PR on a real remote using the gh CLI (GitHub). The graduate branch is
// already pushed to the upstream remote; we run `gh pr create -R <owner/repo>`. Returns a clear
// error if gh is missing or unauthenticated — never a silent failure on the production path.
func (a *App) openRealUpstreamPR(wd, upURL, base, head, title, body string) (*forge.PullRequest, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, fmt.Errorf("gh CLI not found — install it and `gh auth login` to open the upstream PR (branch %q is already pushed to %s)", head, redactCreds(upURL))
	}
	slug := githubSlug(upURL)
	if slug == "" {
		return nil, fmt.Errorf("could not derive owner/repo from upstream URL %s (only github.com is supported for the real-remote path today)", redactCreds(upURL))
	}
	bodyFile := filepath.Join(wd, ".sandforge-pr-body.md")
	if err := os.WriteFile(bodyFile, []byte(body), 0o644); err != nil {
		return nil, err
	}
	// Run gh from the cloned repo dir — `gh pr create` expects a git context even with -R.
	ghCmd := exec.Command("gh", "pr", "create", "-R", slug,
		"--base", base, "--head", head, "--title", title, "--body-file", bodyFile)
	ghCmd.Dir = wd
	ghCmd.Env = os.Environ()
	outB, err := ghCmd.CombinedOutput()
	out := string(outB)
	if err != nil {
		return nil, fmt.Errorf("gh pr create: %w\n%s", err, out)
	}
	url := strings.TrimSpace(lastLine(out))
	return &forge.PullRequest{Number: prNumberFromURL(url), URL: url, Title: title, State: "open"}, nil
}

// prNumberFromURL extracts the PR number from a github PR URL (…/pull/<n>); 0 if not found.
func prNumberFromURL(u string) int {
	const marker = "/pull/"
	i := strings.LastIndex(u, marker)
	if i < 0 {
		return 0
	}
	n := 0
	for _, r := range u[i+len(marker):] {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// githubSlug extracts "owner/repo" from a github URL (https or ssh), stripping creds and .git.
func githubSlug(u string) string {
	u = credInURL.ReplaceAllString(u, "://")
	u = strings.TrimSuffix(u, ".git")
	for _, marker := range []string{"github.com/", "github.com:"} {
		if i := strings.Index(u, marker); i >= 0 {
			return strings.Trim(u[i+len(marker):], "/")
		}
	}
	return ""
}

// upstreamDefaultBranch resolves the upstream remote's default branch (main/master/trunk/…) in the
// clone at wd. Falls back to "main" if it can't be determined — but tries to be right so wiring an
// upstream whose default isn't "main" doesn't fail graduate with a confusing rebase error.
func (a *App) upstreamDefaultBranch(wd string) string {
	// `git remote set-head upstream -a` writes refs/remotes/upstream/HEAD; then read it.
	runGit(wd, "remote", "set-head", "upstream", "-a")
	if out, err := runGit(wd, "symbolic-ref", "refs/remotes/upstream/HEAD"); err == nil {
		ref := strings.TrimSpace(out) // refs/remotes/upstream/<branch> (<branch> may contain slashes)
		const prefix = "refs/remotes/upstream/"
		if b := strings.TrimPrefix(ref, prefix); b != ref && b != "" {
			return b
		}
	}
	return "main"
}

// freeTCPPort asks the OS for an unused TCP port on the loopback interface and returns it as a
// string. There is an inherent race (the port could be taken before compose binds it), but the
// window is tiny and far safer than a hardcoded port that clashes with the developer's other
// services — the exact failure that broke graduate on a busy Docker host.
func freeTCPPort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		return "", err
	}
	return port, nil
}
