#!/usr/bin/env bash
# ════════════════════════════════════════════════════════════════
#  OMNIGO — OpenMapTiles data download + import for Pakistan
# ════════════════════════════════════════════════════════════════
#
#  Purpose:
#    Generate a self-hosted vector tile dataset for the Pakistan region
#    so the OMNIGO map stack can run entirely offline (no MapTiler
#    cloud dependency).
#
#  What this does:
#    1. Downloads the latest Pakistan OSM extract from Geofabrik.
#    2. Imports it into PostgreSQL using imposm3 (so we can run
#       OpenMapTiles' `make import-data`).
#    3. Generates the MBTiles vector tiles via OpenMapTiles' openmaptiles
#       pipeline.
#    4. Drops the result into the `tileserver-data` docker volume so the
#       `tileserver-gl` container can serve it.
#
#  Prerequisites:
#    - Docker + docker-compose up -d omnigo-postgres photon-search
#    - ~30 GB free disk space (PBF + imposm3 tables + MBTiles)
#    - 8 GB RAM recommended (imposm3 + openmaptiles both RAM-hungry)
#
#  Usage:
#    ./download_pakistan_tiles.sh            # full pipeline
#    ./download_pakistan_tiles.sh --skip-pbf  # reuse existing PBF
#    ./download_pakistan_tiles.sh --skip-import  # reuse existing DB
#    ./download_pakistan_tiles.sh --skip-tiles  # reuse existing MBTiles
#
#  Output:
#    Tiles:   infrastructure/docker/openmaptiles/data/pakistan.mbtiles
#    Style:   infrastructure/docker/openmaptiles/data/style.json
#
#  Then point the map-service at it via:
#    MAPLIBRE_STYLE_URL=http://omnigo-tileserver-gl:8080/data/style.json
# ════════════════════════════════════════════════════════════════

set -euo pipefail

# ── Configuration ──────────────────────────────────────────────────
PK_PBF_URL="https://download.geofabrik.de/asia/pakistan-latest.osm.pbf"
PBF_DIR="infrastructure/docker/openmaptiles/data"
PBF_FILE="${PBF_DIR}/pakistan-latest.osm.pbf"
MBTILES_FILE="${PBF_DIR}/pakistan.mbtiles"
STYLE_FILE="${PBF_DIR}/style.json"

PG_HOST="localhost"
PG_PORT="5433"
PG_DB="omnigo_db"
PG_USER="omnigo_user"
PG_PASS="omnigo_password"

# OpenMapTiles config (use the official repo as a template)
OMT_DIR="infrastructure/docker/openmaptiles/openmaptiles"
OMT_REPO="https://github.com/openmaptiles/openmaptiles.git"
OMT_BRANCH="7.0"

# ── Flags ──────────────────────────────────────────────────────────
SKIP_PBF=0
SKIP_IMPORT=0
SKIP_TILES=0
for arg in "$@"; do
  case "$arg" in
    --skip-pbf) SKIP_PBF=1 ;;
    --skip-import) SKIP_IMPORT=1 ;;
    --skip-tiles) SKIP_TILES=1 ;;
    -h|--help)
      grep -E "^# " "$0" | sed 's/^# //'
      exit 0
      ;;
    *) echo "Unknown arg: $arg"; exit 1 ;;
  esac
done

mkdir -p "$PBF_DIR"

# ── Helpers ────────────────────────────────────────────────────────
log() { printf "\033[1;32m[OMT]\033[0m %s\n" "$*"; }
err() { printf "\033[1;31m[OMT]\033[0m %s\n" "$*" >&2; }

check_docker() {
  if ! command -v docker >/dev/null; then
    err "Docker not installed. Install Docker first."
    exit 1
  fi
  if ! docker info >/dev/null 2>&1; then
    err "Docker daemon not running. Start Docker first."
    exit 1
  fi
}

check_disk() {
  local free_kb
  free_kb=$(df -k "$PBF_DIR" | awk 'NR==2 {print $4}')
  local free_gb=$((free_kb / 1024 / 1024))
  if [ "$free_gb" -lt 30 ]; then
    err "Need at least 30 GB free disk. You have ${free_gb} GB."
    exit 1
  fi
}

# ── Step 1: Download the PBF ───────────────────────────────────────
if [ -f "$PBF_FILE" ] && [ "$SKIP_PBF" -eq 1 ]; then
  log "PBF already exists, skipping download (--skip-pbf)"
elif [ -f "$PBF_FILE" ]; then
  log "PBF already exists at $PBF_FILE, skipping download"
  log "  (use --skip-pbf to skip this check explicitly)"
else
  log "Downloading Pakistan OSM extract from Geofabrik..."
  log "  URL: $PK_PBF_URL"
  log "  Target: $PBF_FILE"
  log "  (Pakistan is ~700 MB, this can take 5-15 minutes)"
  curl -L --fail --retry 3 --retry-delay 5 \
    -o "$PBF_FILE" "$PK_PBF_URL"
  log "Download complete: $(du -h "$PBF_FILE" | cut -f1)"
fi

# ── Step 2: Import into PostgreSQL via imposm3 ─────────────────────
if [ "$SKIP_IMPORT" -eq 1 ]; then
  log "Skipping imposm3 import (--skip-import)"
else
  log "Importing $PBF_FILE into PostgreSQL via imposm3..."
  log "  This typically takes 20-40 minutes for a country-sized PBF."
  log "  It writes to the existing 'omnigo-postgres' container."

  # Run imposm3 in a one-shot container with the PBF mounted as a
  # volume. The imposm3 official image is openmaptiles/imposm3.
  docker run --rm \
    -v "$(pwd)/${PBF_DIR}:/data" \
    -e PBF_FILE=/data/pakistan-latest.osm.pbf \
    -e PG_HOST="$PG_HOST" \
    -e PG_PORT="$PG_PORT" \
    -e PG_DB="$PG_DB" \
    -e PG_USER="$PG_USER" \
    -e PG_PASS="$PG_PASS" \
    openmaptiles/imposm3:latest \
    /opt/imposm3/bin/imposm3 \
      -connection "postgis://${PG_USER}:${PG_PASS}@${PG_HOST}:${PG_PORT}/${PG_DB}" \
      -mapping /opt/imposm3-mapping/mapping.yaml \
      -cachedir /tmp \
      -diff /tmp/pakistan.osc \
      -deployproduction \
      -overwritecache \
      -read /data/pakistan-latest.osm.pbf
  log "imposm3 import complete."
fi

# ── Step 3: Generate MBTiles via OpenMapTiles pipeline ──────────────
if [ "$SKIP_TILES" -eq 1 ]; then
  log "Skipping MBTiles generation (--skip-tiles)"
else
  if [ ! -d "$OMT_DIR" ]; then
    log "Cloning OpenMapTiles repo (branch $OMT_BRANCH)..."
    git clone --depth 1 --branch "$OMT_BRANCH" "$OMT_REPO" "$OMT_DIR"
  fi

  log "Running OpenMapTiles 'generate-tiles-pg' target..."
  log "  This typically takes 1-3 hours for a country-sized extract."
  log "  Output: $MBTILES_FILE"

  # The make target reads from the same PostgreSQL we just wrote to.
  cd "$OMT_DIR"
  PG_HOST="$PG_HOST" PG_PORT="$PG_PORT" PG_DB="$PG_DB" \
  PG_USER="$PG_USER" PG_PASS="$PG_PASS" \
  make generate-tiles-pg
  cp -f "$MBTILES_FILE" "../../../$(basename "$PBF_DIR")/pakistan.mbtiles"
  cd -
  log "MBTiles generated: $(du -h "$MBTILES_FILE" | cut -f1)"
fi

# ── Step 4: Generate a minimal style.json ─────────────────────────
log "Generating OpenMapTiles style.json..."
cat > "$STYLE_FILE" <<'EOF'
{
  "version": 8,
  "name": "OMNIGO Pakistan",
  "sources": {
    "openmaptiles": {
      "type": "vector",
      "url": "/data/pakistan.json"
    }
  },
  "layers": [
    {
      "id": "background",
      "type": "background",
      "paint": { "background-color": "#f8f4f0" }
    }
  ],
  "glyphs": "/data/{fontstack}/{start}-{end}.pbf"
}
EOF
log "Style written: $STYLE_FILE"

# ── Step 5: Restart tileserver-gl with the new data ─────────────────
log "Restarting tileserver-gl to pick up the new MBTiles..."
docker compose -f infrastructure/docker/docker-compose.yml restart tileserver-gl

log ""
log "✅ Pakistan OpenMapTiles data ready."
log ""
log "Point your map-service at the new style with:"
log "  MAPLIBRE_STYLE_URL=http://omnigo-tileserver-gl:8080/data/pakistan.json"
log ""
log "Or browse the tiles in your browser at:"
log "  http://localhost:8080/data/pakistan.json"
log "  http://localhost:8080/data/pakistan/{z}/{x}/{y}.pbf"
log ""
log "Drop the MapTiler API key from .env once you've verified it works."
