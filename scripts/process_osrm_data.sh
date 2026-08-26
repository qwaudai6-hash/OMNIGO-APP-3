#!/usr/bin/env bash
# ==============================================================================
# scripts/process_osrm_data.sh
# Processes .osm.pbf into high-performance OSRM routing graph files
# Runs inside official OSRM Docker container on remote self-hosted runner.
# ==============================================================================
set -euo pipefail

MAP_DIR="${MAP_STORAGE_DIR:-/opt/omnigo/map-data}"
REGION_NAME="${MAP_REGION:-all}"

TARGET_REGIONS=("pakistan" "uae")
if [[ "$REGION_NAME" != "all" ]]; then
    IFS=',' read -ra TARGET_REGIONS <<< "$REGION_NAME"
fi

for reg in "${TARGET_REGIONS[@]}"; do
    reg_clean=$(echo "$reg" | tr '[:upper:]' '[:lower:]' | xargs)
    OSM_PBF="${MAP_DIR}/osm/${reg_clean}.osm.pbf"
    ROUTING_DIR="${MAP_DIR}/routing/${reg_clean}"

    if [[ ! -f "$OSM_PBF" ]]; then
        echo "[!] OSM PBF file for ${reg_clean} not found. Running sync_map_data.sh..."
        ./scripts/sync_map_data.sh
    fi

    mkdir -p "$ROUTING_DIR"
    cp -u "$OSM_PBF" "${ROUTING_DIR}/${reg_clean}.osm.pbf" || true

    echo "=== [OMNIGO OSRM Processor] Compiling Road Graph for ${reg_clean} ==="

    docker run --rm -t -v "${ROUTING_DIR}:/data" ghcr.io/project-osrm/osrm-backend:latest \
        osrm-extract -p /opt/car.lua "/data/${reg_clean}.osm.pbf"

    docker run --rm -t -v "${ROUTING_DIR}:/data" ghcr.io/project-osrm/osrm-backend:latest \
        osrm-partition "/data/${reg_clean}.osrm"

    docker run --rm -t -v "${ROUTING_DIR}:/data" ghcr.io/project-osrm/osrm-backend:latest \
        osrm-customize "/data/${reg_clean}.osrm"

    echo "[+] OSRM routing graph ready for ${reg_clean} at ${ROUTING_DIR}."
done

echo "=== [SUCCESS] All regional OSRM routing graphs compiled successfully! ==="
