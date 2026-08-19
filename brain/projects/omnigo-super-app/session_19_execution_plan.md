# Session 19 — Execution Plan: Stripe SDK, Wishlist, Reviews, Mobile Wallet

> **Created:** July 13, 2026
> **Source:** Action items from [[session_18_execution_log]]
> **Architecture:** [[OMNIGO_SuperApp_Architecture]]

---

## ═══════════════════════════════════════
## GOAL
## ═══════════════════════════════════════

Resolve the 4 next customer-side feature gaps:

1. **flutter_stripe SDK** — Card tokenization + 3DS auth in checkout
2. **Wishlist / Favorites** — Favorite toggle + wishlist tab
3. **Product Reviews/Ratings** — Backend review endpoint + frontend display
4. **JazzCash/EasyPaisa Mobile Wallet** — Pakistan payment gateway scaffolding

---

## ═══════════════════════════════════════
## CURRENT STATE
## ═══════════════════════════════════════

### Stripe SDK
- **Backend:** `POST /api/v1/checkout` already creates Payment Intents (Session 18).
  `POST /api/v1/webhooks/stripe` verifies signatures + emits Kafka events.
- **Frontend:** `product_details_screen.dart` calls checkout endpoint, gets
  `client_secret`, but cannot tokenize a card (no `flutter_stripe` package).
  Falls back to "Cash on Delivery" label.
- **pubspec.yaml:** No `flutter_stripe` dependency.

### Wishlist
- **Backend:** No `favorites` table in init.sql. No endpoints.
- **Frontend:** No favorite toggle on product cards. No wishlist tab.
- **SessionRegistry:** No wishlist cache.

### Reviews/Ratings
- **Backend:** No `reviews` table. No endpoints.
- **Frontend:** No review display on product details. No review submission form.

### JazzCash/EasyPaisa
- **Backend:** No gateway-specific endpoints. Stripe is the only wired gateway.
- **Frontend:** Profile tab shows "Mobile wallet — coming soon" placeholder.
- **Note:** JazzCash/EasyPaisa use redirect-based flows (not card tokenization).
  Full integration requires merchant credentials + callback URL handling.
  This session will scaffold the backend endpoint structure + frontend
  selection UI. Full credential-based testing is follow-up work.

---

## ═══════════════════════════════════════
## EXECUTION PLAN
## ═══════════════════════════════════════

### Phase 1: flutter_stripe SDK Integration
> **Estimated time:** 45 min

#### 1A: Add flutter_stripe to pubspec.yaml
- Add `flutter_stripe: ^10.0.0` to dependencies
- Run `flutter pub get`

#### 1B: Initialize Stripe in main.dart
- Add `Stripe.publishableKey = 'pk_test_...'` (read from env or hardcoded test key)
- Call `Stripe.instance.applySettings()` before `runApp()`

#### 1C: Payment Sheet in Checkout Flow
- **File:** `product_details_screen.dart` → `_executeCheckout()`
- After getting `client_secret` from backend:
  1. Call `Stripe.instance.initPaymentSheet(...)` with the client_secret
  2. Call `Stripe.instance.presentPaymentSheet()` to show card entry UI
  3. On success → proceed with order creation
  4. On failure/cancel → show error, don't create order
- Remove "Cash on Delivery" fallback when Stripe is active (keep as fallback when Stripe key missing)

---

### Phase 2: Wishlist / Favorites
> **Estimated time:** 1.5 hours
> **Requires new DB table + backend endpoints + frontend.

#### 2A: Backend — Favorites Table + Endpoints
- **Schema:** Add to `init.sql`:
  ```sql
  CREATE TABLE favorites (
      id BIGSERIAL PRIMARY KEY,
      customer_tracking_id VARCHAR(50) NOT NULL,
      product_tracking_id VARCHAR(50) NOT NULL,
      created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
      UNIQUE(customer_tracking_id, product_tracking_id)
  );
  CREATE INDEX idx_favorites_customer ON favorites(customer_tracking_id);
  ```
- **New file:** `backend/.../wishlist/` — repository, service, handler
  - `POST /api/v1/wishlist/:product_id` — toggle favorite (add/remove)
  - `GET /api/v1/wishlist` — list customer's favorites
  - `DELETE /api/v1/wishlist/:product_id` — remove favorite
- **Wire** into a new `wishlist-service` or attach to product-service

#### 2B: Frontend — Favorite Toggle + Wishlist View
- **File:** `customer_dashboard_screen.dart` → `_buildProductCard()`
  - Add heart icon toggle (filled/outline) on each product card
  - On tap: POST `/api/v1/wishlist/:product_id`
- **New file:** `wishlist_screen.dart` — grid of favorited products
  - GET `/api/v1/wishlist` → display products
  - Tap product → navigate to details
  - Long-press → remove from wishlist
- **File:** `customer_dashboard_screen.dart` → bottom nav
  - Add "Wishlist" tab (or replace decorative tab)

---

### Phase 3: Product Reviews/Ratings
> **Estimated time:** 1.5 hours
> **Requires new DB table + backend endpoints + frontend.

#### 3A: Backend — Reviews Table + Endpoints
- **Schema:** Add to `init.sql`:
  ```sql
  CREATE TABLE reviews (
      id BIGSERIAL PRIMARY KEY,
      product_tracking_id VARCHAR(50) NOT NULL,
      customer_tracking_id VARCHAR(50) NOT NULL,
      rating INTEGER NOT NULL CHECK (rating >= 1 AND rating <= 5),
      comment TEXT,
      created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
      UNIQUE(product_tracking_id, customer_tracking_id)
  );
  CREATE INDEX idx_reviews_product ON reviews(product_tracking_id);
  ```
- **New file:** `backend/.../review/` — repository, service, handler
  - `POST /api/v1/reviews` — create review (auth required, one per customer per product)
  - `GET /api/v1/reviews/:product_id` — list reviews for a product
  - `GET /api/v1/reviews/:product_id/summary` — average rating + count

#### 3B: Frontend — Review Display + Submission
- **File:** `product_details_screen.dart`
  - Add "Reviews" section below description
  - Show average rating (stars) + count
  - Show top 3 reviews (comment + customer name + stars)
  - "Write a Review" button → review form dialog (star selector + comment)
  - On submit: POST `/api/v1/reviews` → refresh display

---

### Phase 4: JazzCash/EasyPaisa Scaffolding
> **Estimated time:** 45 min
> **Scaffolding only — full credential-based integration is follow-up.

#### 4A: Backend — Mobile Wallet Endpoint Structure
- **New file:** `backend/.../wallet/handler/wallet_handler.go`
  - `POST /api/v1/wallet/charge` — accepts gateway (jazzcash/easypaisa), amount, customer_id
  - Returns redirect URL (mock for now — real integration needs merchant credentials)
  - `POST /api/v1/wallet/callback` — webhook for gateway callback
- **Wire** into order-service main.go

#### 4B: Frontend — Payment Method Selection
- **File:** `product_details_screen.dart` → checkout flow
  - Before creating order: show payment method selector (Card / JazzCash / EasyPaisa / Cash)
  - If Card → Stripe Payment Sheet (Phase 1)
  - If JazzCash/EasyPaisa → POST `/api/v1/wallet/charge` → redirect URL
  - If Cash → direct order (current fallback)
- **File:** `customer_dashboard_screen.dart` → profile tab
  - Replace "coming soon" with "Tap to link" → info dialog explaining redirect flow

---

## ═══════════════════════════════════════
## EXECUTION SEQUENCE
## ═══════════════════════════════════════

| Step | Task | Time | Deps |
|------|------|------|------|
| 1 | 2A: Favorites table + backend endpoints | 30 min | None |
| 2 | 2B: Favorite toggle + wishlist screen (Flutter) | 45 min | Step 1 |
| 3 | 3A: Reviews table + backend endpoints | 30 min | None |
| 4 | 3B: Review display + submission (Flutter) | 45 min | Step 3 |
| 5 | 1A: Add flutter_stripe to pubspec | 5 min | None |
| 6 | 1B: Initialize Stripe in main.dart | 10 min | Step 5 |
| 7 | 1C: Payment Sheet in checkout | 30 min | Steps 5-6 |
| 8 | 4A: Wallet endpoint scaffolding (Go) | 20 min | None |
| 9 | 4B: Payment method selector (Flutter) | 25 min | Steps 7-8 |

**Total estimated time: ~4 hours**

---

## ═══════════════════════════════════════
## VERIFICATION GATES
## ═══════════════════════════════════════

After each phase:
1. `cd backend/go-services && go build ./...` — 0 errors
2. `cd frontend/omnigo_app && flutter analyze lib/` — 0 errors

---

> **Scope note:** flutter_stripe requires a publishable key (`pk_test_...`).
> We'll use a placeholder test key. Real key must be injected via env var
> `STRIPE_PUBLISHABLE_KEY` in production. JazzCash/EasyPaisa full
> integration requires merchant credentials from the gateway provider —
> this session scaffolds the endpoint structure only.