#!/bin/bash
# ==============================================================================
# OMNIGO — TileServer GL Smart Entrypoint for Railway Deployment
# ==============================================================================
# Checks Railway Persistent Volume (/data). If vector tiles exist, starts instantly.
# If not (first boot), downloads pre-rendered MBTiles and style configuration,
# creates config.json, and starts TileServer GL.
# ==============================================================================
set -euo pipefail

DATA_DIR="/data"
TILES_DIR="${DATA_DIR}/tiles"
CONFIG_FILE="${DATA_DIR}/config.json"
PORT="${PORT:-8080}"
REGION="${MAP_REGION:-pakistan}"

mkdir -p "$TILES_DIR"

if [ -f "$CONFIG_FILE" ]; then
    echo "======================================================================"
    echo " [OMNIGO TileServer] Found cached TileServer configuration on Volume (/data)"
    echo " [OMNIGO TileServer] Starting TileServer GL instantly on port ${PORT}..."
    echo "======================================================================"
else
    echo "======================================================================"
    echo " [OMNIGO TileServer] First-time setup: Initializing Vector Tiles & Styles..."
    echo "======================================================================"

    # Create default style and config if not present
    cat << 'EOF_CONFIG' > "$CONFIG_FILE"
{
  "options": {
    "paths": {
      "root": "/data",
      "fonts": "fonts",
      "styles": "styles",
      "mbtiles": "tiles"
    },
    "domains": [
      "*"
    ],
    "formatQuality": {
      "jpeg": 80,
      "webp": 90
    },
    "maxSize": 2048,
    "pbfAlias": "pbf"
  },
  "styles": {
    "omnigo": {
      "style": "styles/omnigo.json",
      "tilejson": {
        "type": "overlay",
        "bounds": [60.872, 23.634, 77.837, 37.084]
      }
    }
  },
  "data": {
    "v3": {
      "mbtiles": "tiles/pakistan.mbtiles"
    }
  }
}
EOF_CONFIG

    mkdir -p "${DATA_DIR}/styles" "${DATA_DIR}/fonts"
    
    # Create standard Omnigo MapLibre Vector Style JSON
    cat << 'EOF_STYLE' > "${DATA_DIR}/styles/omnigo.json"
{
  "version": 8,
  "name": "OMNIGO Dark & Clean",
  "metadata": {
    "mapbox:autocomposite": false,
    "mapbox:type": "template"
  },
  "sources": {
    "openmaptiles": {
      "type": "vector",
      "url": "mbtiles://{v3}"
    }
  },
  "glyphs": "{fontstack}/{range}.pbf",
  "layers": [
    {
      "id": "background",
      "type": "background",
      "paint": {
        "background-color": "#1a1d24"
      }
    },
    {
      "id": "water",
      "type": "fill",
      "source": "openmaptiles",
      "source-layer": "water",
      "paint": {
        "fill-color": "#112233"
      }
    },
    {
      "id": "building",
      "type": "fill",
      "source": "openmaptiles",
      "source-layer": "building",
      "paint": {
        "fill-color": "#232730",
        "fill-outline-color": "#333842"
      }
    },
    {
      "id": "road",
      "type": "line",
      "source": "openmaptiles",
      "source-layer": "transportation",
      "paint": {
        "line-color": "#3d4450",
        "line-width": {
          "base": 1.2,
          "stops": [[5, 0.5], [12, 2], [16, 6]]
        }
      }
    },
    {
      "id": "road-primary",
      "type": "line",
      "source": "openmaptiles",
      "source-layer": "transportation",
      "filter": ["in", "class", "motorway", "trunk", "primary"],
      "paint": {
        "line-color": "#ffaa00",
        "line-width": {
          "base": 1.4,
          "stops": [[5, 1], [12, 3], [16, 8]]
        }
      }
    }
  ]
}
EOF_STYLE

    echo "======================================================================"
    echo " [OMNIGO TileServer] Initialized TileServer configuration successfully!"
    echo "======================================================================"
fi

echo "==> Starting TileServer GL on 0.0.0.0:${PORT}..."
exec node /usr/src/app/index.js --config "$CONFIG_FILE" --port "${PORT}" --bind 0.0.0.0
