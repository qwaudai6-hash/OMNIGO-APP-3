# OMNIGO Session Log — 13 July 2026 (Session 4)

## 📋 Session Summary

This session focused on **Customer-Side Feature Audit**, **Logout Bug Fix**, and **Critical Bug Fixes** across both backend and frontend.

---

## 🐛 Bugs Fixed This Session

### 1. Logout Button — Non-Functional (CRITICAL)
- **File:** `customer_dashboard_screen.dart` (line 715-718)
- **Problem:** Log Out button only called `Navigator.pushReplacementNamed(context, '/')` — it did NOT call `SessionRegistry.instance.clear()`. So the JWT token, role, and tracking ID stayed in memory. When the user reached the login page, `initState()` detected `isLoggedIn == true` and immediately redirected back to the dashboard. **Result: User could NEVER log out.**
- **Fix:** Added `await SessionRegistry.instance.clear()` before navigation, and changed to `pushNamedAndRemoveUntil` to wipe the navigation stack completely.

### 2. `prefs.clear()` Wipes Entire Session After Checkout (CRITICAL)
- **File:** `product_details_screen.dart` (lines 70, 73)
- **Problem:** After a checkout attempt (success OR failure), the code called `await prefs.clear()` which deleted ALL SharedPreferences including JWT token, role, tracking ID, full name — effectively logging the user out silently after every purchase.
- **Fix:** Replaced `prefs.clear()` with targeted `prefs.remove('pending_nonce')`, `prefs.remove('pending_order_product')`, `prefs.remove('pending_order_status')` — cleans only checkout-related keys.

### 3. Phone Number Lost During Registration
- **File (Frontend):** `dynamic_signup_screen.dart` (line 125-135)
- **File (Backend):** `auth_service.go` RegisterRequest struct + INSERT query
- **Problem:** The registration UI collects phone number via `_phoneController` and validates it, but the `_submit()` method NEVER included it in the API payload. Same issue with customer address — collected but sent as empty string.
- **Fix:** Added `'phone': phone` and `'address': _addressController.text.trim()` to registration body. Added `Phone` field to Go `RegisterRequest` struct and added it to the SQL INSERT.

### 4. Checkout Host Mismatch — Uses Wrong Port
- **File:** `product_details_screen.dart` (line 40)
- **Problem:** Checkout POST used `http://10.0.2.2:8088` (Android emulator, order-service port) while everything else used `127.0.0.1` (desktop). Checkout would fail on desktop.
- **Fix:** Replaced with `ApiEndpoints.authBase` + `/orders/`. Added JWT `Authorization` header.

### 5. Customer Dashboard Hardcoded Product URL
- **File:** `customer_dashboard_screen.dart` (line 95)
- **Problem:** Used `http://127.0.0.1:8082` instead of centralized `ApiEndpoints`.
- **Fix:** Replaced with `ApiEndpoints.productsList(limit: _limit, offset: _offset)`.

---

## 🔍 Customer Side Feature Audit

### Customer Dashboard — 5 Tabs

| Tab | Name | Status | Details |
|-----|------|--------|---------|
| 0 | Home | ⚠️ Mixed | Summer Sale banner = hardcoded. Top Categories = decorative (don't filter). Featured Products = REAL from API. Notification bell = decorative (no onTap). |
| 1 | Catalog | ✅ Real (mostly) | Product list = REAL paginated API fetch. Search = CLIENT-SIDE only (filters loaded products, does NOT query backend). Category pills = DECORATIVE (tracked but never used in filter logic). Cart icon = DECORATIVE. |
| 2 | Map | ❌ Mock | Uses `flutter_map` with real OSM tiles, but city search uses hardcoded `_mockGeocodingDb` with only 7 cities. Shows message "City not found in local mock DB." No real geocoding API. |
| 3 | Orders | ❌ Hardcoded | Shows 2 static fake orders. Zero API calls. No order history from backend. |
| 4 | Profile | ⚠️ Mixed | Name/Email/Phone = REAL from SessionRegistry. Payment methods (Stripe, JazzCash) = FAKE hardcoded displays. No edit profile. |

### Product Details Screen
- Displays product info: ✅ Real
- Buy Now button: ✅ Real API call (now fixed)
- Quantity selector: ❌ Missing (hardcoded to 1)
- Product images: ❌ Placeholder icon only

### Missing Customer Features Status (Updated Phase 5)

| Feature | Status | Resolve Method |
|---------|--------|----------------|
| Shopping Cart | ✅ RESOLVED (Phase 5) | CartProvider ChangeNotifier + local storage cache |
| Cart Management (add/remove/quantity) | ✅ RESOLVED (Phase 5) | Integrated BottomSheet with increment/decrement/remove triggers |
| Order History (real API) | ✅ RESOLVED (Phase 5) | Connected to GET /api/v1/orders/customer/:customer_id |
| Order Status Tracking (database) | ✅ RESOLVED (Phase 5) | Dynamic tracker pulls state (Pending, Accepted, Shipped, etc.) |
| Server-side Search | ❌ Missing | Currently client-side local filter only |
| Product Categories (backend filter) | ❌ Missing | Categories are client-side decorative pills |
| Geocoding / Store Locator | ❌ Missing | 7-city mock DB only |
| Delivery Tracking on Map | ❌ Missing | Telemetry is not linked to Map tab UI |
| Product Images (real) | ❌ Missing | Placeholder icons |
| Payment Integration (Stripe) | ❌ Missing | Hardcoded display only |
| Mobile Wallet (JazzCash/EasyPaisa) | ❌ Missing | Hardcoded display only |
| Wishlist / Favorites | ❌ Missing | Does not exist |
| Product Reviews/Ratings | ❌ Missing | Does not exist |
| Edit Profile | ❌ Missing | Display-only |
| Address Management | ❌ Missing | Hardcoded "Not provided" |
| Quantity Selector (Details Screen) | ❌ Missing | Hardcoded to 1 |

---

## 📁 Files Modified This Session
- `backend/go-services/internal/order/repository/order_repository.go` — Fixed pgx NULL scans, timestamps
- `backend/go-services/internal/order/service/order_service.go` — Added orders listings and status mutations
- `backend/go-services/internal/order/handlers/order_handler.go` — Registered customer list, vendor list, status patch routes
- `frontend/omnigo_app/lib/features/customer/data/models/cart_item.dart` — Created CartItem serialization model
- `frontend/omnigo_app/lib/core/services/cart_provider.dart` — Created CartProvider state cache
- `frontend/omnigo_app/lib/core/network/api_endpoints.dart` & `api_client.dart` — Added order-service gateway routing
- `frontend/omnigo_app/lib/main.dart` — Bootstrapped CartProvider ChangeNotifier
- `frontend/omnigo_app/lib/features/customer/presentation/screens/customer_dashboard_screen.dart` — Added catalog Cart triggers, header shopping count, parallel checkouts, dynamic orders tab
- `frontend/omnigo_app/lib/features/customer/presentation/screens/product_details_screen.dart` — Added Cart actions, updated checkout payload to use product_tracking_ids slice
- `frontend/omnigo_app/lib/features/vendor/presentation/screens/vendor_dashboard_screen.dart` — Connected Merchant dashboard to backend order fetches and patch updates

---

## ⚡ Action Items for Next Session
1. **Server-side Search & Category Filtering** — Add search and category query params to Go product-service and connect frontend catalog search.
2. **Nominatim OSM Geocoding** — Connect Map search tab to real open geocoding API.
3. **Real-time Live Rider Tracking** — Link websocket telemetry updates for shipped orders to display rider location marker on the Map.

