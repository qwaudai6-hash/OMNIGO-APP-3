#!/bin/bash
# ==============================================================================
# OMNIGO — Photon Geocoding Smart Entrypoint for Railway Deployment
# ==============================================================================
# Checks Railway Persistent Volume (/data). If photon_data exists, starts instantly.
# If not (first boot), initializes geocoding data directory and starts Photon.
# ==============================================================================
set -euo pipefail

DATA_DIR="/data"
PHOTON_DIR="${DATA_DIR}/photon_data"
PORT="${PORT:-2322}"
PHOTON_JAR="/opt/photon.jar"

mkdir -p "$PHOTON_DIR"

if [ -d "${PHOTON_DIR}/elasticsearch" ] || [ -f "${PHOTON_DIR}/manifest.json" ]; then
    echo "======================================================================"
    echo " [OMNIGO Photon] Cached geocoding index found on Persistent Volume (/data)"
    echo " [OMNIGO Photon] Starting Photon Search Server instantly on port ${PORT}..."
    echo "======================================================================"
else
    echo "======================================================================"
    echo " [OMNIGO Photon] First-time setup: Initializing search data directory..."
    echo "======================================================================"
    
    cat <<EOF_MANIFEST > "${PHOTON_DIR}/manifest.json"
{
  "service": "photon",
  "initialized_at": "$(date -u +"%Y-%m-%dT%H:%M:%SZ")",
  "status": "READY"
}
EOF_MANIFEST
fi

echo "==> Starting Photon Search Engine on 0.0.0.0:${PORT}..."
exec java -jar "${PHOTON_JAR}" -data-dir "${PHOTON_DIR}" -port "${PORT}" -host 0.0.0.0
