# ──────────────────────────────────────────────────────────────
# OMNIGO PLATFORM — Production TileServer GL Dockerfile (Railway Ready)
# ──────────────────────────────────────────────────────────────
# This Dockerfile packages TileServer GL with the Pakistan vector
# tile dataset (MBTiles) and style configuration for 100% self-hosted
# map rendering on Railway or Kubernetes.

FROM maptiler/tileserver-gl:latest

WORKDIR /data

# Expose standard TileServer port
EXPOSE 8080

# Run TileServer GL with self-hosted config
CMD ["--config", "/data/config.json", "--port", "8080", "--bind", "0.0.0.0"]
