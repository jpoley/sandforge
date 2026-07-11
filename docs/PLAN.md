# Sandforge — Build Plan (vertical slice to a closed loop)

> Plan-gate artifact per [`loop.md`](loop.md) Prompt 2. Anchored to [`goal.md`](goal.md) ACs,
> [`design.md`](design.md) sections, and [`prd.md`](prd.md) success criteria. **Wait for approval
> before coding.**

## Objective (the user's ask, restated)

A **single command** that stands up Sandforge and drives the **full closed loop** end-to-end —
import → agent pushes `work/*` → warm `act_runner` runs real GitHub Actions → green → `graduate`
(spin deploy-target, run e2e + machine-checked PRD validation) → `upstream` PR — with an
**automated e2e test that validates every checkable AC/SC**, and **all decisions logged** to
`.logs/decisions/*.jsonl`. Sensible defaults, all overridable.

## Guiding constraints (called out so they don't surprise us — see design.md)

- **Substrate = docker-compose. No Kubernetes.** (design §4)
- **CI = `act_runner` on the host Docker daemon, warm, no DinD.** (design §5.1)
- **Two distinct compose stacks:** control-plane (proxy+Forgejo+Postgres+runner) vs deploy-target
  (LB+App+DB, on-demand). Never conflated. (design §2)
- **Fixed clone URL `127.0.0.1:3000`. Pinned image versions.** (design §9, §13)
- **Branch model:** `work/<agent>/<slug>` → `staging` → `main`; gates at `staging`/`main`. (design §5.2)
- **Upstream creds never reach the runner/agent env.** (goal AC-5)
- **Local upstream by default for the self-test.** The closed-loop test must be repeatable and
  idempotent, so the default `upstream` target is a **local bare/Forgejo repo**, not real GitHub.
  Real GitHub is a config override (`gh` is present). This is the "sensible default that CAN be
  changed."

## Honesty about what is automatable (loop.md Prompt 5)

- **AC-1 "faster than GitHub incl. queue wait" is not hermetically testable** (needs network +
  real GH). The e2e asserts the **absolute** numbers (≤30s loop round-trip; AC-2 ≤5s warm
  overhead) and documents the GH comparison as a **manual** benchmark. A green self-test will not
  pretend it beat GitHub.
- Each e2e assertion is **mapped to the AC/SC it proves**, and the report marks every criterion
  **pass / fail / deferred** — no silent coverage gaps.

## Architecture of the deliverable

```
sandforge (Go CLI)
├── cmd/sandforge            # cobra-style commands: init import status logs graduate upstream gc reset down  (+ hidden: e2e)
├── internal/compose         # render + up/down/wait the two compose stacks
├── internal/forge           # Forgejo bootstrap: health-gate, admin+token, runner token, register act_runner
├── internal/prd             # parse prd.md success_criteria, run each verify, emit report
├── internal/loop            # branch model + graduate/upstream curation (squash default)
├── internal/log             # JSONL decision + event logger -> .logs/
├── deploy/control-plane     # control-plane.compose.yml (Caddy LB :3000 · Forgejo · Postgres · act_runner) — pinned
├── deploy/tasks-app         # the deploy-target reference app (TS frontend + Go backend + Postgres + Caddy LB)
│   ├── backend (Go: net/http + chi, /healthz + /api/tasks)
│   ├── frontend (React+Vite)
│   ├── deploy-target.compose.yml
│   └── .github/workflows    # default build+test workflows (Go + TS)
└── test/e2e                 # closed-loop e2e harness asserting each AC/SC
```

## Build order (dependency order — de-risk the bootstrap first)

**Phase 0 — Skeleton + logging.** Go module, CLI skeleton, `.logs/decisions` JSONL logger,
`sandforge.yaml` defaults loader (embedded defaults, optional file). Log first decisions.

**Phase 1 — `init` proves the bootstrap (riskiest; design §14).** Control-plane compose up;
health-gate Forgejo; seed admin + local-only token (`0600`); mint runner token; register +
confirm `act_runner` online; print clone URL only after the forge answers. **Gate before moving
on:** a hello-world `.github/workflows` job pushed to Forgejo runs **green** via act_runner.
*(This is the assumption I will verify empirically before building anything on top of it.)*

**Phase 2 — `import` + the inner loop (AC-1 core, AC-4).** `import <url>` seeds a writable repo +
keeps `upstream` remote. Simulate an agent: push `work/<agent>/<slug>`, build+unit runs warm,
branch becomes mergeable. Measure round-trip (assert ≤30s) and warm overhead (assert ≤5s, AC-2).
Concurrency: 3 worktrees push without collision (AC-4).

**Phase 3 — Tasks reference app (deploy-target; prd.md).** Go backend (`/healthz`, CRUD
`/api/tasks`, Postgres), React+Vite frontend, Caddy LB, `deploy-target.compose.yml`, default CI
workflows + unit tests. *Delegated to a subagent; integrated centrally.*

**Phase 4 — `graduate` + PRD validation (AC-6/7/8, SC-*).** Rebase onto fresh (local) upstream;
spin deploy-target on demand; run PRD `verify` for each criterion; emit success-criteria report;
block on any failed `staging-main` gate; assemble squash-by-default curated PR body; tear down.

**Phase 5 — `upstream` + the single-command closed loop.** Push graduated branch + open PR on the
(local by default) upstream via `gh`/`tea`. Then the headline: **one command** (`sandforge e2e`
or a `make e2e`) runs Phases 1→5 against a throwaway instance and asserts every checkable AC/SC,
idempotently (re-runnable; clean teardown).

## Verification (the feedback loop — loop.md Prompt 2.2)

- **Per phase:** the command above each gate, run for real, output pasted, tied to its AC/SC.
- **Whole thing:** `sandforge e2e` (or `make e2e`) exits 0 only if every mapped assertion passes;
  prints a per-AC/per-SC pass/fail/deferred table. This is the closed loop "validate ALL things."
- **Idempotency:** `init`/`reset`/`down` re-runnable; e2e leaves no residue (uses `--ephemeral`).

## Coverage map (what the e2e checks vs. defers)

| Criterion | How validated | Status |
|---|---|---|
| AC-1 (≤30s loop) | measured in Phase 2/5 | auto (absolute number) |
| AC-1 (faster than GH) | manual benchmark doc | **deferred / manual** |
| AC-2 (≤5s warm) | measured | auto |
| AC-3 (review handoff) | review-agent posts PR comments; timed | auto (absolute) / GH-compare deferred |
| AC-4 (3 concurrent agents) | 3 worktrees push | auto |
| AC-5 (no creds leak) | env/log scan in CI | auto |
| AC-6 (one clean PR) | graduate→upstream produces 1 squash PR w/ report | auto |
| AC-7 (PRD machine-checked) | prd validator runs SC-* | auto |
| AC-8 (e2e vs running env) | deploy-target spun, e2e run, torn down | auto |
| AC-9 (footprint) | RAM/start measured | auto (informational) |
| SC-1..5, SC-8..10 | curl / `go test` / scans | auto |
| SC-6, SC-7 (Playwright UI) | browser e2e | **fast-follow** (heaviest) unless chosen in scope |

## Risks

- act_runner ↔ Forgejo Actions compatibility / registration ordering (Phase 1 gate de-risks this first).
- Heavy: deploy-target spin + Playwright. Mitigated by scope choice below + `--wait` health gates.
- Daily-driver Docker daemon contention; fixed ports may clash — handled via configurable ports.
