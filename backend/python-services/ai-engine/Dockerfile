# Multi-stage Dockerfile for OMNIGO AI Engine (FastAPI + Python)
# gcc/build deps stay in builder; runtime is slim + non-root.

# ═══ Stage 1: Builder ══════════════════════════════════════════
FROM python:3.11-slim AS builder

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc libpq-dev \
    && rm -rf /var/lib/apt/lists/*

COPY requirements.txt .
RUN pip install --user --no-cache-dir -r requirements.txt

# ═══ Stage 2: Runtime ══════════════════════════════════════════
FROM python:3.11-slim

# Only runtime libs needed (no gcc).
RUN apt-get update && apt-get install -y --no-install-recommends \
    libpq5 wget \
    && rm -rf /var/lib/apt/lists/* \
    && addgroup --system omnigo && adduser --system --ingroup omnigo omnigo

WORKDIR /app

# Copy installed packages from builder (--user install → /root/.local)
COPY --from=builder /root/.local /home/omnigo/.local
COPY . .

ENV PATH=/home/omnigo/.local/bin:$PATH
ENV PYTHONUNBUFFERED=1

RUN chown -R omnigo:omnigo /app
USER omnigo

# Internal port (gateway proxies /api/v1/ai/* here). Not publicly exposed.
ARG PORT=8086
ENV PORT=${PORT}
EXPOSE ${PORT}

HEALTHCHECK --interval=30s --timeout=5s --retries=3 --start-period=30s \
  CMD wget -qO- "http://127.0.0.1:${PORT}/health" || exit 1

CMD uvicorn main:app --host 0.0.0.0 --port ${PORT:-8086}