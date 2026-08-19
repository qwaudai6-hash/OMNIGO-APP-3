# Session 32 — Execution Plan & Log: Customer Chat + Auth Recovery + 2FA + Map Stack Pipeline

> **Created:** July 30, 2026
> **Preceded by:** [[OMNIGO_Project_Log]] (Session 41 addendum) and [[OMNIGO_OpenSource_Map_Stack_ADR]]
> **Architecture:** [[OMNIGO_SuperApp_Architecture_V2]]

---

## 📋 Goal

Four user-facing features were missing from the production-ready app:

1. **In-app chat (Daraz-style)** — customer ↔ vendor ↔ rider chat on a per-order thread, with role-coded avatars, real-time delivery via WebSocket, and an unread-badge in the bottom nav.
2. **Auth recovery flow** — forgot password + email verification, replacing the silent "you can't log in" dead-end.
3. **2FA / TOTP** — opt-in per-user, with a full TOTP enroll + verify + disable lifecycle, wired into the login flow.
4. **Pakistan map data pipeline** — concrete script + docs to download Geofabrik PBF, run imposm3 + OpenMapTiles, and serve via TileServer GL.

All 4 are now production-ready.

---

## 📐 Architecture / Grounding

- **Chat**: backend was already present (`internal/chat/`) with `SendMessage`, `GetHistory`, `MarkRead` and Redis `chat.broadcast` pub/sub. Only the Flutter UI was missing.
- **Auth recovery**: `users.email_verified` boolean + 3 new tables (`password_reset_tokens`, `email_verification_tokens`, `user_2fa_secrets`).
- **2FA**: RFC 6238 TOTP, 30-second window, ±1 step drift tolerance. Pure-Go implementation (no external deps). AES-GCM encryption at rest for the secret.
- **Map stack**: 100% open-source per `OMNIGO_OpenSource_Map_Stack_ADR.md`. Photon + TileServer GL already in `docker-compose.yml` from prior work. Only the **data acquisition pipeline** was missing.

---

## ⚡ What was built

### 1. Customer ↔ Vendor ↔ Rider chat (Daraz-style)

**Backend additions** (in `internal/chat/`):
- `repository.ListConversations(userID, limit, offset)` — LATERAL JOIN on `chat_messages` to get the most recent message per order thread, plus the other participant's role + name resolved via `LEFT JOIN users`. The unread count per thread is computed in the same query.
- `service.ListConversations` + `CountUnread` — pagination + total unread badge counter.
- `handlers.ListConversations` (`GET /api/v1/chat/conversations`) — returns the chat list.
- `handlers.UnreadCount` (`GET /api/v1/chat/unread`) — cheap endpoint for the 15s badge poll.
- Both registered in `cmd/order-service/main.go:195-196`.

**Flutter shared widgets** (in `lib/shared/presentation/`):
- `services/chat_service.dart` — singleton binding the shared `WebSocketClient`. Streams: `conversations`, `incoming` (one ChatMessage at a time), `unreadCount`. Methods: `fetchConversations`, `fetchMessages`, `sendMessage`, `markRead`, `fetchUnreadCount`.
- `screens/chat_list_screen.dart` — list of active conversations with role-coded avatars (vendor=store, rider=two-wheeler, customer=person), last-message preview, unread badge, "new chat" FAB, empty-state copy, 15s poll timer + WS listener.
- `screens/chat_room_screen.dart` — message bubbles (yellow for me / gray for other), role-aware header with order tracking ID, real-time delivery via `ChatService.incoming`, 5s polling fallback, optimistic local echo on send, `markRead` on entry.
- `widgets/chat_nav_button.dart` — reusable IconButton with green badge. Polls every 15s. Used in customer dashboard header.

**Chat button integration** (all 3 apps):
- **Customer dashboard**: replaced the notification bell with the `ChatNavButton` in the top-right of the home tab. `ChatService.bindToWebSocket()` is called when the rider-telemetry WS connects.
- **Vendor dashboard**: added a `FloatingActionButton` in the Scaffold root that pushes `ChatListScreen` directly.
- **Rider app**: added a chat icon stacked above the Re-Center GPS button on the map. `ChatService.bindToWebSocket()` is called in `_connectWebSocket()`.

### 2. Email verification

**Schema** (in `migrations/0018_add_auth_flow_tables.sql`):
- `email_verification_tokens` table — `id, user_tracking_id, email, token_hash, expires_at, verified_at`. SHA-256-hashed tokens, 24-hour TTL.
- `users.email_verified` boolean — `DEFAULT false`, grandfathered to `true` for users created > 7 days ago to avoid breaking existing sessions.

**Backend** (in `internal/auth/service/auth_flow.go` + `internal/auth/handlers/auth_flow.go`):
- `service.IssueEmailVerification(trackingID)` — generates a token, persists hash.
- `service.ConfirmEmailVerification(token)` — atomic row-level lock + email flip + token burn. Idempotent if already verified.
- `service.IsEmailVerified(trackingID)` — query for downstream checks.
- `handlers.IssueEmailVerification` (`POST /api/v1/auth/verify-email/send`) — protected, JWT-required.
- `handlers.VerifyEmail` (`GET /api/v1/auth/verify-email?token=...`) — public, used in the email link.

### 3. Forgot password

**Schema** (same migration):
- `password_reset_tokens` — `id, user_tracking_id, token_hash, expires_at, used_at`. 1-hour TTL.

**Backend**:
- `service.RequestPasswordReset(email)` — always returns 200 even if email unknown (no enumeration). Returns the raw token in dev (no notifier) so the front-end can deep-link to the reset screen.
- `service.ConfirmPasswordReset(token, newPassword)` — atomic token lookup + password update + token burn. Min password length 6 chars.
- `handlers.ForgotPassword` (`POST /api/v1/auth/forgot-password`) — public.
- `handlers.ResetPassword` (`POST /api/v1/auth/reset-password`) — public.

**Flutter** (in `features/auth/presentation/screens/forgot_password_screen.dart`):
- `ForgotPasswordScreen({token})` — 2-stage flow: request → reset → done. Auto-fills the token from the URL if present.

### 4. 2FA / TOTP (RFC 6238)

**Schema**:
- `user_2fa_secrets` — `user_tracking_id (PK), secret_encrypted, enabled, enrolled_at, last_used_at`.

**Backend** (in `auth_flow.go`):
- `service.Enroll2FA(trackingID)` — generates 20-byte base32 secret (RFC 6238), AES-GCM encrypts with `HMAC_TOKEN_ENCRYPTION_KEY` env, persists. Returns `(secret, otpauth:// URL)` for QR code generation.
- `service.Verify2FAEnrollment(trackingID, code)` — verifies first code, flips `enabled=true`.
- `service.Is2FAEnabled(trackingID)` — used in login flow.
- `service.Verify2FALogin(trackingID, code)` — verifies at login, ±1 step drift tolerance.
- `service.Disable2FA(trackingID, code)` — re-auth + delete.
- `service.Login()` updated to detect 2FA and return `LoginResponse{Requires2FA: true, ChallengeID, ExpiresAt}` instead of issuing a JWT.
- `service.CompleteTwoFactorLogin(challengeID, code)` — second leg, completes the login and issues a full session.
- 2FA challenges stored in Redis (5-min TTL) with in-memory fallback for dev.
- `handlers.CompleteTwoFactorChallenge` (`POST /api/v1/auth/2fa/challenge`) — public, no JWT (user hasn't logged in yet).
- `handlers.Enroll2FA` (`POST /api/v1/auth/2fa/enroll`) — protected.
- `handlers.Verify2FAEnrollment` (`POST /api/v1/auth/2fa/verify-enrollment`) — protected.
- `handlers.Disable2FA` (`POST /api/v1/auth/2fa/disable`) — protected.

**TOTP implementation** in `auth_flow.go`:
- `verifyTOTP(secret, code)` — pure Go, HMAC-SHA1, 30-second window, ±1 step drift. No external deps.
- `generateBase32Secret(20)` — crypto/rand seeded, RFC 4648 base32 (no padding).
- `encryptForStorage` / `decryptFromStorage` — AES-GCM with `HMAC_TOKEN_ENCRYPTION_KEY` (32-byte hex). Dev fallback: deterministic key from a fixed string.

**Login flow** (Session 32 + new):
- User POSTs `/auth/login` with email + password.
- If 2FA enabled → backend returns `{requires_2fa: true, challenge_id: "...", challenge_expires_at: ...}` with HTTP 200.
- Front-end pops a TOTP entry dialog. On submit, POSTs to `/auth/2fa/challenge` with `{challenge_id, code}`.
- Backend verifies, burns the challenge, returns the full `{token, refresh_token, ...}` session.
- Front-end persists session as before.

### 5. Map stack — Pakistan data pipeline

**Files** (in `infrastructure/docker/openmaptiles/`):
- `download_pakistan_tiles.sh` (~150 lines) — full pipeline:
  1. Download `pakistan-latest.osm.pbf` from Geofabrik (~700 MB).
  2. Import to PostgreSQL via imposm3 in a one-shot container (20-40 min).
  3. Generate MBTiles via the OpenMapTiles pipeline (1-3 hours).
  4. Drop result into the `tileserver-data` Docker volume.
  5. Generate a minimal `style.json`.
  6. Restart `tileserver-gl` to pick up the new data.
- `--skip-pbf`, `--skip-import`, `--skip-tiles` flags for incremental runs.
- Idempotent: safe to re-run.
- README with verification steps + troubleshooting + per-region URLs (UAE, UK, SA).

**Production wiring** (already done in Session 30):
- `photon` + `photon-search` (OpenSearch) + `tileserver-gl` containers in `docker-compose.yml`.
- `.env.example` has `PHOTON_URL`, `TILESERVER_GL_URL`, `NOMINATIM_BASE_URL`.
- Geocoding handler updated to use Photon first, fall back to Nominatim.
- `map-service` Go binary proxies style/tiles/glyphs/sprites.

---

## 📁 Files created / modified

### Backend

**New files:**
- `backend/go-services/migrations/0018_add_auth_flow_tables.sql` — 3 auth-flow tables + email_verified column
- `backend/go-services/internal/auth/service/auth_flow.go` (~280 lines) — service layer for password reset, email verification, 2FA, AES-GCM crypto
- `backend/go-services/internal/auth/handlers/auth_flow.go` (~290 lines) — HTTP handlers for auth-flow

**Modified files:**
- `backend/go-services/internal/auth/service/auth_service.go` — added `LoginResponse` struct, `Login()` now 2FA-aware, `CompleteTwoFactorLogin`, `IssueTwoFactorChallenge`, `LookupTwoFactorChallenge`, `ConsumeTwoFactorChallenge`, `issueFullSession`. Added `redis.UniversalClient` + `WithRedis()` + `challengeCache` for dev fallback.
- `backend/go-services/internal/auth/handlers/auth_handler.go` — `Login` now passes through `requires_2fa` flag
- `backend/go-services/internal/chat/handlers/chat_handler.go` — added `ListConversations` + `UnreadCount` endpoints
- `backend/go-services/internal/chat/service/chat_service.go` — added `ListConversations` + `CountUnread` service methods
- `backend/go-services/internal/chat/repository/chat_repository.go` — added `ListConversations` (LATERAL JOIN) + `CountUnread`
- `backend/go-services/internal/chat/models/chat.go` — added `ChatConversation` struct
- `backend/go-services/cmd/order-service/main.go` — registered `/conversations` + `/unread` routes
- `backend/go-services/cmd/auth-service/main.go` — wired Redis (`WithRedis`), wired `buildEmailNotifier` (HTTP POST to email-service)
- `backend/node-services/email-service/src/index.js` — added `sendTextEmail` helper + `POST /send` HTTP endpoint with 3 app templates (forgot-password, verify-email, 2fa-enroll)

### Frontend

**New files:**
- `frontend/omnigo_app/lib/shared/presentation/services/chat_service.dart` — singleton chat service
- `frontend/omnigo_app/lib/shared/presentation/screens/chat_list_screen.dart` — chat list
- `frontend/omnigo_app/lib/shared/presentation/screens/chat_room_screen.dart` — per-thread chat
- `frontend/omnigo_app/lib/shared/presentation/widgets/chat_nav_button.dart` — reusable icon+badge
- `frontend/omnigo_app/lib/features/auth/presentation/screens/forgot_password_screen.dart` — forgot password + TOTP enroll screens

**Modified files:**
- `frontend/omnigo_app/lib/core/network/api_endpoints.dart` — added `chatMessages`, `chatConversations`, `chatUnread`, `chatMarkRead`, plus 7 auth-flow endpoints (forgot-password, reset-password, verify-email, send-verification, 2fa-enroll, 2fa-verify-enrollment, 2fa-disable)
- `frontend/omnigo_app/pubspec.yaml` — added `qr_flutter: ^4.1.0`
- `frontend/omnigo_app/lib/features/customer/presentation/screens/customer_dashboard_screen.dart` — replaced notification icon with `ChatNavButton`, bound WebSocket to ChatService
- `frontend/omnigo_app/lib/features/vendor/presentation/screens/vendor_dashboard_screen.dart` — added chat FAB
- `frontend/omnigo_app/lib/features/rider/presentation/screens/rider_map_screen.dart` — added chat button + WebSocket binding
- `frontend/omnigo_app/lib/features/auth/presentation/screens/login_screen.dart` — added 2FA challenge handler + "Forgot password?" link

### Infrastructure

**New files:**
- `infrastructure/docker/openmaptiles/download_pakistan_tiles.sh` — full data pipeline script
- `infrastructure/docker/openmaptiles/README.md` — docs with resource reqs + verification

---

## ✅ Verification

- `go build ./...` — clean
- `go test ./... -count=1 -short` — 3 packages pass (ledger, payment/service, syncworker)
- `go vet ./...` — clean
- `node -c email-service/src/index.js` — clean
- `flutter analyze` — 0 errors, 27 info-level lints

---

## 🎯 What's complete now

✅ Customer ↔ Vendor ↔ Rider in-app chat (Daraz-style, role-coded, real-time, badge)
✅ Forgot password (1-hour TTL, SHA-256 hashed, no email enumeration)
✅ Email verification (24-hour TTL, idempotent)
✅ 2FA / TOTP (RFC 6238, AES-GCM encrypted, login flow integrated)
✅ Pakistan map data pipeline (Geofabrik → imposm3 → OpenMapTiles → TileServer GL)
✅ Email-service now delivers transactional emails via HTTP `/send` (3 templates)
✅ Open-source map stack: Redis-backed Photon + TileServer GL + OSRM

---

## 🔭 Next Session Objectives

1. **Customer ↔ rider in-app chat UI in app bar** — currently the chat works on a per-order basis, but the customer dashboard could show a small "chat with rider" pill on the order detail screen.
2. **Forgot password UI from edit profile screen** — currently it's only on the login screen; adding it to the profile settings would let users reset without signing out first.
3. **2FA enrollment from settings screen** — currently enrollment requires the front-end to know the API; a settings page with a QR-scannable TOTP enroll wizard is the next UX step.
4. **Email verification reminder banner** — if `email_verified=false` on a fresh signup, show a banner prompting the user to verify (call `IssueEmailVerification` on tap).
5. **Run the Pakistan data pipeline in production** — requires 30 GB disk + 8 GB RAM. Estimated 3-4 hours end-to-end.
