# ──────────────────────────────────────────────────────────────
# OMNIGO PLATFORM — Production TileServer GL Dockerfile (Railway Pro Ready)
# ──────────────────────────────────────────────────────────────
# Packages TileServer GL with our smart entrypoint.
# Vector tile data and styles reside on Railway Persistent Volume (/data).

FROM maptiler/tileserver-gl:latest

WORKDIR /data

USER root
RUN (apt-get update && apt-get install -y --no-install-recommends wget curl ca-certificates bash && rm -rf /var/lib/apt/lists/*) || true

COPY infrastructure/docker/dockerfiles/tileserver-entrypoint.sh /tileserver-entrypoint.sh
RUN chmod +x /tileserver-entrypoint.sh

EXPOSE 8080

ENTRYPOINT ["/tileserver-entrypoint.sh"]
