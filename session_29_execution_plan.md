# Session 29 — Execution Plan: Admin Security + Rider KYC/Earnings + Rust Gateway Hardening

> **Created:** July 14, 2026
> **Preceded by:** [[session_28_execution_log]]
> **Architecture:** [[OMNIGO_SuperApp_Architecture_V2]]

---

## 🎯 Goal

Advance the OMNIGO super app by fixing the highest-risk gaps found in Session 28:
1. Secure admin endpoints with JWT + role-based access.
2. Add pagination and full entity tracking linkage to the admin panel.
3. Enable rider KYC document upload (CNIC/license) at signup and admin review.
4. Build rider earnings/wallet backend + Flutter screen.
5. Harden the Rust WebSocket gateway with connection auth and basic rate limits.

---

## 📋 Tasks

### Phase 1: Admin Security Hardening
- [ ] Add reusable `AdminRequired()` Gin middleware in `backend/go-services/internal/shared/middleware` that validates JWT and checks `role == 'admin'`.
- [ ] Apply the middleware to all `/api/admin/*` routes in `cmd/admin-service/main.go`.
- [ ] Update Flutter `admin_surveillance_screen.dart` to send `Authorization: Bearer <jwt_token>` on every admin API call.
- [ ] Verify: `curl` to admin endpoint without token returns 401; with admin token returns 200.

### Phase 2: Admin Pagination + Full Tracking Linkage
- [ ] Add `limit`/`offset` query params to `/api/admin/users` and `/api/admin/users/pending`.
- [ ] Add new endpoint `/api/admin/lineage/:order_id/full` returning order + items (product tracking IDs) + delivery gig + ride + timeline events.
- [ ] Add admin UI tab "Full Lineage" that expands the order card to show product IDs, gig ID, ride ID, and status history.
- [ ] Verify: admin UI loads paginated user list and full lineage card without errors.

### Phase 3: Rider KYC Document Upload
- [ ] Add `cnic_url` and `license_url` update to `auth_service.go` signup/profile flow for riders/vendors.
- [ ] Add `PUT /api/v1/auth/kyc` endpoint accepting multipart CNIC + license images, saving to local `./uploads/kyc/`.
- [ ] Expose `GET /api/admin/users/:tracking_id/kyc` returning document URLs for admin review.
- [ ] Add KYC document viewer in admin pending-approval card.
- [ ] Verify: rider uploads documents → admin sees them → approves user.

### Phase 4: Rider Earnings / Wallet
- [ ] Add `rider_wallet` table to `infrastructure/postgres/init.sql` with `rider_tracking_id`, `balance`, `lifetime_earnings`, `updated_at`.
- [ ] Add `internal/wallet` service endpoint `GET /api/v1/wallet/rider/:tracking_id` (self or admin).
- [ ] On delivery completion, credit rider wallet: delivery fee minus admin commission.
- [ ] Add Flutter `rider_wallet_screen.dart` showing balance, lifetime earnings, recent credits.
- [ ] Verify: complete a delivery → wallet balance updates → Flutter screen shows it.

### Phase 5: Rust WebSocket Gateway Hardening
- [ ] Read current `backend/rust-services/websocket-gateway/src/main.rs` and connection logic.
- [ ] Add JWT token validation on WebSocket upgrade query param `?token=<jwt>`.
- [ ] Reject unauthenticated connections with `401`.
- [ ] Add per-IP connection rate limiter (e.g., max 10 connections per minute).
- [ ] Verify: `cargo check` passes; unauthenticated WS connection is rejected.

### Phase 6: Final Verification
- [ ] Run `go build ./...` in `backend/go-services`.
- [ ] Run `flutter analyze lib/` in `frontend/omnigo_app`.
- [ ] Run `cargo check` in `backend/rust-services/websocket-gateway`.
- [ ] Run basic security sanity checks: admin endpoint 401 without token, KYC path traversal not possible, wallet balance never negative.
- [ ] Update Obsidian `session_29_execution_log.md` with results and any blockers.

---

## 🛡️ Security Rules for This Session

- **No plaintext secrets** in code or logs.
- **Path traversal protection** on all file uploads (use `filepath.Base` + UUID).
- **JWT secret from env** only; fail startup if missing.
- **Admin endpoints reject non-admin tokens** hard — no fallback.
- **Wallet writes happen only inside DB transaction**; no negative balance.
- **Rate limits** on gateway to prevent connection exhaustion.

---

## 📁 Expected New/Modified Files

| # | File | Action |
|---|------|--------|
| 1 | `backend/go-services/internal/shared/middleware/admin_auth.go` | NEW |
| 2 | `backend/go-services/cmd/admin-service/main.go` | MODIFY — wire middleware + pagination + full lineage |
| 3 | `backend/go-services/internal/admin/service.go` | MODIFY — pagination, full lineage, KYC fetch |
| 4 | `backend/go-services/internal/auth/handlers/auth_handler.go` | MODIFY — add KYC upload endpoint |
| 5 | `backend/go-services/internal/auth/service/auth_service.go` | MODIFY — save KYC URLs |
| 6 | `backend/go-services/internal/wallet/handler/wallet_handler.go` | MODIFY/NEW — rider wallet endpoints |
| 7 | `backend/go-services/internal/wallet/service/wallet_service.go` | MODIFY/NEW — credit logic |
| 8 | `infrastructure/postgres/init.sql` | MODIFY — add `rider_wallet` table |
| 9 | `frontend/omnigo_app/lib/features/admin/presentation/screens/admin_surveillance_screen.dart` | MODIFY — auth headers, pagination, full lineage, KYC viewer |
| 10 | `frontend/omnigo_app/lib/features/auth/presentation/screens/dynamic_signup_screen.dart` | MODIFY — KYC upload for riders/vendors |
| 11 | `frontend/omnigo_app/lib/features/rider/presentation/screens/rider_wallet_screen.dart` | NEW |
| 12 | `frontend/omnigo_app/lib/features/rider/presentation/screens/rider_map_screen.dart` | MODIFY — add wallet navigation button |
| 13 | `backend/rust-services/websocket-gateway/src/main.rs` | MODIFY — JWT auth + rate limit |
| 14 | `backend/rust-services/websocket-gateway/Cargo.toml` | MAYBE — add deps (jwt, rate-limiter) |

---

## ✅ Done When

- [ ] `go build ./...` passes.
- [ ] `flutter analyze lib/` reports no issues.
- [ ] `cargo check` passes.
- [ ] Admin endpoints require admin JWT.
- [ ] Admin UI shows paginated users + full order lineage.
- [ ] Rider can upload CNIC/license; admin can view and approve.
- [ ] Rider wallet updates on completed delivery and displays in Flutter.
- [ ] Rust gateway rejects unauthenticated WebSocket connections.
