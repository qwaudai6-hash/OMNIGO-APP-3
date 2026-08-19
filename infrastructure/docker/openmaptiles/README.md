# OMNIGO Self-Hosted Map Stack — Pakistan Data Acquisition

This directory contains the scripts and data needed to run the OMNIGO map stack **fully offline** for the Pakistan region.

## Overview

The OMNIGO map stack (see `OMNIGO_OpenSource_Map_Stack_ADR.md`) replaces MapTiler cloud with self-hosted open-source components:

| Component | Purpose |
|-----------|---------|
| **OpenMapTiles** | Vector map data (roads, buildings, POIs, water) |
| **TileServer GL** | Serves the vector tiles + style.json |
| **OSRM** | Routing, ETA, distance, polyline generation (already deployed) |
| **Photon** | Geocoding + reverse geocoding (already deployed) |
| **PostgreSQL + PostGIS** | Spatial database (already deployed) |
| **Redis** | Cache + pub/sub (already deployed) |

## Quick Start

To generate the Pakistan MBTiles dataset:

```bash
# 1. Make sure the base infrastructure is running
docker compose -f infrastructure/docker/docker-compose.yml up -d omnigo-postgres

# 2. Run the data pipeline (downloads ~700 MB PBF, generates ~5 GB MBTiles)
cd infrastructure/docker/openmaptiles
./download_pakistan_tiles.sh

# 3. Verify the tiles are served
curl http://localhost:8080/data/pakistan.json | head -c 200

# 4. Update .env to point at the self-hosted style
MAPLIBRE_STYLE_URL=http://omnigo-tileserver-gl:8080/data/pakistan.json
# MAPLIBRE_API_KEY=  # leave empty once self-hosted

# 5. Restart map-service
docker compose -f infrastructure/docker/docker-compose.yml restart map-service
```

## What the pipeline does

1. **Download** the latest Pakistan OSM extract from Geofabrik (~700 MB compressed, ~5 GB uncompressed).
2. **Import** it into PostgreSQL using [imposm3](https://github.com/openmaptiles/imposm3) (20-40 minutes on a 4-core machine).
3. **Generate** MBTiles vector tiles via the [OpenMapTiles](https://github.com/openmaptiles/openmaptiles) pipeline (1-3 hours).
4. **Wire** the resulting MBTiles into the `tileserver-gl` container via the `tileserver-data` Docker volume.

## Output

| File | Description |
|------|-------------|
| `data/pakistan-latest.osm.pbf` | Raw OSM extract (~700 MB) |
| `data/pakistan.mbtiles` | Vector tiles in MBTiles format (~5 GB) |
| `data/style.json` | MapLibre style.json (generated automatically) |

## Resource requirements

| Resource | Minimum | Recommended |
|----------|---------|-------------|
| Disk | 30 GB | 50 GB |
| RAM | 8 GB | 16 GB |
| CPU | 4 cores | 8 cores |
| Internet | 1 GB (initial download) | stable |

## Adding other regions

The same pipeline works for any Geofabrik region. Sub-resources:

```bash
# UAE
UAE_PBF_URL="https://download.geofabrik.de/asia/united-arab-emirates-latest.osm.pbf"

# UK
UK_PBF_URL="https://download.geofabrik.de/europe/great-britain-latest.osm.pbf"

# Saudi Arabia
SA_PBF_URL="https://download.geofabrik.de/asia/saudi-arabia-latest.osm.pbf"
```

Or use the full planet extract (~80 GB compressed, ~1 TB unpacked) for global coverage.

## Sourcing OSM data

| Source | URL | Use case |
|--------|-----|----------|
| **Geofabrik** | https://download.geofabrik.de/ | Country/region extracts (recommended) |
| **BBBike** | https://download.bbbike.org/osm/bbbike/ | City-level extracts |
| **Planet OSM** | https://planet.openstreetmap.org/ | Full world (large) |

## Production deployment

For 50M-user scale, the generation pipeline is run once per region, but the serving stack (TileServer GL) is replicated:
- One TileServer GL pod per region, behind a CDN
- MBTiles mounted as a read-only volume
- Updates via weekly cron pulling the latest Geofabrik extract and re-running the pipeline

## TilesServer JSON endpoint

The generated style.json is auto-discovered by TileServer GL because it's mounted at `/data/`. The map-service uses this URL:

```
http://omnigo-tileserver-gl:8080/data/pakistan.json    # style
http://omnigo-tileserver-gl:8080/data/pakistan/{z}/{x}/{y}.pbf   # tiles
```

## Verification

After the pipeline completes:

```bash
# Check the generated MBTiles
docker exec -it $(docker compose ps -q tileserver-gl) \
  ls -lh /data/pakistan.mbtiles

# Sample a tile
curl -o /tmp/tile.pbf "http://localhost:8080/data/pakistan/8/200/130.pbf"
ls -lh /tmp/tile.pbf  # should be a few KB

# Visualize via Maputnik or Mapbox Studio
# (open http://localhost:8080 in a browser)
```

## Troubleshooting

| Issue | Solution |
|-------|----------|
| `imposm3: command not found` | The imposm3 image isn't tagged `:latest` — pin to a specific version like `openmaptiles/imposm3:v0.10.0` |
| `OOM during imposm3 import` | Increase Docker memory to 8+ GB |
| `make: generate-tiles-pg: No rule` | You're not in the openmaptiles checkout directory |
| `permission denied writing to /data` | `chmod 777 infrastructure/docker/openmaptiles/data` |
| Empty tiles in TileServer GL | Check that the MBTiles file path inside the container matches `/data/pakistan.mbtiles` |

## Further reading

- Geofabrik downloads: https://download.geofabrik.de/
- OpenMapTiles schema: https://openmaptiles.org/schema/
- imposm3 docs: https://imposm.org/docs/imposm3/
- TileServer GL: https://github.com/maptiler/tileserver-gl
