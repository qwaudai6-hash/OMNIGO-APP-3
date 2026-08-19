# OMNIGO – 100% Open Source & Self-Hosted Map Stack

> **Status:** ✅ Architecture decision ratified. Integration in progress — see [Map Stack Implementation Status](#map-stack-implementation-status) below.
> **Related:** `OMNIGO_Master_Implementation_Plan.md` §6 (Backend Hardening), `task.md` (sprint backlog), `brain/projects/omnigo-super-app/phase_1_execution_details.md`.

## 1. Mandate

Every byte of tile data, routing math, and geocoding the OMNIGO super-app consumes must come from **open-source, self-hosted** infrastructure. No SaaS tile providers, no hosted geocoding APIs, no third-party routing SaaS. The MapTiler API key in `.env` exists only as a **transitional bridge** while OpenMapTiles data is being prepared for the first regional deployment (Pakistan).

---

## 2. The Stack

| # | Component | Open Source | Self-Hosted | Purpose |
|---|-----------|:---:|:---:|---------|
| 1 | **MapLibre GL** | ✅ | ❌ (embedded in Flutter binary) | Flutter Map SDK — renders the map UI in the customer / vendor / rider apps |
| 2 | **OpenMapTiles** | ✅ | ✅ | Vector map data (roads, buildings, POIs, water, landuse) |
| 3 | **TileServer GL** | ✅ | ✅ | Serves vector tiles + the MapLibre style.json to the apps |
| 4 | **OSRM** (Open Source Routing Machine) | ✅ | ✅ | Route calculation, ETA, distance, turn-by-turn narration, polyline generation |
| 5 | **Photon** (or Nominatim) | ✅ | ✅ | Geocoding + reverse geocoding (address ↔ coordinates) |
| 6 | **PostgreSQL + PostGIS** | ✅ | ✅ | Spatial database — nearby search, geofencing, store & delivery zones |
| 7 | **Redis** | ✅ | ✅ | Cache for routes, geocoding results, sessions, frequently-used map data |
| 8 | **Nginx** (or Caddy) | ✅ | ✅ | Reverse proxy, TLS, load balancing, API gateway |

---

## 3. Architecture Decision

**Decision (ADR-001):** The map stack is integrated into the existing monolith infrastructure, not deployed as a separate infrastructure.

### Rationale

The current backend already runs as Docker containers (`infrastructure/docker/docker-compose.yml`). Adding the map services as siblings in the same Docker network gives us:

- ✅ Single infrastructure — one Compose file, one monitoring stack
- ✅ Shared Docker network — `internal-tiles` + `internal-routing` can be private
- ✅ Easier maintenance — one operational team, one CI/CD pipeline
- ✅ Lower infrastructure cost — no second cluster of VMs
- ✅ Faster cross-service communication (kafka + redis + PostGIS are already on the same network)
- ✅ Easy migration to Kubernetes later — the same Docker images can be lifted to k8s without code changes

### Placement

```
┌────────────────────────────────────────────────────────────────────┐
│  Existing Docker Compose (infrastructure/docker/docker-compose.yml) │
│                                                                    │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐  ┌─────────┐  │
│  │ omnigo-      │  │ omnigo-      │  │ omnigo-      │  │ omnigo- │  │
│  │ postgres     │  │ redis cluster│  │ kafka cluster│  │ neo4j   │  │
│  └──────────────┘  └──────────────┘  └──────────────┘  └─────────┘  │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐  ┌─────────┐  │
│  │ omnigo-osrm  │  │ omnigo-osrm- │  │ omnigo-osrm- │  │omnigo-  │  │
│  │ (Pakistan)   │  │ us           │  │ canada       │  │meili-   │  │
│  │ :5000        │  │ :5001        │  │ :5002        │  │search   │  │
│  └──────────────┘  └──────────────┘  └──────────────┘  └─────────┘  │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐  ┌─────────┐  │
│  │ omnigo-tile- │  │ omnigo-      │  │ omnigo-      │  │ minio   │  │
│  │ server-gl    │  │ photon       │  │ map-service  │  │  :9000  │  │
│  │ :8080        │  │ :2322        │  │ (Go proxy)   │  │         │  │
│  └──────────────┘  └──────────────┘  └──────────────┘  └─────────┘  │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐  ┌─────────┐  │
│  │ kong-gateway │  │ notification │  │ email-service│  │ sms-    │  │
│  │ :8000        │  │ -service     │  │              │  │service  │  │
│  └──────────────┘  └──────────────┘  └──────────────┘  └─────────┘  │
└────────────────────────────────────────────────────────────────────┘
```

---

## 4. Application Usage

All three Flutter apps use the **same** centralized self-hosted map platform:

```
┌─────────────────┐
│ Customer App    │──┐
└─────────────────┘  │
┌─────────────────┐  ├──►  ┌──────────────────────────────────────┐
│ Vendor App      │──┤     │  Shared Map Stack                   │
└─────────────────┘  │     │  ┌────────────┐  ┌────────────┐    │
┌─────────────────┐  │     │  │ MapLibre GL │  │ TileServer │    │
│ Rider App       │──┘     │  │  (Flutter) │──│ GL         │    │
└─────────────────┘        │  └────────────┘  └────────────┘    │
                           │  ┌────────────┐  ┌────────────┐    │
                           │  │ OSRM       │  │ Photon     │    │
                           │  └────────────┘  └────────────┘    │
                           │  ┌────────────┐  ┌────────────┐    │
                           │  │ PostGIS    │  │ Redis      │    │
                           │  └────────────┘  └────────────┘    │
                           └──────────────────────────────────────┘
```

---

## 5. Map Service (Go glue layer)

`backend/go-services/internal/map/service/map_service.go` is the **API-key firewall** that lets the apps talk to the map stack without ever knowing the URL of the upstream:

```
┌──────────┐   GET /api/v1/map/style.json  ┌──────────────┐
│  Flutter │ ─────────────────────────────► │              │
│  App     │                                │  map-service │
│          │ ◄─── style.json (internal      │  (Go)        │
│          │      URLs rewritten)           │              │
└──────────┘                                │              │
                                            │  rewrites:   │
                                            │  api.maptiler│
                                            │  ──► /api/...│
                                            │              │
                                            │  upstream:   │
                                            │  tileserver- │
                                            │  gl:8080     │
                                            │              │
                                            │  fallback:   │
                                            │  MapTiler    │
                                            │  (bridge)    │
                                            └──────────────┘
```

This is the migration path from MapTiler (today) → TileServer GL (this quarter). The mobile app never changes.

### Existing endpoints (already live)

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/v1/map/style.json` | Returns the rewritten style.json with internal tile/sprite/glyph URLs |
| GET | `/api/v1/map/tiles/{source}/{z}/{x}/{y}` | Proxy for raster or vector tiles |
| GET | `/api/v1/map/glyphs/{fontstack}/{start}-{end}.pbf` | Proxy for font glyph PBFs |
| GET | `/api/v1/map/sprites/{id}.{png,json}` | Proxy for sprites |
| GET | `/api/v1/map/health` | Liveness probe |

### TODO endpoints (next sprint)

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/v1/map/reverse` | Reverse geocode lat/lng → address (proxy to Photon) |
| GET | `/api/v1/map/search` | Forward geocode address → lat/lng (proxy to Photon) |
| GET | `/api/v1/map/route` | OSRM route calculation — replaces the monolithic OSRM call today |

---

## 6. Phased Migration Plan

### Phase 1 — Bridge (today, production)
- MapTiler API key in `.env` only
- Map-service proxies tiles to MapTiler
- OSRM already self-hosted for PK/US/CA
- Geocoding currently hits public Nominatim (needs to be self-hosted)

### Phase 2 — Geocoding self-hosting (this sprint)
- Deploy Photon container
- `geocoding_handler.go` and `admin/geo.go` rewrite to call `http://photon:2322` instead of `nominatim.openstreetmap.org`
- Add per-IP rate limit on the geocoding handler

### Phase 3 — Tile self-hosting (next sprint)
- Generate OpenMapTiles MBTiles for Pakistan from `pakistan-latest.osm.pbf`
- Deploy TileServer GL container
- Set `MAPLIBRE_STYLE_URL=http://tileserver-gl:8080/styles/positron/style.json`
- Remove MapTiler from `.env.example`
- Drop `MAPLIBRE_API_KEY` requirement (or mark deprecated)

### Phase 4 — Reverse proxy & TLS (production hardening)
- Add Nginx container in front of TileServer GL + OSRM + Photon
- Single internal hostname: `tiles.internal.omnigo.io`
- TLS termination at Nginx
- Public-facing services still go through Kong

### Phase 5 — Per-country scaling (international expansion)
- Each region gets its own Compose stack:
  - Pakistan: `pk` (already partially built)
  - UAE: `ae`
  - UK: `uk`
  - Saudi Arabia: `sa`
- Same Compose file, different OSM extracts
- Shared Postgres + Redis cluster across regions
- GeoDNS routes user to nearest region

---

## 7. Map Stack Implementation Status

| Component | Status | Location | Notes |
|-----------|:---:|----------|-------|
| MapLibre GL Flutter SDK | ✅ Live | `pubspec.yaml:15` | `maplibre_gl: ^0.19.0+2` |
| `MapLibreMapWidget` (centralised) | ✅ Live | `lib/features/shared/presentation/widgets/map_libre_map_widget.dart` | Markers, polylines, register handshake |
| 4 map screens migrated | ✅ Live | `customer_dashboard`, `vendor_inventory`, `vendor_live_map`, `rider_map` | All using `MapLibreMapWidget` |
| `map-service` Go binary | ✅ Live | `internal/map/service/map_service.go` | Style/tile/glyph/sprite proxy |
| OSRM Pakistan | ✅ Live | `docker-compose.yml:205-231` | Port 5000, requires manual PBF drop |
| OSRM US | ✅ Live | `docker-compose.yml:232-258` | Port 5001 |
| OSRM Canada | ✅ Live | `docker-compose.yml:259-285` | Port 5002 |
| Geocoding via Photon | ❌ Missing | should be `docker-compose.yml` + `geocoding_handler.go` | Currently hits `nominatim.openstreetmap.org` directly |
| Geocoding via Nominatim | ❌ Missing | should be `docker-compose.yml` | Fallback option if Photon too heavy |
| TileServer GL | ❌ Missing | should be in `docker-compose.yml` | Currently uses MapTiler as bridge |
| OpenMapTiles data | ❌ Missing | to be generated from `pakistan-latest.osm.pbf` | ~5 GB for Pakistan |
| Photon container | ❌ Missing | `docker-compose.yml` | 1 GB RAM, Java/Spring |
| Nginx reverse proxy | ❌ Missing | `docker-compose.yml` | Per Phase 4 |
| PostGIS extension | ⚠️ Partial | `internal/postgres/init.sql` | Extension installed, no spatial indexes yet |
| Geofencing (PostGIS queries) | ⚠️ Partial | `internal/delivery/repository/delivery_repository.go` | H3 used instead |

### Open Bugs (from audit)

1. **CRITICAL:** `router.go` missing `/api/v1/map` route — map will 404 in production. Fix: add `add("/api/v1/map", "MAP_SERVICE_URL", ...)` to `internal/gateway/router.go`.
2. **CRITICAL:** `router.go` missing `/api/v1/payment` (singular) route — Stripe checkout will 404. Fix: add `add("/api/v1/payment", "ORDER_SERVICE_URL", ...)` so `stripeCheckout()` resolves.
3. **CRITICAL:** Stripe `order_id` race in `product_details_screen.dart:270-486` — buy-now flow creates Stripe PaymentIntent before order exists. Fix: same pattern as `checkout_screen.dart` (create order first, then Stripe).
4. **CRITICAL:** AI self-healing Flutter typo — `/admin/i/audit-overview` should be `/admin/ai/audit-overview`. Fix: `api_endpoints.dart:285`.
5. **HIGH:** Geocoding handler directly hits `nominatim.openstreetmap.org` — rate-limited, will fail at scale. Fix: self-host Photon and rewrite both `geocoding_handler.go` and `admin/geo.go`.
6. **HIGH:** No marker clustering — 100+ live rider markers on customer map will tank performance. Fix: add MapLibre cluster source/aggregation.

---

## 8. Future Scaling

Initially only Pakistan map data is hosted. As OMNIGO expands internationally:

- **PK** stack: `pakistan-latest.osm.pbf` + `OSRM` + `Photon`
- **UAE** stack: `uae-latest.osm.pbf` + `OSRM` + `Photon`
- **UK** stack: `great-britain-latest.osm.pbf` + `OSRM` + `Photon`
- **SA** stack: `saudi-arabia-latest.osm.pbf` + `OSRM` + `Photon`

Each region is a separate Compose stack with its own dataset, but the architecture and code are identical. GeoDNS at the application layer routes the user to the nearest region.

If the monolith ever splits into microservices, these Docker services can be migrated to Kubernetes without code changes — the same `docker-compose.yml` translates to `kubectl apply -f` directly.

---

> **Note:** This ADR builds on `OMNIGO_Master_Implementation_Plan.md` §6 (Map architecture) and the updated Flutter map migration sprint (committed in the previous session).
