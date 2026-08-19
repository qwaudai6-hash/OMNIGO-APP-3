# OMNIGO Session Log — 13 July 2026 (Session 17)

> **Continuation of:** [[session customer audit]] (Session 4 action items)
> **Preceded by:** [[session_15_audit_report]] → [[vendor_remaining_phases_plan]] (Session 16 vendor module fixes)
> **Architecture:** [[OMNIGO_SuperApp_Architecture]]

---

## 📋 Session Summary

Resolved the **3 remaining action items** from the Session 4 customer-side audit:
1. Server-side search & category filtering (was client-side only)
2. Nominatim OSM geocoding (was 7-city mock DB)
3. Real-time live rider tracking on the customer Map tab (was missing entirely)

All changes verified with `go build ./...` (0 errors) and `flutter analyze lib/` (0 errors).

---

## 🐛 Issues Resolved

### 1. Server-side Search & Category Filtering ✅

**Problem:** The customer catalog tab fetched ALL products via `_fetchCatalog()` and then filtered them client-side with `.where()` — meaning search never reached the backend and category pills were purely decorative.

**Root cause:** The Go product-service **already supported** `?search=` (ILIKE on name/description) and `?category=` query params via `ListProducts` in `product_repository.go:68-114`. The Flutter app simply never passed them.

**Fix:**
- **`api_endpoints.dart`** — `productsList()` now accepts optional `search` and `category` params, appends them as URL-encoded query string (`search=...&category=...`). Skips `category` when value is `'All'`.
- **`customer_dashboard_screen.dart`**:
  - Added `Timer? _searchDebounce` for 400ms debounced server fetch on keystroke.
  - `_fetchCatalog({bool reset = false})` — now passes `_searchQuery` + `_selectedCategory` to `ApiEndpoints.productsList()`. When `reset: true`, clears `_allProducts` and resets pagination (`_offset=0`, `_hasMore=true`).
  - `_onSearchChanged(val)` — updates `_searchQuery`, cancels previous debounce timer, triggers `_fetchCatalog(reset: true)` after 400ms.
  - `_onCategorySelected(cat)` — updates `_selectedCategory`, immediately calls `_fetchCatalog(reset: true)`.
  - `_getFilteredProducts()` — now simply returns `_allProducts` (server already filtered). Old client-side `.where()` removed.
  - Search `TextField.onChanged` wired to `_onSearchChanged`.
  - Category pills `onTap` wired to `_onCategorySelected`.

**Backlink:** Action item #1 in [[session customer audit#Action Items for Next Session]]

---

### 2. Nominatim OSM Geocoding ✅

**Problem:** The customer Map tab used a hardcoded `_mockGeocodingDb` with only 7 cities (Lahore, Karachi, Islamabad, London, New York, Faisalabad, Peshawar). Any other query returned `"City not found in local mock DB."`

**Fix:**
- **`customer_dashboard_screen.dart` → `_searchMap(String query)`** — rewritten as `async`:
  1. Calls `https://nominatim.openstreetmap.org/search?format=json&limit=1&q=...` with a proper `User-Agent: OMNIGO-SuperApp/1.0 (com.omnigo.superapp)` header (required by OSM Tile Usage Policy).
  2. Parses `lat`/`lon` from the first JSON result, moves the map camera, drops a marker.
  3. Shows a SnackBar with the resolved `display_name`.
  4. **Fallback:** if Nominatim fails or returns nothing, falls back to the local `_mockGeocodingDb` (kept as offline safety net).
  5. Added `bool _isGeocoding` state — the search icon morphs into a spinner while the API call is in flight.
- Mock DB comment updated: *"Kept as a fast-path fallback only if Nominatim is unreachable."*

**Backlink:** Action item #2 in [[session customer audit#Action Items for Next Session]]

---

### 3. Real-time Live Rider Tracking on Customer Map ✅

**Problem:** The customer Map tab had no link to the WebSocket telemetry stream. Even though the Rust WS gateway (port 8087) and the Flutter `WebSocketClient` existed, the customer dashboard never subscribed to rider GPS updates.

**Fix:**
- **`customer_dashboard_screen.dart`:**
  - Added imports: `websocket_client.dart`, `dart:async`.
  - Added state: `WebSocketClient? _wsClient`, `StreamSubscription<dynamic>? _wsSub`, `StreamSubscription<WSConnectionState>? _wsStateSub`, `ValueNotifier<Map<String, LatLng>> _riderMarkers`.
  - `_connectRiderTelemetry()` — creates a `WebSocketClient`, connects with the customer's JWT token, subscribes to the broadcast stream. Each frame is parsed as `{rider_id, lat, lng, updated_at}` (matches the Go delivery repository's `UpdateRiderLocation` Redis pub/sub payload schema). Updates `_riderMarkers` ValueNotifier with `riderId → LatLng` mapping.
  - `_fetchOrders()` — after loading orders, checks if any order has `status == 'shipped'`. If yes, calls `_connectRiderTelemetry()`. This avoids opening a WS connection when no active delivery exists.
  - `dispose()` — cancels `_wsSub`, `_wsStateSub`, calls `_wsClient?.disconnect()`, disposes `_riderMarkers`.
  - `_buildMapTab()` — added a `ValueListenableBuilder<Map<String, LatLng>>` that renders a `MarkerLayer` of rider icons (neon `#CAFF33` `two_wheeler_rounded`) on top of the base OSM tiles. Rebuilds ONLY the marker layer — the base map is untouched.
  - Added a live tracking status banner at the bottom of the Map tab: shows `"Live rider tracking: N riders on the move"` when `_riderMarkers` is non-empty, hidden otherwise.

**Dependency:** Requires the Rust WS gateway (port 8087) to be running. See master roadmap Phase 2 in [[session_15_audit_report]].

**Backlink:** Action item #3 in [[session customer audit#Action Items for Next Session]]

---

## 📁 Files Modified This Session

| File | Change |
|------|--------|
| `frontend/.../core/network/api_endpoints.dart` | `productsList()` now accepts `search` + `category` params |
| `frontend/.../customer/presentation/screens/customer_dashboard_screen.dart` | Server-side search/category wiring, Nominatim geocoding, WS rider tracking |

No backend Go changes were required — the Go product-service already supported search/category query params.

---

## ✅ Verification

```
go build ./...     → 0 errors
flutter analyze lib/ → 0 errors (info-level deprecation warnings only)
```

---

## 📊 Updated Customer Feature Status (from [[session customer audit]])

| Feature | Previous | Now |
|---------|----------|-----|
| Server-side Search | ❌ Missing | ✅ Resolved |
| Product Categories (backend filter) | ❌ Missing | ✅ Resolved |
| Geocoding / Store Locator | ❌ Missing | ✅ Resolved (Nominatim) |
| Delivery Tracking on Map | ❌ Missing | ✅ Resolved (WS telemetry) |
| Product Images (real) | ❌ Missing | ❌ Still placeholder icons |
| Payment Integration (Stripe) | ❌ Missing | ❌ Still hardcoded display |
| Mobile Wallet (JazzCash/EasyPaisa) | ❌ Missing | ❌ Still hardcoded display |
| Wishlist / Favorites | ❌ Missing | ❌ Does not exist |
| Product Reviews/Ratings | ❌ Missing | ❌ Does not exist |
| Edit Profile | ❌ Missing | ❌ Display-only |
| Address Management | ❌ Missing | ❌ Hardcoded "Not provided" |
| Quantity Selector (Details Screen) | ❌ Missing | ❌ Hardcoded to 1 |

---

## ⚡ Action Items for Next Session

1. **Quantity Selector** — Add quantity stepper to `product_details_screen.dart` (currently hardcoded to 1).
2. **Product Images** — Wire real image URLs from the `image_url` column to the product cards/details (currently placeholder icons).
3. **Stripe Payment Integration** — Replace the hardcoded payment method display in the Profile tab with a real Stripe Payment Intent flow.
4. **Edit Profile + Address Management** — Add editable forms for customer profile fields and saved addresses.

---

> **Session 16 context:** This session (17) continues directly from the vendor module fixes in [[vendor_remaining_phases_plan]] (Session 16). The WebSocket client hardened in Session 16 (broadcast stream + reconnection) is now consumed by the customer Map tab for live rider tracking.