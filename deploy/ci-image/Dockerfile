# Sandforge CI job image — the image act_runner launches for `runs-on: ubuntu-latest`.
#
# Two reasons this exists (do not delete):
#  1. Docker 29+ refuses tar-extraction through the /var/run -> /run symlink that the upstream
#     act images ship ("path escapes from parent"), which silently HANGS every CI job. We make
#     /var/run a real directory.  (see .logs/decisions D-009)
#  2. We bake Go + Node so the inner loop is WARM — no per-run toolchain download — which is the
#     whole point of AC-1/AC-2 (goal.md). Pinned for reproducibility (design §13).
ARG ACT_BASE=ghcr.io/catthehacker/ubuntu:act-22.04
FROM ${ACT_BASE}

ARG GO_VERSION=1.25.4
# TARGETARCH is an automatic build arg populated by BuildKit from the build platform
# (arm64 on Apple Silicon, amd64 on x86_64). Declaring it WITHOUT a default lets BuildKit
# inject the real arch — a hardcoded `=amd64` previously pulled an amd64 Go toolchain onto an
# arm64 base image, so cgo invoked the arm64 gcc with `-m64` and every CI job failed.
ARG TARGETARCH

# 1. De-symlink /var/run so Docker 29's hardened archive extraction works.
RUN rm -f /var/run && mkdir -p /var/run && (cp -a /run/. /var/run/ 2>/dev/null || true)

# 2. Pinned Go toolchain, arch-matched to the image (node + npm already present in the act base).
#    Falls back to dpkg's arch if TARGETARCH is somehow empty (plain `docker build` on old engines).
RUN ARCH="${TARGETARCH:-$(dpkg --print-architecture 2>/dev/null || echo amd64)}" \
 && curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" -o /tmp/go.tgz \
 && tar -C /usr/local -xzf /tmp/go.tgz \
 && rm /tmp/go.tgz
ENV PATH=/usr/local/go/bin:/root/go/bin:$PATH
ENV GOTOOLCHAIN=local

# Smoke: fail the build if toolchains aren't actually warm AND the Go arch matches the image,
# so an arch mismatch fails the IMAGE BUILD loudly instead of every CI job silently later.
RUN go version && node --version && npm --version && ls -ld /var/run \
 && test "$(go env GOARCH)" = "$(go env GOHOSTARCH)" \
 && test "$(go env GOARCH)" = "$(dpkg --print-architecture)"
