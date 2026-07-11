# Sandforge — Design

> Settled distillation of the decisions in `archive/round2.md` (source of truth for rationale + open
> questions). Success/kill criteria live in `goal.md` and are referenced, not restated.
>
> **First deploy target assumption:** the first example deploy/e2e target is a **docker-compose
> stack of LB + App + DB**; cloud (Terraform) and k8s (Helm) targets come later as examples.

---

## 1. Identity

Sandforge is the **Inner-Loop SDLC Forge**: a one-command, fully-local Git forge (Forgejo) plus a
warm GitHub-Actions runner (`act_runner`), built so multiple AI agents iterate code → review →
fix → check, then graduate **one clean PR** to a real upstream. It is **not** an agent framework
and **not** a Kubernetes platform.

---

## 2. The two compose stacks (do not blur)

Sandforge involves **two** docker-compose stacks. They are different layers and named distinctly
throughout:

| Stack | Lifetime | Contents | Purpose |
|-------|----------|----------|---------|
| **Control-plane stack** | Long-lived / warm | reverse-proxy (LB) · Forgejo · Postgres · `act_runner` | *Sandforge itself.* The forge + CI the agents work against. |
| **Deploy-target stack** | On-demand (spun at `graduate`, torn down after) | LB · TS frontend + Go backend · Postgres | *The app under test.* The "first target" ([`prd.md`](prd.md)) — where full e2e runs against a realistic running env. |

Both are compose; both have an LB/App/DB shape — which is exactly why they must never be
conflated. The control plane is *infrastructure*; the deploy target is *the user's product*.

---

## 3. Architecture

![A local GitHub for the inner loop — agents open and review pull requests locally over Forgejo; CI runs on the host; only the final curated PR is pushed to a separate upstream remote](diagrams/local-architecture.png)

Everything except `GitHub` is local. Developers and agents — each in its own git worktree — edit one
local git that carries **two remotes**: it pushes `work/*` to the local Forgejo (`127.0.0.1:3000`)
for the inner loop, and pushes the finished `main` straight to its GitHub `origin` remote.
**Forgejo Actions** queues the CI jobs; the registered `act_runner` polls Forgejo, runs them as
containers on the host Docker daemon, and posts check-runs back onto the PR. Forgejo, `act_runner`,
and the on-demand deploy-target (test) stack all run as containers on that host Docker. The
deploy-target stack ([§6](#6-the-deploy-target-stack--e2e-the-first-target)) is where full e2e runs.

---

## 4. Substrate — docker-compose core (DECIDED)

**Core = docker-compose. No Kubernetes in the default path.** Rationale (full version in
`archive/round2.md` §4):
- Startup is excluded from the speed contract; loop time is the contract (`goal.md` AC-1).
- CI runs in Docker via `act_runner` **regardless** of substrate — k8s would not make the loop
  faster.
- Deploy targets are out of core scope (examples), so the one reason to bundle k8s is gone.

k8s appears **only** in a future opt-in *example* deploy target (Helm into k3d/kind — k3d
preferred as a local convenience target). It is never part of the default bring-up.

---

## 5. CI/CD design

### 5.1 Engine
- **`act_runner` (Forgejo Actions / `nektos/act`)**, run **warm and always-on**, using **the same
  Docker daemon the user's `docker` CLI (and `docker compose`) already targets** so builds reuse the
  host cache and there is **no Docker-in-Docker** (avoid DinD: slow/fragile — `archive/round2.md`
  §21). **How** the runner connects to that one daemon is auto-detected and logged (never DinD):
  - **socket mode** — bind-mount the local unix socket (standard/Desktop/rootless/Colima); the
    in-container socket gid is probed and injected as `group_add`.
  - **tcp mode** — dial a TCP `DOCKER_HOST` (a docker *context*, Docker Desktop's TCP option, a
    locally-exposed socket) for hosts where the socket can't be bind-mounted. Nothing is mounted; no
    gid/group access is needed. Because it's the *same* daemon compose uses, job containers still
    attach to `sandforge-net` and resolve `forgejo` exactly as in socket mode. **v1 scope:** the
    daemon's published ports must be reachable at loopback (the forge/health-gate/clone URLs live at
    `127.0.0.1`); a fully *remote* host is out of scope for now.

  Override via `SANDFORGE_RUNNER_MODE=socket|tcp`. (`ssh://` DOCKER_HOST is out of scope — expose
  the daemon over `tcp://`.)
- Executes the user's **real GitHub Actions workflows** (`.github/workflows`; `.forgejo/workflows`
  also read). ~95% compatible; marketplace actions that call GitHub's own APIs may differ — a
  strong default, not a guarantee.
- Ships **default per-language CI workflows** (build+test) for repos that have none (set: **open**,
  `archive/round2.md` §2 ❓ — Node/Python/Go… candidates).

### 5.2 Branch model (linear, three tiers)

![Branch model — work/<agent>/<slug> to staging to main, gated at each promotion](diagrams/branch-model.png)
- **`work/*`** — agent playground, **no** protection. Build+unit run here; loose/auto-merge among
  work branches keeps iteration fast.
- **`staging`** — the "non-main but semi-protected" branch. **Full e2e (+ optional CD) fire here,
  against the on-demand deploy-target stack.** Promotion `work/* → staging` gated on green
  build+unit.
- **`main`** — clean, protected, upstream-target. Promotion `staging → main` gated on green e2e +
  PRD success-criteria validation. This is the branch `graduate`/`upstream` act on.

### 5.3 Auto-merge
Gated on green checks at the protected boundaries (`staging`, `main`); loose inside `work/*`. (Not
unconditional — the whole point of having CI.)

---

## 6. The deploy-target stack & e2e (the "first target")

The first example target is a **docker-compose stack: LB → (TS frontend + Go backend) →
Postgres** — the **Tasks reference app**, fully specified in [`prd.md`](prd.md). It is the
realistic running env for full e2e — the concrete answer to "full tests in a running env."

**Lifecycle (wired into the flow, not abstract):**
1. On promotion to `staging` (or at `graduate`), Sandforge **spins the deploy-target stack on
   demand** via its compose file (LB + App + DB).
2. The workflow **deploys the just-built App image** into that stack and waits for health.
3. **Full e2e tests run against the LB endpoint** (black-box, prod-shaped: traffic → LB → App →
   DB).
4. On green, promotion proceeds; the stack is **torn down** (never kept hot — `goal.md` AC-9,
   `archive/round2.md` I-2).

**Deploy is a workflow step, not a Sandforge feature.** Sandforge provides the example compose
file + the spin/teardown hooks; the user's workflow owns `deploy`. Later targets (cloud via
Terraform, k8s via Helm) plug in the same way — examples, not core.

---

## 7. The curation pipeline — messy history → one clean PR

The actual product. (`archive/round2.md` §3.)
- **Diff base:** current upstream default branch **at upstream-time** (not the import snapshot).
- **Drift:** rebase onto fresh upstream **at `graduate`** (then re-run e2e), so conflicts surface
  while agents+human are still in the loop. `upstream` is a thin, no-logic push of the
  already-reconciled, already-green branch.
- **History:** **squash by default**, user-overridable (`history: squash|rebase|merge`).
- **Curation is agent-proposed, human-approved:** an agent assembles the squashed commit + PR
  body (specs, design, e2e summary, **PRD success-criteria report**); the human approves before
  `upstream`.
- **PRD validation (DECIDED):** machine-checked against a structured **[`prd.md`](prd.md)** —
  `graduate` parses its `success_criteria`, runs each, emits a per-criterion report, and blocks
  `upstream` on any failed `staging-main` gate (`goal.md` AC-7).

---

## 8. Concurrency model

- **One bare clone per repo on the host; each agent gets its own `git worktree`** (shared object
  store → cheap; isolated index → no corruption). Agents run in **tmux** panes.
- **Branch namespacing:** `work/<agent>/<slug>` → no collisions; the forge UI shows who did what.
- **Locking:** `import`/`reset`/`gc` take a lock and refuse (or `--force`) while pushes are in
  flight. Forgejo serializes concurrent pushes server-side.

---

## 9. Networking

- **Reverse proxy / LB in the control-plane stack binds a fixed `127.0.0.1:3000`** → stable clone
  URL across restarts, no `/etc/hosts` edits, no sudo, works in `git` and every agent. (Drop the
  README's `forge.localhost` ingress — `archive/round2.md` §8, I-5.)
- **HTTP on loopback** for v1 (token in clear is fine on 127.0.0.1; no inbound exposure).
- **SSH** (`localhost:222`) offered as an option for tools that prefer keys.

---

## 10. Persistence

- **Default = persistent.** `--ephemeral` for disposable (CI-of-CI, demos).
- Persistence = a **named volume** holding Forgejo repos + Postgres. Ephemeral = anonymous volume,
  nuked on `down`.
- **`sandforge export`** dumps git refs + PR metadata to a tarball → disposable-but-recoverable.

---

## 11. Secrets & threat model

- **Threat model = messy agent, not malicious** (`archive/round2.md` §6). Token-scoping suffices;
  OS/VM-level sandboxing is out of scope for v1.
- **Runner/agents get a local-Forgejo-only token.** Upstream production credentials live **only**
  in the `upstream` step (reuse the host's existing `gh`/`glab` auth) and are **never** injected
  into the cluster/runner/agent env (`goal.md` AC-5).

---

## 12. CLI surface

| Command | Description |
|---------|-------------|
| `sandforge init` | Bring up the control-plane stack (proxy + Forgejo + Postgres + warm `act_runner`); seed admin+token; print clone URL. Idempotent/resumable. |
| `sandforge import <url>` | Seed a writable repo from upstream; keep `upstream` as a git remote for rebasing. |
| `sandforge status` | One screen: forge URL, repos, active `work/*` branches, last check-runs, control-plane health. |
| `sandforge logs [svc]` | Tail control-plane (or deploy-target) logs. |
| `sandforge graduate <repo> <branch>` | Rebase onto fresh upstream, spin deploy-target stack, run full e2e + PRD validation, assemble the curated squashed PR. |
| `sandforge upstream <repo> <branch>` | Push the graduated branch + open a PR on the real upstream via `gh`/`glab`/`tea`. **Only production-touching command.** |
| `sandforge gc` | Delete merged/stale `work/*` branches + their worktrees (grace period). |
| `sandforge reset [<repo>]` | Wipe sandbox state (per-repo or whole instance). |
| `sandforge down` | Tear down the control-plane stack (optionally keep the volume). |

> Agents never call these — they just `git` against `localhost:3000`. The CLI is the developer's
> control plane.

---

## 13. Build shape

- A **thin Go CLI** wrapping off-the-shelf parts: docker-compose (substrate), official Forgejo +
  Postgres images, `act_runner` (CI), `gh`/`glab`/`tea` (the Upstreamer). Don't hand-build
  provider PR integrations.
- **Pinned versions** of Forgejo + `act_runner` in the bundled compose file (reproducibility >
  freshness; `latest` is a "just works" lie — `archive/round2.md` §15, I-4).
- Opinionated embedded defaults; one optional `sandforge.yaml` that 90% of users never touch.

---

## 14. Bootstrap correctness (the flaky bits, handled)

- Admin+token seeded **idempotently** (`FORGEJO__*` env + `forgejo admin user create`), token to
  `~/.sandforge/credentials` at `0600`; `reset` rotates it.
- **Runner registration chicken-and-egg:** health-gate Forgejo → mint runner token
  (`forgejo actions generate-runner-token`) → start `act_runner` → confirm online — all inside
  the one command, sequenced (`archive/round2.md` §12). Health-gated startup: the clone URL is not printed
  until the forge answers.

---

## 15. Open decisions (see `archive/round2.md` for full list + rationale)

- ✅ **PRD format** — DECIDED: machine-readable [`prd.md`](prd.md).
- ✅ **Benchmark repo** — DECIDED: the first-target Tasks app.
1. **Default per-language CI workflows** shipped (start with TS + Go to match the first target).
2. **Post-upstream review round-trip** — v1 or v1.1.
3. Example deploy targets after the first target (which clouds; local-kube on k3d vs kind).

---

## 16. Out of scope (see `goal.md` Non-Goals)

Agent orchestration; bundling AI tools; owning deploy targets; Kubernetes in the default path;
multi-tenant/shared hosting; replacing the team's production CI/CD.
