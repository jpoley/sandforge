# Containerized Sandforge agents router (local "Copilot"). Builds the sandforge binary and runs
# `agents serve` inside the compose network, so Forgejo posts webhooks container-to-container (no
# host.docker.internal) and the router clones/pushes via the in-network forge address.
#
# Build context is the repo ROOT (the Go module) — so this image is only buildable from a source
# checkout. The standalone embedded binary uses the host daemon (`sandforge agents start`) instead.
#
# NOTE: agent COMMANDS run inside this image. The seed echo-agents work as-is. To run a real CLI
# (claude/codex), extend this image to install it (and mount its credentials), or use the host daemon.
FROM golang:1.25-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/sandforge ./cmd/sandforge

FROM alpine:3.20
# git: the dispatcher clones/pushes the repo. ca-certificates: TLS to any non-loopback forge.
RUN apk add --no-cache git ca-certificates && \
    git config --system user.email "agent@sandforge.local" && \
    git config --system user.name "sandforge-agent"
COPY --from=build /out/sandforge /usr/local/bin/sandforge
EXPOSE 3999
ENTRYPOINT ["sandforge"]
CMD ["agents", "serve", "--port", "3999"]
