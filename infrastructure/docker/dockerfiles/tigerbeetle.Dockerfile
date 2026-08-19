# ──────────────────────────────────────────────────────────────
# OMNIGO APP - TigerBeetle Standalone Dockerfile for Railway
# ──────────────────────────────────────────────────────────────
# This Dockerfile wraps the official TigerBeetle image with a custom
# entrypoint script that automatically formats the data file on
# the first run, and then starts the replica.
#
# Railway Deployment Instructions:
# 1. Add a new GitHub Repo service in Railway and point it to this Dockerfile.
# 2. Go to the Settings of the newly created service.
# 3. Under "Volumes", add a new Volume and mount it to `/data`.
# 4. That's it! TigerBeetle will securely save ledger data to the volume.

FROM ghcr.io/tigerbeetle/tigerbeetle:latest

# Switch to root to configure the entrypoint
USER root

# Copy our custom entrypoint script
COPY infrastructure/docker/dockerfiles/tigerbeetle-entrypoint.sh /tigerbeetle-entrypoint.sh

# Make sure it's executable
RUN chmod +x /tigerbeetle-entrypoint.sh

# Expose TigerBeetle default port
EXPOSE 3000

# Run the custom entrypoint script
ENTRYPOINT ["/tigerbeetle-entrypoint.sh"]
