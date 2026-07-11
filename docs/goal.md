# Sandforge — Goal

> **This file is the single authority on what "success" means.** `design.md` and `loop.md`
> reference it; they do not restate it. If a criterion changes, change it here.

---

## Mission (one line)

**The fastest, fully-local inner loop of the AI SDLC: a one-command local Git forge where
multiple AI agents iterate code → review → fix → check against real GitHub-Actions CI, and emit
one clean, validated PR to upstream.**

---

## The problem

1. **GitHub is going usage-based.** Aggressive agent iteration (dozens of branches, throwaway
   PRs, CI runs) becomes a metered cost center.
2. **GitHub's loop is slow.** Hosted-runner queue waits and Copilot-review turnaround add minutes
   per iteration — fatal when an agent wants 50 iterations an hour.
3. **Multi-tool handoff has nowhere to live.** Teams want agent A (one model/tool) to write,
   agent B (another) to review, agent C to fix — with real gated checks between hops — but no
   local, tool-agnostic surface exists for that.
4. **Noise pollutes the team's git.** None of this messy iteration belongs on the team's
   production forge.

Sandforge solves all four by moving the **inner loop** entirely local: flat-cost, queue-free,
tool-agnostic, isolated. Only one deliberate step ever touches production.

---

## Who it's for

A single developer running one or more AI coding agents locally who wants to iterate fast and
cheap, then hand off **one clean PR** to their team's real upstream (GitHub/GitLab/Forgejo).

---

## Principles (non-negotiable)

- **Local-first.** The inner loop never depends on a network service.
- **One command, zero required tinkering.** `sandforge` just works; config is optional and most
  users never touch it.
- **Run GitHub Actions — don't reinvent CI.** Execute the user's real workflows via `act_runner`.
- **Loop time is the contract** (see Success). Startup is a one-time, excluded cost.
- **Disposable *or* persistent** — both are first-class.
- **Sandforge is not an agent framework** and **not a Kubernetes platform.** It's a forge + a
  warm CI runner. (See Non-Goals.)

---

## What success looks like — testable acceptance criteria

> Per the repo execution rules, every criterion is objectively verifiable.

### Speed (the contract)
- **AC-1 — Loop beats GitHub.** For a representative repo, the inner-loop round-trip (agent
  pushes `work/*` → warm `act_runner` build+unit green → branch mergeable) completes in **≤ 30 s**
  *and* is **demonstrably faster than the same repo's GitHub Actions run including GH queue
  wait.** Representative benchmark repo = the first-target **Tasks app** ([`prd.md`](prd.md));
  swap in a heavier repo later to stress the claim.
- **AC-2 — Warm runner.** First CI job after an idle period incurs **≤ 5 s** runner overhead (no
  cold-runner penalty).
- **AC-3 — Review handoff beats Copilot.** Agent-A-pushes → review-agent-posts-PR-comments
  completes in **≤ the equivalent Copilot-review turnaround** on GitHub (target: well under).

### Correctness & isolation
- **AC-4 — Concurrent agents.** 3 agents on 3 git worktrees push, open PRs, and receive checks
  with **no branch collision and no index corruption.**
- **AC-5 — Nothing leaks.** Upstream production credentials are never readable by any CI runner
  or agent process; they are used **only** during `sandforge upstream`.

### The deliverable
- **AC-6 — One clean PR.** A full path — import → agents iterate → `graduate` (full e2e green +
  PRD success-criteria validated) → `upstream` — produces a **single squash-by-default PR** (user
  can override) against the real upstream, carrying **specs, design docs, e2e tests, and a PRD
  success-criteria report.**
- **AC-7 — PRD validation is real, not a checkbox.** "Success criteria met" is **machine-checked**
  against a structured PRD. **DECIDED:** PRD is a machine-readable **`prd.md`** with explicit,
  per-criterion gates; `graduate` emits a success-criteria report and blocks `upstream` on any
  failed `staging-main` gate. See [`prd.md`](prd.md) for the format + the first target's criteria.
- **AC-8 — Full e2e against a running env.** `graduate` spins the **first deploy target** (a
  docker-compose **LB · TS frontend + Go backend · Postgres** stack — see [`prd.md`](prd.md)) on
  demand, runs full e2e against it, and tears it down.

### Footprint (comfort bounds, not the contract)
- **AC-9 — Footprint.** Steady-state control plane (forge + DB + runner + proxy) **≤ ~2 GB** RAM
  so agents keep the rest of a 16 GB laptop. Cold start **≤ ~2 min** (not a gate; see Kill).

---

## Kill criteria (decide now, pre-sunk-cost)

Abandon or rework the approach if, after a fair v1 attempt:
- **K-1:** the inner loop is **not faster than GitHub** for the representative repo (AC-1 fails) —
  the entire premise dies.
- **K-2:** steady-state RAM **> 4 GB** with 2 agents running (AC-9 blown by 2×).
- **K-3:** first-run success rate **< 95%** across macOS + Linux.

> Startup time is **not** a kill criterion (it's excluded from the speed contract).

---

## v1 scope

- **In:** one-command bring-up (docker-compose control plane); import a repo; concurrent agents
  via worktrees+tmux; GH-Actions CI via warm `act_runner`; branch model `work/* → staging →
  main`; `graduate`/`upstream` curation (squash default); **first deploy/e2e target = compose
  LB+App+DB**; persistent (default) and `--ephemeral` modes; `status`/`logs`.
- **Out (v1):** additional deploy targets (cloud via Terraform, k8s via Helm — shipped later as
  *examples*); post-upstream review round-trip (candidate v1.1); stacked/multi-PR graduation.

---

## Non-Goals

- ❌ Not an agent orchestrator/scheduler; doesn't pick or bundle AI tools.
- ❌ Not a Kubernetes platform. No k8s in the default path. (k8s appears only in an opt-in
  *example* deploy target, later.)
- ❌ Deploy targets are **examples, not core** — Sandforge runs the user's deploy workflow step;
  it does not own the target.
- ❌ Not a shared/multi-tenant server. Single-developer, local.
- ❌ Not a replacement for the team's production CI/CD or forge.

---

## Open decisions that gate this goal

Tracked with full rationale in `archive/round2.md` (ranked list). Resolved since: PRD format
(machine-readable `prd.md`, gates AC-7) and the benchmark repo (the first-target Tasks app, gates
AC-1). Remaining: default per-language CI workflows; post-upstream review round-trip (v1 or v1.1).
