# OMNIGO Super App - Project Log

This file tracks the session-wise progress of the OMNIGO Super App (E-Commerce, Delivery, and Ride-Hailing) built using a highly scalable polyglot microservices architecture.

---

## Session 1: Master Architecture & Implementation (July 12, 2026)

**Goal:** Lay the foundational infrastructure and code base for the entire Super App, supporting 9 Million users, dual pricing (PKR/USD), Vendor Commissions, Rider Delivery tracking, and Live Ride Hailing.

### 1. Database & Infrastructure Setup
- Configured a comprehensive `docker-compose.yml`.
- Set up **PostgreSQL** as the primary relational database.
- Created `init.sql` schema to handle `users`, `vendor_stores`, `products`, `orders`, `deliveries`, and `rides`.
- Set up **Redis Cluster** for caching and Geo-Spatial Tracking.
- Set up **Apache Kafka** & Zookeeper for asynchronous event-driven queues.
- Configured **OSRM (Open Source Routing Machine)** for localized distance/time calculations.

### 2. Universal Tracking ID (UTID) Implementation
- Designed the overarching `UTID` tracking concept where every entity has a unique tracking identity (e.g., `CUST-xyz`, `VEND-abc`, `PROD-123`, `RIDR-789`). This allows tracking orders, products, and rides back to the specific vendor or customer.

### 3. Go Core Microservices (Phase 3)
Built lightweight, high-concurrency Go APIs connecting to PostgreSQL, Redis, and Kafka:
- **Vendor Store Service**: CRUD operations for vendor management.
- **Product Service**: Products with dual currency support.
- **Order Service**: Order placement, which triggers the `orders.created` Kafka event.
- **Delivery Gig Service**: A consumer/producer that assigns orders to nearby riders using Redis `GEORADIUS` and emits `delivery.requested` events.
- **Ride Service**: Allows customers to hail rides (Uber/Careem style), emitting `ride.requested` events.
- **Commission Logic**: Implemented 1-2% flexible cuts for vendors and 2-5% cuts for riders.

### 4. Rust Security & Real-Time Gateway (Phase 4)
Built highly secure, memory-safe Rust services using `actix-web`:
- **Auth Service**: Uses `Argon2id` for password hashing and issues secure JWTs. Also enforces mandatory `cnic_url` and `license_url` for Riders during signup, locking their account (`is_verified = false`) until an Admin approves them.
- **WebSocket Gateway**: Maintains persistent websocket connections using `actix-web-actors`, routing live gig data directly to the rider's flutter app via their UTID.

### 5. Python AI Engine (Phase 5)
Built a `FastAPI` intelligence engine:
- **Fraud Detection API**: Endpoint to catch suspicious or high-risk orders.
- **ETA Prediction API**: Endpoint to calculate precise arrival times using ML logic.
- **Recommendation Engine**: Endpoint to suggest products based on user browsing history.

### 6. Node.js Event Handlers (Phase 6)
Built fast asynchronous workers using `Express` and `kafkajs`:
- **Notification Service**: Consumes Kafka events and pushes notifications to devices via Firebase Cloud Messaging (FCM).
- **Email Service**: Consumes Kafka events and sends receipts via `Nodemailer`.
- **Web Storefront**: Scaffolded an SSR Server using **Next.js** for web users.

### 7. Flutter Mobile App Initialization (Phase 7)
- Scaffolded `omnigo_app` using Flutter.
- Set up `api_client.dart` for REST API communication to NGINX.
- Set up `websocket_client.dart` for connecting to the Rust Gateway.
- Created `rider_map_screen.dart` integrating `flutter_map` (Leaflet) and OpenStreetMap tiles to view live locations and gig boundaries securely without Google Maps costs.

---

**Next Session Objectives:**
- Boot up Docker Infrastructure (`docker-compose up -d`).
- Wire the Flutter App UI completely with the microservices.
- Begin E2E testing of the Order/Ride flow.

---

## Session 2: Master Super App System Logic & UX Overhaul (July 12, 2026)

**Goal:** Refine and document the core business logic of OMNIGO as a Super App (E-Commerce + Delivery + Ride Hailing/Pick & Drop) in our Obsidian vault, and implement the dynamic routing dashboard flow.

### 1. Chain of Tracking ID System
Every user gets a prefix-based Tracking ID (UTID) assigned automatically at signup:
- **Customer Tracking ID (`CUST-xxxx`):** Tracks their profile, location, orders, purchase history (what they bought, from which vendor store), and rides.
- **Vendor Tracking ID (`VEND-xxxx`):** Identifies the vendor owner.
- **Vendor Store Tracking ID (`STOR-xxxx`):** Identifies the vendor's E-commerce storefront (enabling multi-vendor stores per vendor owner).
- **Product Tracking ID (`PROD-xxxx`):** Uniquely identifies each product, linking it back to its specific `STOR-xxxx` and `VEND-xxxx`.
- **Rider Tracking ID (`RIDR-xxxx`):** Identifies the rider, tracking which orders they delivered, from which store they picked them up, and to which customer they delivered them.
- **Admin Tracking ID (`ADMN-xxxx`):** Grants access to view the complete tracking ecosystem.

### 2. E-Commerce Order to Gig Broadcast Flow
1. **Purchase:** A Customer (`CUST-xxxx`) purchases a Product (`PROD-xxxx`) from a Vendor's Store (`STOR-xxxx`).
2. **Alert:** The order is instantly pushed via Kafka to the Vendor Dashboard with all customer and order details.
3. **Acceptance & Slip:** The Vendor accepts the order and is given the option to print an order/delivery slip.
4. **Gig Broadcast:** The Vendor broadcasts the order as a **Gig Order** (broadcasted via location using Redis Geo).
5. **Rider Notification:** The Gig Order appears in real-time on the Leaflet Map of all nearby Riders (`RIDR-xxxx`).
6. **Delivery Execution:** The Rider accepts the Gig, picks up the parcel from the vendor's store, and delivers it to the customer. Every state change updates the admin dashboard showing the exact chains of Tracking IDs.

### 3. Ride Hailing & Pick-and-Drop System
- If a Rider does not have an active product delivery, they can toggle their status to offer **Pick & Drop Services** (Uber/Careem style) directly to Customers.
- Customers can book riders for personal travel or direct package delivery (Vendor sending custom parcel to a customer outside the standard shop flow).

### 4. Leaflet Map Integration (OpenStreetMap)
- The map uses `flutter_map` with OpenStreetMap (OSM) tiles to prevent API billing.
- Integrated into the Flutter bottom navigation bar.
- Dynamic markers show customers nearby services and riders their broadcasted Gig pickup/drop coordinates.

### 5. International & Localized Features
- **Payment Gateways:** Supports Stripe (International) and Easypaisa/Jazzcash (Pakistan).
- **Multi-language:** Multi-language localization configuration.

---

## Session 3: Real Code & Billion Dollar UI Overhaul (July 12, 2026)

**Goal:** Elevate OMNIGO's design system to meet international client expectations by implementing functional code (Customer Profile, Catalog Filtering, Leaflet Map Search) and saving complete Obsidian architecture documents.

### 1. Unified Project Obsidian Documentation
- Created [docs/OMNIGO_SuperApp_Architecture.md](file:///home/phatan/Documents/OMNIGO%20E%20COMMERCE%20APP/docs/[[[[OMNIGO_SuperApp_Architecture]]]].md) containing the complete microservice architecture diagram, data schema mappings, order-to-gig flows, and geo-spatial logic.

### 2. Customer Profile Integration (Bottom Navigation Tab 4)
- Implemented a gorgeous, clean profile panel showing:
  - Tracking ID (UTID)
  - Personal Information (Name, Phone, Email, Delivery Address)
  - Active Payment Methods (Stripe / JazzCash / EasyPaisa)
  - Customer Service & Multi-language Settings.

### 3. Product Catalog with Dynamic Search Filter
- Enhanced the E-Commerce tab:
  - Added category selectors (All, Shoes, Apparel, Electronics).
  - Wired the top Search bar to dynamically filter catalog products in real-time, matching industrial product lookup requirements.

### 4. Leaflet Map Geocoder Search API
- Added location search to the `flutter_map` dashboard.
- Users can input cities/regions (e.g., Karachi, Lahore, Islamabad, London, New York) to automatically geocode and center the Leaflet Map camera, plotting custom mock target markers.

---

## Session 4: Enterprise High-Concurrency Optimization (July 12, 2026)

**Goal:** Refine the system architecture to handle 9 Million concurrent users smoothly by resolving database, geo-tracking, routing, and intelligence bottlenecks.

### 1. Database Scaling Refinement
- **PgBouncer:** Added a PgBouncer layer between Go/Rust services and the PostgreSQL database cluster to prevent connection exhaustion.
- **Read/Write Segregation:** Configured read segregation where all read requests (catalog, profile status) query multiple **PostgreSQL Read Replicas**, leaving the **PostgreSQL Primary DB** exclusively for transaction writes.
- **Nonce-Based Idempotency Guard:** Coded in the Go Order Service to validate incoming transactions using a SHA-256 hash of Customer ID, Product ID, and a Device Session Nonce, backed by a 120-second Redis TTL.

### 2. Geo-Tracking Memory & Flow Management
- **TTL Location Logs:** Configured location logs in Redis to automatically clear via TTL (5-minute expiration) to avoid memory crashes.
- **TimescaleDB Cold Storage:** Set up cold storage archiving where time-series location data is periodically batched from Redis and saved into **TimescaleDB** for audit logs.
- **Rust WS sliding window buffer:** Built a sliding window buffer inside the Rust WebSocket gateway. It holds incoming telemetry logs in a Redis-backed ZSET for 2 seconds, sorts them chronologically by Vector Clock, and pushes them to Kafka to fix out-of-order tracking jumps.

### 3. CPU-Intensive Compute Optimization
- **Stateless OSRM Clusters:** Moved the C++ OSRM routing engine to a stateless auto-scaling cluster (Kubernetes Pods) scaling on CPU demand.
- **Dynamic H3 k-Ring Expansion:** Coded a dynamic search ring algorithm inside the Go Delivery service. If no riders are found within the primary hexagon (5km), it dynamically expands the radius up to 25km until matching riders are returned.
- **Async Python AI Pipeline:** Decoupled Python AI Service using Kafka queues (recommendations/fraud predictions are pushed to event streams and batch-processed asynchronously by Python AI) and introduced gRPC for high-speed sync tasks.

---

## Session 5: CDC Event Sourcing & Graph Database Sync (July 12, 2026)

**Goal:** Implement Change Data Capture (CDC) pipelines using Debezium and Kafka SMTs to stream database updates directly to Neo4j graph schemas, bypassing dual-write lags.

### 1. Kafka Connect SMT (Single Message Transform)
- **Unwrap SMT Engine:** Configured Debezium with the `ExtractNewRecordState` transform. This flattens the complex database WAL envelope, outputting clean, unnested transactional updates directly to Kafka topics (`dbstream.public.orders`).

### 2. Neo4j Graph Synchronization Worker
- **Go Graph Sync Worker:** Written a complete, signal-controlled worker in Go using Franz-go and the official Neo4j Driver v5.
- **Optimistic Batching (UNWIND):** Configured the worker to run on an async queue. Records are batched (up to 100 records or 500ms timeout) and written in a single transaction using the Cypher `UNWIND` pipeline, preventing database lock contention.
- **Bootstrap Constraint Integration:** Embedded constraint initialization on startup to execute `CREATE CONSTRAINT FOR (c:Customer) REQUIRE c.utid IS UNIQUE` (along with Store and Order uniqueness constraints) to ensure indexation and zero deadlocks.

---

## Session 6: E-Commerce Shopping Cart & Payment Checkout Engine (July 12, 2026)

**Goal:** Create high-performance, race-condition protected checkout transactional backend endpoints and Redis-cached cart services.

### 1. Redis Cluster Shopping Cart Service
- **Go Cart Service:** Coded a Redis-only cart manager in Go. Handles `AddToCart`, `RemoveFromCart`, and `GetCart` via Redis Hash maps (`cart:CUST_ID`) with 7 days TTL cache policy, completely bypassing PostgreSQL.

### 2. Stripe Checkout Transaction Service
- **Safe Stock Decrementation:** Coded transactional query `SET stock = stock - X WHERE id = Y AND stock >= X` in Go. Ensures atomic product checkouts and prevents negative stock values under high concurrency.
- **Stripe SDK Integration:** Integrated `github.com/stripe/stripe-go/v76` to generate Payment Intents. If Stripe fails, the PostgreSQL transaction rolls back immediately, releasing locked stock.
- **Lock Idempotency:** Integrates Redis-backed checkout idempotency locks to block duplicate billing.

---

## Session 16: Vendor Module Complete Audit Resolution (July 13, 2026)

**Goal:** Execute all remaining vendor module phases from the Session 15 audit — critical bugfixes, catalog CRUD, live map telemetry, dashboard metrics wiring, dead code cleanup.

### Priority 0: Critical Bugfixes
- **P0-B:** Rewrote `infrastructure/postgres/init.sql` to match Go code as source of truth — BIGSERIAL PKs, `stores` table (not `vendor_stores`), `base_price`/`stock`/`description`/`category`/`image_url` columns, tracking-id-based refs, `updated_at` triggers.
- **P0-C:** Fixed `docker-compose.yml` — mounted `init.sql` into `/docker-entrypoint-initdb.d/`, aligned credentials to `omnigo_user/omnigo_password/omnigo_db`, deleted stale `mock_data.sql`.
- **P0-A:** Fixed WebSocket dual-listen crash in `websocket_client.dart` — routed channel stream through `StreamController.broadcast()`, added platform-aware host (10.0.2.2), exponential backoff reconnection (1s→30s cap), `WSConnectionState` enum.

### Phase 1 Finish
- **P1-A:** `/vendor-analytics` route guard verified (already in place in `main.dart:78`).
- **P1-B:** Signup metadata passthrough verified (already in place in `dynamic_signup_screen.dart:135-144`).

### Phase 2 Complete: Dynamic Catalog CRUD
- **P2-A:** Added `AddProduct` vendor endpoint — POST `/api/v1/vendor/products/`, vendor ID from auth header, `VerifyStoreOwnership` guard, auto PROD-xxxxxx UTID.
- **P2-B:** Added `UpdateProduct` endpoint — PUT `/api/v1/vendor/products/:id`, partial update via `*pointer` fields, dynamic SET clause, ownership-guarded.
- **P2-C:** New `vendor_add_product_screen.dart` (dual-mode add/edit form), FAB + tap-to-edit + long-press-to-delete in inventory screen.
- **P2-D:** Fixed `ProductModel.fromJson()` + `toJson()` to include `category` field.

### Phase 4 Complete: Kafka & Cache
- **P4-B:** Removed dead Kafka allocations from `vendor-store-service/main.go` + `product-service/main.go`.

### Phase 5 Complete: Live Map & Dashboard
- **P5-A:** Dashboard `$300.00` → live `/api/v1/vendor/metrics` total_revenue; `2 Gigs` → count of `status=='shipped'` orders.
- **P5-B:** WebSocket reconnection logic (done with P0-A).
- **P5-C:** Live map telemetry model `LiveInventoryTelemetry` → `RiderTelemetry` (lat/lng/rider_id), rider marker rendering, status overlay text fixed.

**Full details:** [[session_17_customer_audit_resolution]] (Session 17 log includes verification + cross-references)

---

## Session 17: Customer Audit Action Items Resolution (July 13, 2026)

**Goal:** Resolve the 3 remaining action items from the Session 4 customer-side audit.

### 1. Server-side Search & Category Filtering ✅
- **Problem:** Customer catalog fetched ALL products then filtered client-side with `.where()`. Category pills were decorative.
- **Fix:** `ApiEndpoints.productsList()` now accepts `search` + `category` params. `customer_dashboard_screen.dart` uses 400ms debounced server fetch (`_onSearchChanged`) and immediate category filter (`_onCategorySelected`). Go backend already supported `?search=` (ILIKE) and `?category=` — no Go changes needed.

### 2. Nominatim OSM Geocoding ✅
- **Problem:** Map tab used 7-city hardcoded `_mockGeocodingDb`.
- **Fix:** `_searchMap()` rewritten as `async` — calls `https://nominatim.openstreetmap.org/search?format=json&limit=1&q=...` with proper `User-Agent` header. Mock DB kept as offline fallback. Loading spinner on search icon while geocoding.

### 3. Real-time Live Rider Tracking on Customer Map ✅
- **Problem:** Customer Map tab had no WS telemetry link — rider location never shown.
- **Fix:** Added `WebSocketClient` integration — `_connectRiderTelemetry()` connects when customer has shipped orders. Parses `{rider_id, lat, lng}` frames, updates `ValueNotifier<Map<String, LatLng>> _riderMarkers`. Map tab renders rider markers via `ValueListenableBuilder` (isolated rebuild, base map untouched). Live tracking status banner at bottom.

**Full details:** [[session_17_customer_audit_resolution]]

---

**Next Session Objectives:**
- Quantity selector on product details screen.
- Real product images (wire `image_url` to image widgets).
- Stripe payment integration (replace hardcoded display).
- Edit profile + address management forms.

---

## Session 18: Customer Experience Gaps (July 13, 2026)

**Goal:** Resolve 4 remaining customer-side feature gaps — quantity selector, real product images, edit profile + address management, Stripe payment integration.

### Phase 1: Quantity Selector + Real Product Images
- Quantity stepper (−/count/+) on product details. `total_amount = unitPrice * _quantity`. `CartProvider.addItem()` accepts optional quantity param.
- Product cards and details screen now use `Image.network(image_url)` with error fallback.

### Phase 2: Edit Profile + Address Management
- Backend: `GET /api/v1/auth/profile` + `PATCH /api/v1/auth/profile` (partial update via `*string` pointers, tracking_id guard).
- SessionRegistry: added `address` field + `updateProfile()` method.
- New `edit_profile_screen.dart` — form with Full Name, Phone, Address (email read-only).
- Profile tab: live address display + "Edit Profile" button.

### Phase 3: Stripe Payment Integration
- Backend: `POST /api/v1/checkout` (Payment Intent) + `POST /api/v1/webhooks/stripe` wired into order-service.
- Profile tab: fake payment cards replaced with "Tap to add a card" + clear status.
- Checkout flow: calls checkout endpoint, gets client_secret, marks payment method.

**Full details:** [[session_18_execution_log]]

---

## Session 19: Stripe SDK, Wishlist, Reviews, Mobile Wallet (July 13, 2026)

**Goal:** Resolve the final 4 customer-side gaps — flutter_stripe SDK, wishlist/favorites, product reviews/ratings, JazzCash/EasyPaisa scaffolding.

### Phase 1: flutter_stripe SDK
- Added `flutter_stripe: ^10.0.0` to pubspec. Initialized in main.dart with publishable key.
- Checkout flow: `initPaymentSheet()` → `presentPaymentSheet()` — full native card entry + 3DS auth.

### Phase 2: Wishlist / Favorites
- New `favorites` table (UNIQUE customer+product). Toggle/list/remove endpoints wired to product-service.
- Flutter: optimistic heart toggle on product cards, `_fetchWishlist()` on init.

### Phase 3: Product Reviews/Ratings
- New `reviews` table (rating 1-5 CHECK, UNIQUE customer+product, upsert via ON CONFLICT).
- Create/list/summary endpoints. COALESCE for zero-review products.
- Flutter: reviews section in product details — avg rating badge, top 3 reviews, star-selector dialog for submission.

### Phase 4: JazzCash/EasyPaisa Scaffolding
- `POST /api/v1/wallet/charge` (returns redirect URL) + `POST /api/v1/wallet/callback` (form-encoded gateway callback).
- Flutter: payment method selector dialog (Card / JazzCash / EasyPaisa / Cash) before checkout.

**All customer-side audit items from Sessions 4–19 are now resolved.**

**Full details:** [[session_19_execution_log]]

---

## Session 20: PayFast, Wishlist Tab, Order Details, Admin Module (July 13, 2026)

**Goal:** PayFast PK full integration, wishlist bottom-nav tab, order detail view, admin module audit resolution.

### Phase 1: PayFast PK Full Integration
- **Research:** PayFast PK (gopayfast.com) — auth token, MD5 signature, hosted checkout, callback verification.
- **Backend:** `payfast_service.go` — GetAuthToken, CreateSignature (MD5), InitiateHostedCheckout (redirect URL), VerifyCallback (integrity hash). Routes: `/api/v1/wallet/payfast/charge` + `/callback`.
- **Frontend:** "PayFast (PK)" option in payment selector → POST charge → redirect URL.

### Phase 2: Wishlist Tab
- New `wishlist_screen.dart` — grid of favorited products, remove via long-press/heart tap.
- Bottom nav: 6th tab (heart icon) — Home, Search, Wishlist, Map, Orders, Profile.

### Phase 3: Order History Detail View
- New `order_detail_screen.dart` — full breakdown: header, visual status timeline (4 steps), products, store/vendor/rider info, payment, OTP.
- Orders tab: each order tappable → OrderDetailScreen.

### Phase 4: Admin Module Fix
- **Backend:** Fixed admin-service (DB creds, Neo4j optional, graceful shutdown). Fixed lineage query. Added user verification API (ListPendingVerifications, ApproveUser, ListAllUsers).
- **Frontend:** Complete rewrite of admin screen — 3 tabs (Lineage search, Pending approvals with Approve button, All Users). Real API, no mock data.

**Full details:** [[session_20_execution_log]]

---

## Session 21: Security, Debezium, Env Config, DB Optimization (July 13, 2026)

**Goal:** Hardened backend security, configured event streams, and tuned database indexes for enterprise scaling.

- **Real JWT Cryptographic Signing:** Replaced fake token strings with cryptographically verified HMAC-SHA256 tokens in `auth-service`.
- **CORS & Rate Limiting Middleware:** Implemented a shared middleware package with tight cross-origin policies and Redis-backed token bucket limits (30 req/min for auth, 100 req/min for core APIs).
- **Debezium CDC Connector:** Configured PostgreSQL CDC stream to Kafka topic `dbstream.public.orders` using `ExtractNewRecordState` SMT.
- **Advanced DB Tuning:** Added composite indexes, optimized Postgres connection pools, and configured table partitions.

**Full details:** [[session_21_execution_log]]

---

## Session 22: Event-Driven Telemetry & Geospatial Sync (July 13, 2026)

**Goal:** Implemented a highly scalable, real-time driver tracking engine with dual-write decoupling.

- **Rust Actix WebSocket Gateway:** Configured WebSocket Gateway to asynchronously publish incoming driver coordinates to Kafka topic `rider.location.updated` and automatically report `offline` status on connection loss.
- **Go Location Sync Worker:** Created `location-sync-worker` to consume telemetry events and perform Dual-Writes: live tracking state in Redis (`GEOADD`/`ZREM`) and historical trace tracking in PostGIS.
- **Geospatial Dispatch:** Updated `delivery-gig-service` to query the live Redis `riders:locations` index using `GEORADIUS` to match ready orders to the nearest riders within a 5km radius.
- **Background Telemetry Service:** Integrated `flutter_background_service` and `web_socket_channel` to stream GPS locations continuously from Android background processes, wired to a functional UI Online/Offline toggle.
- **Architecture Validation:** Verified that the stateless WebSocket gateway + Kafka + sync worker -> Redis/PostGIS architecture aligns with industry-standard, high-scale templates.

**Full details:** [[session_22_execution_log]]

---

## Session 23: Concurrency, Telemetry, and Geospatial Hardening (July 13, 2026)

**Goal:** Hardened real-time tracking pipelines, resolved event loop blocks, eliminated Redis slot hotspots, and optimized mobile CPU/battery usage.

- **Rust Gateway Hardening:** Added unique session Uuids to connection registries to prevent eviction races on quick reconnections, and serialized Kafka writes per socket.
- **Go Worker Non-Blocking Loop:** Refactored Kafka poll actions to a background goroutine, processing events via a Go channel to allow immediate graceful shutdown.
- **Redis Cluster H3 Sharding:** Sharded rider coordinates using resolution 5 H3 indexes (`riders:locations:h3:<h5_hex>`) and updated the delivery gig matcher to search k-ring 1 neighbor hexagons in parallel.
- **Mobile Hardware Optimization:** Integrated speed-based adaptive update frequencies and battery-saving transmission scaling inside the background Flutter isolate.

**Full details:** [[session_23_execution_log]]

---

## Session 24: Offline Buffering & JWT Refresh Rotation (July 13, 2026)

**Goal:** Hardened mobile client reliability during cellular dead zones and secured JWT authentication lifecycles for multi-day rider shifts using RTR.

### 1. Database Schema (`infrastructure/postgres`)
- **User Refresh Tokens Table:** Created table `user_refresh_tokens` manually inside the PostgreSQL container with unique constraints on `token_hash` and indexes on `user_tracking_id` and `token_hash` to support fast RTR lookups.

### 2. Go Auth Service (`backend/go-services`)
- **Refresh Token Rotation (RTR):** Shortened access token validity to 1 hour and implemented rotation. When a client requests a new access token, a fresh, rotated cryptographically secure UUID-based refresh token is generated and persisted as a SHA-256 hash.
- **Compromise Detection:** If a revoked refresh token is reused, all refresh tokens associated with that user are instantly invalidated to prevent session hijacking.
- **DB Configuration Fallback:** Fixed `main.go` database configuration loader to fallback cleanly to default credentials when config files are absent.

### 3. Rust WebSocket Gateway (`websocket-gateway`)
- **Signed HMAC-SHA256 Token Validation:** Added parsing and verification of real signed HMAC-SHA256 JWT tokens. Uses `jsonwebtoken` crate to verify signature, expiration, and extract tracking identity.
- **Backward Compatibility:** Retained fallback support for legacy mock token validation formats during testing.

### 4. Flutter Client (Registry, Sign-Up & Telemetry Isolate)
- **Persistent Token Storage:** Updated `SessionRegistry` and `DynamicSignUpScreen` to save and load the `refresh_token` in `SharedPreferences`.
- **Local Telemetry FIFO Buffer:** Configured the background telemetry isolate to buffer coordinates to local memory (`SharedPreferences` serialized FIFO queue) if the WebSocket connection drops, and send buffered coordinates sequentially upon reconnection.
- **Proactive Token Refresh:** Integrated proactive background HTTP POST token refresh cycles when token expires or gateway throws 401.

### 5. Automated E2E Telemetry Test Harness (Go)
- **E2E Integration Test:** Written a Go integration client test script in `scripts/test_e2e_telemetry.go` verifying the entire path: registration, login, token refresh rotation, reuse protection, and WebSocket connection.

---

**Next Session Objectives:**
- Implement dynamic rider routing using OSRM APIs in the Rider Map.
- Add multi-vehicle type support (Bike, Rickshaw, Car) and dynamic delivery pricing estimation.
- Build automated tests for the admin-service user verification flows.

---

## Session 25: Advanced Rider Routing & OSRM Integration (July 14, 2026)

**Goal:** Implement dynamic, state-aware OSRM driving calculations and client-side route deviation tracking with auto-recalculation in Flutter.

### 1. Go Backend State-Aware Routing
- **Model Update:** Updated `RouteResponse` struct in `delivery.go` to return coordinate matrices (`Coordinates [][]float64`) instead of polyline string.
- **Dynamic Origin-Destination Selection:** Modified `GetRoute` to read the gig status and query Redis for the assigned rider's latest telemetry coordinates.
  - Heading to Pickup (Status: `accepted`): Routes from `RiderPosition -> PickupLocation`.
  - In-Transit (Status: `picked_up` / `in_transit`): Routes from `RiderPosition -> DropoffLocation`.
  - Fallback (no RiderPosition): Routes from `PickupLocation -> DropoffLocation`.
- **Stateless Cache Layer:** Configured OSRM coordinates query results to be cached in Redis with a 15-second TTL, preventing duplicate OSRM calls while ensuring real-time routing accuracy for moving riders.

### 2. Flutter Client Route Deviation & Auto-Reroute
- **Natively Parsed GeoJSON Coordinates:** Refactored `_loadRoute` to parse coordinates list directly into a `List<LatLng>`, removing the dependency on external string decoders.
- **Cartesian Cross-Track Distance Projection:** Added a lightweight cross-track distance formula in `rider_map_screen.dart` to calculate the rider's current distance to the route's segments.
- **Auto-Reroute & Throttling:** If the rider is >80 meters off-route for 3 consecutive GPS updates, the app automatically triggers a recalculation, throttled to once every 30 seconds to prevent API flooding.

### 3. Verification & Validation
- Go backend builds successfully.
- Flutter analyze check passed with zero compilation issues.

---

**Next Session Objectives:**
- Implement dynamic surge multipliers based on rider-customer H3 density ratios.
- Add multi-vehicle choices (Bike, Rickshaw, Car) with base rate and per-km pricing calculations.
- Design vehicle selector UI sheet on the customer dashboard.

---

## Session 40: Bug-Hunt Remediation (July 16, 2026)

**Source:** Session 39 bug-hunt (7 bugs documented). Bugs #1–#4 were already fixed in Sessions 31–33 and verified in code this session; #5, #6, #7 completed now.

### BUG #5 — Consolidate Checkout (DONE)
- **Root cause:** `customer_dashboard_screen.dart` carried a dead `if (false)` bottom-sheet checkout (old lines 634–786) calling `_processCartCheckout`, which POSTed with wrong `currency: 'USD'` and bypassed the fixed `CheckoutScreen`.
- **Fix (delete over add):** removed the dead bottom-sheet block, `_processCartCheckout`, `_showSuccessCheckoutDialog`, `_showErrorCheckoutDialog`, the `_isCheckoutProcessing` field + "Securing Transaction..." overlay. Single canonical flow = `CartScreen` → `CheckoutScreen` (BUG #1-fixed).
- `Uuid`/`http`/`jsonEncode`/`SharedPreferences` still used elsewhere; imports valid. Grep confirms zero refs to deleted symbols.

### BUG #6 + #7 — Location Pipeline Latency (DONE)
- **Fix:** `syncworker/worker.go:45` flush ticker `2 * time.Second` → `500 * time.Millisecond`.
- `go build ./internal/syncworker/` passes.
- **ponytail:** chose one-line flush-interval drop over the bigger "direct H3-sharded write from gateway" refactor. Latency drops ~1.5s. Upgrade path: direct sharded write if dispatch visibility still lags under load.

### Session 40 Verification Summary
- Priority bugs #1–#4: already DONE (verified in code, Sessions 31–33).
- Optional #5, #6, #7: DONE this session.
- All 7 bugs resolved.

**Next Session Objectives:**
- Manual QA pass on the unified checkout flow (Cart → Checkout → order 201).
- Load-test sync-worker at 500ms flush under peak rider-online volume.

---

## Session 41: Location Pipeline — Live Cache Layer + Peak-Load Fast Path (July 16, 2026)

**Goal (from Session 40 follow-up):** add a non-breaking, separate layer to `syncworker/worker.go` for low-latency rider GPS + safe peak-load direct DB writes. The stable batch `flushBatch` path is untouched.

### Added (separate methods, no base-code edits except one fan-out call in Start)
- **Redis Live GPS Cache** (`cacheLiveGPS`): flat geo set `riders:live:gps` + raw JSON `rider:live:gps:<id>` (30s TTL). OSRM reads freshest positions without waiting on SQL sync. Offline clears the entry.
- **Peak-Load Switch** (`isPeakLoad` + `SetPeakMode`): Redis-backed distributed lock `syncworker:peak:direct-db` (SetNX, 5m TTL) — reuses the existing lock convention from order/payment/delivery services. Only one worker owns the toggle, so no race on flip.
- **Direct DB-Sharded Write** (`directDBShardedWrite`): during peak, mirrors the H3-sharded Redis writes (`rider:clock:`, `rider:last_h5:`, `riders:locations:h3:*`) immediately for a single rider. Per-rider+clock idempotency key (`SetNX`, 2s) prevents duplicate direct writes from replayed Kafka messages.
- **`routeLiveLayer`**: the single per-payload entry from `Start()`; fire-and-forget (200ms ctx) so it can never stall the stable flush loop. Runs `cacheLiveGPS` always + `directDBShardedWrite` only when peak active.

### Safety guarantees
- Base `flushBatch` (PostGIS history + H3 Redis pipeline) is byte-for-byte unchanged.
- Peak path is additive and idempotent; normal path unaffected when lock key absent.
- `go build ./internal/syncworker/` passes.

---

**Next Session Objectives (OSRM feature section — appended per request):**
1. **Dynamic peak-load switch via distributed lock.** Wire `SetPeakMode` to the autoscaler/operator so peak windows flip `syncworker:peak:direct-db` safely. Validate no race when multiple workers boot simultaneously (only one acquires the SetNX lock). Confirm direct H3-sharded writes land before dispatch query during surge.
2. **Redis live GPS cache backing OSRM.** Point the Rider Map / route-deviation math at `riders:live:gps` (sub-500ms) instead of the SQL-synced store; keep SQL as the async source of truth via `flushBatch`. Add a cache-miss fallback to the existing sharded key path.
3. **Load-test at 500ms flush + peak direct-write** under peak rider-online volume; measure dispatch visibility latency before/after the live layer.

---

## Session 41 — Addendum: Redsync Quorum Lock (July 16, 2026)

**Change from original scope:** the simple SetNX peak switch (single key, one winner) was replaced with a **full Redsync quorum lock** safe across multi-master / multi-shard Redis. A SetNX-only lock is unsafe when Redis is deployed as independent masters (no cross-master coordination) — two workers on different masters could both believe they own the toggle. Redsync requires N/2+1 masters, so a minority of reachable masters cannot grant the lock.

### Code changes in `backend/go-services/internal/syncworker/worker.go`
- `Worker.redis` type widened from `*redis.ClusterClient` to `redis.UniversalClient` (ClusterClient still satisfies it; lets tests use plain `*redis.Client`).
- `NewWorker(db, kafka, redisClient redis.UniversalClient)` and `NewWorkerQuorum(db, kafka, redisMasters ...redis.UniversalClient)` both accept `redis.UniversalClient`. Removed the dead `newWorkerQuorumUniversal` + `asCluster` helpers.
- `peakMutex()` -> `rs.NewMutex("syncworker:peak:switch", WithExpiry(5m), WithTries(3))`.
- `peakSwitchValueKey = "syncworker:peak:direct-db"`; `isPeakLoad` reads the value key without holding the lock.
- `SetPeakMode(ctx, on)` acquires the quorum peak mutex, then SETs or DELs the value key, returns `bool` (true = exclusive owner applied the change). Errors are logged and treated as "not owned" (safe default).
- `directDBShardedWrite` kept its SetNX idempotency gate (`directDBIdemKeyPrefix + RiderID`, 2s TTL) AND added a per-rider quorum mutex `syncworker:direct-db:rider:<id>` (2s expiry) so a single rider's shard move is atomic even under replayed Kafka messages.
- **Stable `flushBatch` (PostGIS history + H3 Redis pipeline) is byte-for-byte unchanged.**

### Tests (`worker_test.go` + `worker_stress_test.go`)
- `newMiniNode(t)` wraps `miniredis.Run()` into a plain `*redis.Client` (miniredis v2.38.0 has no `RunCluster` helper; cluster command support is internal only).
- PASS: `TestNewWorkerSeedsQuorum`, `TestNewWorkerQuorumMultiMaster`, `TestRouteLiveLayerNilRedis`.
- PASS: `TestQuorumPeakSwitchContention` (32 goroutines × 50 flips).
- PASS: `TestQuorumNoMasterMajorityFailsSafe` (1 live + 2 dead masters → lock refused, value never set → safe survival).
- PASS: `TestQuorumPerRiderWriteNoRace` — asserts each rider is a member of EXACTLY ONE h3 bucket (shard-move atomicity). NOTE: the assertion uses `ZRange` membership, NOT `ZRank`, because miniredis returns a non-negative rank for absent members (real Redis returns nil). This is a miniredis quirk, not a code bug.
- `go build ./internal/syncworker/` and `go vet ./internal/syncworker/` clean; full package test suite green (18.4s).

### Git commit scope
- Committed ONLY the syncworker quorum work (`worker.go` + the two test files) to avoid bundling the dozens of unrelated working-tree modifications across the monorepo.

---

## Session 42: In-App Chat + Auth Recovery + 2FA + Map Stack Pipeline (July 30, 2026)

**Goal:** Close the four remaining production gaps — Daraz-style in-app chat, forgot-password flow, email verification, TOTP-based 2FA, and the self-hosted map stack data pipeline.

**Detailed execution plan + log:** [[session_32_execution_plan_and_log]]
**Map stack ADR:** [[OMNIGO_OpenSource_Map_Stack_ADR]]

### What was built

**1. In-app chat (Daraz-style, all 3 apps)**
- Backend: `GET /api/v1/chat/conversations` + `GET /api/v1/chat/unread` with LATERAL JOIN for the chat list per user.
- Frontend: shared `ChatService` singleton, `ChatListScreen`, `ChatRoomScreen`, `ChatNavButton` with green badge.
- Chat button integrated into customer dashboard (replacing notification bell), vendor dashboard (FAB), and rider map (above Re-Center GPS).

**2. Auth recovery — forgot password**
- Schema: `password_reset_tokens` (1-hour TTL, SHA-256-hashed).
- Endpoints: `POST /api/v1/auth/forgot-password` (always 200, no enumeration), `POST /api/v1/auth/reset-password`.
- Frontend: `ForgotPasswordScreen` (2-stage: request → reset → done), linked from the login screen.

**3. Auth recovery — email verification**
- Schema: `email_verification_tokens` (24-hour TTL) + `users.email_verified` boolean.
- Endpoints: `POST /api/v1/auth/verify-email/send` (protected), `GET /api/v1/auth/verify-email?token=...` (public).
- Grandfathered: users > 7 days old treated as `email_verified=true`.

**4. 2FA / TOTP**
- Schema: `user_2fa_secrets` (AES-GCM-encrypted secret at rest).
- Endpoints: `POST /api/v1/auth/2fa/enroll` (returns base32 secret + otpauth URL), `POST /api/v1/auth/2fa/verify-enrollment`, `POST /api/v1/auth/2fa/disable` (protected), `POST /api/v1/auth/2fa/challenge` (public, login-time).
- TOTP: pure Go, RFC 6238, HMAC-SHA1, 30-second window with ±1 step drift.
- Login flow updated: `/auth/login` returns `{requires_2fa: true, challenge_id, challenge_expires_at}` when 2FA is enabled; front-end pops TOTP dialog and POSTs to `/auth/2fa/challenge`.

**5. Email delivery (replaces dev token echo)**
- email-service: added `POST /send` HTTP endpoint with 3 templates (forgot-password, verify-email, 2fa-enroll).
- auth-service: `buildEmailNotifier()` POSTs to email-service when notifier is wired. When not wired (dev), the reset_token / verify_token is returned in the response so the front-end can deep-link.

**6. Pakistan map data pipeline**
- `infrastructure/docker/openmaptiles/download_pakistan_tiles.sh` — full pipeline: Geofabrik PBF → imposm3 → OpenMapTiles → TileServer GL.
- Idempotent with `--skip-pbf` / `--skip-import` / `--skip-tiles` flags.
- README with resource requirements (30 GB disk + 8 GB RAM) + verification + per-region URLs (UAE, UK, SA).

### Verification
- `go build ./...` clean
- `go test ./... -count=1 -short` — 3 packages pass
- `go vet ./...` clean
- `node -c email-service/src/index.js` clean
- `flutter analyze` — 0 errors, 27 info-level lints

### App is now production-ready for the originally requested features
- ✅ Customer ↔ vendor ↔ rider chat (Daraz-style)
- ✅ Forgot password + email verification + 2FA
- ✅ Self-hosted map stack with Pakistan data acquisition script
- ✅ Email-service delivers transactional emails

---

## Session 43: PayFast Hardening, Flow Consolidation & De-Hardcoding (August 25, 2026)

**Goal:** Full audit of the PayFast Option C payment pipeline, fix every bug that could block or corrupt payments, consolidate the divergent Buy Now / checkout flows onto one production path, and eliminate all hardcoded deployment values.

**Detailed execution log:** [[session_33_execution_log]]

### Critical fixes

- **Boot-panic elimination**: `NewClientFromEnv()` no longer panics on missing `PAYFAST_BASE_URL` — one env var previously crashed the entire payment orchestrator (Stripe/JazzCash/EasyPaisa/COD included). Graceful degradation to `IsConfigured()==false` + loud startup ERROR.
- **Env template mismatch fixed**: code accepts both `PAYFAST_BASE_URL` (canonical) and `PAYFAST_API_URL` (legacy alias); `.env.example` files updated. Following the repo's own templates previously guaranteed a dead payment service.
- **Detached settlement context**: `VerifyAndSettle`/`ExecuteSplit` use `context.WithoutCancel` — captured funds can never be stranded by a client disconnect mid-settlement.
- **Circuit breaker sees 5xx**: any gateway `>=500` is now transient → breaker trips on real outages instead of hammering a dying upstream.
- **Saved-card 3DS step-up**: tokenized responses can now carry `data_3ds_html`; saved-card payments route through the same 3DS callback machinery as new cards instead of misfailing.
- **PCI leak guard**: PAN/CVV/CNIC/OTP/instrument-token are `json:"-"` on all request structs; serialization test enforces it.
- **IPN retry semantics**: transient → `503` (gateway redelivers), unknown basket → `200 ignored`, validation → `400`.
- **Idempotency-Key header** end-to-end with stable replay responses; retry-after-failure allowed via derived keys; cross-order key reuse rejected.
- Legacy `/wallet/payfast/*` silent **sandbox fallback removed** (`503 not configured`) — was sending prod traffic to the sandbox gateway.

### Flow consolidation

- New shared Flutter widget `payfast_card_sheet.dart` (card sheet + 3DS WebView + formatter) used by BOTH checkout and product screens (~270 duplicated lines deleted).
- Buy Now (`product_details_screen.dart`) migrated from legacy hosted-checkout to Option C orchestrator flow with `Idempotency-Key: buynow-$nonce`. Success lands on OrderSuccessScreen like cart checkout.
- **Financial correctness:** Buy Now orders now produce fraud checks, audit rows, gateway verification AND the admin/vendor/delivery ledger split — previously marked "paid" with NO split (silent ledger imbalance) and invisible in the Admin PayFast Hub.
- Legacy endpoints kept alive for old app releases but advertise `Deprecation`/`Sunset` headers + per-call usage logs → data-driven removal later. Wallet top-up flow untouched.

### Zero-hardcoding pass

- Railway URLs, sandbox fallbacks, dummy phone `03000000000`, literal sunset date, timeout magic numbers (20s/25s), and frontend magic `'account_type_id': '2'` — ALL replaced with env config or server-side derivation. New vars: `PAYFAST_WEB_ORIGIN`, `PAYFAST_GATEWAY_TIMEOUT_SECONDS`, `PAYFAST_LEGACY_SUNSET_DATE`, canonical `PAYFAST_BASE_URL`, plus `PAYFAST_HASH_KEY` documented.

### Verification

- `go build ./...` clean · `go vet` clean · full `go test -count=1 ./internal/...` green
- `dart analyze` clean on touched files
- Hardcode grep across touched backend files: zero matches

### Deferred follow-ups

- float64 → integer-paisa money refactor (dedicated session required)
- Sentinel-error pattern rollout to Stripe/COD handlers

