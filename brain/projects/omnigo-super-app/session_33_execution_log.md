# Session 43 — Execution Log: PayFast Hardening, Flow Consolidation & De-Hardcoding

> **Date:** August 25, 2026
> **Preceded by:** [[OMNIGO_Project_Log]] (Session 42)
> **Scope:** PayFast PK gateway end-to-end audit → bug fixes → Buy Now / Checkout flow consolidation → zero-hardcoding pass

---

## 📋 Goal

Three-part effort on the PayFast payment pipeline:

1. **Full audit** of the Option C tokenized flow (`internal/payment/payfast/` + orchestrator service/handler) for bugs that could block or corrupt payments.
2. **Consolidate** the two divergent PayFast flows (legacy hosted-checkout on Buy Now vs Option C on cart checkout) onto a single production path.
3. **Remove every hardcoded** deployment value (URLs, dates, phones, timeouts) in favor of environment configuration.

All verified: `go build ./...`, `go vet`, full `-count=1` test suite green; `dart analyze` clean on touched files.

---

## 🔍 Audit Findings (what was broken)

| # | Severity | Finding | File |
|---|----------|---------|------|
| A | 🚨 CRITICAL | Orchestrator **boot-time panic**: `NewClientFromEnv()` panicked when `PAYFAST_BASE_URL` unset — even when PayFast wasn't configured at all. One missing env var killed the *entire* payment service (Stripe webhook, JazzCash/EasyPaisa, COD, card vault all dead). | `payfast/client.go:41` + `cmd/payment-orchestrator/main.go:132` |
| B | 🚨 CRITICAL | **Env var name mismatch**: repo `.env.example` documented `PAYFAST_API_URL`; code read only `PAYFAST_BASE_URL`. Following the repo's own templates guaranteed Bug A triggered → PayFast could never work on a template deploy. | docs vs `client.go` |
| C | 🚨 HIGH | Legacy `/wallet/payfast/charge` silently fell back to the **sandbox URL** when unset → production creds against sandbox = every payment silently fails. | `wallet/handler/payfast_charge.go` |
| D | 🚨 HIGH | Settlement ran on the **cancellable HTTP request context** after funds were already captured — client disconnect mid-settlement left paid orders stuck unsettled. | `payfast_service.go` |
| E | HIGH | Circuit breaker ignored **all 5xx** GatewayErrors (only 408/502/503/504 counted) → breaker never tripped on genuine gateway 500 outages. | `payfast/errors.go` |
| F | HIGH | Saved-card flow **ignored 3DS step-up challenges** (`Data3DSHTML`) → healthy payments misreported as failed when bank demanded verification. | `payfast_service.go` |
| G | MEDIUM | PAN/CVV/CNIC had live JSON tags (`json:"card_number,omitempty"`) on request structs → one accidental `json.Marshal` debug log away from cardholder-data leak. | `payfast/models.go` |
| H | MEDIUM | IPN handler returned **200 for transient failures** → PayFast never retried redeliveries; settlement signals lost. | `handlers/payfast_handler.go` |
| I | LOW | Idempotency key contained a fresh UUID per attempt → column UNIQUE existed but could never dedupe client retries. | `payfast_service.go` |
| J | LOW | `postMessage(..., '*')` wildcard origin on 3DS success page; dead `failureCount` breaker field; string-matching error→HTTP mapping. | handler/breaker |

---

## ✅ Part 1 — Backend Hardening

### Reliability
- **Detached settlement context**: `VerifyAndSettle()` and `ExecuteSplit()` now call `context.WithoutCancel(ctx)` at entry. Money already captured can never be aborted locally by an upstream disconnect. Values preserved, cancellation dropped.
- **Graceful degradation instead of panic** (`client.go`): construction NEVER panics. Creds without URL → loud ERROR log + `IsConfigured()==false`; other gateways stay alive. `IsConfigured()` now requires merchant ID **and** secured key **and** base URL.
- **Env alias**: client resolves `PAYFAST_BASE_URL` → fallback `PAYFAST_API_URL` (template compatibility). New tests: `TestNewClientGracefulDegradation`.
- **Circuit breaker sees real outages**: `IsTransient` treats ANY `GatewayError.StatusCode >= 500 || == 408` as transient. Matrix-tested in `TestIsTransientGatewayStatuses`.
- **Saved-card 3DS step-up**: `TokenizedTransactionResponse` gained 3DS fields (`data_3ds_html`, `otp_required`, …). When the issuer demands a challenge even on tokenized txns, the saved-card branch persists `3ds_required` state (instrument token + split metadata) and returns `3ds_redirect` — resuming through the exact same `Handle3DSCallback` machinery as new-card flow.

### Security
- **PAN/CVV/CNIC/OTP/instrument-token set to `json:"-"`** across request structs; API client uses form-encoding so zero wire impact. Serialization guard test added (`TestSensitiveRequestFieldsNotSerializable`).
- **postMessage origin lockdown**: new `PAYFAST_WEB_ORIGIN` env → fallback first `CORS_ALLOWED_ORIGINS` entry → `"*"` only as last-resort dev fallback with WARNING log.
- **hashKey misconfig alarm**: falling back securedKey→hashKey signing now logs an unmissable startup warning (PayFast issues these as two DIFFERENT secrets).

### API correctness
- **Idempotency-Key header support** end-to-end:
  - Handler injects standard `Idempotency-Key` header into the request.
  - Service resolves key → pre-insert replay lookup inside the order-lock transaction.
  - Same key + same order + non-terminal status → **stable replay response** (`idempotentReplayResponse`: `settlement_pending` / `in_progress` / `gateway_pending` mapping) instead of double charge.
  - Terminal prior attempt (failed/refunded) → derived per-attempt retry key so genuine retry-after-failure works.
  - Key reuse across different orders → hard conflict reject.
- **Typed sentinel errors** (`ErrValidation` / `ErrNotFound` / `ErrForbidden` / `ErrConflict`) wrapped at every classification point; handler maps via `errors.Is()` — string matching eliminated.
- **IPN retry semantics**: validation failures → `400`; unknown basket → `200 {"status":"ignored"}` (stops pointless redelivery); transient/timeout → **`503`** (PayFast retries); anything else → `500`.
- Dead `failureCount` field removed from CircuitBreaker.

---

## ✅ Part 2 — Flow Consolidation

### Shared widget extraction (Flutter)
- **NEW** `lib/features/customer/presentation/widgets/payfast_card_sheet.dart`:
  - `showPayFastCardDetailsSheet()` — card entry bottom sheet (MM/YY/CVV validators, digits-only formatters, autofill blocked).
  - `showPayFast3DSChallenge()` — in-app WebView ACS challenge modal with `context.mounted` async-gap guard.
  - `PayFastCardNumberFormatter` — "4111 1111 1111 1111" live formatting.
- `checkout_screen.dart` refactored onto the shared widgets; local copies deleted (~270 lines removed).

### Buy Now → Option C (`product_details_screen.dart`)
- The legacy hosted-checkout branch replaced with the orchestrator flow:
  - Card sheet → `POST /api/v1/payments/payfast/payment` with **`Idempotency-Key: buynow-$nonce`**.
  - Handles: `failed` → error dialog · `3ds_redirect` → shared 3DS modal · `gateway_pending`/`in_progress` → "processing at gateway" snackbar.
- Success lands on **OrderSuccessScreen** via `pushAndRemoveUntil` — identical post-payment UX as cart checkout.
- Dead `_showSuccessDialog` + `paymentMethod` variable removed.
- **Financial impact:** Buy Now orders now get fraud checks, `payment_transactions` audit rows, gateway status verification AND the admin/vendor/delivery **ledger split** — previously they were marked "paid" with NO split (silent ledger imbalance) and were invisible in the Admin PayFast Hub.

### Legacy endpoints — deprecated, NOT removed (old app versions keep working)
- `payfastDeprecationHeaders()` on both `PayFastCharge` + `PayFastCallback`:
  - `Deprecation: true`, `Sunset:` from `PAYFAST_LEGACY_SUNSET_DATE` (unset → rolling +1 year), `Link rel=successor-version`.
  - Per-call `[DEPRECATED]` log line (ip + UA) → removal date becomes data-driven once legacy traffic hits zero.
- Wallet top-up flow (`/wallet/customer/load`) untouched — legitimate separate use case.

---

## ✅ Part 3 — Zero-Hardcoding Pass

| Was hardcoded | Now |
|---|---|
| Railway prod URL fallbacks (×2 wallet handlers) | `PUBLIC_BASE_URL` / `WALLET_RETURN_URL` env → clean 500 if missing (no silent foreign domain) |
| Sandbox URL silent fallback on legacy charge | `503 PayFast is not configured` |
| Dummy phone `03000000000` sent to gateway | Optional `customer_mobile_no` request field — customer's real number or omitted entirely |
| Sunset date literal `31 Dec 2026` | `PAYFAST_LEGACY_SUNSET_DATE` env, rolling default |
| Timeout magic numbers 20s/25s (×4 sites) | `PAYFAST_GATEWAY_TIMEOUT_SECONDS` → `DefaultGatewayTimeout()`, `Client.Timeout()` accessor, `gatewayContext()` derives client-timeout+5s |
| Frontend magic `'account_type_id': '2'` | Omitted from both screens — orchestrator derives instrument type server-side |
| Unescaped `merchant_id` in hosted-checkout query string | `url.QueryEscape` applied |

Also fixed en route: `merchant_id` was previously injected unescaped into the hosted checkout redirect query.

---

## 🧪 Verification

```
go build ./...                          ✅ clean
go vet ./internal/... ./cmd/...         ✅ clean
go test -count=1 ./internal/...         ✅ ledger, payfast, payment,
                                           payment_orchestrator/{service,fraud},
                                           syncworker — ALL PASS
dart analyze (touched files)            ✅ No issues (2 pre-existing unrelated
                                           unused_field warnings remain by design)
Hardcode grep (railway.app / gopayfast /
dummy phone / literal dates)            ✅ zero matches in touched backend files
```

New/updated tests:
- `TestNewClientGracefulDegradation` — no-panic contract, IsConfigured truth table, env alias precedence
- `TestIsTransientGatewayStatuses` — full HTTP-status matrix
- `TestSensitiveRequestFieldsNotSerializable` — PAN/CVV/CNIC/OTP/token leak guards
- `TestIdempotentReplayResponse` + `TestClassificationSentinelsAreDistinct`

---

## ⚙️ New Environment Variables (all optional, sane defaults)

| Var | Purpose | Default |
|---|---|---|
| `PAYFAST_BASE_URL` | Canonical gateway endpoint (canonical name; `PAYFAST_API_URL` accepted as alias) | — required for PayFast |
| `PAYFAST_HASH_KEY` | Separate HMAC signing key issued by PayFast | falls back to SECURED_KEY + WARNING |
| `PAYFAST_WEB_ORIGIN` | Trusted origin for 3DS `postMessage` | first `CORS_ALLOWED_ORIGINS` entry |
| `PAYFAST_GATEWAY_TIMEOUT_SECONDS` | Per-call gateway HTTP timeout | `20` |
| `PAYFAST_LEGACY_SUNSET_DATE` | Advertised removal date on deprecated endpoints | rolling +1 year |

Both `.env.example` and `backend/go-services/.env.railway.example` updated.

---

## 📌 Deploy Checklist (Railway)

1. Set `PAYFAST_BASE_URL` (+ `PAYFAST_HASH_KEY`, merchant ID/key) — routes register only when valid.
2. Ensure `PUBLIC_BASE_URL` is publicly reachable HTTPS (3DS callbacks + IPN hit it).
3. `INTERNAL_CALLBACK_SECRET` must be set (service fail-fasts otherwise — by design).
4. Watch logs for `[DEPRECATED]` lines to decide when legacy endpoints can finally be removed.

---

## 🔭 Known Follow-Ups (deliberately deferred)

- **float64 → integer-paisa money refactor** across ledger/escrow/orders — high-risk cross-cutting migration, needs its own dedicated session.
- Monolith `strings.Contains` error mapping in other handlers (Stripe/COD) could adopt the same sentinel pattern used by PayFast.
- `_coBought` unused fields in `product_details_screen.dart` (pre-existing analyzer warnings, unrelated).
