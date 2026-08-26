# Multi-stage Dockerfile for OMNIGO Go microservices + API Gateway
# Builds inside Docker — no pre-compiled binaries needed.
# Usage:
#   docker compose build --build-arg SERVICE=gateway   (default, public entry point)
#   docker compose build --build-arg SERVICE=auth-service
#
# The gateway is the ONLY service that should be publicly exposed.
# All other services communicate over Railway private networking.

# ═══ Stage 1: Build ═══════════════════════════════════════════
FROM golang:1.22-alpine AS builder

RUN apk add --no-cache git ca-certificates gcc musl-dev bash

WORKDIR /app

# Cache dependency downloads
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build all services + gateway. Errors fail the build (no 2>/dev/null).
RUN chmod +x ./build_all.sh && ./build_all.sh

# ═══ Stage 2: Runtime ═════════════════════════════════════════
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata bash wget

# Non-root user for security
RUN addgroup -S omnigo && adduser -S omnigo -G omnigo

WORKDIR /app

# Copy all built binaries
COPY --from=builder /app/bin /app/bin

# Copy migrations so the entrypoint can run them before starting the app
COPY migrations /app/migrations

RUN chown -R omnigo:omnigo /app
USER omnigo

# The SERVICE argument controls which binary to run.
# Default: monolith (all-in-one entry point). It is safe to expose publicly
# because it starts internal services on loopback and proxies external traffic.
ARG SERVICE=monolith
ENV SERVICE_NAME=${SERVICE}

# Gateway listens on $PORT (Railway injects this). Internal services
# listen on their own internal ports via EnvPort().
ARG PORT=8000
EXPOSE ${PORT}

# Healthcheck: every service exposes /health. Gateway probes upstreams via /readyz.
HEALTHCHECK --interval=30s --timeout=5s --retries=3 --start-period=20s \
  CMD wget -qO- "http://127.0.0.1:${PORT}/health" || exit 1

# Entrypoint: migrations are now executed by each Go service on startup using
# the embedded golang-migrate runner with advisory-lock serialization. The
# runtime image therefore only needs to start the selected binary.
CMD ["sh", "-c", "exec /app/bin/${SERVICE_NAME}"]