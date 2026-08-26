#!/usr/bin/env bash
# ==============================================================================
# scripts/process_osrm_data.sh
# Processes .osm.pbf into high-performance OSRM routing graph files
# Runs inside official OSRM Docker container on remote self-hosted runner.
# ==============================================================================
set -euo pipefail

MAP_DIR="${MAP_STORAGE_DIR:-/opt/omnigo/map-data}"
REGION_NAME="${MAP_REGION:-pakistan}"
OSM_PBF="${MAP_DIR}/osm/${REGION_NAME}.osm.pbf"
ROUTING_DIR="${MAP_DIR}/routing/${REGION_NAME}"

if [[ ! -f "$OSM_PBF" ]]; then
    echo "[!] OSM PBF file not found at ${OSM_PBF}. Running sync_map_data.sh first..."
    ./scripts/sync_map_data.sh
fi

mkdir -p "$ROUTING_DIR"
cp -u "$OSM_PBF" "${ROUTING_DIR}/${REGION_NAME}.osm.pbf" || true

echo "=== [OMNIGO OSRM Processor] Processing Road Graph for ${REGION_NAME} ==="

# 1. Extract road graph
echo "==> 1/3 Extracting road graph (osrm-extract)..."
docker run --rm -t -v "${ROUTING_DIR}:/data" ghcr.io/project-osrm/osrm-backend:latest \
    osrm-extract -p /opt/car.lua "/data/${REGION_NAME}.osm.pbf"

# 2. Partition routing cells (Multi-Level Dijkstra)
echo "==> 2/3 Partitioning graph (osrm-partition)..."
docker run --rm -t -v "${ROUTING_DIR}:/data" ghcr.io/project-osrm/osrm-backend:latest \
    osrm-partition "/data/${REGION_NAME}.osrm"

# 3. Customize metrics & turn penalties
echo "==> 3/3 Customizing turn penalties (osrm-customize)..."
docker run --rm -t -v "${ROUTING_DIR}:/data" ghcr.io/project-osrm/osrm-backend:latest \
    osrm-customize "/data/${REGION_NAME}.osrm"

echo "=== [SUCCESS] OSRM routing graph compiled at ${ROUTING_DIR} ==="
