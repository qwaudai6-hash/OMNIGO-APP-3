#!/bin/bash
# ==============================================================================
# OMNIGO — OSRM Smart Entrypoint for Railway Deployment
# ==============================================================================
# This script runs on container startup on Railway.
# If /data/routing/map.osrm exists on the persistent volume, it starts instantly.
# If not (first boot), it downloads the OSM data at high datacenter speed,
# builds the MLD routing graph, saves it to /data/routing, and starts serving.
# ==============================================================================
set -euo pipefail

DATA_DIR="/data"
ROUTING_DIR="${DATA_DIR}/routing"
OSM_DIR="${DATA_DIR}/osm"
MAP_OSRM="${ROUTING_DIR}/map.osrm"
PORT="${PORT:-5000}"
REGION="${MAP_REGION:-pakistan}"
FORCE_REBUILD="${FORCE_MAP_REBUILD:-false}"

mkdir -p "$ROUTING_DIR" "$OSM_DIR"

if [ -f "$MAP_OSRM" ] && [ "$FORCE_REBUILD" != "true" ]; then
    echo "======================================================================"
    echo " [OMNIGO OSRM] Cached routing graph found on Persistent Volume (/data)"
    echo " [OMNIGO OSRM] Skipping download and build. Starting server instantly..."
    echo "======================================================================"
else
    echo "======================================================================"
    echo " [OMNIGO OSRM] First-time setup: Initializing routing graph for ${REGION}..."
    echo "======================================================================"

    # Region URL mapping
    PBF_URL="${MAP_URL:-https://download.geofabrik.de/asia/pakistan-latest.osm.pbf}"
    if [ "$REGION" = "uae" ] || [ "$REGION" = "dubai" ] || [ "$REGION" = "gcc" ]; then
        PBF_URL="https://download.geofabrik.de/asia/gcc-states-latest.osm.pbf"
    fi

    RAW_PBF="${OSM_DIR}/${REGION}.osm.pbf"

    echo "==> Downloading OpenStreetMap PBF from: ${PBF_URL}"
    wget -c -q --show-progress "${PBF_URL}" -O "${RAW_PBF}"

    echo "==> Extracting OSRM graph with car profile..."
    cp -f "${RAW_PBF}" "${ROUTING_DIR}/map.osm.pbf"
    cd "${ROUTING_DIR}"
    
    osrm-extract -p /opt/car.lua map.osm.pbf

    echo "==> Partitioning OSRM Multi-Level Dijkstra graph..."
    osrm-partition map.osrm

    echo "==> Customizing OSRM routing weights..."
    osrm-customize map.osrm

    # Clean up temporary PBF to preserve volume disk space
    rm -f "${ROUTING_DIR}/map.osm.pbf"

    # Write manifest
    cat <<EOF_MANIFEST > "${ROUTING_DIR}/manifest.json"
{
  "region": "${REGION}",
  "source": "${PBF_URL}",
  "built_at": "$(date -u +"%Y-%m-%dT%H:%M:%SZ")",
  "algorithm": "mld",
  "status": "READY"
}
EOF_MANIFEST

    echo "======================================================================"
    echo " [OMNIGO OSRM] Routing graph built and saved to /data/routing successfully!"
    echo "======================================================================"
fi

echo "==> Starting OSRM Routed Server on 0.0.0.0:${PORT} (MLD mode)..."
exec osrm-routed --algorithm mld --bind 0.0.0.0 --port "${PORT}" "${MAP_OSRM}"
