# OMNIGO APP — Full QA Test Report
- **Date:** 2026-09-01
- **Environment:** Production (Railway)
- **Tester:** Automated + Manual Code Scan
- **Status:** 20 Tests Run, 20 Bugs Found

---

## Test Results Summary

### PASS/FAIL Matrix
- - Test 1: COD Full Lifecycle — ✅ PASS
- - Test 2: Wallet Paid + Escrow — ✅ PASS
- - Test 3: Customer Cancel COD — ✅ PASS
- - Test 4: Vendor Cancel (pre-fix) — ❌ FAIL (FORBIDDEN — local fix applied, not deployed)
- - Test 5: Rider A Cancel → Rider B Accept — ✅ PASS
- - Test 6: Dispute on COD — ⚠️ PARTIAL (report created but orders.dispute_status not synced — local fix applied, not deployed)
- - Test 7: Return on Delivered COD — ✅ PASS
- - Test 8: Vendor Cancel Fix Verification — ❌ FAIL (fix only in local code, not deployed to Railway)
- - Test 9a: Invalid Status Transition — ✅ PASS (pending→delivered blocked)
- - Test 9b: Duplicate Nonce Prevention — ✅ PASS
- - Test 9c: Wallet Insufficient Funds — ✅ PASS (error message improved)
- - Test 9d: Unauth Access Blocked — ✅ PASS (401 on all)
- - Test 9e: Vendor Self-Accept Blocked — ✅ PASS (FORBIDDEN_ROLE)
- - Test 9f: Admin Self-Demotion — ✅ PASS (route 404 — safe)
- - Test 9g: Product Stats Endpoint — ❌ FAIL (404 — endpoint missing)
- - Test 9h: Vendor Status PATCH — ✅ PASS (customer blocked)
- - Test 9i: Rider Double Accept — ⚠️ PARTIAL (empty tracking_id from DB lookup issue)
- - Test 10: Database Integrity — ❌ FAIL (see DB Issues below)

---

## Bugs Found — Backend (Go)

### BUG-01 — CRITICAL — Vendor Cancel Auth Check
- - File: `order_handler.go:424`
- - Issue: `CancelOrder` only allows customer or admin. Vendor who owns the order gets `FORBIDDEN_NOT_ORDER_OWNER`
- - Impact: Vendor cannot cancel their own orders before shipping
- - Fix: ✅ APPLIED LOCALLY — allows `order.VendorTrackID == callerID`
- - Status: **NOT DEPLOYED** to Railway

### BUG-02 — CRITICAL — Dispute Status Not Synced
- - File: `delivery_service.go:942` + `delivery_repository.go`
- - Issue: `CreateOrderDispute` does NOT update `orders.dispute_status`. Frontend sees stale "NONE"
- - Impact: Admin and customer cannot see dispute status on order
- - Fix: ✅ APPLIED LOCALLY — `UpdateOrderDisputeStatus()` added
- - Status: **NOT DEPLOYED** to Railway

### BUG-03 — CRITICAL — Duplicate Delivery Records (30 per order!)
- - File: `delivery_service.go:191` → `delivery_repository.go:38`
- - Issue: `CreateGig` does raw `INSERT INTO deliveries` with no uniqueness check on `order_tracking_id`. Every `orders.created` Kafka event creates a new delivery row. Cancel + re-ship creates duplicates.
- - Impact: DB bloat (556 no-rider deliveries across 51 orders, some orders have 30 duplicate gig records). Makes gig lookup unreliable.
- - Fix NEEDED: Add `ON CONFLICT (order_tracking_id) DO UPDATE` or check for existing active gig before INSERT

### BUG-04 — CRITICAL — Double Vendor Wallet Credit in Escrow Release
- - File: `escrow/service.go:79-143`
- - Issue: `ReleaseExpiredHolds` is not transactional. Two concurrent cron runs can both fetch `status='held'` rows, both pass the `escrow_released` check, and both credit the vendor wallet. No `SELECT ... FOR UPDATE` or idempotency guard.
- - Impact: Vendor receives 2x their escrow amount
- - Fix NEEDED: Wrap per-hold flow in a transaction with `SELECT ... FOR UPDATE SKIP LOCKED`

### BUG-05 — CRITICAL — MarkOrderDelivered Bypasses State Machine
- - File: `order_service.go:387-394`
- - Issue: `repo.MarkOrderDelivered` does unconditional `UPDATE orders SET status = 'delivered'` with no WHERE clause on current status. Then `UpdateOrderStatus` reads the already-delivered status and short-circuits. Pending or cancelled orders can be marked delivered. No Kafka event emitted.
- - Impact: Invalid state transitions, missing downstream notifications
- - Fix NEEDED: Enforce `WHERE status IN ('shipped','in_transit')` in repo method

### BUG-06 — HIGH — Escrow Not Frozen on Cancel/Return
- - File: `order_service.go:324-329`
- - Issue: When order transitions to `cancelled` or `returned`, escrow hold is NOT reversed. `ReleaseExpiredHolds` doesn't check order status — it only checks `escrow_released` flag. Cancelled orders' escrow eventually releases to vendor.
- - Impact: Vendor receives money for cancelled/returned orders
- - Fix NEEDED: Freeze or reverse escrow hold on cancel/return

### BUG-07 — HIGH — Delivery Cancel Error Silently Swallowed
- - File: `delivery_service.go:118`
- - Issue: `_ = s.repo.CancelDeliveryForOrder(...)` — error discarded. If DB fails, gig stays active with rider assigned. Kafka offset committed, event lost permanently.
- - Impact: Rider delivers to customer who already cancelled
- - Fix NEEDED: Retry or dead-letter on failure

### BUG-08 — HIGH — Rider Wallet Credit Lost on Transient Failure
- - File: `delivery_service.go:380-388`
- - Issue: If `CreditDelivery` fails, log says "Warning" but delivery is marked completed. No retry, no outbox, no reconciliation. Rider permanently loses earnings.
- - Impact: Rider loses delivery fee for that order
- - Fix NEEDED: Outbox pattern or reconciliation cron

### BUG-09 — HIGH — Orphaned COD Debts on Cancel
- - Issue: 9 COD debts (total 5,094 PKR) exist for cancelled/refunded orders. No cleanup on cancel.
- - Impact: Riders appear to owe money for orders that were cancelled
- - Fix NEEDED: Cancel/delete `cod_debts` when order is cancelled/refunded

### BUG-10 — HIGH — Ghost Orders (Delivered, No Delivery Record)
- - Issue: 8 orders in `delivered` state have NO delivery record and NO rider_tracking_id. Set to delivered via `MarkOrderDelivered` bypass.
- - Impact: Data inconsistency, cannot trace who delivered
- - Fix NEEDED: Fix BUG-05 to prevent this state

### BUG-11 — MEDIUM — ReleaseExpiredHolds Not Transactional
- - File: `escrow/service.go:117-141`
- - Issue: Ledger transfer, hold status update, escrow_released flag, and vendor wallet credit are 4 separate statements with no wrapping transaction. Crash after `escrow_released=TRUE` but before wallet credit = money stuck forever.
- - Fix NEEDED: Wrap in DB transaction

### BUG-12 — MEDIUM — OnCashCollected Duplicate on DB Error
- - File: `cod.go:68-86`
- - Issue: `existing, _ := s.repo.GetByOrderID(...)` — error discarded. Transient DB error → nil → duplicate transaction created.
- - Fix NEEDED: Check error, return if non-nil

---

## Bugs Found — Frontend (Flutter/Dart)

### BUG-13 — CRITICAL — Buy Now: Wrong Payment Field Name
- - File: `product_details_screen.dart:369`
- - Issue: Sends `'payment_method'` but checkout_screen sends `'payment_gateway'`. Backend reads one field; the other is silently ignored. Buy Now likely defaults to COD every time.
- - Fix NEEDED: Change `'payment_method'` to `'payment_gateway'`

### BUG-14 — CRITICAL — Buy Now: Missing Delivery Coordinates
- - File: `product_details_screen.dart:363-371`
- - Issue: Order payload missing `dropoff_lat` and `dropoff_lng`. All Buy Now orders have no delivery location.
- - Impact: Rider assignment and delivery routing impossible
- - Fix NEEDED: Collect GPS location before checkout

### BUG-15 — HIGH — Stripe Failure Doesn't Cancel Order
- - File: `checkout_screen.dart:196-207`
- - Issue: Snackbar says "order cancelled" but no cancel API call. Order stays pending. Inventory held indefinitely.
- - Fix NEEDED: Call cancel endpoint on Stripe failure

### BUG-16 — HIGH — COD Orders Stuck in Payment Poll
- - File: `checkout_screen.dart:287-306`
- - Issue: After COD order, polls for "paid" status. COD is `pending`, not `paid`. After 45s timeout: cart not cleared, user sees "Payment processing at bank" — wrong for COD.
- - Fix NEEDED: Skip poll for COD, clear cart, navigate to success

### BUG-17 — HIGH — "No Client Secret" Leaves Orphan Order
- - File: `checkout_screen.dart:209`
- - Issue: If Stripe endpoint returns no client_secret, exception thrown, order never cancelled. User retries → duplicate order.
- - Fix NEEDED: Cancel order before throwing

### BUG-18 — MEDIUM — Out-of-Stock Items Can Be Purchased
- - File: `product_details_screen.dart:52-55`
- - Issue: `_maxQuantity` returns 1 when stock is 0. User can buy 0-stock items.
- - Fix NEEDED: Return 0, disable Buy Now/Add to Cart

### BUG-19 — MEDIUM — Vendor Dashboard Active Gigs Undercount
- - File: `vendor_dashboard_screen.dart:104-106`
- - Issue: Counts only `shipped` status. Misses `accepted`, `in_transit`, `picked_up`.
- - Fix NEEDED: Count all active states

### BUG-20 — MEDIUM — API Client Crashes on Empty Response
- - File: `api_client.dart:263-269`
- - Issue: `jsonDecode('')` throws FormatException on HTTP 204 No Content
- - Fix NEEDED: Check `response.body.isEmpty` before decode

---

## Database Integrity Issues

### DB-01 — Duplicate Deliveries
- - 42 orders have multiple delivery records (up to 30 per order)
- - 556 no-rider + 30 has-rider = 586 total deliveries for ~84 orders
- - All duplicates are `cancelled` status (repeated broadcast on re-ship)

### DB-02 — Ghost Orders
- - 8 orders in `delivered` state with NO delivery record
- - All from vendor `VEND-864e800a`, no rider_tracking_id

### DB-03 — Orphaned COD Debts
- - 9 pending COD debts for cancelled/refunded orders
- - Total: 5,094 PKR owed by riders for non-existent deliveries

### DB-04 — Dispute Status Column
- - 90+ orders have `dispute_status = 'NONE'` but no matching dispute record
- - This is expected (NONE is default) — NOT a bug

### DB-05 — Vendor Payouts Table
- - 3 payouts: 1 approved (500), 1 rejected (200), 1 pending (1000)
- - No issues found

---

## Wallet & Escrow Final State

### Customer Wallet
- - Balance: 1,394.02 PKR (started at 1,993.02, spent 2×599 = 1,198, wallet deducted correctly)

### Vendor Wallet
- - Balance: 10,465.43 PKR
- - Lifetime: 10,503.45 PKR
- - Difference: 38.02 PKR (expected — admin commission on deliveries)

### Rider A Wallet
- - Balance: 1,258.75 PKR
- - Cash in Hand: 1,797.00 PKR (COD pending settlement)
- - Lifetime: 1,198.00 PKR (delivery fees earned)

### Rider B Wallet
- - Balance: 142.50 PKR
- - Cash in Hand: 0.00 PKR

### Escrow Holds (Active)
- - 3 held holds totaling 1,797.00 PKR
- - 2 released holds (940.25 PKR)
- - 15 paid_out holds (10,074.18 PKR)

### COD Debts
- - 17 pending (9,388 PKR total)
- - 1 settled (350 PKR)
- - **9 orphaned** (5,094 PKR) — need cleanup

---

## Action Items (Priority Order)

### IMMEDIATE (Fix Before Next Deploy)
- - [ ] Fix BUG-03: Add unique constraint or upsert for deliveries
- - [ ] Fix BUG-04: Transactional escrow release (FOR UPDATE SKIP LOCKED)
- - [ ] Fix BUG-05: MarkOrderDelivered state machine enforcement
- - [ ] Fix BUG-09: Clean up orphaned COD debts on cancel
- - [ ] Fix BUG-13: Flutter `payment_method` → `payment_gateway`
- - [ ] Fix BUG-14: Add delivery coordinates to Buy Now

### HIGH (Fix Within 1 Week)
- - [ ] Deploy BUG-01 (vendor cancel auth) to Railway
- - [ ] Deploy BUG-02 (dispute status sync) to Railway
- - [ ] Fix BUG-06: Freeze escrow on cancel/return
- - [ ] Fix BUG-07: Delivery cancel error handling
- - [ ] Fix BUG-08: Rider wallet credit reliability
- - [ ] Fix BUG-15-17: Stripe failure handling in Flutter
- - [ ] Fix BUG-16: COD payment poll skip

### MEDIUM (Fix Within 2 Weeks)
- - [ ] Fix BUG-11: Transactional escrow release
- - [ ] Fix BUG-12: OnCashCollected error handling
- - [ ] Fix BUG-18: Out-of-stock purchase prevention
- - [ ] Fix BUG-19: Vendor dashboard active gigs count
- - [ ] Fix BUG-20: API client empty response handling

### CLEANUP (Fix When Convenient)
- - [ ] Clean ghost orders (8 delivered orders with no delivery record)
- - [ ] Clean orphaned COD debts (9 records, 5,094 PKR)
- - [ ] Add database index: `orders(created_at DESC)` (already applied locally)
- - [ ] Fix Flutter Bug-09 (PayFast hosted redirect cleanup)
- - [ ] Fix Flutter Bug-11 (DI singleton in vendor dashboard)
- - [ ] Fix Flutter Bug-12 (picked_up status timeline step)
