# Sandforge — User Guide

> **Sandforge is the fastest, fully-local inner loop of the AI SDLC.** It stands up a local Git
> forge (Forgejo) + a warm GitHub-Actions runner (`act_runner`) with **one command**, so one or
> more AI coding agents can iterate code → review → fix → run real CI, then graduate **one clean
> PR** to your real upstream. (Today `sandforge upstream` opens that PR on **GitHub** via `gh`,
> or on the local-default **Forgejo** via its API; GitLab/other hosts are planned.)
>
> **You do not need this repository.** Install the single `sandforge` binary and you have
> everything — all deploy assets are embedded.

---

## 1. Install (no repo checkout required)

Sandforge is one self-contained binary. Pick whichever you prefer:

```bash
# A) Go toolchain (Go 1.25+)
go install github.com/jpoley/sandforge/cmd/sandforge@latest   # drops `sandforge` in $(go env GOPATH)/bin

# B) Download a release binary, then put it on your PATH
#    (macOS arm64/amd64, Linux arm64/amd64)
chmod +x sandforge && sudo mv sandforge /usr/local/bin/

# verify
sandforge --help
```

**Prerequisites** (the only things Sandforge does *not* bundle):

| Need | Why | Check |
|------|-----|-------|
| Docker (Desktop or Engine) 24+ with the **compose** plugin | runs the forge + CI | `docker info && docker compose version` |
| `git` | agents push/pull | `git --version` |
| **Node + npm** *(for `graduate`/`e2e` only)* | the deploy-target's frontend + Playwright checks (SC-6/7/8) run on the host via `verify.sh` | `node --version && npm --version` |
| `gh` *(optional)* | only for pushing the final PR to a **real** GitHub upstream | `gh auth status` |

> Sandforge runs CI on **whichever Docker daemon your own `docker` CLI already uses** — the same one
> `docker compose` targets — with **no Docker-in-Docker**. It auto-detects how to connect and logs
> the choice:
> - **local unix socket** (standard Docker, Docker Desktop, rootless, Colima) → the runner mounts it;
>   the socket group id is auto-detected. Works out of the box on macOS/Windows Docker Desktop.
> - **TCP daemon** (`DOCKER_HOST=tcp://…`, or a docker *context* pointing at one) → the runner
>   **dials that endpoint** instead. **No socket is mounted and no root/group access is needed** —
>   this is the path for hosts where you can't bind-mount `/var/run/docker.sock` (Docker Desktop's
>   TCP option, a locally-exposed socket, rootless with a TCP proxy).
>
> **Scope for v1:** tcp mode targets a daemon whose **published ports are reachable at `127.0.0.1`**
> (the local machine, or a VM that forwards loopback like Docker Desktop). A *fully remote* build
> host is **not** supported yet: the forge (and the on-demand deploy-target stack at `graduate`) are
> published on the daemon's host, but the CLI health-gates and hands agents clone URLs at
> `127.0.0.1`. Override the auto-choice with `SANDFORGE_RUNNER_MODE=socket|tcp`. (`ssh://` DOCKER_HOST
> isn't supported — expose the daemon over `tcp://` instead.)

---

## 2. The 60-second loop

```bash
sandforge init                       # bring up the control plane (forge + Postgres + warm runner)
# → prints: forge http://127.0.0.1:3000   login sandforge / sandforge-dev   token …

sandforge import https://github.com/you/yourrepo.git   # seed a working repo from your upstream
sandforge status                     # one-screen health + active work/* branches
```

`init` is **idempotent and resumable** — run it again any time; it heals the control plane. It
also *warms* the runner so your **first** CI push is already fast.

The forge is at a **stable** `http://127.0.0.1:3000` across restarts (no `/etc/hosts`, no sudo).
Login: `sandforge` / `sandforge-dev`. An API token is written to `~/.sandforge/<project>/credentials` (mode `0600`).

---

## 3. Wire up *any* repo

You have two ways in:

**A) Import from your upstream (recommended).** Keeps a link back to upstream for the final PR:

```bash
sandforge import https://github.com/you/yourrepo.git   yourrepo
# Creates a writable forge repo at http://127.0.0.1:3000/sandforge/yourrepo
# and a bare clone on your host for cheap per-agent worktrees.
```

**B) Push an existing local checkout** straight into the forge:

```bash
TOKEN=$(jq -r .token ~/.sandforge/sandforge/credentials)
git remote add sandforge http://sandforge:$TOKEN@127.0.0.1:3000/sandforge/yourrepo.git
git push sandforge HEAD:main
```

### What CI runs

Sandforge runs your **real** GitHub Actions workflows (`.github/workflows`, and
`.forgejo/workflows`) via `act_runner` on the host Docker daemon — ~95% compatible with GitHub.
The warm CI job image bakes Go + Node so there is no per-run toolchain download.

### The branch model

![Branch model — work/<agent>/<slug> to staging to main, gated at each promotion](diagrams/branch-model.png)

- `work/*` — agent playground, no protection; your CI workflow (**build + unit**) runs on every push here, fast.
- `staging` — the promotion target for green work branches (`sandforge` creates it on import).
- `main` — clean, protected; the branch `graduate` / `upstream` act on. The full **e2e + PRD
  success-criteria** validation runs at `graduate` (against an on-demand deploy-target stack), not
  on a plain push.

---

## 4. Wire up the top coding agents

The trick is universal: **point the agent's git remote at the Sandforge forge and let it push
`work/<agent>/<slug>` branches.** Agents never call the `sandforge` CLI — they just use `git`
against `http://127.0.0.1:3000`. Give each agent its **own worktree** so three can run with zero
collisions.

First, capture the clone URL with the token once:

```bash
export SF_TOKEN=$(jq -r .token ~/.sandforge/sandforge/credentials)
export SF_REMOTE="http://sandforge:$SF_TOKEN@127.0.0.1:3000/sandforge/yourrepo.git"
# Give each agent an isolated worktree off one shared bare clone:
git clone --bare "$SF_REMOTE" ~/.sf/yourrepo.git
git -C ~/.sf/yourrepo.git worktree add ~/work/claude  -b work/claude/feature  main
git -C ~/.sf/yourrepo.git worktree add ~/work/codex   -b work/codex/feature   main
git -C ~/.sf/yourrepo.git worktree add ~/work/cursor  -b work/cursor/feature  main
```

Then start each agent **in its own worktree directory**:

| Agent | How to wire it |
|-------|----------------|
| **Claude Code** (Anthropic) | `cd ~/work/claude && claude` — it commits and you `git push origin work/claude/<slug>`. Add a `CLAUDE.md` note: "push to the `origin` remote (the local Sandforge forge); CI runs automatically." |
| **OpenAI Codex CLI** | `cd ~/work/codex && codex` — point its task at the worktree; push `work/codex/<slug>`. |
| **Cursor** (Composer/Agent) | Open `~/work/cursor` as the project; set the repo's git remote to `$SF_REMOTE`. Cursor's agent pushes branches; CI fires on push. |
| **GitHub Copilot / Copilot CLI** | Use Copilot in the worktree; `gh`/`git push` to the Sandforge remote. (Copilot *review* happens later, on the upstream PR — see §6.) |
| **Aider** | `cd ~/work/aider && aider` — it auto-commits; `git push origin work/aider/<slug>` triggers CI. |
| **Gemini CLI / Jules** | Work in a worktree; push `work/gemini/<slug>`. |
| **Cline / Windsurf / Continue** | Same pattern — set the workspace git remote to `$SF_REMOTE`, push `work/*`. |

After a push, watch the run:

```bash
sandforge status                     # shows active work/* branches + last check-runs
sandforge logs act_runner -f         # live CI output
```

---

## 5. The local agent PR-review loop — the whole point

This is the reason Sandforge runs a real forge (Forgejo) and **not** a bare git server. A bare
`git` remote can receive pushes — and nothing else. Forgejo gives you **GitHub's pull-request,
review-thread, and check-run surface, locally and instantly**, so one agent's work is reviewed by
*another* agent and gated on real CI **before** it is ever promoted — with no GitHub minutes, no
rate limits, and no cloud round-trip.

![A local GitHub for the inner loop — agents open PRs and review each other locally; only the final curated PR leaves the machine](diagrams/local-architecture.png)

**The loop, end to end:**

1. **Agent A** pushes `work/<agent>/<slug>`. **Forgejo Actions** queues the workflow jobs; the
   registered **`act_runner`** (which polls Forgejo) picks them up and runs your **real**
   `.github/workflows` (build + unit) as containers on the host Docker daemon, then posts a
   **check-run** back onto the commit.
2. **Open a PR** (`work/<agent>/<slug>` → `staging`) — in the web UI at
   `http://127.0.0.1:3000/sandforge/yourrepo/pulls`, or over the REST API (it is the **same shape
   as GitHub's**).
3. The PR shows the CI **check-run status** (pass/fail) — promotion is gated on green.
4. **Agent B — the reviewer** — reads the PR diff and posts review comments via that same API. It
   never calls the `sandforge` CLI; it only needs the forge URL + the token.

Because the API matches GitHub's, any agent, prompt, or tool that already speaks GitHub PRs works
unchanged — just pointed at `127.0.0.1:3000`:

```bash
export SF_TOKEN=$(jq -r .token ~/.sandforge/sandforge/credentials)
API=http://127.0.0.1:3000/api/v1/repos/sandforge/yourrepo

# Agent A — open the PR (work branch -> staging)
curl -s -H "Authorization: token $SF_TOKEN" -H 'Content-Type: application/json' \
  -d '{"head":"work/claude/feature","base":"staging","title":"Add tasks filter","body":"why + what"}' \
  "$API/pulls"

# Agent B (reviewer) — read the diff, then leave review comments on the PR
curl -s -H "Authorization: token $SF_TOKEN" "$API/pulls/1.diff"
curl -s -H "Authorization: token $SF_TOKEN" -H 'Content-Type: application/json' \
  -d '{"body":"nit: guard the empty-list case; otherwise LGTM"}' \
  "$API/issues/1/comments"
```

To wire a **reviewer agent**, give a second agent (Claude Code, Codex, …) the PR number, the diff
URL, and `SF_TOKEN`, and let it post comments via that endpoint (or drive the web UI / `tea` / a
`gh` pointed at the forge). Everything is local and flat-cost — instant turnaround, no Actions
minutes; the self-test's review handoff lands in **~0.3s** (AC-3).

> This local review loop is distinct from the **upstream** PR (§6). Agents review each other *here*,
> on the forge; only the final, curated, already-reviewed PR is pushed to your real upstream.

---

## 6. Graduate → one clean PR

When a branch is good, turn the messy sandbox history into **one validated PR**:

```bash
sandforge graduate yourrepo main
# 1. rebases onto fresh upstream
# 2. spins the on-demand deploy-target stack (LB + app + DB) on a free port
# 3. runs full e2e + machine-checks the PRD success criteria (docs/prd.md format)
# 4. tears the stack down, assembles a squash-by-default PR body
#    (specs + design + e2e summary + PRD success-criteria report)
```

`graduate` **blocks** if any `staging-main` success criterion fails — "done" is machine-checked,
not a checkbox.

```bash
sandforge upstream yourrepo main     # the ONLY production-touching command. It runs `graduate`
# first (rebase + e2e + PRD gates) and, only if all staging-main gates pass, opens ONE squash PR
# on your real upstream — via the Forgejo API for the local-default upstream, or via `gh` for a
# real GitHub remote (it reuses your existing `gh auth`).
```

Your upstream production credentials are used **only** here — they are never injected into the CI
runner or any agent environment.

---

## 7. Validate the whole thing

```bash
sandforge e2e                        # stand it up and run the WHOLE closed loop on a throwaway
                                     # instance, asserting every acceptance criterion. Exits 0 only
                                     # if all pass; prints a per-AC / per-SC table.
sandforge e2e --no-playwright        # same, skipping the browser UI criteria (faster)
```

This is the headline self-test: `init → 3 concurrent agents → warm CI → review handoff → promote
→ graduate (deploy-target + PRD) → upstream PR → isolation scan`, fully automated.

---

## 8. Commands

| Command | Description |
|---------|-------------|
| `sandforge init` | Bring up the control plane (forge + Postgres + warm runner). Idempotent. |
| `sandforge import <url> [name]` | Seed a writable repo from upstream; keep upstream as a remote. |
| `sandforge status` | One-screen instance summary. |
| `sandforge logs [service] [-f]` | Tail control-plane (or a service's) logs. |
| `sandforge graduate <repo> <branch>` | Rebase onto upstream, spin deploy-target, run e2e + PRD validation. |
| `sandforge upstream <repo> <branch>` | Graduate then open the upstream PR (only prod-touching cmd). |
| `sandforge e2e [--no-playwright]` | Stand it up and run the whole closed loop, validating every AC. |
| `sandforge reset` | Wipe instance state + rotate credentials. |
| `sandforge down [--keep]` | Tear down the control plane (optionally keep volumes). |

---

## 9. Configuration (optional — most users never touch it)

Defaults are embedded and sensible. Override via environment or a `sandforge.yaml` in your cwd:

| Env | Default | Meaning |
|-----|---------|---------|
| `SANDFORGE_PROJECT` | `sandforge` | instance id / compose project name (run several side by side) |
| `SANDFORGE_HTTP_PORT` | `3000` | the fixed forge port |
| `SANDFORGE_NETWORK` | `sandforge-net` | docker network job containers attach to |
| `SANDFORGE_CI_IMAGE` | `sandforge/ci:ubuntu-22.04` | CI job image for `runs-on: ubuntu-latest` |
| `SANDFORGE_DOCKER_SOCKET` | `/var/run/docker.sock` | host docker socket (for rootless / custom setups; socket mode) |
| `SANDFORGE_RUNNER_MODE` | `auto` | how the runner reaches Docker: `auto` \| `socket` (mount) \| `tcp` (dial `DOCKER_HOST`) — never DinD |
| `DOCKER_HOST` | *(your docker's)* | honored as-is: `tcp://…` selects tcp mode; `unix://…` selects socket mode |
| `SANDFORGE_EPHEMERAL` | unset | `1` = disposable instance (anonymous volumes, nuked on `down`) |

```yaml
# sandforge.yaml (all optional)
project: myinstance
http_port: 3000
history: squash          # squash | rebase | merge  (how graduate curates history)
images:
  ci: sandforge/ci:ubuntu-22.04
```

---

## 10. Troubleshooting

Sandforge is built to **fail loudly with the real cause** — if something breaks, the error names
it. Common cases:

| Symptom | Cause & fix |
|---------|-------------|
| `docker daemon not reachable` | Start Docker Desktop / the engine. `docker info` must work. |
| `runner is crash-looping … permission denied … docker.sock` | Socket mode: the error dumps the runner logs + the injected gid. Usually a non-standard socket — set `SANDFORGE_DOCKER_SOCKET`. If you can't mount the socket at all, use a TCP daemon: `DOCKER_HOST=tcp://…` (or `SANDFORGE_RUNNER_MODE=tcp`). |
| `docker socket … not found` at init | You're on a remote/TCP Docker with no local socket. Set `DOCKER_HOST=tcp://…` so sandforge auto-selects tcp mode (the runner dials it — no socket needed). |
| forge port `3000` in use | Another service holds it. Stop it, or set `SANDFORGE_HTTP_PORT`. |
| CI job fails to build | Run `sandforge logs act_runner`; the full job log is in the forge UI under the repo's Actions tab. |
| Want a clean slate | `sandforge reset` wipes state + rotates the token; `sandforge down --keep` keeps volumes. |

For the full catalogue of failure modes and how each is guarded, see
[`premortem.md`](premortem.md).
