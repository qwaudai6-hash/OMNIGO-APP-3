# Session 25 — Execution Plan: Advanced Rider Routing & OSRM Integration (v2 Improved)

> **Created:** July 14, 2026
> **Architecture Plan:** Dynamic Rerouting & Client-Side Deviation Detection
> **Preceded by:** [[session_24_execution_log]]

---

## 📋 Goal

Implement dynamic, state-aware OSRM routing in the Go backend and client-side polyline deviation tracking in the Flutter app to support auto-recalculation when riders veer off-course.

---

## 📐 Improved Architecture Design

Instead of static store-to-customer routes, the system dynamically calculates routes based on the current delivery status and the rider's real-time position. The client monitors route adherence and automatically requests updates upon deviation.

### Sequence Flow

```mermaid
sequenceDiagram
    participant App as Flutter Rider Client
    participant Service as Go Delivery Service
    participant Redis as Redis Location Store
    participant OSRM as OSRM Container (:5000)
    
    App->>Service: 1. GET /api/v1/delivery/gig/{id}/route
    rect rgb(240, 248, 255)
        Note over Service, Redis: Determine route origin based on gig status & rider position
        Service->>Redis: 2. Query latest GPS for assigned rider (Redis H3 / Location keys)
        Redis-->>Service: 3. Return [rider_lng, rider_lat]
    end
    
    alt Status = "accepted"
        Note over Service: Route: Rider -> Store (Pickup)
        Service->>OSRM: 4. route/v1/driving/RiderCoords;PickupCoords?geometries=geojson
    else Status = "picked_up" OR "in_transit"
        Note over Service: Route: Rider -> Customer (Dropoff)
        Service->>OSRM: 4. route/v1/driving/RiderCoords;DropoffCoords?geometries=geojson
    end
    
    OSRM-->>Service: 5. Return GeoJSON LineString coordinates & ETA/Distance
    Service-->>App: 6. Response: coordinates: [[lng, lat], ...], distance_meters, duration_seconds
    
    loop Every GPS Update (10m)
        App->>App: 7. Compute cross-track error to closest polyline segment
        alt Cross-track error > 80 meters (3 consecutive ticks)
            Note over App: Deviation Detected! Auto-recalculating...
            App->>Service: 8. Trigger GET /api/v1/delivery/gig/{id}/route
        end
    end
```

---

## ⚡ Technical Breakdown

### 1. Go Backend: State-Aware Routing & GeoJSON Output
- **File:** [delivery_service.go](file:///run/media/phatan/New%20Volume/OMNIGO%20E%20COMMERCE%20APP/backend/go-services/internal/delivery/service/delivery_service.go)
- **Model Update:** [delivery.go](file:///run/media/phatan/New%20Volume/OMNIGO%20E%20COMMERCE%20APP/backend/go-services/internal/delivery/models/delivery.go)
  - Change `RouteResponse` to return `[][]float64` mapping GeoJSON LineString points.
- **Service Logic:**
  - Retrieve current gig status from database.
  - Query Redis to resolve the rider's latest location coordinates.
  - Build query coords:
    - If status == `accepted`: `RiderPosition -> PickupLocation`.
    - If status == `picked_up` or `in_transit`: `RiderPosition -> DropoffLocation`.
    - Fallback (no RiderPosition in Redis): `PickupLocation -> DropoffLocation`.
  - Hit OSRM via HTTP using service hostname `http://osrm:5000` (within Docker network) or fallback configured URL.
  - Parse the OSRM GeoJSON geometry coordinates list.

### 2. Flutter Client: Deviation Tracking & Auto-Reroute
- **File:** [rider_map_screen.dart](file:///run/media/phatan/New%20Volume/OMNIGO%20E%20COMMERCE%20APP/frontend/omnigo_app/lib/features/rider/presentation/screens/rider_map_screen.dart)
- **Actions:**
  - Load the coordinate pairs directly into a `List<LatLng>`, avoiding regex/string polyline decoders.
  - Render using `flutter_map` `PolylineLayer`.
  - Implement **Cross-Track Distance Estimation (Cartesian Projection)** inside the location stream listener:
    - Convert latitude/longitude differences locally to approximate meters.
    - Check the distance between the rider's current position and all segments of the active polyline.
    - If the minimum distance to the closest segment exceeds 80 meters for 3 consecutive GPS readings:
      - Trigger `_loadRoute()` to get a recalculated route.
      - Show visual feedback (e.g. a small banner "Rerouting...").

### 3. Verification Steps
- Build Go backend: `go build ./...`
- Verify Flutter analysis: `flutter analyze`
- Test endpoint via curl or integration scripts to ensure JSON geometry coordinates return successfully.
