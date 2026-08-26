# Root Dockerfile for Railway deployment of OMNIGO Monolith & Microservices
FROM golang:1.23-alpine AS builder

RUN apk add --no-cache git ca-certificates gcc musl-dev bash

WORKDIR /app/backend/go-services

# Cache Go dependencies
COPY backend/go-services/go.mod backend/go-services/go.sum ./
RUN go mod download

# Copy backend source
COPY backend/go-services/ .

RUN go mod tidy
RUN chmod +x ./build_all.sh && ./build_all.sh

# ═══ Stage 2: Runtime ═════════════════════════════════════════
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata bash wget curl

# Non-root user for security
RUN addgroup -S omnigo && adduser -S omnigo -G omnigo

WORKDIR /app

# Copy all built binaries
COPY --from=builder /app/backend/go-services/bin /app/bin

# Copy migrations so entrypoint/services can run them
COPY backend/go-services/migrations /app/migrations

RUN chown -R omnigo:omnigo /app
USER omnigo

ARG SERVICE=monolith
ENV SERVICE_NAME=${SERVICE}

ARG PORT=8000
EXPOSE ${PORT}

HEALTHCHECK --interval=30s --timeout=5s --retries=3 --start-period=20s \
  CMD wget -qO- "http://127.0.0.1:${PORT}/health" || exit 1

CMD ["sh", "-c", "exec /app/bin/${SERVICE_NAME}"]
