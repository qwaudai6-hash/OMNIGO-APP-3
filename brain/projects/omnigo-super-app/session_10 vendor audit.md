# OMNIGO Session 10 — Architecture Integrity & Audit (vendor) Resolution

## 1. What We Did (Accomplished Tasks)
- Updated the root `docker-compose.yml` with the correct PostgreSQL port (`5433`), WAL logical replication parameters, a 3-node Redis Cluster running on host network, and Kafka running in KRaft mode.
- Redefined `models/vendor.go` to match the actual active PostgreSQL database schema table (`stores`) and fields (`vendor_tracking_id`, `store_tracking_id`, `store_name`, `latitude`, `longitude`, `created_at`).
- Refactored `auth_service.go` to remove structural compile-time errors, clean up unused imports, and handle vendor registration safety locks.
- Tested and successfully compiled the `auth-service` binary.

---

## 2. Claude Audit Findings: What is Real vs. Mock

Below is the verified audit list highlighting the core missing blocks and discrepancies between what was claimed and what actually exists:

### 📱 Frontend (Flutter) Audit Results
- **Vendor Dashboard (`vendor_dashboard_screen.dart`)**: ❌ **100% Mock**. Orders are hardcoded in a local list, earnings are hardcoded at `$300.00`, and active gigs are hardcoded at `2`. No API connection exists.
- **Vendor Live Map (`vendor_live_map_screen.dart`)**: ⚠️ **Dead Code / Unreachable**. This screen is not registered in `main.dart` or any router. Telemetry is mock-simulated via a local 3-second timer loop instead of real WebSockets.
- **Vendor Catalog CRUD**: ❌ **Does Not Exist**. No listing, add, edit, or delete screens exist under `lib/features/vendor/`.
- **Auth Registry Form (`dynamic_signup_screen.dart`)**: ⚠️ **Broken Inputs & Silent Fallback**. The vendor form collects "Business Name" and "Address" but silently discards them (not sent to the backend). Additionally, if the API is down, the screen silently routes the user to a mock dashboard with an orange "Offline Mode" toast.
- **Dead Assets**: `login_screen.dart` and `role_selection_screen.dart` are unused legacy files.

### ⚙️ Backend (Go Services) Audit Results
- **Auth Endpoints (`auth_service.go` & `auth_handler.go`)**: ✅ **Real / Working**. User registration and credentials verification with bcrypt work on port `8080`. However, JWT tokens are fake session string templates, and there is no middleware route protection.
- **Vendor Service (`vendor_service.go` & `vendor_repository.go`)**: ⚠️ **Database Mismatch**. The Go struct was pointing to `vendor_stores` (which doesn't exist in the active DB) and had type mismatches (VendorID mapped to `int` instead of UUID strings or tracking ID fields).
- **Missing Services**: ❌ **Does Not Exist**. `vendor_metrics_service.go` and `vendor_inventory_kafka_producer.go` are missing from the repository.
- **Dead Imports**: Redis and Kafka clients are initialized in the `vendor-store-service` main.go but never passed to any repositories or service layers.

---

## 3. Galti & Defects Analysis (What I Done Wrong & Got Caught)

### ❌ MISTAKE #1: Distributed Deadlock Trap (Synchronous call inside DB Transaction)
- **What I did:** I proposed a synchronous HTTP REST call to the Vendor Store Service (Port 8081) from the Auth Service (Port 8080) *inside* the Postgres transaction block, rolling back if the call failed.
- **Why it was dangerous:** Database transactions must release locks in `<2ms` at a 50M scale. If the network call delayed, it would hold DB row locks open, starving the connection pool and causing cascading app-wide failures.
- **Fix:** Removed the synchronous network dependency. The Auth database transaction now registers the user and commits immediately. Eventual store creation will run asynchronously via Kafka.

### ❌ MISTAKE #2: Flutter Asynchronous Route Guard Crash (Disk I/O Jank)
- **What I did:** Proposed reading from `SharedPreferences` asynchronously inside the synchronous `onGenerateRoute` lifecycle callback in Flutter.
- **Why it was dangerous:** Flutter navigation is synchronous. Awaiting disk I/O on-the-fly blocks the main rendering UI thread, causing frame rate drops (jank) or engine crashes.
- **Fix:** Switched to a **Startup Cache Registry Pattern**. We bootstrap session data once at app startup, store it in a synchronous memory singleton (`SessionRegistry`), and query the memory cache instantly inside `onGenerateRoute`.

### ❌ MISTAKE #3: The Forbidden Store Provisioning Trap (Unverified Kafka Emit)
- **What I did:** Automatically triggered the `vendor.registered` Kafka event on user registration for vendors.
- **Why it was dangerous:** New vendors are `is_verified = false` by default. Triggering a store provisioning event during registration would populate database slots for unverified or fake accounts.
- **Fix:** Removed the event emission from the registration lifecycle. The `vendor.registered` event will only be emitted once an Admin explicitly approves the vendor account via the approval webhook.

### ❌ MISTAKE #4: Unused Imports Go Compilation Crashes
- **What I did:** Left unused imports (`log`, `github.com/twmb/franz-go/pkg/kgo`) inside `auth_service.go` after removing the Kafka emitter block.
- **Why it failed:** Go compiler strictly forbids unused imports and throws compilation errors.
- **Fix:** Cleaned up all unused imports and verified a successful project compile.

---

## 4. The 5-Phase Vendor Module Plan

### Phase 1: Authentication Integrity & Navigation Routing (CURRENT)
- **Backend:** Update models and auth flow (Completed).
- **Frontend:** Modify `DynamicSignupScreen` to pass store metadata, implement memory-cached `SessionRegistry`, and guard `/vendor-live-map` synchronously.

### Phase 2: Dynamic Catalog Management (CRUD)
- **Backend:** Build the product handler `vendor_product_handler.go` (Update, Delete, Out-of-Stock toggle).
- **Frontend:** Create `vendor_catalog_screen.dart` for product addition, editing, and stock toggles.

### Phase 3: Analytics & COALESCE Aggregations
- **Backend:** Implement `vendor_metrics_service.go` with safe DB aggregations to avoid nil-pointer Go panics.
- **Frontend:** Build the store metrics display.

### Phase 4: Kafka Batch Updates & Redis Cache Invalidation
- **Backend:** Create `vendor_inventory_kafka_producer.go` and configure Redis Cluster eviction inside `product_service.go`.

### Phase 5: Live Map Real-Time Streaming & Order Dashboard
- **Frontend:** Connect `vendor_live_map_screen.dart` to real WebSocket pipelines and bind dashboard order lists.

---

## 5. Next Steps (What We Are Doing Next)
We are proceeding to **Phase 1 (Frontend Integration)**:
1. Wire store name and address metadata inputs inside `DynamicSignupScreen`.
2. Code the synchronous in-memory `SessionRegistry` singleton.
3. Configure the `/vendor-live-map` route guard inside `main.dart`.



The vendor auditor has delivered an incredibly detailed and comprehensive report. Now I need to compile this into an actionable plan and write it to Obsidian. Let me organize the findings and create a clear execution plan for the pending vendor work.

Key findings from the audit:

**COMPLETED (Done):**

1. Phase 1 (Auth & Navigation): Mostly done - SessionRegistry, route guards, auth flow
2. Phase 3 (Analytics): Fully done - metrics backend + frontend analytics screen
3. Partial Phase 2: ToggleStock + DeleteProduct backend done, inventory list UI done
4. Partial Phase 5: WebSocket client exists (but has bugs), dashboard order list binding done
5. **PENDING/BROKEN:**
    - Schema is completely out of sync across init.sql, live database, and Go code
    - Phase 2 missing AddProduct and UpdateProduct vendor endpoints, plus all the Flutter UI for adding/editing/deleting products
    - Phase 4 has Kafka initialized but no producer implementation
    - Phase 5 lacks the WebSocket gateway on port 8087, and dashboard earnings/gigs are hardcoded
    - WebSocket has a dual-listen bug
    - Docker init.sql isn't mounted and database credentials don't match
    - JWT implementation is just fake string templates
    - vendor_analytics route isn't properly guarded
    - Flutter app needs significant work
    - ProductModel is missing the category field



### 📊 Summary Jo Audit Mein Nikla

|Phase|Status|Remaining|
|---|---|---|
|Phase 1 (Auth & Nav)|✅ 90%|Route guard + signup verify|
|Phase 2 (Catalog CRUD)|⚠️ 40%|AddProduct, UpdateProduct, Flutter CRUD UI|
|Phase 3 (Analytics)|✅ 100%|Nothing — fully wired!|
|Phase 4 (Kafka)|⚠️ 50%|Kafka producer missing, dead allocations|
|Phase 5 (Live Map)|⚠️ 30%|Dashboard mocks, WS bugs, no gateway|

### 🔴 3 Critical Fixes Required First

1. **WebSocket dual-listen crash** — `stream` consumed twice
2. **init.sql completely stale** — doesn't match Go code at all
3. **Docker doesn't mount init.sql** — fresh setup creates no tables

---
# implementation Plan: Vendor Module — Complete Remaining Phases

This plan resolves all pending vendor work identified by the exhaustive file-by-file audit. Obsidian docs: vendor_module_complete_audit.md and vendor_remaining_phases_plan.md.

---

## User Review Required

IMPORTANT

**12-Step Execution Sequence.** Review the priority order below. All critical bugfixes (P0) run first, then remaining phase gaps are filled in dependency order. Estimated total: ~3.5 hours.

WARNING

**Schema Decision:** The `init.sql` schema is completely stale — Go code runs against a manually modified live DB. The plan rewrites `init.sql` to match Go code (source of truth). If you want UUID FK-based normalized schema instead, this requires rewriting ALL Go repositories.

CAUTION

**Kafka Deferral:** Kafka producer wiring (Phase 4-A) and Live Map telemetry model fix (Phase 5-C) are **deferred** because their consumers/gateway don't exist yet. They belong to master roadmap Phases 2-3.

---

## Open Questions

IMPORTANT

1. **Schema Source of Truth:** Should we keep the tracking-ID-based flat schema (Go code = source of truth) or rewrite Go code to use UUID FK-based normalized schema (init.sql = source of truth)?
2. **Kafka Strategy:** Wire Kafka producers now (dead events with no consumers) or defer until notification/CDC consumers are ready?

---

## Proposed Changes

### Priority 0: Critical Bugfixes

#### [MODIFY] websocket_client.dart

- Fix dual-listen crash: Use `StreamController.broadcast()` to re-broadcast channel stream
- Add platform-aware host resolution instead of hardcoded `localhost:8087`

#### [MODIFY] init.sql

- Rewrite all table schemas to match Go code column names and types
- Tables: users, stores, products, orders, deliveries, rides

#### [MODIFY] docker-compose.yml

- Mount init.sql into `/docker-entrypoint-initdb.d/`
- Fix credentials to `omnigo_user/omnigo_password/omnigo_db`

#### [DELETE] mock_data.sql

- Stale file with incompatible schema

---

### Phase 1 Finish: Auth & Navigation

#### [MODIFY] main.dart

- Add `/vendor-analytics` to route guard block

#### [VERIFY] dynamic_signup_screen.dart

- Confirm business_name and address fields pass to auth API

---

### Phase 2 Complete: Dynamic Catalog CRUD

#### [MODIFY] vendor_product_handler.go

- Add `AddProduct` POST endpoint with vendor auth + store ownership check
- Add `UpdateProduct` PUT endpoint with partial field updates + ownership check

#### [MODIFY] product_repository.go

- Add `UpdateProductSecure()` method with dynamic SET clause

#### [NEW] vendor_add_product_screen.dart

- Full product creation form: name, description, price, stock, category, image URL

#### [MODIFY] vendor_inventory_screen.dart

- Add FAB "+" button → navigate to add product screen
- Add swipe-to-delete with confirmation dialog
- Add tap-to-edit navigation
- Fix ProductModel to parse `category` field

---

### Phase 5 Partial: Dashboard & WebSocket

#### [MODIFY] vendor_dashboard_screen.dart

- Wire earnings card to real vendor metrics API (already exists)
- Wire active gigs count from orders with status='shipped'

#### [MODIFY] websocket_client.dart

- Add exponential backoff reconnection logic
- Add connection state management enum

---

### Cleanup

#### [MODIFY] vendor-store-service/main.go & product-service/main.go

- Remove dead Kafka client allocations

---

## Verification Plan

### Automated Tests

- `cd backend/go-services && go build ./...` — All Go services compile cleanly
- `cd frontend/omnigo_app && flutter analyze` — Zero Dart analysis errors
- `cargo check` in `backend/rust-services/websocket-gateway` — Rust builds clean

### Manual Verification

- Test AddProduct → verify product appears in inventory list
- Test ToggleStock → verify stock updates persist
- Test DeleteProduct → verify product removed from list
- Test dashboard → verify earnings card shows real metrics data
- Test `docker-compose up` on fresh environment → verify tables created