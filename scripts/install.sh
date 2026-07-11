#!/usr/bin/env bash
# Sandforge installer — no Go toolchain required.
#
#   curl -fsSL https://raw.githubusercontent.com/jpoley/sandforge/main/scripts/install.sh | bash
#
# Resolution order (first that works wins):
#   1. pre-built release binary from GitHub Releases (fastest; needs a tagged release)
#   2. local Go toolchain (`go install`)
#   3. build inside a golang Docker container (no Go on the host; Docker is already a
#      Sandforge requirement, so this is the universal fallback)
#
# Options (env):
#   SANDFORGE_INSTALL_DIR   target dir for the binary   (default: ~/.local/bin)
#   SANDFORGE_NO_SKILL=1    skip installing the Claude Code skill into ~/.claude/skills
#
# Principle (docs/premortem.md): never fail silently, never guess — every failure names the
# missing thing and the exact fix.
set -euo pipefail

REPO="jpoley/sandforge"
RAW="https://raw.githubusercontent.com/${REPO}/main"
INSTALL_DIR="${SANDFORGE_INSTALL_DIR:-$HOME/.local/bin}"

say()  { printf '  %s\n' "$*"; }
ok()   { printf '  \033[32m✅ %s\033[0m\n' "$*"; }
warn() { printf '  \033[33m⚠️  %s\033[0m\n' "$*"; }
die()  { printf '  \033[31m❌ %s\033[0m\n' "$*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || die "curl is required to install (how are you running this?)"

# --- platform ------------------------------------------------------------------------------
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in
  darwin|linux) ;;
  *) die "unsupported OS '$OS' — sandforge supports macOS and Linux" ;;
esac
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64)  ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) die "unsupported architecture '$ARCH' (need amd64 or arm64)" ;;
esac
say "platform: ${OS}/${ARCH}"

mkdir -p "$INSTALL_DIR"
BIN="$INSTALL_DIR/sandforge"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# --- method 1: pre-built release binary ----------------------------------------------------
install_from_release() {
  local url="https://github.com/${REPO}/releases/latest/download/sandforge_${OS}_${ARCH}.tar.gz"
  say "trying pre-built release: $url"
  if ! curl -fsSL "$url" -o "$TMP/sandforge.tar.gz" 2>/dev/null; then
    warn "no pre-built release available (repo has no tagged release yet) — falling back"
    return 1
  fi
  tar -xzf "$TMP/sandforge.tar.gz" -C "$TMP" sandforge
  install -m 0755 "$TMP/sandforge" "$BIN"
  ok "installed pre-built release binary"
}

# --- method 2: local Go toolchain ----------------------------------------------------------
install_with_go() {
  command -v go >/dev/null 2>&1 || return 1
  say "building with local Go ($(go version | awk '{print $3}'))"
  GOBIN="$INSTALL_DIR" go install "github.com/${REPO}/cmd/sandforge@latest" || return 1
  ok "installed via go install"
}

# --- method 3: build inside a golang container (no Go on host) ------------------------------
install_with_docker() {
  command -v docker >/dev/null 2>&1 || {
    warn "docker CLI not found — cannot use the container build fallback"
    return 1
  }
  docker info >/dev/null 2>&1 || {
    warn "docker daemon is not running — cannot use the container build fallback"
    return 1
  }
  say "building inside golang container (no Go needed on this machine; takes a minute)…"
  docker run --rm -v "$TMP:/out" \
    -e "GOOS=$OS" -e "GOARCH=$ARCH" -e CGO_ENABLED=0 \
    golang:1.25 sh -ec "
      git clone --depth 1 https://github.com/${REPO}.git /src
      cd /src
      go build -trimpath -ldflags '-s -w' -o /out/sandforge ./cmd/sandforge
    " || return 1
  install -m 0755 "$TMP/sandforge" "$BIN"
  ok "built and installed via Docker (golang:1.25)"
}

echo "Installing sandforge → $BIN"
install_from_release || install_with_go || install_with_docker || die \
"could not install sandforge by any method. You need ONE of:
    • a pre-built release            (none published yet for ${OS}/${ARCH})
    • Go 1.25+                       https://go.dev/dl/
    • Docker (daemon running)        https://docs.docker.com/get-docker/  — sandforge needs Docker anyway"

"$BIN" --help >/dev/null 2>&1 || die "installed binary failed to run: $BIN --help"

# --- Claude Code skill ----------------------------------------------------------------------
# Drops the sandforge-setup skill where Claude Code discovers it, so an AI coding agent can
# install/diagnose/run sandforge end to end (`/sandforge-setup`). Opt out: SANDFORGE_NO_SKILL=1.
if [ "${SANDFORGE_NO_SKILL:-0}" != "1" ]; then
  # Claude Code is "present" if ~/.claude exists OR the claude CLI is on PATH (fresh installs may
  # not have created ~/.claude yet — mkdir below handles that).
  if [ -d "$HOME/.claude" ] || command -v claude >/dev/null 2>&1; then
    SKILL_DIR="$HOME/.claude/skills/sandforge-setup"
    mkdir -p "$SKILL_DIR"
    if curl -fsSL "$RAW/.claude/skills/sandforge-setup/SKILL.md" -o "$SKILL_DIR/SKILL.md"; then
      ok "installed Claude Code skill → $SKILL_DIR (use /sandforge-setup)"
    else
      warn "could not fetch the Claude skill (offline?) — skipping; re-run later or copy .claude/skills/sandforge-setup from the repo"
    fi
  else
    say "Claude Code not detected (no ~/.claude, no claude CLI) — skipping the /sandforge-setup skill; install Claude Code and re-run to get it"
  fi
fi

# --- preflight report (what's still missing to actually RUN sandforge) ----------------------
echo
echo "Preflight for 'sandforge init':"
if command -v docker >/dev/null 2>&1; then
  if docker info >/dev/null 2>&1; then
    ok "docker daemon running"
    if docker compose version >/dev/null 2>&1; then
      ok "docker compose v2"
    else
      warn "docker Compose v2 missing — install the compose plugin (https://docs.docker.com/compose/install/)"
    fi
  else
    warn "docker installed but the daemon is NOT running — start Docker Desktop (or 'colima start' / 'systemctl start docker')"
  fi
else
  warn "docker not installed — required. https://docs.docker.com/get-docker/"
fi
command -v git >/dev/null 2>&1 && ok "git" || warn "git not installed — required"
command -v node >/dev/null 2>&1 && ok "node (for 'graduate'/'e2e' PRD checks)" || say "ℹ️  node/npm not found — only needed for 'sandforge graduate'/'e2e'"
command -v gh   >/dev/null 2>&1 && ok "gh (for 'sandforge upstream')"          || say "ℹ️  gh not found — only needed for the final 'sandforge upstream' PR"

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) warn "$INSTALL_DIR is not in your PATH — add:  export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac

echo
ok "sandforge installed: $BIN"
say "next: sandforge init    (or in Claude Code: /sandforge-setup)"
