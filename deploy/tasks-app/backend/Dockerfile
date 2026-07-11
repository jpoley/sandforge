# syntax=docker/dockerfile:1
# ---- build stage ----
FROM golang:1.25-alpine AS build
WORKDIR /src

# Copy module files + vendored deps first for layer caching and a hermetic,
# offline build (helps the SC-9 cold-build budget).
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY . .

# Static binary, build from the vendor tree (no network needed).
ENV CGO_ENABLED=0 GOFLAGS=-mod=vendor
RUN go build -ldflags="-s -w" -o /out/tasksapp .

# ---- final stage ----
FROM alpine:3.21
RUN adduser -D -u 10001 app
COPY --from=build /out/tasksapp /usr/local/bin/tasksapp
USER app
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/tasksapp"]
