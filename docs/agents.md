# Sandforge Local Agents — a local "Copilot"

Sandforge can subscribe to your local Forgejo's events and route `@mention` triggers (e.g.
`@claude`, `@codex`) to coding agents that **review** a PR (comment back) or **hand off a fix**
(push a commit to the PR branch) — entirely on your machine, with a web UI to manage it and
everything persisted to files. It emulates the GitHub Copilot coding-agent experience locally.

This feature is **opt-in and separable** from the core inner-loop forge: only the `sandforge agents`
commands touch it, and it does not change the `init`/`graduate`/`upstream`/`e2e` contract.

---

## How it works

```
you @mention an agent on a PR/issue
        │
        ▼
Forgejo fires a webhook  ──►  the router (HMAC-verifies, parses)
                                   │  finds @handle → matching agent
                                   ▼
                              dispatcher: clone repo @ PR head
                                   │  run the agent's command (env carries repo/PR/comment/token)
                                   ├─► post the agent's output back as a comment   (review)
                                   └─► push any commits the agent made to the branch (fix/handoff)
                                   ▼
                              persist every step to events.jsonl  ◄── the web UI reads this
```

- **PR comments** arrive from Forgejo as `issue_comment` events with `is_pull: true`; new commits
  arrive as `pull_request` events. The router subscribes to both.
- **Loop guard:** every comment an agent posts carries a hidden marker (`<!-- sandforge-agent -->`);
  the router ignores any inbound comment that contains it, so an agent never re-triggers itself.
- **Webhook security:** deliveries are signed (HMAC-SHA256) with a generated secret and verified
  before anything runs.

---

## Two ways to run it

| | **Host daemon** (`agents start`) | **Container** (`agents up`) |
|---|---|---|
| What runs the agent commands | the host (uses your installed `git` + agent CLIs) | inside the router image |
| Reaches Forgejo via | `host.docker.internal` | the compose network (`forgejo:3000`) |
| Always-on | yes — restarted by `sandforge init` | yes — `restart: unless-stopped` |
| Real agent CLIs (claude/codex) | use your host install directly | bake them into the image (or use the host daemon) |
| Needs the source repo | no (works from the standalone binary) | yes (the image builds from source) |

Both expose the same web UI and JSON API, and share the same files (agents + event log).

---

## Quickstart

You need a running control plane first:

```bash
sandforge init                 # brings up Forgejo + runner + Postgres
sandforge import <git-url>     # seed a repo to work against (or use one you already imported)
```

### Option A — host daemon (recommended for real agent CLIs)

```bash
sandforge agents start --repo <owner>/<name>     # detached; survives the terminal, restarted by init
sandforge agents status                          # running? where?
open http://127.0.0.1:3999                        # the web UI
```

### Option B — container (fully "in a container")

```bash
sandforge agents up --repo <owner>/<name>        # builds the router image + runs it on the network
open http://127.0.0.1:3999
sandforge agents down                            # tear it down
```

Then **@mention an agent on any PR or issue** in your local Forgejo
(`http://127.0.0.1:3000`), e.g. a PR comment:

```
@claude please review this --focus=security
```

Within seconds the agent's review appears as a reply, and the run shows up in the UI timeline.

> The shipped `@claude` / `@codex` agents are **safe echo stubs** so the loop works out of the box
> with nothing installed. Point their command at a real CLI (below) to go live.

---

## Managing agents

### In the web UI (`http://127.0.0.1:3999`)

- **Agents** panel — add/edit/enable/disable/delete agents, set the command, kind, and "auto-run
  on PR open".
- **Manual trigger** — invoke any agent against an owner/repo/issue right now (no webhook needed).
- **Timeline** — every delivery, route, and result with status, duration, and the agent's output
  (click a row to expand). Auto-refreshes.

### From the CLI

```bash
sandforge agents list
sandforge agents add claude --name Claude --kind review -- claude -p "Review this PR and comment."
sandforge agents add fixbot --kind implement -- sh -c 'your-agent --apply && git add -A && git commit -m fix'
sandforge agents trigger <repo> <issue#> <handle> [--pull] [-m "instruction"]
```

> Flag order: for `add`, flags go **before** the `--`, and the command follows `--`. For `trigger`,
> the three positionals (repo, issue, handle) come first, then `--pull` / `-m`.

---

## Wiring a real agent CLI

When an agent fires, its command runs with **cwd = a fresh clone of the repo** at the PR head (or
the default branch for a plain issue), and this environment:

| Env var | Meaning |
|---|---|
| `SANDFORGE_HANDLE` | the matched handle (e.g. `claude`) |
| `SANDFORGE_KIND` | `review` or `implement` |
| `SANDFORGE_REPO` / `SANDFORGE_OWNER` / `SANDFORGE_NAME` | `owner/name`, and the parts |
| `SANDFORGE_ISSUE` | the issue/PR number |
| `SANDFORGE_IS_PULL` | `true` on a PR |
| `SANDFORGE_BRANCH` | the PR head branch (empty for a plain issue) |
| `SANDFORGE_COMMENT` | the triggering comment (the instruction) |
| `SANDFORGE_TRIGGER` | `mention`, `manual`, or `on_open` |
| `SANDFORGE_FORGE_URL` | the forge base URL the clone came from |
| `SANDFORGE_TOKEN` | a **local-Forgejo-only** token (never an upstream/production cred) |

**Contract:**
- Whatever the command prints to stdout/stderr is posted back as the PR/issue comment (the review).
- If the command makes **git commits** in the clone and it's a PR, the commits are pushed to the
  head branch (the fix/handoff).
- A non-zero exit posts a visible `⚠️ agent … failed` comment — it never fails silently.

Examples:

```bash
# Claude Code as a reviewer (host daemon; claude must be on PATH):
sandforge agents add claude --kind review -- \
  sh -c 'claude -p "Review the diff on this branch. Be concise. Flag security + correctness." '

# Codex as a fixer that edits + commits:
sandforge agents add codex --kind implement -- \
  sh -c 'codex exec "$SANDFORGE_COMMENT" && git add -A && git commit -m "codex: $SANDFORGE_COMMENT" || true'
```

For the **container** path, the CLI must exist *inside the image* — extend
`deploy/agents-router/Dockerfile` to install it (and mount any credentials), or use the host daemon,
which uses your existing host install directly.

---

## Files (everything persists)

Under `~/.sandforge/<project>/agents/`:

| File | What |
|---|---|
| `config.json` | the agents + the webhook HMAC secret (mode `0600`) |
| `events.jsonl` | append-only timeline of every delivery/route/result (greppable) |
| `daemon.json` / `daemon.pid` | the host daemon's desired config + running pid |
| `daemon.log` | the host daemon's stdout/stderr |

The container mounts this same directory, so the host CLI and the container agree on state.

---

## Security model

- The listener binds all interfaces so the Forgejo container can deliver webhooks. Therefore:
  - `/webhook` is **HMAC-verified** (a wrong/absent signature is rejected).
  - the UI and every command-executing API route are **restricted to loopback callers** in host
    mode (a LAN request gets `403`). In container mode the port is published as `127.0.0.1:<port>`
    on the host, which is the boundary instead.
- The token handed to agents is the **local-Forgejo-only** token. Your upstream/production
  credentials are never injected into an agent (consistent with the core forge's AC-5).
- Threat model is the same as the rest of Sandforge: a *messy* agent, not a malicious one, on a
  single-developer local machine.

---

## Lifecycle & integration

- `sandforge init` restarts the host daemon **if you previously enabled it** (`agents start`).
- `sandforge down` stops the daemon/container but keeps it enabled, so the next `init` brings it
  back. `sandforge agents stop` disables it; `sandforge reset` wipes everything.
- Webhooks are registered **idempotently** (existing hook with the same URL is reused), so
  restarting never creates duplicates.

---

## Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| No reply to a mention | Is the daemon/container running? `sandforge agents status`. Is the repo wired? `--repo`. |
| `webhook signature mismatch` | The hook secret and the router's secret diverged — re-run `agents start`/`up` (it re-registers) or recreate the hook. |
| Deliveries never arrive (Linux) | `host.docker.internal` may not resolve — set `SANDFORGE_AGENTS_HOST` to your docker bridge gateway, or use `agents up` (container, no host.docker.internal needed). |
| `agent … failed: command failed` | The agent command errored — open the timeline row for its output, or check `daemon.log`. |
| Container UI returns 403 | You're on an old build; the container disables the loopback guard (the published port is the boundary). Rebuild: `sandforge agents down && agents up`. |
| `containerized router needs the sandforge source` | `agents up` builds from source; run it from a checkout, or use `agents start` (host daemon) with the standalone binary. |

---

## Command reference

```
sandforge agents up      [--port N] [--repo owner/name]   # run as a container on the control-plane network
sandforge agents down                                     # tear down the container
sandforge agents start   [--port N] [--repo owner/name]   # always-on host daemon (restarted by init)
sandforge agents stop                                     # stop + disable the daemon
sandforge agents status                                   # daemon running? where?
sandforge agents serve   [--port N] [--repo owner/name]   # run in the foreground (Ctrl-C)
sandforge agents list                                     # list configured agents
sandforge agents add <handle> [--name N] [--kind K] [--on-open] -- <command...>
sandforge agents trigger <repo> <issue#> <handle> [--pull] [-m "instruction"]
```
