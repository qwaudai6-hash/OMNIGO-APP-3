# ──────────────────────────────────────────────────────────────
# OMNIGO PLATFORM — Production OSRM Dockerfile (Railway Pro Ready)
# ──────────────────────────────────────────────────────────────
# Lightweight base image. Map data downloads and builds directly
# onto the Railway Persistent Volume (/data) during first boot.
# Subsequent container boots start instantly with 0 downloads.

FROM osrm/osrm-backend:latest

WORKDIR /data

# Install wget & certificates for downloading map data
RUN apt-get update && \
    apt-get install -y --no-install-recommends wget ca-certificates bash curl jq && \
    rm -rf /var/lib/apt/lists/*

# Copy the smart entrypoint
COPY infrastructure/docker/dockerfiles/osrm-entrypoint.sh /osrm-entrypoint.sh
RUN chmod +x /osrm-entrypoint.sh

# Expose standard OSRM port
EXPOSE 5000

# Start via smart entrypoint
ENTRYPOINT ["/osrm-entrypoint.sh"]
