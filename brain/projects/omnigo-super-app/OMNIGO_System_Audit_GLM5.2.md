# OMNIGO Super App — Comprehensive Audit Report (GLM 5.2 Follow-up)

**Date**: July 13, 2026
**Scope**: Full end-to-end audit across Customer, Vendor, and Rider modules (Frontend Flutter & Backend Go/Rust/Postgres/Redis/Kafka).

---

## 1. 🏍️ RIDER MODULE (Highest Critical Gaps)

### Frontend (`features/rider/`)
- ❌ **Completely Mocked UI & State**: `rider_map_screen.dart` relies entirely on local hardcoded state.
- ❌ **No WebSocket Connection**: Hardcoded popup for gig broadcasts (e.g., "Nike Store"). No actual WS connection is established to listen for real gigs.
- ❌ **No Backend Communication**: `_acceptGig()` and `_deliverGig()` are local UI triggers. No REST API calls exist.
- ❌ **No Real GPS**: Uses `flutter_map` with static coordinates (Lahore). No `geolocator` integration for live device tracking, and no WebSocket telemetry publishing.

### Backend (`delivery-gig-service` & `websocket-gateway`)
- ✅ **Good**: Kafka consumer for `orders.created`. H3 Hexagonal Ring expansion logic for efficient nearby rider searches. Rider registration safety compliance (`cnic_url`, `license_url`).
- ❌ **Missing Fulfillment APIs**: `delivery-gig-service` lacks REST endpoints for riders to Accept/Reject a gig or Update Delivery Status (Picked Up, Completed, Failed).
- ❌ **Missing WebSocket Sink**: The Rust `websocket-gateway` tracks sessions and routes telemetry flawlessly, but lacks a Kafka consumer for the `deliveries.broadcasted` topic, meaning riders never receive gig offers.

---

## 2. 🏪 VENDOR MODULE

### Frontend (`features/vendor/`)
- ✅ **Good**: Dashboard wiring, Accept Order flow, Print Slip, Live Telemetry rendering via `ValueNotifier`.
- ❌ **Mocked Map Coordinates**: The Store's origin coordinates on the Live Map and Inventory Screen are hardcoded. It does not fetch actual dynamic GPS coordinates from the backend.
- ❌ **Client-Side "Active Gigs" Calculation**: Active gigs are calculated locally by counting shipped orders in the list. This should come from the backend metrics API.

### Backend (`vendorstore`, `product`, `order`)
- ✅ **Good**: Inventory CRUD (Add, Edit, Toggle, Delete) with Redis pipelined cache invalidation. Dashboard analytics with multi-table joins and COALESCE aggregations. Fail-Open Idempotency lock via Redis.
- ❌ **CRITICAL SCALE GAP**: The frontend fetches products using the global catalog endpoint (`GET /api/v1/products`) and filters them client-side. The `VendorProductHandler` lacks a dedicated `GET /api/v1/vendor/products` route, which will cause empty inventories at scale due to pagination limits.

---

## 3. 🛒 CUSTOMER MODULE

### Frontend (`features/customer/`)
- ✅ **Good**: `CartProvider` local state, Stripe integration (`flutter_stripe`), Live order tracking (WebSockets), Search & Category Debouncing.
- ❌ **Public API Abuse**: Map location search directly queries the public **Nominatim OpenStreetMap API**. At scale, this will instantly trigger IP rate-limiting bans, breaking map functionality for customers.

### Backend (`order-service`, `product-service`, `wallet-service`)
- ✅ **Good**: Search and Category PostgreSQL query optimizations (`ILIKE`, exact match, composite indexes, Redis caching).
- ❌ **Double-Billing Vulnerability**: The `CreateOrder` idempotency check "Fails-Open" if Redis is down. Under a network partition, duplicate orders could pass through, double-charging customers.
- ❌ **Synchronous DB Bottleneck**: During checkout, the repository performs a synchronous blocking query to fetch the `vendor_tracking_id` from the `stores` table, which will crash throughput under heavy load.
- ❌ **Unsecure JazzCash Integration**: The callback route blind-accepts incoming POST data without validating the gateway integrity salt, presenting a critical security risk.

---

## Conclusion & Next Steps
The backend is fundamentally robust but lacks crucial real-world fulfillment state machines (Rider APIs) and several critical high-scale optimizations (Pagination fixes, Idempotency hardening, API rate-limit protections). The execution plan will detail the architectural steps required to bridge these gaps.
