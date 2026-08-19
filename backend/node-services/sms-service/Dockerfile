# Multi-stage Node.js Dockerfile for OMNIGO sms-service
# Non-root, minimal runtime image.

# ═══ Stage 1: Build ═══════════════════════════════════════════
FROM node:20-alpine AS builder

WORKDIR /app

COPY package.json package-lock.json ./
RUN npm ci --production

# ═══ Stage 2: Runtime ═════════════════════════════════════════
FROM node:20-alpine

RUN addgroup -S omnigo && adduser -S omnigo -G omnigo

WORKDIR /app

# Copy only production node_modules + source from builder
COPY --from=builder /app/node_modules ./node_modules
COPY --from=builder /app/package.json ./package.json
COPY src ./src

RUN chown -R omnigo:omnigo /app
USER omnigo

# Internal port (not publicly exposed — gateway proxies to it)
ARG PORT=8091
ENV PORT=${PORT}
EXPOSE ${PORT}

HEALTHCHECK --interval=30s --timeout=5s --retries=3 --start-period=10s \
  CMD wget -qO- "http://127.0.0.1:${PORT}/health" || exit 1

CMD ["node", "src/index.js"]
