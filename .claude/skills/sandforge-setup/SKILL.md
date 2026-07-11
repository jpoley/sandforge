---
name: sandforge-setup
description: >-
  Fetch, install, diagnose and run Sandforge (the one-command local Git forge for AI-agent inner
  loops) end to end, including wiring @claude to the Claude Code CLI on a subscription (no API
  key). Use when the user wants to install/set up/start/run sandforge, connect coding agents to
  it, or find out why it won't come up (Docker not running, port busy, missing tools, …).
---

# Sandforge setup — install, diagnose, run, wire agents

You are setting up Sandforge for the user with **zero manual steps**. Work through the phases in
order. Never skip a failing check silently — report exactly what is missing and the one-line fix,
apply the fix yourself when it's safe (e.g. installing the binary), and ask only when the fix
needs the user (e.g. starting Docker Desktop the first time on macOS).

## Phase 1 — Preflight diagnostics

Run these checks (batch them in one shell call where possible) and show the user a short
✅/❌ report before doing anything else:

| Check | Command | On failure tell the user |
|---|---|---|
| Docker CLI | `command -v docker` | Install Docker: https://docs.docker.com/get-docker/ |
| Docker daemon **running** | `docker info` | macOS: open Docker Desktop (`open -a Docker`) or `colima start`; Linux: `sudo systemctl start docker` |
| Compose v2 | `docker compose version` | Install the compose plugin: https://docs.docker.com/compose/install/ |
| git | `command -v git` | Install git (xcode-select --install / apt install git) |
| Forge port free | `lsof -nP -iTCP:3000 -sTCP:LISTEN` (empty = free) | Another service owns :3000 — either stop it or use `SANDFORGE_HTTP_PORT=3001 sandforge init` |
| Claude Code CLI | `claude --version` | Only needed for @claude agent wiring (Phase 4) — install Claude Code |
| node/npm *(optional)* | `command -v node` | Only needed for `sandforge graduate` / `e2e` PRD checks |
| gh *(optional)* | `command -v gh` | Only needed for the final `sandforge upstream` PR to GitHub |

Docker (running) + git are **required** — stop and report if they can't be fixed. The optional
ones are informational; continue without them.

## Phase 2 — Install the binary (if `command -v sandforge` fails)

```bash
curl -fsSL https://raw.githubusercontent.com/jpoley/sandforge/main/scripts/install.sh | bash
```

The installer needs **no Go toolchain**: it tries a pre-built release, then local Go, then builds
inside a `golang` Docker container. It installs to `~/.local/bin` (override with
`SANDFORGE_INSTALL_DIR`). If `~/.local/bin` isn't on PATH, use the full path in later commands and
tell the user the `export PATH` line the installer printed. If every method fails, the installer
says exactly why — relay that verbatim.

## Phase 3 — Bring up the control plane

Run `sandforge init` **in the background** (it takes 1–3 minutes on first run: pulls images,
builds the warm CI image, health-gates the forge):

- Start it with a background shell call; then poll `sandforge status` (or wait for the init
  process to exit) instead of blocking.
- On success it prints the forge URL (`http://127.0.0.1:3000`), the admin login
  (`sandforge` / a **random per-instance password**) and a token prefix. The full credentials are
  at `~/.sandforge/sandforge/credentials` (0600) — point the user there; don't paste the password
  into the conversation unless they ask.
- `init` is idempotent — safe to re-run after fixing whatever it complained about. Its error
  messages name the exact remedy (busy port, docker socket gid, ssh:// DOCKER_HOST, …); relay
  them and apply the fix where safe.

## Phase 4 — Wire @claude (subscription, no API key)

Goal: `@claude` mentions on local PRs run the **Claude Code CLI under the user's subscription** —
no `ANTHROPIC_API_KEY` required or wanted.

1. Verify the CLI works on subscription auth: `claude -p "say ok"` (if it fails with an auth
   error, have the user run `claude` once to log in).
2. If `ANTHROPIC_API_KEY` is set in the environment, warn: with the key set, `claude -p` may bill
   the API instead of the subscription. The agent command below unsets it explicitly so routing
   stays on the subscription.
3. Register the agent and start the always-on router:

```bash
sandforge agents add claude --name Claude --kind review -- \
  sh -c 'unset ANTHROPIC_API_KEY; claude -p "Review the diff on this branch. Be concise. Flag security and correctness issues."'
sandforge agents start
sandforge agents status
```

The router daemon survives the terminal and is restarted by `sandforge init`. The web UI is at
`http://127.0.0.1:3999`.

## Phase 5 — Verify and report

1. `sandforge status` — control plane healthy.
2. Optionally import the user's repo: `sandforge import <upstream-url> [name]`.
3. Smoke-test the agent wiring without a webhook:
   `sandforge agents trigger <repo> <pr#> claude -m "@claude review"` (needs an imported repo
   with a PR), or at minimum confirm `sandforge agents status` shows the daemon running.
4. Report to the user: forge URL, where credentials live, agent router URL, and what (if
   anything) was skipped and why.

## Troubleshooting quick table

| Symptom | Cause → fix |
|---|---|
| `compose up … address already in use` | Port 3000 busy → `SANDFORGE_HTTP_PORT=<port> sandforge init` |
| Runner crash-loops `permission denied` on docker.sock | Socket gid probe failed → error text names the fix; remote/TCP daemons: `SANDFORGE_RUNNER_MODE=tcp` |
| CI jobs hang forever | Stale/foreign-arch CI image → `sandforge init` rebuilds when the embedded Dockerfile changed; else `docker rmi sandforge/ci:ubuntu-22.04` and re-run `init` |
| `DOCKER_HOST=ssh://…` error | Unsupported for the runner → expose the daemon over `tcp://` or use a local socket |
| `@claude` never fires | Router not running (`sandforge agents status`), or the forge container can't reach the host listener → set `SANDFORGE_AGENTS_HOST` on Linux bridge networks |
| Agent replies but bills the API | `ANTHROPIC_API_KEY` set in the router env → use the `unset ANTHROPIC_API_KEY;` wrapper from Phase 4 |

Full docs: `docs/userguide.md` (using sandforge) and `docs/agents.md` (agent router) in
https://github.com/jpoley/sandforge.
