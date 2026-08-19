# Session 18 — Execution Plan: Customer Experience Gaps

> **Created:** July 13, 2026
> **Source:** Action items from [[session_17_customer_audit_resolution]] + [[session customer audit]]
> **Architecture:** [[OMNIGO_SuperApp_Architecture]]

---

## ═══════════════════════════════════════
## GOAL
## ═══════════════════════════════════════

Resolve the 4 remaining customer-side feature gaps identified in the
Session 4 audit:

1. **Quantity Selector** on the product details screen
2. **Real Product Images** wired from `image_url` to image widgets
3. **Edit Profile** + **Address Management** (backend + frontend)
4. **Stripe Payment Integration** (real Payment Intent flow)

---

## ═══════════════════════════════════════
## CURRENT STATE ANALYSIS
## ═══════════════════════════════════════

### Quantity Selector
- **File:** `product_details_screen.dart`
- **Current:** Checkout hardcodes `product_tracking_ids: [prodId]` (1 item)
  and `total_amount: price` (no multiplication). No quantity stepper UI.
- **Cart:** `CartProvider.addItem(p)` adds 1 unit per call — no quantity
  param exposed.

### Product Images
- **File:** `customer_dashboard_screen.dart` `_buildProductCard()` line ~1055
- **Current:** Product card image area shows `Icon(Icons.shopping_bag_outlined)`
  placeholder — ignores `p['image_url']`.
- **File:** `product_details_screen.dart` line ~159
- **Current:** Hero widget shows `Icon(Icons.shopping_bag)` — ignores
  `prod['image_url']`.
- **Backend:** `products.image_url TEXT` column exists in init.sql (Session 16
  rewrite). Go `Product.ImageURL` field exists. Vendor AddProduct form
  already accepts image_url. ✅ Backend ready.

### Edit Profile + Address Management
- **Backend:** `auth_service.go` has Register + Login only. No `UpdateProfile`
  endpoint. `users` table has `full_name`, `phone`, `address`, `email`
  columns.
- **Frontend:** Profile tab (`_buildProfileTab()`) is display-only.
  `_buildInfoRow('Default Address', 'Not provided', ...)` is hardcoded.
- **SessionRegistry:** Stores `fullName`, `email`, `phone` — but NOT `address`.
  No `updateProfile()` method.

### Stripe Payment Integration
- **Backend:** `checkout_service.go` and `webhook_handler.go` already exist
  in the codebase (Session 6 created them). Need to verify they're wired
  to the order-service routes.
- **Frontend:** Profile tab shows fake `_buildPaymentCard('Stripe Credit / Debit',
  'Linked Visa (**** 9081)', ...)`. No Stripe SDK, no Payment Intent flow.

---

## ═══════════════════════════════════════
## EXECUTION PLAN — PRIORITY ORDER
## ═══════════════════════════════════════

### Phase 1: Quantity Selector + Real Product Images (Frontend-only)
> **Estimated time:** 45 min
> **No backend changes needed.

#### 1A: Quantity Selector on Product Details Screen
- **File:** `product_details_screen.dart`
- **Changes:**
  1. Add `int _quantity = 1` state field.
  2. Add a quantity stepper widget (− / count / +) between description
     and checkout buttons. Min 1, max `product['stock']` (if available).
  3. Update `_executeCheckout()`:
     - `product_tracking_ids`: repeat prodId `_quantity` times (or pass
       as array — backend accepts `[]string`).
     - `total_amount`: `price * _quantity`.
  4. Update "Add to Cart" button: pass quantity to `CartProvider.addItem()`.
  5. Update `CartProvider.addItem()` signature to accept optional
     `int quantity` param (default 1). Update `cart_item.dart` model
     if needed.

#### 1B: Real Product Images
- **File:** `customer_dashboard_screen.dart` → `_buildProductCard()`
- **Change:** Replace `Icon(Icons.shopping_bag_outlined)` placeholder with
  `Image.network(p['image_url'], fit: BoxFit.cover)` when URL is non-empty.
  Keep placeholder icon as fallback for empty URLs.
- **File:** `product_details_screen.dart` → Hero widget
- **Change:** Replace `Icon(Icons.shopping_bag)` with `Image.network` when
  `prod['image_url']` is non-empty. Keep icon fallback.

---

### Phase 2: Edit Profile + Address Management (Backend + Frontend)
> **Estimated time:** 1.5 hours
> **Requires Go backend changes + new Flutter screen.

#### 2A: Backend — UpdateProfile Endpoint
- **New code in:** `auth_service.go` + `auth_handler.go`
- **Route:** `PATCH /api/v1/auth/profile`
- **Auth:** Extract tracking_id from JWT/Bearer header (same pattern as
  vendor product handler).
- **Request body (partial update — all optional):**
  ```json
  {
    "full_name": "Updated Name",
    "phone": "+92 300 1234567",
    "address": "House 12, Gulberg III, Lahore"
  }
  ```
- **SQL:** `UPDATE users SET full_name=$1, phone=$2, address=$3,
  updated_at=NOW() WHERE tracking_id=$4`
- **Response:** Updated user fields (excluding password_hash).
- **Register route** in `auth_handler.go RegisterRoutes()`.

#### 2B: Backend — GetProfile Endpoint
- **Route:** `GET /api/v1/auth/profile`
- **Auth:** Same tracking_id extraction.
- **SQL:** `SELECT tracking_id, email, full_name, phone, address, role,
  region FROM users WHERE tracking_id=$1`
- **Response:** User profile JSON (no password_hash).

#### 2C: SessionRegistry — Add address field
- **File:** `session_registry.dart`
- **Add:** `String? _address` field + getter + persist in `saveSession()`
  + clear in `clear()`. Hydrate from SharedPreferences in `hydrate()`.
- **Add:** `updateProfile()` method that updates in-memory fields + prefs.

#### 2D: Flutter — Edit Profile Screen
- **New file:** `customer/.../edit_profile_screen.dart`
- **Form fields:** Full Name, Phone, Address (all editable). Email
  read-only (unique constraint — changing requires verification flow,
  out of scope).
- **Submit:** PATCH `/api/v1/auth/profile` with JWT header.
- **On success:** Call `SessionRegistry.instance.updateProfile()` to
  refresh in-memory cache, then `Navigator.pop()` to return to profile
  tab.
- **Wire:** Add "Edit Profile" button in `_buildProfileTab()` that
  navigates to this screen.

#### 2E: Flutter — Profile Tab Address Display
- **File:** `customer_dashboard_screen.dart` → `_buildProfileTab()`
- **Change:** Replace hardcoded `'Not provided'` for Default Address
  with `SessionRegistry.instance.address ?? 'Not provided'`.

---

### Phase 3: Stripe Payment Integration (Backend + Frontend)
> **Estimated time:** 1 hour
> **Requires verifying existing checkout_service + Stripe SDK wiring.

#### 3A: Verify Backend Stripe Checkout
- **Files to audit:** `checkout_service.go`, `webhook_handler.go`,
  `order-service/main.go`
- **Check:** Is `checkout_service` registered in order-service routes?
  Does it create Stripe Payment Intents? Does `webhook_handler` verify
  Stripe signatures?
- **If missing:** Wire `POST /api/v1/checkout` route to
  `checkout_service.CreateCheckout()`. Ensure Stripe webhook endpoint
  `POST /api/v1/webhooks/stripe` is registered.

#### 3B: Flutter — Replace Fake Payment Cards
- **File:** `customer_dashboard_screen.dart` → `_buildProfileTab()`
- **Change:** Replace hardcoded `_buildPaymentCard('Stripe Credit / Debit',
  'Linked Visa (**** 9081)', ...)` with a "Add Payment Method" button
  that launches a Stripe payment sheet (or a placeholder for now if
  Stripe SDK not yet added to pubspec).
- **Note:** Full Stripe SDK integration (flutter_stripe package) requires
  adding `flutter_stripe: ^10.0.0` to `pubspec.yaml` and initializing
  with publishable key. For this session, we'll wire the backend
  Payment Intent creation and add the frontend scaffolding. Full card
  tokenization can be a follow-up if time-constrained.

#### 3C: Flutter — Checkout Flow with Payment Intent
- **File:** `product_details_screen.dart` → `_executeCheckout()`
- **Change:** Before creating the order, call `POST /api/v1/checkout`
  to create a Payment Intent. If Stripe returns a client_secret, show
  payment confirmation. On success, proceed with order creation.
- **Fallback:** If Stripe is not configured (no API key), fall back to
  the current direct-order flow with a clear "Cash on Delivery" label.

---

## ═══════════════════════════════════════
## EXECUTION SEQUENCE
## ═══════════════════════════════════════

| Step | Task | Time | Deps |
|------|------|------|------|
| 1 | 1B: Wire real product images (card + details) | 15 min | None |
| 2 | 1A: Quantity selector on details screen | 30 min | None |
| 3 | 2A: Backend UpdateProfile endpoint (Go) | 25 min | None |
| 4 | 2B: Backend GetProfile endpoint (Go) | 15 min | Step 3 |
| 5 | 2C: SessionRegistry address field + updateProfile() | 15 min | None |
| 6 | 2E: Profile tab address from SessionRegistry | 5 min | Step 5 |
| 7 | 2D: Edit Profile screen (Flutter) | 40 min | Steps 3-6 |
| 8 | 3A: Verify/fix backend Stripe checkout wiring | 20 min | None |
| 9 | 3B: Replace fake payment cards in profile | 15 min | Step 8 |
| 10 | 3C: Checkout flow with Payment Intent | 25 min | Step 8 |

**Total estimated time: ~3.5 hours**

---

## ═══════════════════════════════════════
## FILES TO BE MODIFIED
## ═══════════════════════════════════════

| File | Phase | Change |
|------|-------|--------|
| `product_details_screen.dart` | 1A, 1B, 3C | Quantity stepper, real image, Payment Intent |
| `customer_dashboard_screen.dart` | 1B, 2E, 3B | Real product images, address display, payment cards |
| `cart_provider.dart` | 1A | addItem() quantity param |
| `cart_item.dart` | 1A | quantity field (if missing) |
| `auth_service.go` | 2A, 2B | UpdateProfile, GetProfile methods |
| `auth_handler.go` | 2A, 2B | PATCH /profile, GET /profile routes |
| `session_registry.dart` | 2C | address field, updateProfile() |
| `edit_profile_screen.dart` | 2D | NEW file — edit profile form |
| `checkout_service.go` | 3A | Verify/fix route registration |
| `order-service/main.go` | 3A | Verify checkout route wired |

---

## ═══════════════════════════════════════
## VERIFICATION GATES
## ═══════════════════════════════════════

After each phase:
1. `cd backend/go-services && go build ./...` — must pass with 0 errors
2. `cd frontend/omnigo_app && flutter analyze lib/` — must pass with 0 errors
3. Manual smoke check: quantity changes price, images load from URL,
   profile edits persist, checkout creates Payment Intent

---

> **Decision:** Stripe full SDK integration (flutter_stripe package) is
> scoped as scaffolding-only for this session. Full card tokenization
> and 3DS auth flows are follow-up work. The backend Payment Intent
> creation will be wired so the frontend can consume it immediately
> when flutter_stripe is added.