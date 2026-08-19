# OMNIGO International OSRM Routing — Application Plan

> Scope: Enable dynamic routing for Pakistan, United States, and Canada using OSRM.

## 1. Current State

- Pakistan OSRM container already defined in `infrastructure/docker/docker-compose.yml`.
- The container downloads `pakistan-latest.osm.pbf` and runs `osrm-extract / partition / customize` on first startup.
- Backend `delivery-gig-service` has a single `OSRM_URL` config and calls OSRM for route + pricing.

## 2. Goal

Run region-aware OSRM routing so a store/customer pair in PK, US, or CA each hits the correct OSRM instance. The service should pick the right OSRM URL based on the store/order region.

## 3. Proposed Architecture

```
                    ┌────────────────────┐
                    │ delivery-gig-service│
                    └────────┬───────────┘
                             │ region from store/order
            ┌────────────────┼────────────────┐
            │                │                │
            ▼                ▼                ▼
    ┌──────────────┐ ┌──────────────┐ ┌──────────────┐
    │  osrm-pk     │ │  osrm-us     │ │  osrm-ca     │
    │  :5000       │ │  :5001       │ │  :5002       │
    └──────────────┘ └──────────────┘ └──────────────┘
```

## 4. Step-by-Step Application Plan

### Step 4.1 — Add region to data model

- `stores` table: add `region VARCHAR(2)` default `'PK'`.
- `orders` table: add `region VARCHAR(2)` default `'PK'` (already has implicit region via store).
- `DeliveryGig` model: add `Region string`.
- `OrderEvent` payload: include `Region`.
- When order is created, copy store's region to order and event.
- When gig is created, persist region.

### Step 4.2 — Multi-region OSRM in docker-compose

Add two more OSRM services to `infrastructure/docker/docker-compose.yml`:

```yaml
  osrm-us:
    image: osrm/osrm-backend:v5.27.1
    container_name: omnigo-osrm-us
    ports:
      - "5001:5000"
    command: >
      bash -c "
        if [ ! -f /data/north-america-latest.osrm.hsgr ]; then
          echo 'Downloading OSRM extract for North America...' &&
          curl -L -o /data/north-america-latest.osm.pbf https://download.geofabrik.de/north-america-latest.osm.pbf &&
          osrm-extract -p /opt/car.lua /data/north-america-latest.osm.pbf &&
          osrm-partition /data/north-america-latest.osrm &&
          osrm-customize /data/north-america-latest.osrm;
        fi &&
        osrm-routed --algorithm mld --bind 0.0.0.0 --port 5000 /data/north-america-latest.osrm
      "
    volumes:
      - osrmdata-us:/data
    restart: always

  osrm-ca:
    image: osrm/osrm-backend:v5.27.1
    container_name: omnigo-osrm-ca
    ports:
      - "5002:5000"
    command: >
      bash -c "
        if [ ! -f /data/canada-latest.osrm.hsgr ]; then
          echo 'Downloading OSRM extract for Canada...' &&
          curl -L -o /data/canada-latest.osm.pbf https://download.geofabrik.de/north-america/canada-latest.osm.pbf &&
          osrm-extract -p /opt/car.lua /data/canada-latest.osm.pbf &&
          osrm-partition /data/canada-latest.osrm &&
          osrm-customize /data/canada-latest.osrm;
        fi &&
        osrm-routed --algorithm mld --bind 0.0.0.0 --port 5000 /data/canada-latest.osrm
      "
    volumes:
      - osrmdata-ca:/data
    restart: always

volumes:
  osrmdata:
  osrmdata-us:
  osrmdata-ca:
```

### Step 4.3 — Backend routing by region

Change `OSRMURL` from a single string to a region map in config:

```go
OSRMURLs map[string]string `mapstructure:"osrm_urls"`
```

Default fallback:

```go
osrmURLs := map[string]string{
    "PK": "http://localhost:5000",
    "US": "http://localhost:5001",
    "CA": "http://localhost:5002",
}
```

In `DeliveryService`, change `osrmURL string` to `osrmURLs map[string]string`. Every route/estimate call resolves the URL with `gig.Region` or `req.Region`.

### Step 4.4 — Service code changes

- `GetRoute(ctx, trackingID)` keeps current logic but picks URL by `gig.Region`.
- `EstimateRide(ctx, req)` adds `Region` field and picks URL accordingly.
- If region missing, fall back to PK.

### Step 4.5 — Flutter / customer app

- Ensure checkout payload sends `dropoff_lat`/`dropoff_lng`.
- (Optional) Send `region` based on store selection or user address.

### Step 4.6 — First-time application

1. Stop current stack: `docker-compose down`.
2. Pull new compose and run:
   ```bash
   docker-compose up -d osrm-pk osrm-us osrm-ca
   ```
3. Wait for downloads + preprocessing. Pakistan is ~100 MB PBF / minutes; US is ~10 GB PBF / 1-2 hours; Canada is ~3 GB PBF / 30-60 minutes. Do this once; afterward volumes persist.
4. Start remaining services.
5. Verify:
   ```bash
   curl "http://localhost:5000/route/v1/driving/74.3587,31.5204;74.3627,31.5184?overview=false"
   curl "http://localhost:5001/route/v1/driving/-74.0060,40.7128;-73.9352,40.7306?overview=false"
   curl "http://localhost:5002/route/v1/driving/-79.3832,43.6532;-75.6972,45.4215?overview=false"
   ```

## 5. Disk/RAM Estimates

| Region | PBF Size | Processed OSRM Data | RAM Needed | Preprocessing Time |
|--------|----------|---------------------|------------|--------------------|
| Pakistan | ~100 MB | ~500 MB | 2-4 GB | 5-15 min |
| United States | ~10 GB | ~30-40 GB | 16-32 GB | 1-3 hours |
| Canada | ~3 GB | ~10-15 GB | 8-16 GB | 30-90 min |

For production, pre-build OSRM files on a CI/build machine and copy into each region's volume, instead of running extraction inside the container on every new host.

## 6. Operational Notes

- Keep separate named volumes per region so re-creation does not re-download everything.
- For dev laptops, keep only Pakistan + Canada running to save disk; US can be spun up on demand.
- Use `OSRM-Source` header already added to distinguish cache vs OSRM hits.
- Add health endpoints to each OSRM container via `docker-compose` `healthcheck`:
  ```yaml
  healthcheck:
    test: ["CMD", "curl", "-f", "http://localhost:5000/route/v1/driving/0,0;0,0?overview=false"]
    interval: 30s
    timeout: 10s
    retries: 5
  ```

## 7. Security / Compliance

- OSRM only exposes route geometry; no personal data flows into OSRM.
- For US/CA, confirm geofabrik.de download is allowed in target deployment region.
- Store processed map data in encrypted volumes in cloud deployments.

## 8. Success Criteria

- `GET /api/v1/delivery/gig/:id/route` returns a valid polyline for PK/US/CA orders.
- `POST /api/v1/ride/estimate` returns vehicle fares for PK/US/CA.
- Rider map renders polyline and ETA for all three regions.
- No regressions in `go build`, `cargo check`, `flutter analyze`.

## 9. Files to Modify

- `infrastructure/docker/docker-compose.yml`
- `infrastructure/postgres/init.sql`
- `backend/go-services/internal/shared/config/config.go`
- `backend/go-services/internal/delivery/service/delivery_service.go`
- `backend/go-services/internal/delivery/models/delivery.go`
- `backend/go-services/internal/order/models/order.go`
- `backend/go-services/internal/order/repository/order_repository.go`
- `backend/go-services/internal/order/service/order_service.go`
- `backend/go-services/cmd/delivery-gig-service/main.go`
- `frontend/omnigo_app/lib/core/network/api_endpoints.dart` (if region-aware endpoints needed)
- `docs/session-27-plan.md` (next session plan)

---
Plan created: 2026-07-14
