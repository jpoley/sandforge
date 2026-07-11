# Sandforge — Premortem & Resilience Ledger

> **Purpose.** Enumerate everything that can make `sandforge` fail — especially *fail silently* —
> and record the structural fix for each. Grounded in real failures observed on a macOS / Apple
> Silicon / Docker Desktop 29.5 host, not theory. Every row is either **FIXED** (with the commit
> mechanism) or **GUARDED** (a loud check that converts a silent hang into an actionable error).
>
> The guiding rule, from the user: **it must never fail silently.** A wait-loop that times out with
> a generic message is a bug. Every failure must name its cause and the next action.

---

## A. Failure classes (the root patterns)

These are the *categories*; the table in §B maps concrete failure modes onto them.

| Class | Description | Archetype seen |
|-------|-------------|----------------|
| **C1 host-vs-container identity** | A value read on the host is meaningless inside a container (uid/gid, socket ownership, `127.0.0.1`, paths). | Docker socket GID: host path GID ≠ in-container GID (Docker Desktop proxies it to gid 0). |
| **C2 BSD-vs-GNU userland** | A command assumed to be GNU behaves differently (or errors) on macOS/BSD. | `stat -c` is GNU-only; macOS `stat` errored → fell to a default. |
| **C3 silent default fallback** | On detection failure, code *guesses* a default instead of failing loud. | `detectDockerGID` returned hardcoded `"1001"` → runner crash-looped forever. |
| **C4 arch mismatch** | Toolchain/image/binary architectures disagree (amd64 vs arm64). | CI image arm64 + amd64 Go toolchain → `gcc: -m64 unrecognized`. |
| **C5 readiness-by-logstring** | "Up" inferred from a log substring that is fragile or printed before real readiness. | `waitRunnerOnline` matched a registration-time string the daemon never prints. |
| **C6 fixed-port clash** | A hardcoded host port collides with the developer's other services. | Deploy-target LB `:8088` already held by an unrelated `traefik`. |
| **C7 stale/leftover state** | A prior aborted run leaves containers/volumes/ports/locks behind. | Orphaned deploy-target stack holding its port + volume. |
| **C8 version/API drift** | A pinned client API version mismatches the running server. | `DOCKER_API_VERSION: 1.44` pinned vs server 29.5.3. |
| **C9 network reachability** | A service name resolvable in one network isn't in another. | Job containers must reach `forgejo:3000`, not `127.0.0.1:3000`. |
| **C10 missing host dependency** | A required host tool (docker, compose, git, gh) is absent or unauthenticated. | `docker compose` plugin missing; `gh` not logged in. |

---

## B. Concrete failure modes → fix/guard

### Bootstrap / control plane

| # | Failure mode | Class | Status | Fix / Guard |
|---|--------------|-------|--------|-------------|
| 1 | Docker socket GID wrong → runner `permission denied`, crash-loops, generic 60s timeout | C1,C2,C3 | **FIXED** | `config.DetectDockerGID` probes the socket GID *inside a container* (`docker run --entrypoint stat <runner-image>`); injected as `group_add`. No host `stat -c`. |
| 2 | Detection can't determine GID → silently guesses 1001 | C3 | **FIXED** | Detection now returns a **loud error**; `init` aborts with a remediation hint. No default guess. |
| 3 | Runner crash-loops but `init` waits the full timeout then says only "did not come online" | C5 | **FIXED** | `waitRunnerOnline` watches `RestartCount`/`State.Restarting`, **fails fast**, and dumps the container's last log lines + the injected GID + socket path. |
| 4 | Readiness matched a log string the daemon never prints | C5 | **FIXED** | Readiness now requires container `State.Running` **and** a positive daemon signal (`declared successfully` / `poller … launched`) **and** absence of the `cannot ping the docker daemon` error line. |
| 5 | Forge not up before clone URL printed | C9 | **GUARDED** | `WaitHealthy` polls `/api/healthz` for `"pass"` before proceeding (design §14). |
| 6 | `docker` / `compose` absent | C10 | **GUARDED** | `compose.MustDocker` checks `docker info` + `docker compose version` up front. |
| 7 | Control-plane port 3000 already in use | C6 | **FIXED** | `init` detects the `already allocated` / `address already in use` case and fails loud, naming the port and the `SANDFORGE_HTTP_PORT` override (design mandates a *stable* 3000, so we explain rather than auto-reassign). |
| 8 | `DOCKER_API_VERSION` pinned to 1.44 vs newer engine | C8 | **GUARDED** | Overridable via `SANDFORGE_DOCKER_API`; 1.44 is a backward-compatible floor that the proven-working Docker 29 server honors. Left as a documented, overridable default rather than churning a working path. |
| 9 | Admin/token seeding not idempotent on re-run | C7 | **GUARDED** | `ensureAdmin` tolerates "already exists"; `ensureToken` reuses a valid saved token, else mints a uniquely-named one. |
| 10 | Stale runner registration after `reset` | C7 | **GUARDED** | `reset` wipes the state dir (incl. `runner/.runner`); registration re-runs cleanly. |

### CI / inner loop

| # | Failure mode | Class | Status | Fix / Guard |
|---|--------------|-------|--------|-------------|
| 11 | CI image arm64 + amd64 Go → `gcc -m64` failure on every job | C4 | **FIXED** | Dockerfile uses BuildKit's auto `TARGETARCH` (no `=amd64`); Go tarball is arch-matched. |
| 12 | Future arch mismatch slips back in silently | C4 | **FIXED** | Image build **smoke test** asserts `GOARCH == GOHOSTARCH == dpkg arch` — a mismatch fails the image build, not every CI job later. |
| 13 | cgo C-toolchain dependence in CI | C4 | **FIXED** | Workflow sets `CGO_ENABLED=0` (backend is pure Go) — no gcc invoked at all. |
| 14 | Docker 29 tar-extract hang via `/var/run`→`/run` symlink | — | **FIXED** | CI image de-symlinks `/var/run` to a real dir (D-009). |
| 15 | Job containers can't resolve the forge | C9 | **GUARDED** | Runner `config.yml` sets `container.network` to the compose net; checkout reaches `forgejo:3000`. |
| 16 | `WaitRunComplete` hangs forever on a stuck run | C5 | **GUARDED** | 5-minute deadline + terminal-status set; returns a timeout error, not a hang. |

### Graduate / deploy-target / upstream

| # | Failure mode | Class | Status | Fix / Guard |
|---|--------------|-------|--------|-------------|
| 17 | Deploy-target LB port 8088 clash with host services | C6 | **FIXED** | `Graduate` picks an OS-assigned free port (`freeTCPPort`); explicit `TASKS_LB_PORT` still wins. |
| 18 | `prd.md` moved to `docs/` → graduate can't find it | C7 | **FIXED** | `resolvePRDPath` checks `docs/prd.md` then legacy root. |
| 19 | Leftover deploy-target stack from an aborted graduate | C7 | **FIXED** | `graduate` runs `compose down -v` for the deploy-target project before `up`, and again on `defer`. |
| 20 | Rebase-onto-upstream conflict | — | **GUARDED** | Surfaces the conflict in-loop with a clear error (design §7) rather than pushing a broken tree. |
| 21 | Upstream creds leak into runner/CI | — | **GUARDED + TESTED** | Canary planted in the CLI env; AC-5 scan asserts it's absent from runner env *and* CI logs. |

### Cross-cutting

| # | Failure mode | Class | Status | Fix / Guard |
|---|--------------|-------|--------|-------------|
| 22 | Any `exec.Command` failure swallowed | C3 | **GUARDED** | Command errors are wrapped with context + combined output throughout; wait-loops dump state on timeout. |
| 23 | Binary requires the repo checkout for `deploy/` assets | C7 | **FIXED** | Deploy assets + `docs/prd.md` are embedded as a tarball (`assets/deploy.tar.gz`) and materialized to a content-addressed dir under `~/.sandforge` at runtime. A tarball (not `//go:embed all:`) is used because go:embed silently drops the nested-module `backend/` and errors on npm symlinks. The standalone binary runs the full `e2e` green from outside the repo. |
| 24 | `make` required (user wants none) | — | **FIXED** | Makefile removed; `magefiles/magefile.go` provides all targets (`mage build/e2e/install/...`). |
| 25 | go:embed silently drops nested-module `backend/` (own go.mod) | C7 | **FIXED** | Embed via tarball; `genAssets` + `Materialize` both assert `backend/main.go` + `ci.yml` are present, failing loud if not. |

### Review-round findings (subagent + codex review) — all fixed

| # | Failure mode | Class | Status | Fix / Guard |
|---|--------------|-------|--------|-------------|
| 26 | `WaitRunComplete` matched a task with empty `head_sha` → reported another agent's run as ours (false green/red) | C5 | **FIXED** | Skip empty-`HeadSHA` tasks; require a real prefix match; `continue` (not `break`) so a later terminal task for the same SHA is found. |
| 27 | `SimulateAgent` swallowed `rev-parse` error → empty SHA fed into the above | C3 | **FIXED** | Empty/failed HEAD is now a hard error before push/wait. |
| 28 | `prd.Runner.Timeout` was dead → a hung `verify.sh` hangs the whole CLI forever | C5 | **FIXED** | `exec.CommandContext` with the timeout; a timeout is a non-silent FAIL. |
| 29 | `Upstream` swallowed the squash chain → could open an EMPTY PR reported as success | C3 | **FIXED** | Every git step checked; "nothing to commit" refuses to open an empty PR. |
| 30 | Concurrent `git worktree add` on one bare repo → intermittent AC-4 flake | C7 | **FIXED** | Serialized behind a mutex (push + CI wait stay parallel). |
| 31 | Upstream default branch hardcoded `main` (despite a "detect" comment) | — | **FIXED** | `upstreamDefaultBranch` reads `refs/remotes/upstream/HEAD`, preserving slashes (`release/2026`). |
| 32 | `loadMeta` error swallowed in E2E → nil-deref panic | C3 | **FIXED** | Checked + wrapped. |
| 33 | Materialize TOCTOU: two processes corrupt one hash dir | C7 | **FIXED** | Extract to a temp dir + atomic rename; adopt a winner's dir; prune stale hashes. |
| 34 | Forgejo **token leaked into committed `.logs/`** | — | **FIXED** | `.logs/` gitignored + untracked; logs moved to the state dir; authed URLs redacted before logging. |
| 35 | `go install sandforge/...@latest` impossible (module path had no dot) | — | **FIXED** | Module renamed to `github.com/jpoley/sandforge`. |
| 36 | Default `/var/run/docker.sock` breaks rootless / custom Docker | C1 | **FIXED** | `detectDockerSocket` falls back to `$DOCKER_HOST` / `$XDG_RUNTIME_DIR/docker.sock`. |
| 36b | **Bind-mounting the docker socket is impossible** on the host (remote/TCP daemon, locked-down Desktop) → init dies at the gid probe | C1 | **FIXED** | Runner modes: `auto` resolves the effective `DOCKER_HOST`/context — unix→**socket mode** (mount + gid), tcp→**tcp mode** (runner *dials* the same daemon; no mount, no gid). Still no DinD (design §5.1); mode logged + persisted. |
| 37 | `sandforge upstream` was a documented command that always errored | — | **FIXED** | Wired to graduate→PR; real GitHub remotes open the PR via `gh`. |

---

## C. The standing rule for new code

1. **No silent fallback.** If you can't determine a value, return an error that names what you
   couldn't determine and what to try — never a guessed default. (C3)
2. **Probe from the right vantage point.** Container-facing values are probed inside a container,
   not on the host. (C1)
3. **No BSD/GNU assumptions.** Prefer Go stdlib or a portable flag; if you shell out, test on
   macOS. (C2)
4. **Readiness is a state, not a string.** Assert the actual condition (process up + connected +
   no error), then optionally corroborate with a log line. (C5)
5. **Every wait-loop dumps underlying state on timeout** — last logs, the value it injected, the
   thing it was waiting for. (C5)
6. **Ephemeral ports are OS-assigned;** only the one intentionally-stable port (forge :3000) is
   fixed, and a clash there fails loud. (C6)
7. **Clean up before and after.** Tear down prior state at the start of an idempotent op, not only
   in `defer`. (C7)
