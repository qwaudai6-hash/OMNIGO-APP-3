# Session 20 — Execution Plan: PayFast, Wishlist Tab, Order Details, Admin Module

> **Created:** July 13, 2026
> **Source:** Action items from [[session_19_execution_log]] + [[session_15_audit_report]]
> **Architecture:** [[OMNIGO_SuperApp_Architecture]]

---

## ═══════════════════════════════════════
## GOAL
## ═══════════════════════════════════════

4 features:
1. **PayFast PK Full Integration** — Signed payment requests, redirect flow, integrity hash verification
2. **Wishlist Tab** — Dedicated bottom-nav tab showing favorited products
3. **Order History Detail View** — Tap an order → full breakdown (products, rider, status timeline)
4. **Admin Module Audit** — Fix admin-service, user verification API, RBAC, lineage queries

---

## ═══════════════════════════════════════
## PAYFAST RESEARCH FINDINGS
## ═══════════════════════════════════════

### PayFast PK (gopayfast.com)
- **Auth:** Merchant_ID + Secured_Key → POST auth endpoint → receive one-time auth token
- **Signature:** `md5(merchant_id:merchant_name:amount:order_id)`
- **Hosted Checkout Flow:**
  1. Get auth token from PayFast
  2. Create signature with MD5
  3. Build payload: MERCHANT_ID, MERCHANT_NAME, TOKEN, TXNAMT, CUSTOMER_MOBILE_NO, CUSTOMER_EMAIL_ADDRESS, SIGNATURE, SUCCESS_URL, FAILURE_URL, BASKET_ID, ORDER_DATE, CHECKOUT_URL
  4. POST to PayFast hosted page URL
  5. Customer pays on PayFast page → redirected to SUCCESS_URL or FAILURE_URL
  6. Backend callback (IPN) verifies transaction status
- **API Base:** `https://api.payfast.com.pk` (production) / sandbox URL for testing
- **Env vars needed:** `PAYFAST_MERCHANT_ID`, `PAYFAST_SECURED_KEY`, `PAYFAST_MERCHANT_NAME`, `PAYFAST_API_URL`, `PAYFAST_RETURN_URL`

---

## ═══════════════════════════════════════
## EXECUTION PLAN
## ═══════════════════════════════════════

### Phase 1: PayFast PK Full Integration
> **Time:** 1 hour

#### 1A: Backend — PayFast Service (Go)
- **New file:** `internal/wallet/service/payfast_service.go`
  - `GetAuthToken()` — POST to PayFast auth endpoint with merchant_id + secured_key
  - `CreateSignature(merchantID, merchantName, amount, orderID)` — MD5 hash
  - `InitiateHostedCheckout(req)` — builds payload, returns redirect URL
  - `VerifyCallback(formData)` — verifies integrity hash from PayFast callback
- **Update:** `internal/wallet/handler/wallet_handler.go`
  - `POST /api/v1/wallet/payfast/charge` — get token, create signature, return redirect URL
  - `POST /api/v1/wallet/payfast/callback` — verify + process callback
- **Env vars:** read from `PAYFAST_MERCHANT_ID`, `PAYFAST_SECURED_KEY`, `PAYFAST_MERCHANT_NAME`, `PAYFAST_API_URL`

#### 1B: Frontend — PayFast in Payment Selector
- **File:** `product_details_screen.dart`
  - Add "PayFast" option in payment method dialog
  - On select: POST `/api/v1/wallet/payfast/charge` → get redirect URL → launch in WebView
  - On callback: check payment status → proceed/cancel order

---

### Phase 2: Wishlist Tab
> **Time:** 45 min

#### 2A: Wishlist Screen
- **New file:** `customer/.../wishlist_screen.dart`
  - GET `/api/v1/wishlist/` → fetch favorited product IDs
  - Fetch product details for each favorited ID (batch query or iterate)
  - Display as grid (reuse `_buildProductCard` pattern)
  - Tap → navigate to product details
  - Long-press → remove from wishlist + refresh

#### 2B: Bottom Nav Integration
- **File:** `customer_dashboard_screen.dart`
  - Replace the "Home" tab with "Wishlist" (or add as 6th tab)
  - Update `_buildBottomNavbar()` with wishlist icon
  - Update `IndexedStack` to include wishlist screen

---

### Phase 3: Order History Detail View
> **Time:** 1 hour

#### 3A: Order Detail Screen
- **New file:** `customer/.../order_detail_screen.dart`
  - Accepts order JSON map
  - Displays:
    - Order tracking ID + status badge
    - Product list (product_tracking_ids)
    - Store info (store_tracking_id)
    - Rider info (rider_tracking_id, if assigned)
    - Total amount + currency
    - Delivery status timeline (pending → accepted → shipped → delivered)
    - OTP code (if present)
  - Live status update via WebSocket (if order is shipped)

#### 3B: Wire Orders Tab
- **File:** `customer_dashboard_screen.dart` → `_buildOrdersTab()`
  - Make each order card tappable → navigate to OrderDetailScreen
  - Pass full order JSON

---

### Phase 4: Admin Module Fix
> **Time:** 1.5 hours

#### 4A: Fix Admin Service Compile + DB
- **File:** `admin-service/main.go`
  - Fix DB credentials: `omnigo_user:omnigo_password@localhost:5433/omnigo_db`
  - Add Neo4j credentials: `neo4j/omnigo123`
  - Add graceful shutdown
- **File:** `admin/service.go`
  - Fix SQL query: use `order_tracking_id` (not `tracking_id`), `store_tracking_id` (not `tracking_id` on stores), correct column names matching init.sql
  - Fix `current_h3_hexagon` — exists in deliveries table now (Session 16 init.sql)
  - Make Neo4j optional (graceful degradation if Neo4j is down)

#### 4B: User Verification API
- **New methods in:** `admin/service.go`
  - `ListPendingVerifications()` — SELECT users WHERE is_verified = false AND role IN ('rider', 'vendor')
  - `ApproveUser(trackingID)` — UPDATE users SET is_verified = true WHERE tracking_id = $1
  - `ListAllUsers(role filter)` — SELECT with pagination
- **New routes in:** `admin-service/main.go`
  - `GET /api/admin/users/pending` — list pending riders/vendors
  - `PATCH /api/admin/users/:tracking_id/approve` — approve user
  - `GET /api/admin/users` — list all users with optional role filter

#### 4C: Admin Frontend — Real API Wiring
- **File:** `admin_surveillance_screen.dart`
  - Replace mock data with real API calls
  - Add "Pending Approvals" section (GET /api/admin/users/pending)
  - Add "Approve" button per pending user
  - Add order lineage search (GET /api/admin/lineage/:order_id)
  - Add "All Users" tab with role filter

---

## ═══════════════════════════════════════
## EXECUTION SEQUENCE
## ═══════════════════════════════════════

| Step | Task | Time |
|------|------|------|
| 1 | 1A: PayFast service (Go) | 30 min |
| 2 | 1B: PayFast in Flutter payment selector | 15 min |
| 3 | 2A: Wishlist screen | 25 min |
| 4 | 2B: Bottom nav integration | 10 min |
| 5 | 3A: Order detail screen | 35 min |
| 6 | 3B: Wire orders tab tappable | 10 min |
| 7 | 4A: Fix admin service compile + DB + queries | 25 min |
| 8 | 4B: User verification API (Go) | 25 min |
| 9 | 4C: Admin frontend real API | 35 min |

**Total: ~3.5 hours**

---

## VERIFICATION GATES

After each phase:
1. `cd backend/go-services && go build ./...` — 0 errors
2. `cd frontend/omnigo_app && flutter analyze lib/` — 0 errors[[]]
   [[session_21_execution_plan]]