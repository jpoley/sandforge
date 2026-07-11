---
# Machine-readable header — the PRD-validation tool parses THIS block.
# Each success criterion has: id, statement, level, verify (how it's checked), gate.
#   level:  unit | integration | e2e | nonfunctional
#   gate:   work-staging | staging-main   (which promotion this criterion blocks)
# `graduate` runs every criterion against the on-demand deploy-target stack and emits a
# success-criteria report; the PR is blocked unless all `staging-main` gates pass (goal.md AC-7).
prd:
  id: first-target-tasks-app
  title: "Tasks — reference 3-tier app (TS + Go + Postgres)"
  version: 0.1.0
  status: draft
  stack:
    frontend: { lang: typescript, framework: "React + Vite", served_by: lb }
    backend:  { lang: go, framework: "net/http + chi", api: rest-json }
    database: { engine: postgres, version: "16" }
    lb:       { kind: reverse-proxy, candidate: "caddy | nginx", routes: ["/ -> frontend", "/api/* -> backend"] }
  success_criteria:
    - id: SC-1
      statement: "Backend exposes GET /healthz returning 200 within 2s of container start."
      level: integration
      gate: work-staging
      verify: "curl -fsS http://backend:8080/healthz returns HTTP 200"
    - id: SC-2
      statement: "POST /api/tasks creates a task and returns 201 with a JSON body containing a non-empty id."
      level: integration
      gate: work-staging
      verify: "POST {title} -> 201; response.id is a non-empty string"
    - id: SC-3
      statement: "GET /api/tasks returns previously created tasks (list reflects writes)."
      level: integration
      gate: work-staging
      verify: "create N tasks, GET returns >= N with matching titles"
    - id: SC-4
      statement: "PATCH /api/tasks/{id} toggling done=true is reflected on subsequent GET."
      level: integration
      gate: work-staging
      verify: "PATCH done=true -> 200; GET shows done=true for that id"
    - id: SC-5
      statement: "Data persists across a Postgres container restart (no data loss)."
      level: e2e
      gate: staging-main
      verify: "create task -> restart db service -> GET still returns the task"
    - id: SC-6
      statement: "Through the LB, the frontend at / returns 200 and renders the task list from the API."
      level: e2e
      gate: staging-main
      verify: "GET http://lb/ -> 200; Playwright sees list populated from /api/tasks"
    - id: SC-7
      statement: "End-to-end via the LB: creating a task in the UI makes it appear in the list without reload errors."
      level: e2e
      gate: staging-main
      verify: "Playwright: fill form + submit -> new task row visible"
    - id: SC-8
      statement: "Backend unit tests and frontend unit tests both pass."
      level: unit
      gate: work-staging
      verify: "go test ./...  &&  npm test -- --run"
    - id: SC-9
      statement: "Cold container build + deploy-target stack healthy within 60s on a 16GB laptop."
      level: nonfunctional
      gate: staging-main
      verify: "docker compose -f deploy-target.compose.yml up --wait completes <= 60s"
    - id: SC-10
      statement: "No secret values appear in CI logs or image layers."
      level: nonfunctional
      gate: staging-main
      verify: "log + image scan finds no entries matching the known secret patterns"
---

# Tasks — first deploy-target reference app

> **Why this file exists (two jobs):**
> 1. It is the **canonical example of Sandforge's machine-readable PRD format** — the structured
>    header above is what the PRD-validation step parses to produce the success-criteria report
>    (see [`goal.md`](goal.md) AC-6/AC-7, [`design.md`](design.md) §7).
> 2. It is the **spec for the first deploy-target stack** — a minimal-but-real 3-tier app
>    (TypeScript frontend · Go backend · Postgres) behind an LB, used to exercise Sandforge's
>    full e2e/`graduate` flow ([`design.md`](design.md) §6).

## 1. Overview

A minimal task-tracking app. A user can create tasks, list them, and mark them done. It is
intentionally small but covers a realistic 3-tier shape (browser → LB → frontend/backend → DB) so
Sandforge's e2e-against-a-running-env path is genuinely exercised, not toy-mocked.

## 2. Users & primary flows

- **User** opens the app, sees their tasks, adds a task, toggles one done.
- That single flow is enough to validate routing through the LB, frontend↔backend wiring, and
  persistence — the things that break in real 3-tier deploys.

## 3. Architecture (the deploy-target stack)

![Deploy-target stack — browser to LB; / to frontend, /api/* to backend to Postgres 16](diagrams/deploy-target.png)

This is the **deploy-target stack** (`design.md` §2) — distinct from Sandforge's own
control-plane stack. It is spun **on demand** at `graduate`, e2e runs against the LB, then it is
torn down.

## 4. Functional requirements

| # | Requirement | Maps to |
|---|-------------|---------|
| FR-1 | Create a task with a title | SC-2 |
| FR-2 | List all tasks | SC-3 |
| FR-3 | Mark a task done / not done | SC-4 |
| FR-4 | Tasks persist in Postgres | SC-5 |
| FR-5 | Served as one origin through the LB (`/` UI, `/api/*` API) | SC-6, SC-7 |
| FR-6 | Health endpoint for orchestration | SC-1 |

## 5. API contract (sketch)

```
GET    /healthz            -> 200 "ok"
GET    /api/tasks          -> 200 [{id, title, done, createdAt}]
POST   /api/tasks {title}  -> 201 {id, title, done:false, createdAt}
PATCH  /api/tasks/{id} {done} -> 200 {id, title, done, ...}
```

## 6. Success criteria

Authoritative list is the machine-readable `success_criteria` block in the header. Summary:
- **work → staging gate** (fast): SC-1, SC-2, SC-3, SC-4, SC-8 — API + unit correctness.
- **staging → main gate** (full): SC-5, SC-6, SC-7 (e2e through the LB + persistence) and
  non-functional SC-9 (deploy time), SC-10 (no secret leakage).

## 7. Out of scope (v0.1)

Auth, multi-user, pagination, search, real styling. Deliberately omitted to keep the first target
small; later versions can add criteria here and the validation picks them up automatically.

## 8. Notes for the PRD-validation tool

- Treat the header `success_criteria` as the contract; `verify` describes the check each maps to.
- Emit a report: per-criterion pass/fail + the gate it blocks. Block `upstream` if any
  `staging-main` criterion fails (goal.md AC-7).
- Adding/removing a criterion here changes the gate automatically — the PRD is the single source
  of truth for "done."
