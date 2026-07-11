# syntax=docker/dockerfile:1
# ---- frontend build stage ----
FROM node:22-alpine AS build
WORKDIR /app
# Install deps from the lockfile for reproducibility/speed (npm ci).
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# ---- caddy stage: bake the static dist + Caddyfile ----
FROM caddy:2.8-alpine
COPY lb/Caddyfile /etc/caddy/Caddyfile
COPY --from=build /app/dist /srv
EXPOSE 80
