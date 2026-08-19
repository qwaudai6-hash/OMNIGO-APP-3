# OMNIGO Super App — Master Execution Roadmap (Post-GLM 5.2 Audit)

**Date**: July 13, 2026
**Context**: This roadmap bridges the billion-dollar scale gaps identified in the exhaustive system audit. It divides the remaining engineering work into hyper-focused Session Phases to guarantee zero-fault, industrial-strength execution.

---

## 🟢 PHASE 1: Rider Fulfillment Engine (REST API & Delivery States)
**Goal**: Build the core backend logic allowing Riders to accept gigs and progress delivery states.

### Step-by-Step Execution:
1. **[Backend/Go] `delivery-gig-service`**:
   - Create `POST /api/v1/delivery/gig/accept` endpoint.
   - Implement transactional Row-Level Locking (`SELECT FOR UPDATE`) in PostgreSQL to prevent race conditions if multiple riders accept the same gig.
   - Validate that the rider is within the original broadcasted H3 hexagon radius.
2. **[Backend/Go] `delivery-gig-service`**:
   - Create `PATCH /api/v1/delivery/gig/:id/status` endpoint.
   - Enforce a strict state machine transition: `broadcasted` -> `accepted` -> `picked_up` -> `in_transit` -> `completed` (or `failed`).
3. **[Backend/Go] Kafka Producers**:
   - Emit `delivery.accepted` and `delivery.status_updated` events so the order-service can update the customer's view simultaneously.

---

## 🟢 PHASE 2: Real-time Telemetry & WebSocket Sink (Rust Gateway)
**Goal**: Wire the Rust WebSocket Gateway to Kafka so gigs are pushed to Riders in real-time.

### Step-by-Step Execution:
1. **[Backend/Rust] `websocket-gateway/src/main.rs`**:
   - Add the `rdkafka` crate dependency.
   - Spawn an async Tokio task to run a Kafka consumer listening to the `deliveries.broadcasted` topic.
2. **[Backend/Rust] Payload Fanout Logic**:
   - When a JSON payload arrives, parse the array of eligible `rider_tracking_ids`.
   - Lock the `DashMap` session registry, find the active WebSocket connections for those riders, and push the JSON byte payload.
3. **[Testing] Local Validation**:
   - Run a mock Python script to publish to Kafka and verify the Rust gateway pushes down to the Flutter WebSockets.

---

## 🟢 PHASE 3: Flutter Client Wiring (Rider App)
**Goal**: Connect the Flutter Rider Map screen to the newly created WebSockets and REST APIs.

### Step-by-Step Execution:
1. **[Frontend] GPS Integration**:
   - Add `geolocator` to `pubspec.yaml` and prompt for location permissions.
   - Replace static Lahore coordinates with live hardware GPS streams.
2. **[Frontend] Telemetry Emitter**:
   - Wire `websocket_client.dart` to emit location JSON packets every 3 seconds to the Rust gateway.
3. **[Frontend] Gig Reception & Actions**:
   - Listen to the WebSocket stream for `action: "GIG_BROADCAST"`. Trigger the bottom sheet UI dynamically.
   - Wire the "Accept" button to the `POST /gig/accept` REST API. Hand errors (e.g., "Gig already taken").

---

## 🟢 PHASE 4: Vendor Scale Hardening
**Goal**: Fix the severe data fetching bottleneck in the Vendor inventory to prevent crashes at scale.

### Step-by-Step Execution:
1. **[Backend/Go] `product-service`**:
   - Create `GET /api/v1/vendor/products` endpoint in `vendor_product_handler.go`.
   - Implement SQL-level filtering (`WHERE vendor_tracking_id = $1`) with `LIMIT` and `OFFSET` pagination.
2. **[Frontend] `vendor_inventory_screen.dart`**:
   - Remove global fetch. Point to the new vendor-specific paginated endpoint.
3. **[Backend/Go] `vendorstore` Metrics**:
   - Rewrite the SQL query for `GET /api/v1/vendor/metrics` to `COUNT(id) FROM orders WHERE status='shipped'` to calculate Active Gigs on the backend.
4. **[Frontend] Dynamic Coordinates**:
   - Update `vendor_live_map_screen.dart` to fetch the store's GPS coordinates from `GET /api/v1/stores/:id` instead of hardcoding.

---

## 🟢 PHASE 5: Customer Double-Billing Safeguard & Map Proxy
**Goal**: Secure the checkout and payment flows against distributed network partitions and API bans.

### Step-by-Step Execution:
1. **[Backend/Go] `order-service`**:
   - Change `CreateOrder` idempotency check from "Fail-Open" to "Fail-Closed" in Redis. Add a composite unique index on `(customer_tracking_id, nonce)` in Postgres as a fallback.
   - Remove synchronous `stores` table lookup. The frontend must pass `vendor_tracking_id` directly in the checkout payload.
2. **[Backend/Go] `wallet-service`**:
   - Implement SHA256 HMAC validation on the JazzCash webhook callback to prevent spoofing.
3. **[Backend/Go] `admin-service`**:
   - Create `GET /api/v1/geo/search` as a proxy to Nominatim. Add strict internal Redis rate limiting and caching.
4. **[Frontend] Map Search**:
   - Update the Flutter search bar to hit the internal proxy instead of public OSM APIs.
