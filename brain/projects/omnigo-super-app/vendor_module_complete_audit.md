# OMNIGO Vendor Module — Complete Exhaustive Audit (Session 16)

> **Audit Date:** July 13, 2026
> **Audit Method:** Automated subagent read every single backend Go file, Flutter Dart screen, database schema, Docker config, and migration file line-by-line.
> **Cross-Reference:** [[session_15_audit_report]] → [[phase_1_execution_details]] → This Document

---

## 🔗 Backlinks & Context

- **Audit Origin:** [[session_15_audit_report]] (4 concurrent subagent audits)
- **Phase 1 Fixes Applied:** [[phase_1_execution_details]] (UUID scanning, *int pointer, import paths)
- **Architecture Blueprint:** [[OMNIGO_SuperApp_Architecture]]
- **Vendor 5-Phase Plan Source:** [[session_10 vendor audit]]

---

## ═══════════════════════════════════════
## A. BACKEND GO FILES — VERIFIED STATUS
## ═══════════════════════════════════════

### 1. `vendor_product_handler.go` (Port 8082)

| Endpoint | Method | Route | Status |
|----------|--------|-------|--------|
| ToggleStock | PATCH | `/api/v1/vendor/products/:product_id/stock` | ✅ REAL — Uses `*int` pointer |
| DeleteProduct | DELETE | `/api/v1/vendor/products/:product_id` | ✅ REAL — Ownership verified |
| AddProduct (Vendor) | POST | — | ❌ MISSING — Only general CreateProduct exists (no vendor auth) |
| UpdateProduct | PUT/PATCH | — | ❌ MISSING — No edit endpoint |

> **Backlink:** Fix applied in [[phase_1_execution_details#3. Out of Stock Validation Pointer Override]]

---

### 2. `product_repository.go`

- ✅ `CreateProduct` — 11 columns INSERT (tracking IDs, category, image_url)
- ✅ `ListProducts` — ILIKE search + category filter + pagination
- ✅ `UpdateProductStockSecure` — Ownership-verified
- ✅ `DeleteProductSecure` — Ownership-verified
- ✅ Reader/Writer pool separation

> **Backlink:** Category support added in [[phase_1_execution_details#2. Product Category Mapping & Indexing]]

⚠️ **SCHEMA MISMATCH vs init.sql:** Go code uses `product_tracking_id, vendor_tracking_id, base_price, stock, image_url, description` — init.sql uses `tracking_id, store_id UUID FK, base_price_usd, category_id UUID FK, images JSONB`. **Columns do NOT match.**

---

### 3. `product.go` (Model)

- ✅ Full struct: ID, ProductTrackingID, VendorTrackingID, StoreTrackingID, SKU, Name, Description, BasePrice, Stock, IsFeatured, ImageURL, Category
- ⚠️ `ID` is `int` but init.sql uses `UUID`
- ⚠️ `Stock` is plain `int` in model (handler uses `*int` for request only)
- ⚠️ `description, stock, image_url, vendor_tracking_id` columns don't exist in init.sql

---

### 4. `vendorstore/` — Full CRUD Assessment

| File | Endpoints | Status |
|------|-----------|--------|
| `vendor_handler.go` | POST `/api/v1/stores/`, GET `/api/v1/stores/:tracking_id` | ✅ REAL |
| `vendor_metrics_handler.go` | GET `/api/v1/vendor/metrics` | ✅ REAL |
| `vendor_service.go` | CreateStore, GetStore, GetVendorMetrics (Redis cache-first 5m TTL) | ✅ REAL |
| `vendor_repository.go` | CreateStore, GetStore, GetVendorMetrics, GetDailyTrends, GetProductStats | ✅ REAL |
| — | UpdateStore, DeleteStore, ListStoresByVendor | ❌ MISSING |

⚠️ **Schema:** Go queries table `stores` — init.sql defines `vendor_stores`. Different column names too.
⚠️ **Dead Code:** Kafka client initialized in `vendor-store-service/main.go` but **NEVER USED**.

> **Backlink:** Metrics COALESCE fix from [[session_10 vendor audit#Phase 3: Analytics & COALESCE Aggregations]]

---

### 5. `order_repository.go`

- ✅ CreateOrder (10 columns), GetOrderByTrackingID, GetOrdersByCustomerID, GetOrdersByVendorID, UpdateOrderStatus
- ⚠️ Uses `customer_tracking_id, store_tracking_id, vendor_tracking_id` — init.sql uses `customer_id UUID, store_id UUID`

> **Backlink:** Array slice mapping fix from [[phase_1_execution_details#4. Explicit Array Slice Mapping Safeguards]]

---

### 6. `delivery_repository.go`

- ✅ CreateGig, UpdateRiderLocation (H3 hex + Redis + PubSub), GetRidersInHexagon
- ⚠️ INSERT missing mandatory columns: `pickup_lat, pickup_lng, dropoff_lat, dropoff_lng, delivery_fee, currency`

---

### 7. `auth_service.go`

- ✅ Register (bcrypt, UTID generation, is_verified=false for rider/vendor)
- ✅ Login (password verify, verification guard)
- ✅ UUID scanning via `interface{}` — **FIXED**
- ⚠️ Inserts `business_name, address` — columns don't exist in init.sql
- ⚠️ JWT is fake string: `jwt_token_session_{trackingID}_{timestamp}`

> **Backlink:** UUID fix from [[phase_1_execution_details#1. User Registration & Login Type-Safety]]

---

## ═══════════════════════════════════════
## B. FLUTTER FRONTEND — VERIFIED STATUS
## ═══════════════════════════════════════

### 1. `vendor_dashboard_screen.dart`

| Feature | Status | Details |
|---------|--------|---------|
| Order Fetch | ✅ REAL | `ApiClient().get('/orders/vendor/${trackingId}')` |
| Accept Order | ✅ REAL | PATCH to updateOrderStatus |
| Broadcast Gig | ✅ REAL | PATCH with status 'shipped' |
| Print Slip | ✅ REAL | Dialog shows real order data |
| Nav → Inventory | ✅ DONE | Route registered + guarded |
| Nav → Live Map | ✅ DONE | Route registered + guarded |
| Nav → Analytics | ✅ DONE | Route registered but ⚠️ **NOT GUARDED** in main.dart |
| Earnings Card | ❌ MOCK | Hardcoded `$300.00` |
| Active Gigs Card | ❌ MOCK | Hardcoded `2 Gigs` |

---

### 2. `vendor_inventory_screen.dart`

| Feature | Status |
|---------|--------|
| Product List | ✅ REAL API (port 8082) |
| Filter by Vendor | ✅ Client-side |
| Toggle Stock | ✅ REAL (optimistic UI + rollback) |
| Store Map | ✅ Embedded FlutterMap |
| Add Product UI | ❌ MISSING |
| Edit Product UI | ❌ MISSING |
| Delete Product UI | ❌ MISSING (backend exists) |
| Category Field | ❌ Missing from ProductModel.fromJson() |

---

### 3. `vendor_analytics_screen.dart` — ✅ FULLY REAL

- Real API fetch from port 8081
- Revenue, WoW Growth, Completed/Pending Orders, Active Products
- Custom sparkline with bezier curves from daily_trends
- No mock data whatsoever

> **Backlink:** Phase 3 completion verified — [[session_10 vendor audit#Phase 3]]

---

### 4. `vendor_live_map_screen.dart` — ⚠️ PARTIALLY REAL

- ✅ Uses real WebSocketClient (not Timer mock)
- ⚠️ Connects to port 8087 — **NO SERVICE EXISTS on 8087**
- ⚠️ Telemetry model is "inventory price updates" not rider location
- ⚠️ No reconnection logic

---

### 5. `websocket_client.dart` — 🔴 CRITICAL BUG

- ❌ **Dual-listen crash:** `connect()` calls `_channel.stream.listen()` consuming the stream. `get stream` returns `_channel.stream` again — throws `Bad state: Stream has already been listened to.`
- ⚠️ Hardcoded `localhost:8087` — doesn't use platform-aware host resolution

---

## ═══════════════════════════════════════
## C. INFRASTRUCTURE DIVERGENCE
## ═══════════════════════════════════════

### Schema Divergence Matrix

| Aspect | init.sql Schema | Go Code Assumes |
|--------|-----------------|-----------------|
| Store table name | `vendor_stores` | `stores` |
| Store PK type | UUID | int64 (BIGSERIAL) |
| Store vendor ref | `vendor_id UUID FK` | `vendor_tracking_id VARCHAR` |
| Store coordinates | `location_lat, location_lng` | `latitude, longitude` |
| Products PK | UUID | int |
| Products price | `base_price_usd` | `base_price` |
| Products store ref | `store_id UUID FK` | `store_tracking_id VARCHAR` |
| Products images | `images JSONB` | `image_url TEXT` |
| Products stock | None | `stock INT` |
| Products description | None | `description TEXT` |
| Orders customer | `customer_id UUID FK` | `customer_tracking_id VARCHAR` |
| Orders store | `store_id UUID FK` | `store_tracking_id VARCHAR` |
| Users biz fields | None | `business_name, address` |

> **Conclusion:** Go code runs against a **manually modified live DB** that doesn't match init.sql. The init.sql is **stale and non-authoritative**.

### Other Infrastructure Issues

- ⚠️ Docker init.sql NOT mounted — fresh `docker-compose up` won't create tables
- ⚠️ DB credentials mismatch: docker-compose `admin/admin123/omnigo` vs Go `omnigo_user/omnigo_password/omnigo_db`
- ⚠️ `mock_data.sql` is dead/stale — yet another incompatible schema
- ⚠️ Kafka initialized in 2 services, used in 0

---

## ═══════════════════════════════════════
## D. VENDOR 5-PHASE COMPLETION STATUS
## ═══════════════════════════════════════

### Phase 1: Auth Integrity & Navigation ✅ 90% COMPLETE

| Task | Status |
|------|--------|
| Backend auth flow | ✅ Done |
| SessionRegistry | ✅ Done |
| Route guards | ⚠️ Missing `/vendor-analytics` guard |
| DynamicSignupScreen store metadata | ⚠️ Unverified |

### Phase 2: Dynamic Catalog CRUD ⚠️ 40% COMPLETE

| Task | Status |
|------|--------|
| ToggleStock backend + UI | ✅ Done |
| DeleteProduct backend | ✅ Done |
| DeleteProduct UI | ❌ Missing |
| AddProduct vendor endpoint | ❌ Missing |
| UpdateProduct endpoint | ❌ Missing |
| Add/Edit Product UI screens | ❌ Missing |
| ProductModel category parse | ❌ Missing |

### Phase 3: Analytics & COALESCE ✅ 100% COMPLETE

All backend metrics + frontend analytics screen + sparkline chart fully wired.

### Phase 4: Kafka Batch & Redis Cache ⚠️ 50% COMPLETE

| Task | Status |
|------|--------|
| Redis cache for metrics | ✅ Done (5m TTL) |
| Redis cache invalidation (products) | ✅ Done (pipeline-based) |
| Kafka producer file | ❌ Missing |
| Kafka wiring in services | ❌ Dead code |

### Phase 5: Live Map & Order Dashboard ⚠️ 30% COMPLETE

| Task | Status |
|------|--------|
| WebSocket client (Flutter) | ✅ Done (but dual-listen bug) |
| Dashboard order list binding | ✅ Done |
| WebSocket gateway service | ❌ No service on 8087 |
| Dashboard earnings API | ❌ Hardcoded $300 |
| Dashboard gigs API | ❌ Hardcoded 2 Gigs |
| Reconnection logic | ❌ Missing |

---

## ═══════════════════════════════════════
## E. CRITICAL BUGS REGISTRY
## ═══════════════════════════════════════

| # | Bug | Severity | File |
|---|-----|----------|------|
| 1 | WebSocket dual-listen crash | 🔴 CRITICAL | `websocket_client.dart` |
| 2 | init.sql completely stale vs live DB | 🔴 CRITICAL | `infrastructure/postgres/init.sql` |
| 3 | Docker doesn't mount init.sql | 🟡 HIGH | `docker-compose.yml` |
| 4 | DB credentials mismatch | 🟡 HIGH | `docker-compose.yml` vs Go configs |
| 5 | Kafka dead infrastructure | 🟡 MEDIUM | `vendor-store-service/main.go`, `product-service/main.go` |
| 6 | No real JWT verification | 🟡 HIGH | `auth_service.go` |
| 7 | `/vendor-analytics` route not guarded | 🟡 MEDIUM | `main.dart` |
| 8 | Flutter ProductModel missing category | 🟡 MEDIUM | `vendor_inventory_screen.dart` |
| 9 | mock_data.sql stale | 🟢 LOW | Root directory |

---

> **Next Action:** Execute remaining vendor phases. See [[vendor_remaining_phases_plan]] for the execution roadmap.
