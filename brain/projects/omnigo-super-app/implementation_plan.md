# Phase 7: Event-Driven Telemetry & Rider Geospatial Updates

This plan focuses on upgrading the Rider platform's location tracking and order assignment mechanisms to reach "billion-dollar industry level" scale, eliminating battery drain and database bottlenecks.

## User Review Required

> [!IMPORTANT]
> Based on your feedback, we will use a **Hybrid Geospatial Architecture (Redis + PostGIS)**. Redis will handle high-speed live telemetry, while PostGIS will handle permanent record-keeping and route analytics. Please review the updated plan below.

## Proposed Architecture

### 1. Flutter Rider App (Telemetry Producer)
- Replace HTTP polling with a persistent WebSocket connection (`web_socket_channel`).
- Implement `flutter_background_service` to allow location streaming even when the app is minimized or the screen is locked, ensuring accurate ETA for the customer.

### 2. WebSocket Gateway (Go Service)
- Build a lightweight `websocket-gateway` to terminate WS connections from riders.
- Authenticate WS connections using JWT tokens.
- **Dual-Write Architecture**:
  1. Stream live telemetry data (Lat/Lng) directly into a high-throughput **Redis Cluster**.
  2. Push a lightweight `rider.location.updated` event to **Kafka** to decouple the persistent database load.

### 3. Geospatial Indexing (Live: Redis, Historical: PostGIS)
- **Live Search (Redis)**: The `delivery-gig-service` will use `GEORADIUS` on Redis to instantly find online riders within a 5km radius for new orders, completely bypassing PostgreSQL.
- **Permanent Storage (PostGIS)**: A background worker (e.g., `graph-sync-worker`) will consume the Kafka location events and batch-insert them into a PostgreSQL table equipped with PostGIS (`rider_location_history`). This provides permanent route analytics and historical geofencing.

## Proposed Changes

### Flutter Rider App

#### [NEW] [telemetry_service.dart](file:///home/phatan/Documents/OMNIGO%20E%20COMMERCE%20APP/frontend/omnigo_app/lib/features/rider/services/telemetry_service.dart)
- Background service setup using `flutter_background_service`.
- Continuous `Geolocator` stream.
- Re-connecting WebSocket client.

#### [MODIFY] [rider_dashboard_screen.dart](file:///home/phatan/Documents/OMNIGO%20E%20COMMERCE%20APP/frontend/omnigo_app/lib/features/rider/presentation/screens/rider_dashboard_screen.dart)
- UI toggle to go "Online/Offline", which controls the background telemetry service.

### Backend Go Services

#### [NEW] [websocket-gateway](file:///home/phatan/Documents/OMNIGO%20E%20COMMERCE%20APP/backend/go-services/cmd/websocket-gateway/main.go)
- Create a dedicated bidirectional streaming microservice.
- Implement Redis `GEOADD`.
- Implement Kafka Producer to emit `rider.location.updated` events.

#### [MODIFY] [graph-sync-worker](file:///home/phatan/Documents/OMNIGO%20E%20COMMERCE%20APP/backend/go-services/cmd/graph-sync-worker/main.go)
- Add a Kafka consumer to read location events.
- Perform batch inserts into PostgreSQL using PostGIS spatial columns (`GEOGRAPHY(Point)`).

#### [MODIFY] [delivery_service.go](file:///home/phatan/Documents/OMNIGO%20E%20COMMERCE%20APP/backend/go-services/internal/delivery/service/delivery_service.go)
- Implement `FindNearbyRiders(lat, lng, radius)` using Redis Geospatial queries instead of PostgreSQL.

## Verification Plan
1. **Automated Tests**: Run `go build ./...` to verify compilation.
2. **PostgreSQL Migration**: Ensure PostGIS extension is enabled and `rider_location_history` table exists.
3. **Manual Verification**: Run the Flutter Rider app in the background and verify Redis `GEOPOS` updates in real-time, and PostgreSQL row counts increase.
