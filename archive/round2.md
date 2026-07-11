# Sandforge — Round 2: Recommended Answers + Open Decisions

This folds `answers1.txt` into the Round-1/2/3 grilling (`DESIGN-QUESTIONS.md`). For each
question: what `answers1.txt` **decided**, then **our recommended answer** for what's still
open, then **❓ Decide** — a crisp question back to you. Put your call under each ❓.

---

## What `answers1.txt` locked (the new north star)

Sandforge is the **innermost loop of a local AI SDLC** — a private, fully-local forge where
multiple AI coding tools (Claude Code, Codex, Cursor, …) hand work back and forth (code →
review → fix → check) across models, with **local CI** (and optional CD to a user-defined
running env), faster and cheaper than GitHub (which is moving to usage-based pricing). Output:
**one clean, quality PR** — specs, design docs, full e2e tests, PRD success-criteria
validation. **One command** to start, **zero required tinkering**, optionally configurable.
**Disposable or persistent** (both supported). **Concurrent agents** via git worktrees + tmux.

This is a **fourth identity** beyond Round 1's three — call it the **Inner-Loop SDLC Forge**.
It changes the answer to "why not a fork": a fork is remote (latency + usage-based $) and
single-flow; Sandforge is local (fast, flat-cost) and built for multi-agent handoff with gated
checks. That justification holds — *if* the substrate is actually fast and actually one-command.

> ⚠️ **The central new tension (read this first).** Four goals now collide:
> (a) "one command, just works, no tinkering", (b) "faster than GitHub", (c) "local CI **and**
> CD into a real running env for full e2e", (d) the README's **minikube/k8s** substrate.
> minikube's cold-start, per-platform driver fragility, and DNS/ingress friction directly
> fight (a) and (b). But (c) — review-app deploys / a running env — is exactly what k8s is
> *good* at. So the substrate is the make-or-break decision, and it's still open. Our
> recommendation below is **k3d** as the sweet spot, not minikube.

---

## Section 1 — Identity

**Q1.1 primary identity / Q1.2 why-not-fork / Q1.3 visibility**

**DECIDED.** Identity = Inner-Loop SDLC Forge (above). Fork loses on latency, usage-based
cost, locality, and multi-agent-handoff-with-checks. Settled.

**❓ Decide:** Is "faster than GitHub" measured as **inner-loop latency** (push/PR/check
round-trip, where local always wins) or **time-to-first-green** including startup? If startup
counts, minikube is disqualified. Confirm which clock we're racing.

> 

---

## Section 2 — Forge ⟹ CI ⟹ runners ⟹ infra

**Q2.1 which forge features / Q2.2 CI in-or-out / Q2.3 real-CI-vs-stand-in**

**DECIDED.** CI is **IN** (build + test), CD is **IN as an option**, e2e runs in a
user-configured running env, gated off a **non-main, semi-protected branch**. Forge features
exercised: branches, PRs, status checks, review handoff, gated/auto merge. So the full forge is
justified — this is no longer "ceremony over bare git."

**DECIDED (your call): "resemble GitHub Actions as much as possible — don't reinvent, just run
it."** → CI = **Forgejo Actions + `act_runner`**, which *is* the GitHub-Actions execution model
(it wraps `nektos/act` and runs `.github/workflows` YAML in Docker). We are **not** building a CI
system; we run the user's existing GH Actions workflows locally. **Plus: ship stack templates**
(workflow + deploy manifests) for common stacks so `init` is genuinely one command for the
common cases (see §4 for the deploy target).

**Our recommended answer (open part — *how* CI runs):**
- Use **Forgejo Actions + a single warm `act_runner`** (host Docker) baked into the one-command
  bring-up. It runs the GitHub Actions workflow syntax, so workflows are portable to/from real
  GitHub — directly serving the "transfers cleanly upstream" goal.
- **Branch model (linear, three tiers):**
  `work/*`  →  `staging`  →  `main`
  - `work/*` — agent playground, **no** protection; build+unit run here, loose/auto-merge among
    work branches.
  - `staging` — the "non-main but semi-protected" branch from `answers1.txt`. **Full e2e +
    optional CD fire here.** Promotion `work/* → staging` gated on green build+unit.
  - `main` — clean, protected, the upstream-target. Promotion `staging → main` gated on green
    e2e + PRD-success validation. This is the curated branch `graduate`/`upstream` act on.
- **CI fidelity:** runners execute the **user's actual workflow files** (`.forgejo/workflows`,
  and Forgejo can also read `.github/workflows` — *mostly* GitHub-compatible; some marketplace
  actions won't run unmodified, so "transfers cleanly" is a strong default, not a guarantee).
  Default workflows shipped for repos that have none.

**❓ Decide:** (1) Reuse `.github/workflows` directly (our rec — maximizes "just run it"), or
require a `.forgejo/` copy? (2) Which **default CI workflows** ship for repos that have none
(per-language build+test: Node, Python, Go, …)? *(Deploy is NOT core — deploy targets are
examples; see §4.)*

> 

---

## Section 3 — The real product: messy history → one clean PR

**Q3.1 diff base / Q3.2 squash-rebase-cherrypick + who curates / Q3.3 drift**

**DECIDED (scope).** Output PR must carry: specs, design docs, full e2e tests, PRD
success-criteria validation. So "clean PR" = curated *content*, not just squashed commits.

**Our recommended answer (open part — mechanics):**
- **Diff base:** the **current upstream default branch at upstream-time**, not the import
  snapshot. Rebase `main`-of-sandbox onto fresh upstream during `graduate`, so conflicts
  surface while the human + agents are still in the loop (not at the irreversible `upstream`
  step).
- **History strategy: DECIDED — squash by default, user-overridable.** Default = squash the
  messy `work/*` history into one clean commit (or a small PRD-keyed series); a config knob
  (`history: squash|rebase|merge`) lets the user change it. Curation is **agent-proposed,
  human-approved**: an agent assembles the squashed commit + PR body (specs/design/test
  summary/PRD checklist); the human approves before `upstream`.
- **Drift reconciliation happens at `graduate`** (rebase + re-run e2e), and `upstream` is a
  thin, no-logic push of the already-reconciled, already-green branch.

**❓ Decide:** (1) Squash-to-one vs. preserve a curated multi-commit series? (2) Is the PRD a
real input file (`prd.md`) that Sandforge reads to generate the success-criteria checklist, or
freeform? A machine-readable PRD makes "validation of success criteria" automatable.

> 

---

## Section 4 — Substrate (DECIDED)

**Q4.1 why k8s / Q4.2 resource math / Q4.3 cold-start honesty**

**DECIDED — the substrate is settled by three of your calls:**
1. **Startup excluded; loop time is the contract** → nothing chosen for cold-start; forge+runner
   stay **warm/persistent**.
2. **CI = "just run GitHub Actions," not reinvent** → CI executes via **`act_runner`** (Forgejo
   Actions / `nektos/act`) in **Docker**, regardless of substrate — the k8s layer never makes the
   loop faster.
3. **CD targets are OUT OF CORE SCOPE — examples only.** Deploy can target anything
   (Terraform→cloud, Helm→k8s); Sandforge does **not** own the target. That was the *only* reason
   to consider bundling k8s — and it's now gone.

**⇒ Sandforge core substrate = docker-compose**: Forgejo + a warm `act_runner` (host Docker) +
a volume. No Kubernetes in the core. This is the lightest thing that runs the forge + GH-Actions
CI, the most likely to "just work," and the smallest idle footprint on a daily-driver laptop.

**Our recommended answer:**
- **CI engine (settled):** `act_runner` **warm, always-on**, using the **host Docker daemon**
  (mount the socket) so builds reuse the host cache and there's no Docker-in-Docker (avoid DinD —
  slow/fragile, see §21).
- **Deploy = a workflow step, not a Sandforge feature.** The runner just executes whatever the
  user's workflow does (`terraform apply`, `helm upgrade`, `gh`, a deploy hook). Sandforge stays
  agnostic; targets live in the user's repo.
- **Ship example deploy templates (clearly marked "examples, not core"):** one **simple compute
  target per cloud** (e.g. AWS/GCP/Azure via Terraform) **+ one local-kube example** (Helm into
  **k3d/kind**) for users who want a fully-local e2e/CD path without touching a cloud.
- **The optional local-kube example** is the *only* place a k8s cluster appears, and it's
  **opt-in, spun on demand** when that example runs — never part of the default bring-up.

**kind vs k3d (you asked "why not kind?") — now only relevant to that optional local-kube
example, so low-stakes:**
- **minikube: no** (heaviest, flakiest drivers, worst DNS/ingress).
- **kind** = vanilla upstream k8s → best prod-fidelity; but no built-in ingress/LB (more glue).
- **k3d** = k3s, batteries-included (Traefik ingress + ServiceLB + registry) → "deploy → URL"
  with near-zero config.
- **Lean: k3d for the local-kube example** (it's a *local* convenience target, so batteries win);
  offer kind as an alt for users whose prod is vanilla k8s and who want the example to match.

**❓ Decide:** (1) Confirm **docker-compose core, no k8s in the default path**. (2) Which example
deploy targets ship in v1 — which cloud(s) + the local-kube example? (3) Local-kube example on
**k3d** (our lean) or **kind**?

> 

---

## Section 5 — Persistence vs. disposability

**Q5.1 which wins / Q5.2 what to persist**

**DECIDED.** Both modes supported, both valid.

**Our recommended answer (open part — defaults & mechanism):**
- **Default = persistent**, with `--ephemeral` (or `sandforge up --throwaway`) for disposable.
  Rationale: a long-running inner loop with accumulated branches/PRs is the common case; pure
  throwaway is the exception (CI-of-CI, demos).
- **Persist the named Docker/k8s volume** holding Forgejo's repos + DB. Disposable = no volume
  (tmpfs/anonymous), nuked on `down`.
- **Bonus cheap mode:** `sandforge export` dumps just the git refs + PR metadata to a tarball,
  so you can be disposable-but-recoverable (ties to README §11 "export a session").

**❓ Decide:** Default persistent or default disposable? (We say persistent.)

> 

---

## Section 6 — Threat model

**Q6.1 malicious vs messy / Q6.2 supply-chain-into-yourself**

**Not addressed in `answers1.txt`. Our recommended answer:**
- **Threat model = messy agent, not malicious.** Agents are your own tools under your account;
  defend against accidents (bad pushes, runaway loops, leaking the local token), not adversarial
  code exfiltration. So Forgejo-token-scoping is sufficient; OS/VM-level sandboxing is out of
  scope for v1.
- **But keep the one hard rule:** upstream prod credentials live **only** in the Upstreamer's
  process at `upstream`-time, never in any env the runner/agents can read. CI runners get a
  **local-Forgejo-only** token. This contains Q6.2 (agent can't get its planted code
  auto-pushed to prod — `upstream` is human-gated and runs outside the agent env).

**❓ Decide:** Confirm "messy not malicious" for v1. (If any agent is ever untrusted/3rd-party,
we must revisit — that flips substrate toward per-agent containers.)

> 

---

## Section 7 — Build shape

**Q7.1 real binary vs wrapper / Q7.2 custom Upstreamer vs gh/glab/tea**

**DECIDED (constraint).** One command, well-known parts, zero required tinkering, optional
config. That argues hard for a **thin orchestrator over off-the-shelf parts**, not a
reimplementation.

**Our recommended answer:**
- Sandforge = a **thin CLI** that wraps: Docker/k3d (substrate), an official **Forgejo image +
  Helm chart or compose file** (forge), **act_runner** (CI), and **`gh`/`glab`/`tea`** (the
  Upstreamer). Don't hand-build three provider PR integrations — shell out to `gh`/`glab`/`tea`.
- Ship **opinionated bundled defaults** ("well-known parts") as embedded manifests; expose a
  single optional `sandforge.yaml` that 90% of users never touch.

**❓ Decide:** Language/distribution for the CLI — **Go single static binary** (matches your
ecosystem, easy `brew`/`curl` install) vs. a shell-script + compose bundle? (We say Go binary.)

> 

---

## Section 8 — Networking / DNS

**Q8.1 resolution / Q8.2 URL stability / Q8.3 HTTP vs HTTPS**

**Our recommended answer:**
- **Bind Forgejo to a fixed `127.0.0.1:3000`** (compose) or a fixed k3d port-mapping. Stable
  across restarts, no `/etc/hosts` edits, no sudo, works in `git` and every agent. Drop
  `forge.localhost`; use `http://localhost:3000` as the canonical clone URL.
- **HTTP on loopback** for v1 (token in clear is fine on 127.0.0.1; no network exposure per
  README §9). Offer optional HTTPS later for users who proxy it.
- **SSH remote** (`localhost:222`) as an option for agents/tools that prefer SSH keys.

**❓ Decide:** Fixed `localhost:3000` over the README's `forge.localhost` ingress? (We say yes —
it's the single biggest "just works" win.)

> 

---

## Section 9 — Webhooks & auto-merge mechanics

**Q9.1 webhook delivery / Q9.2 gate or unconditional merge**

**Our recommended answer:**
- On a single Docker network (compose or k3d), Forgejo → act_runner status flow works without
  ingress. **Auto-merge is conditional on green checks** (build+unit for `work/* → staging`),
  *not* unconditional — that's the whole point of having CI.
- `work/*` branches can merge fast/loose among themselves; the **gates live at the
  staging→main promotion**, so the agent playground stays fast while `main` stays clean.

**❓ Decide:** Confirm gated auto-merge at the protected boundary (staging/main), loose inside
`work/*`.

> 

---

## Section 10 — Concurrency (worktrees + tmux)

**Q10.1 shared vs separate clones / Q10.2 branch collisions / Q10.3 locking**

**DECIDED (requirement).** Must handle concurrent agents via git worktrees + tmux.

**Our recommended answer:**
- **One bare clone per repo on the host; each agent gets its own `git worktree`** (your stated
  model) → no shared-index corruption, cheap (shared object store), fast.
- **Namespace branches per agent/session:** `work/<agent>/<slug>` (e.g. `work/claude/auth-fix`)
  → no collisions, and the forge UI shows who did what.
- **Server-side serialization is free:** Forgejo handles concurrent pushes; the host worktrees
  isolate working state. `reset`/`import` take a lock and refuse while pushes are in flight (or
  `--force`).

**❓ Decide:** Branch namespace convention — `work/<agent>/<slug>`? And should Sandforge
*manage* the worktrees/tmux (spawn them) or just be the forge they point at? (We lean: be the
forge; ship an optional helper to spin worktrees, but don't own tmux.)

> 

---

## Section 11 — Mirror correctness

**Q11.1 force-push/delete / Q11.2 LFS/submodules/large / Q11.3 read-only ambiguity**

**Our recommended answer (resolves the §11.3 ambiguity):**
- **Do NOT use Forgejo's "pull mirror" feature for the agent repo.** Seed a **normal writable
  repo** from an upstream snapshot, and keep `upstream` as a **named git remote** for rebasing.
  "Read-only" then means *disciplinary* (agents never push to upstream; only the Upstreamer
  does), enforced by credential scoping — not a Forgejo mirror lock.
- **Re-sync = `git fetch upstream` into the base branch**, surfaced at `graduate`. Upstream
  force-push/branch-delete → reported as a drift warning, never silently applied over agent work.
- **LFS/submodules/large repos:** enable Forgejo LFS in the bundled config; support submodules;
  document a repo-size ceiling and make the volume size configurable. Don't pretend a 10 GB
  monorepo is free.

**❓ Decide:** Confirm "writable repo + upstream remote" model over Forgejo pull-mirror. And:
do we need LFS/submodule support in **v1**, or is that v2?

> 

---

## Section 12 — Bootstrap & secrets

**Q12.1 admin/token seeding / Q12.2 prod cred isolation**

**Our recommended answer:**
- Bootstrap admin + token **idempotently** via Forgejo's CLI/env (`FORGEJO__...` env +
  `forgejo admin user create` on first boot), pinned to a known Forgejo version (see Q15.2).
  Token written to `~/.sandforge/credentials` at `0600`. `reset` rotates it.
- **Runner registration is the classic flaky step:** act_runner needs a registration token that
  only a *running* Forgejo can mint — a chicken-and-egg. Handle it explicitly: health-gate
  Forgejo → generate the runner token via `forgejo actions generate-runner-token` → start
  act_runner → confirm it appears online, all inside the one command. This ordering is why naive
  compose files flake; we sequence it, not hope.
- **Prod creds:** reuse the host's existing `gh`/`glab` auth at `upstream`-time only; never
  inject into the cluster/runner/agent env. (Aligns with §9 + Q6.2.)

**❓ Decide:** Reuse the host's `gh auth` for upstreaming (zero new secret to manage) — yes?

> 

---

## Section 13 — Observability

**Q13.1 silent-failure surfacing / Q13.2 partial-init recovery**

**Our recommended answer:**
- `sandforge status` shows **forge + runner + last-N check-runs + cluster health** in one view;
  `sandforge logs` tails the relevant container. A push whose CI failed shows as a red check in
  `status`, not a silent swallow.
- `init` is **idempotent and resumable** — re-running converges; partial failures don't require
  a full `down`. Health-gated startup (wait-for-ready) so the printed clone URL is never handed
  out before the forge actually answers.

**❓ Decide:** Is a one-screen `status` (forge URL, repos, active `work/*` branches, last checks,
health) the right v1 surface?

> 

---

## Section 14 — `graduate` preflight environment

**Q14.1 where preflight runs / Q14.2 preflight = CI or pre-filter**

**Our recommended answer:**
- Preflight runs **in the same runner/CI environment** as in-loop checks (not the host) so
  "passes locally ⇒ passes upstream" actually holds. This is the payoff of using
  GitHub-compatible workflows.
- Preflight **is the real e2e + PRD-validation gate**, not a cheap pre-filter — it's the
  staging→main promotion gate. `graduate` = "run the full gate + assemble the curated PR";
  `upstream` = "push the already-green result."

**❓ Decide:** Confirm preflight runs in-runner (not host) and is the full gate.

> 

---

## Section 15 — Portability & versioning

**Q15.1 platforms / Q15.2 version pinning**

**Our recommended answer:**
- **v1 platforms:** macOS + Linux (Docker Desktop / native Docker / WSL2). Windows via WSL2
  only. Don't promise native Windows.
- **Pin Forgejo + act_runner to known-good versions** in the bundle (reproducibility >
  freshness); `sandforge upgrade` bumps deliberately. `latest` (README §7) is a reproducibility
  lie for a "just works" tool — override it.

**❓ Decide:** Pin versions by default (we strongly recommend) vs. float `latest`?

> 

---

## Section 16 — Competitive landscape

**Q16.1 vs worktrees / Q16.2 vs gitpod/gitea+act/soft-serve/gerrit**

**Our recommended answer (the one-liner moat):** Worktrees alone give isolation but **no PRs,
no checks, no multi-agent handoff, no CD env** — Sandforge adds the *gated review+CI loop* on
top of worktrees. vs. raw `gitea+act_runner` compose: Sandforge is the **one-command,
zero-config, opinionated bundle + the curate→upstream workflow** they don't provide. vs.
Gitpod/devcontainers: those are *editor/runtime* envs, not a *forge*. vs. Gerrit: right model
(curate-before-merge) but heavyweight, Java, not agent/Actions-native.

**❓ Decide:** Is the moat "**one-command opinionated local forge + multi-agent curate→upstream
loop**" — i.e. integration + workflow, not novel infra? (We think yes; that also tells us to
*wrap*, not *build*, the parts.)

> 

---

## Section 17 — The central contradiction (Round 3)

**Q17.1 does the fidelity justification eat itself / Q17.2 a fourth identity**

**DECIDED by `answers1.txt`.** The fourth identity *is* the answer (Inner-Loop SDLC Forge). The
justification doesn't eat itself because the driver isn't "replicate the team's CI exactly" —
it's **local speed + flat cost + multi-agent handoff with gated checks**. CI is real but its job
is to *gate the handoff loop*, not to be a byte-perfect clone of upstream CI. So fidelity is
"good enough that work transfers," not "identical," which is achievable on a laptop.

> 

## Section 18 — Unit of value (Round 3)

**Q18.1 could it be just-a-curation-command / Q18.2 forge essential because agents open PRs**

**DECIDED by `answers1.txt` (Q18.2 wins).** The forge **is** essential: the product is agents
*opening PRs and responding to review across tools/models* with checks — not just emitting
commits. A pure curation command (no forge) is therefore insufficient. Settled.

> 

## Section 19 — Lifecycle gaps the state machine hides (Round 3)

**Q19.1 GC of dead branches / Q19.2 multi-graduation / Q19.3 post-upstream round-trip**

**Not addressed in `answers1.txt`. Our recommended answer:**
- **GC:** `work/*` branches auto-expire — `sandforge gc` (and an optional age-based sweep)
  deletes merged/stale work branches and their worktrees. `staging`/`main` never auto-GC'd.
  Keeps the forge from rotting without a full `reset`.
- **Multi-graduation:** support **one PR per graduated branch**, and a **stacked series** when
  branches are interdependent (graduate in dependency order; `upstream` can open a stack). Don't
  force everything into a single PR — the "one clean PR" goal is *per coherent change*, not
  *per sandbox*.
- **Post-upstream round-trip (important for the handoff identity):** `upstream` is **not
  terminal**. After the real PR gets review comments, `sandforge sync <pr>` pulls the upstream PR
  branch + review threads back into a `work/*` branch so agents iterate on the feedback, then
  re-`graduate`. This closes the loop the README's `Upstreamed --> [*]` wrongly cut off.

**❓ Decide:** (1) Auto-GC merged `work/*` by default (we say yes, with a grace period)?
(2) Need stacked/multi-PR graduation in v1, or single-PR v1 + stacks later?
(3) Is the post-upstream review round-trip in-scope for v1? (We think it's core to the identity,
but it's the most complex piece — could be v1.1.)

> 

## Section 20 — Success & kill criteria (Round 3) — **testable, per your CLAUDE.md**

**Q20.1 v1 acceptance criterion / Q20.2 kill threshold**

**DECIDED (your call): the contract is LOOP TIME, not startup.** Cluster/forge startup is a
one-time, amortized cost and is **excluded** from the speed metric. The thing we must beat is
GitHub's *loop* latency — runner queue waits **and** Copilot-review turnaround, both of which can
be very slow. Local wins because there is no shared-runner queue and the reviewer is a local
model.

**Our recommended (concrete, tunable) numbers:**
- **v1 acceptance — loop-time first (the contract):**
  - Inner-loop round-trip (agent pushes `work/*` → warm `act_runner` build+unit green →
    mergeable) in **≤ 30 s** for a small repo — and **demonstrably faster than the same repo's
    GitHub Actions** run *including* GH's queue wait.
  - Review handoff (agent A pushes → review agent posts comments on the PR) **≤ time it takes
    Copilot review to return** on the equivalent GitHub PR — ideally well under.
  - Runner is **warm**: zero cold-runner penalty per loop (first job after idle ≤ 5 s overhead).
  - 3 concurrent agents on 3 worktrees push, open PRs, and get checks **without collision or
    index corruption**.
  - A full path: import → agents iterate → `graduate` (e2e + PRD validation green) → `upstream`
    produces **one clean PR** with specs/design/tests attached.
- **Secondary (not the contract): startup ≤ ~2 min cold, steady-state forge+runner RAM ≤ ~2 GB**
  so agents keep the rest of a 16 GB laptop. These are comfort bounds, not pass/fail gates.
- **Kill thresholds (decide now, pre-sunk-cost):** if **loop time is not faster than GitHub**
  for a representative repo, or steady-state RAM **> 4 GB** with 2 agents running, or first-run
  success **< 95%** across mac/linux — the approach needs rework. (Startup time is *not* a kill
  criterion anymore.)

**❓ Decide:** Confirm the loop-time targets (≤ 30 s small-repo loop; review ≤ Copilot turnaround;
warm runner). Give us a **representative repo** to benchmark "faster than GitHub" against — that
makes the contract real instead of a vibe.

> 

---

## The top open decisions, ranked (answer these and the rest fall out)

**Already decided:** CI = run GH Actions via `act_runner` (don't reinvent); startup excluded,
loop-time is the contract; squash by default (overridable); **substrate = docker-compose core,
no k8s in default path**; CD targets are examples, not core; minikube rejected.

1. **PRD format (Q3.2):** machine-readable `prd.md` vs freeform. (Drives auto-validation of
   success criteria — see I-6.) **Now the highest-leverage open call.**
2. **Default CI workflows (Q2 ❓):** which per-language build+test templates ship for repos with
   none (Node, Python, Go, …).
3. **Example deploy targets (Q4 ❓):** which cloud(s) + the local-kube example; local-kube on
   k3d (lean) or kind.
4. **Post-upstream round-trip (Q19.3):** in v1 or v1.1? Core to the multi-agent handoff identity
   but the most complex piece.
5. **Default mode (Q5):** persistent vs disposable by default. (We say persistent.)
6. **Clone URL (Q8):** fixed `localhost:3000` vs `forge.localhost` ingress. (We say fixed.)
7. **Representative repo to benchmark "faster than GitHub" (Q20):** name one so the loop-time
   contract is testable.

---

## Issues / contradictions we're calling out

- **I-1 (RESOLVED):** Substrate = **docker-compose core, no k8s in default path.** Startup
  excluded (loop-time is the contract), CI runs in Docker via `act_runner` regardless, and CD
  targets are examples not core — so the one reason to bundle k8s is gone. k8s appears only in an
  opt-in local-kube *example*.
- **I-2 (RESOLVED):** CD is out of core, so "fast loop vs heavy CD" no longer fights inside
  Sandforge. Inner loop = always-on warm forge+runner (fast). Deploy/e2e-against-a-running-env =
  a workflow step the user owns, spun on demand by their workflow — never kept hot by Sandforge.
- **I-3 (zero-tinker vs. user-defined env — now smaller):** With CD external, the only thing a
  user supplies for deploy is their own workflow step/creds; Sandforge ships working defaults and
  examples. Zero-tinker holds for the core loop.
- **I-4 (`latest` vs reproducible):** README §7 `version: latest` contradicts "must just work."
  Pin by default.
- **I-5 (forge.localhost):** ingress/DNS is the #1 first-run failure mode; fixed loopback port
  removes it.
- **I-6 (PRD validation needs structure):** "validate success criteria of the PRD" is only
  automatable if the PRD is machine-readable. Otherwise it's a human checkbox — decide which.
