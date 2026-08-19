# Tasks — Session 25: Advanced Rider Routing & OSRM Integration (v2 Improved)

- `[x]` **Task 1: OSRM Integration in Go Backend**
  - `[x]` Modify `delivery_service.go` to dynamically route based on status and rider coords.
  - `[x]` Parse OSRM GeoJSON geometry coordinates list.
  - `[x]` Implement Redis shared route cache with 15-second TTL (replacing duplicate OSRM calls).
  - `[x]` Build check successful.

- `[x]` **Task 2: Polyline Rendering & Local Deviation in Flutter**
  - `[x]` Update `rider_map_screen.dart` to parse GeoJSON coordinate list directly (removing package dependency).
  - `[x]` Render path using PolylineLayer.
  - `[x]` Implement local Cartesian distance cross-track projection algorithm.
  - `[x]` Implement automatic recalculation on 3 consecutive deviation ticks (>80m) throttled to 30s.
  - `[x]` Flutter analyze check successful.

- `[x]` **Task 3: Active Routing Cache (Architectural Review)**
  - `[x]` Evaluated local caching in Rust vs. shared caching in Redis.
  - `[x]` Confirmed Go/Redis shared caching is optimal for stateless horizontal scaling.

- `[x]` **Task 4: Verification**
  - `[x]` Go service compilation check.
  - `[x]` Flutter lint/analyzer check.
