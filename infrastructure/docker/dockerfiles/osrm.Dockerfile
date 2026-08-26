# ──────────────────────────────────────────────────────────────
# OMNIGO PLATFORM — Production OSRM Dockerfile (Railway Pro Ready)
# ──────────────────────────────────────────────────────────────
# Lightweight base image. Map data downloads and builds directly
# onto the Railway Persistent Volume (/data) during first boot.
# Subsequent container boots start instantly with 0 downloads.

FROM osrm/osrm-backend:latest

WORKDIR /data

# Configure Debian archive repositories for Stretch EOL and install required tools
RUN sed -i 's/deb.debian.org/archive.debian.org/g' /etc/apt/sources.list && \
    sed -i 's|security.debian.org/debian-security|archive.debian.org/debian-security|g' /etc/apt/sources.list && \
    sed -i '/stretch-updates/d' /etc/apt/sources.list && \
    apt-get -o Acquire::Check-Valid-Until=false update && \
    apt-get install -y --no-install-recommends wget curl ca-certificates bash jq && \
    rm -rf /var/lib/apt/lists/*

# Copy the smart entrypoint
COPY infrastructure/docker/dockerfiles/osrm-entrypoint.sh /osrm-entrypoint.sh
RUN chmod +x /osrm-entrypoint.sh

# Expose standard OSRM port
EXPOSE 5000

# Start via smart entrypoint
ENTRYPOINT ["/osrm-entrypoint.sh"]
