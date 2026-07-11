#!/usr/bin/env bash
# verify.sh — Sandforge PRD success-criteria runner for the Tasks deploy-target.
#
# Contract:  verify.sh <SC-id>   runs the check for that criterion against the
#            running deploy-target stack and exits 0=pass, non-zero=fail.
#            verify.sh all        runs every SC and prints a PASS/FAIL table.
#
# Env:
#   BASE_URL       LB origin (default http://127.0.0.1:8088)
#   TASKS_PROJECT  compose project name (default sandforge-tasks)
#   TASKS_LB_PORT  host port the LB publishes (default 8088)
#   COMPOSE        compose file path (default deploy-target.compose.yml)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

BASE_URL="${BASE_URL:-http://127.0.0.1:${TASKS_LB_PORT:-8088}}"
TASKS_PROJECT="${TASKS_PROJECT:-sandforge-tasks}"
COMPOSE="${COMPOSE:-deploy-target.compose.yml}"
export TASKS_PROJECT TASKS_LB_PORT="${TASKS_LB_PORT:-8088}"

compose() { docker compose -p "$TASKS_PROJECT" -f "$COMPOSE" "$@"; }

log()  { printf '  %s\n' "$*" >&2; }
fail() { printf 'FAIL: %s\n' "$*" >&2; return 1; }

# ensure_npm <dir> — install node deps idempotently (only if node_modules is missing). Prefer the
# reproducible `npm ci`; fall back to `npm install` if there is no lockfile. All chatter -> stderr
# so it never pollutes a criterion's captured stdout.
ensure_npm() {
  local dir="$1"
  [ -d "$dir" ] || { fail "missing dir $dir"; return 1; }
  if [ -d "$dir/node_modules" ]; then return 0; fi
  log "installing npm deps in $dir (one-time) ..."
  # With a lockfile, use `npm ci` ONLY — a failed ci must fail the criterion, not silently fall
  # back to `npm install` (which would mutate the lockfile and validate against unlocked deps).
  if [ -f "$dir/package-lock.json" ]; then
    ( cd "$dir" && npm ci --no-audit --no-fund ) >&2 \
      || { fail "npm ci failed in $dir (lockfile out of sync — not silently installing)"; return 1; }
  else
    ( cd "$dir" && npm install --no-audit --no-fund ) >&2 \
      || { fail "npm install failed in $dir"; return 1; }
  fi
}

# ensure_playwright_browsers — install the Chromium browser Playwright needs (idempotent; a second
# call is a fast no-op once the browser is cached under ~/.cache/ms-playwright).
ensure_playwright_browsers() {
  log "ensuring Playwright Chromium is installed ..."
  ( cd e2e && npx --yes playwright install chromium ) >&2 \
    || { fail "playwright browser install failed"; return 1; }
}

# --- helpers ---------------------------------------------------------------

# create_task <title> -> prints the created id (and asserts 201 + non-empty id)
create_task() {
  local title="$1" body code id
  body="$(curl -fsS -w '\n%{http_code}' -X POST "$BASE_URL/api/tasks" \
    -H 'Content-Type: application/json' \
    -d "{\"title\":\"$title\"}")" || { fail "POST /api/tasks failed"; return 1; }
  code="$(printf '%s' "$body" | tail -n1)"
  body="$(printf '%s' "$body" | sed '$d')"
  [ "$code" = "201" ] || { fail "POST returned $code, want 201"; return 1; }
  id="$(printf '%s' "$body" | jq -r '.id')"
  [ -n "$id" ] && [ "$id" != "null" ] || { fail "response.id empty"; return 1; }
  printf '%s' "$id"
}

# --- success criteria ------------------------------------------------------

sc_1() {
  # GET /healthz returns 200, measured within 2s.
  local code t
  t="$(curl -fsS -o /dev/null -w '%{http_code} %{time_total}' \
        --max-time 2 "$BASE_URL/healthz")" || { fail "healthz unreachable within 2s"; return 1; }
  code="${t%% *}"
  local elapsed="${t##* }"
  log "healthz: HTTP $code in ${elapsed}s"
  [ "$code" = "200" ] || { fail "healthz returned $code"; return 1; }
}

sc_2() {
  # POST creates a task -> 201 with non-empty id.
  local id
  id="$(create_task "sc2-$(date +%s%N)")" || return 1
  log "created task id=$id"
}

sc_3() {
  # Create N tasks, GET returns >= N with matching titles.
  local n=3 i title titles=() ids
  for i in $(seq 1 "$n"); do
    title="sc3-$(date +%s%N)-$i"
    create_task "$title" >/dev/null || return 1
    titles+=("$title")
  done
  local list
  list="$(curl -fsS "$BASE_URL/api/tasks")" || { fail "GET /api/tasks failed"; return 1; }
  local count
  count="$(printf '%s' "$list" | jq 'length')"
  [ "$count" -ge "$n" ] || { fail "GET returned $count tasks, want >= $n"; return 1; }
  for title in "${titles[@]}"; do
    printf '%s' "$list" | jq -e --arg t "$title" 'any(.[]; .title == $t)' >/dev/null \
      || { fail "title $title missing from list"; return 1; }
  done
  log "all $n titles present (list has $count)"
}

sc_4() {
  # PATCH done=true -> 200; GET shows done=true.
  local id code
  id="$(create_task "sc4-$(date +%s%N)")" || return 1
  code="$(curl -fsS -o /dev/null -w '%{http_code}' -X PATCH "$BASE_URL/api/tasks/$id" \
        -H 'Content-Type: application/json' -d '{"done":true}')" \
    || { fail "PATCH failed"; return 1; }
  [ "$code" = "200" ] || { fail "PATCH returned $code, want 200"; return 1; }
  local done
  done="$(curl -fsS "$BASE_URL/api/tasks" | jq -r --arg id "$id" '.[] | select(.id==$id) | .done')"
  [ "$done" = "true" ] || { fail "GET shows done=$done for $id"; return 1; }
  log "task $id done=true after PATCH"
}

sc_5() {
  # Persistence across DB restart.
  local id title
  title="sc5-$(date +%s%N)"
  id="$(create_task "$title")" || return 1
  log "created $id; restarting db..."
  compose restart db >/dev/null 2>&1 || { fail "db restart failed"; return 1; }
  # Wait for db healthy again.
  local i
  for i in $(seq 1 30); do
    if compose ps db --format '{{.Health}}' 2>/dev/null | grep -q healthy; then break; fi
    sleep 1
  done
  # Retry the GET (lazy reconnect window).
  for i in $(seq 1 15); do
    if curl -fsS "$BASE_URL/api/tasks" | jq -e --arg id "$id" 'any(.[]; .id==$id)' >/dev/null 2>&1; then
      log "task $id survived db restart"
      return 0
    fi
    sleep 1
  done
  fail "task $id not found after db restart"
}

sc_6() {
  # Playwright: list populated through the LB.
  ensure_npm e2e || return 1
  ensure_playwright_browsers || return 1
  ( cd e2e && E2E_BASE_URL="$BASE_URL" npx playwright test -g "SC-6" )
}

sc_7() {
  # Playwright: create-in-UI appears.
  ensure_npm e2e || return 1
  ensure_playwright_browsers || return 1
  ( cd e2e && E2E_BASE_URL="$BASE_URL" npx playwright test -g "SC-7" )
}

sc_8() {
  # Unit tests: backend + frontend. No running stack required.
  # Backend is pure Go (CGO off → hermetic, no C toolchain).
  ( cd backend && CGO_ENABLED=0 go test ./... ) || { fail "go test failed"; return 1; }
  ensure_npm frontend || return 1
  ( cd frontend && npm test -- --run ) || { fail "frontend tests failed"; return 1; }
}

sc_9() {
  # Cold build + up --wait <= 60s. Tears down first for an honest cold number.
  log "tearing down for a cold measurement..."
  compose down -v >/dev/null 2>&1
  local start end elapsed
  start="$(date +%s)"
  if ! compose up --build --wait --wait-timeout 90 >/dev/null 2>&1; then
    fail "stack did not become healthy"
    return 1
  fi
  end="$(date +%s)"
  elapsed=$(( end - start ))
  log "cold build + up --wait: ${elapsed}s"
  [ "$elapsed" -le 60 ] || { fail "took ${elapsed}s, budget 60s"; return 1; }
}

sc_10() {
  # No secret values in container logs or image layers.
  # Pragmatic pattern set: private keys, AWS access keys, generic bearer tokens,
  # and a planted canary sentinel. The DB password is an env value and is
  # tolerated in `inspect`, but must not appear in logs or baked image history.
  local patterns='BEGIN [A-Z ]*PRIVATE KEY|AKIA[0-9A-Z]{16}|ASIA[0-9A-Z]{16}|xox[baprs]-[0-9A-Za-z-]+|SANDFORGE_CANARY_[A-Za-z0-9]+'
  local hits=0 svc img
  # Scan logs.
  if compose logs --no-color 2>/dev/null | grep -nEi "$patterns" >&2; then
    hits=1
  fi
  # Scan image history (baked layers) for each service image.
  for svc in backend lb; do
    img="$(compose images -q "$svc" 2>/dev/null | head -n1)"
    [ -n "$img" ] || continue
    if docker history --no-trunc --format '{{.CreatedBy}}' "$img" 2>/dev/null \
        | grep -nEi "$patterns" >&2; then
      hits=1
    fi
  done
  [ "$hits" -eq 0 ] || { fail "secret pattern found in logs/image layers"; return 1; }
  log "no secret patterns found in logs or image history"
}

# --- dispatch --------------------------------------------------------------

run_one() {
  local sc="$1" fn
  fn="sc_$(printf '%s' "$sc" | sed 's/^SC-//; s/^sc-//')"
  if ! declare -F "$fn" >/dev/null; then
    echo "unknown criterion: $sc" >&2
    return 2
  fi
  "$fn"
}

run_all() {
  local ids=(SC-1 SC-2 SC-3 SC-4 SC-5 SC-6 SC-7 SC-8 SC-9 SC-10)
  local sc rc results=()
  local overall=0
  for sc in "${ids[@]}"; do
    printf '\n=== %s ===\n' "$sc" >&2
    if run_one "$sc"; then
      results+=("$sc PASS");
    else
      rc=$?; results+=("$sc FAIL"); overall=1
    fi
  done
  printf '\n==== success-criteria report ====\n'
  printf '%s\n' "${results[@]}"
  return "$overall"
}

main() {
  local arg="${1:-}"
  case "$arg" in
    ""|-h|--help)
      echo "usage: verify.sh <SC-1..SC-10|all>" >&2; exit 2 ;;
    all|ALL) run_all ;;
    *) run_one "$arg" ;;
  esac
}

main "$@"
