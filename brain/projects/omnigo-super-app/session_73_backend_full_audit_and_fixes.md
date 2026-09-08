# Session 73 — Backend Full Audit & Fix Plan

> **Date:** September 8, 2026
> **Preceded by:** [[session_72_payfast_complete_integration_plan]] (Session 72 - PayFast Integration)
> **Scope:** Fix all broken endpoints so entire app returns 200/201

---

## Goal

Fix all broken backend endpoints:
1. **Admin Service 503** → Fix Redis init (25+ routes blocked)
2. **Chat Conversations 500** → Fix NULL scan safety
3. Verify entire app works end-to-end

---

## Audit Results — All Services

### Endpoint Health Check (Sept 8, 2026)

| # | Endpoint | Status | Notes |
|---|----------|--------|-------|
| 1 | POST /auth/login | ✅ 200 | Working |
| 2 | POST /auth/register | ✅ 201 | Working |
| 3 | GET /auth/profile | ✅ 200 | Working |
| 4 | GET /products | ✅ 200 | Empty (no products yet) |
| 5 | GET /stores | ✅ 200 | Empty (no stores yet) |
| 6 | GET /wallet/customer/:id | ✅ 200 | Working |
| 7 | GET /cart | ✅ 200 | Working |
| 8 | GET /orders/customer/:id | ✅ 200 | Empty (no orders yet) |
| 9 | GET /wishlist/ | ✅ 200 | Working |
| 10 | GET /payments/cards | ✅ 200 | Working |
| 11 | POST /ride/estimate | ✅ 400 | Route exists (validation error) |
| 12 | POST /delivery/estimate-fee | ✅ 400 | Route exists (validation error) |
| 13 | GET /vendor/metrics | ✅ 200 | Working |
| 14 | **GET /chat/conversations** | **❌ 500** | NULL scan issue |
| 15 | **GET /admin/orders** | **❌ 503** | Redis URL rejection |
| 16 | **GET /admin/users** | **❌ 503** | Redis URL rejection |
| 17 | **GET /admin/analytics/overview** | **❌ 503** | Redis URL rejection |
| 18 | **GET /admin/finance/ledger-kpis** | **❌ 503** | Redis URL rejection |
| 19 | Vendor products | 401 | Expected (admin user) |
| 20 | Vendor stores/me | 404 | Expected (no store created) |

---

## Service Architecture Audit

### Monolith → Child Process Model

The monolith (`cmd/monolith/main.go`) spawns child processes:
- auth-service (port 9000) ✅
- product-service (port 9001) ✅
- vendor-store-service (port 9002) ✅
- delivery-gig-service (port 9003) ✅
- ride-service (port 9004) ✅
- order-service (port 9005) ✅
- payment-orchestrator (port 9006) ✅
- admin-service (port 9007) ❌ BROKEN (Redis)
- websocket-gateway (port 9008) ⚠️ Minor (no Ping check)
- map-service (port 9010) ✅

### Redis Init Pattern Comparison

| Service | Pattern | URL Support | Status |
|---------|---------|-------------|--------|
| auth-service | `cache.NewRedisClient()` | Yes | ✅ GOOD |
| product-service | `cache.NewRedisClient()` | Yes | ✅ GOOD |
| order-service | `cache.NewRedisClient()` | Yes | ✅ GOOD |
| **admin-service** | **Custom `redis.NewClusterClient`** | **No — URLs rejected** | **❌ BROKEN** |
| vendor-store-service | `cache.NewRedisClient()` | Yes | ✅ GOOD |
| delivery-gig-service | `cache.NewRedisClient()` | Yes | ✅ GOOD |
| ride-service | `cache.NewRedisClient()` | Yes | ✅ GOOD |
| payment-orchestrator | `cache.NewRedisClient()` | Yes | ✅ GOOD |
| websocket-gateway | Custom `redis.NewClient` | Yes (no Ping) | ⚠️ MINOR |
| map-service | None | N/A | ✅ OK |

---

## Fix 1: Admin Service 503 (25+ routes)

### Root Cause

`cmd/admin-service/main.go:175` — custom Redis init code rejects `rediss://` URL format:

```go
// BROKEN: This rejects Railway/Upstash Redis URLs
if redisAddr != "" && !strings.HasPrefix(redisAddr, "redis://") && !strings.HasPrefix(redisAddr, "rediss://") {
    rdb = redis.NewClusterClient(...)
} else {
    rdb = nil  // <-- Always happens on Railway
}
```

Railway injects `REDIS_URL=rediss://default:pass@host:port` → code enters `else` branch → `rdb = nil` → rate limiter middleware (`ratelimit.go:30-37`) sees `nil` → returns **HTTP 503 for EVERY admin request**.

### Exact Changes

**File:** `backend/go-services/cmd/admin-service/main.go`

| # | Lines | Current | New |
|---|-------|---------|-----|
| 1 | imports (after line 28) | (missing cache import) | Add `"github.com/omnigo/backend/internal/shared/cache"` |
| 2 | 165-186 | 22-line custom Redis block | 6-line `cache.NewRedisClient(ctx, []string{redisAddr})` |
| 3 | 206 | `middleware.RateLimit(rdb, 100, ...)` | Guard nil: `var rdbVal redis.UniversalClient; if redisClient != nil { rdbVal = redisClient.Client }; middleware.RateLimit(rdbVal, 100, ...)` |
| 4 | 1265 | Same as above | Same guard |

### Endpoints Fixed (25+)

- `GET /admin/orders`
- `GET /admin/users`
- `GET /admin/users/pending`
- `PATCH /admin/users/:id/approve`
- `GET /admin/analytics/overview`
- `GET /admin/analytics/heatmap/deliveries`
- `GET /admin/analytics/heatmap/vendors`
- `GET /admin/analytics/demand-heatmap`
- `GET /admin/finance/ledger-kpis`
- `GET /admin/finance/daily-revenue`
- `GET /admin/finance/payments`
- `GET /admin/finance/payfast/summary`
- `GET /admin/finance/payfast/transactions`
- `GET /admin/finance/vendor-payouts`
- `GET /admin/finance/stripe-events`
- `GET /admin/verifications/pending`
- `GET /admin/wallet/overview`
- `GET /admin/riders/:id/gps-trail`
- `GET /admin/payments/cards/:id`
- `GET /admin/rider/cod-collection`
- `GET /admin/lineage/:order_id`
- `GET /admin/lineage/:order_id/full`
- `GET /admin/ai/audit-overview`
- `GET /admin/disputes`
- `GET /api/v1/geo/reverse`

---

## Fix 2: Chat Conversations 500

### Root Cause

`chat_repository.go:266` — SQL `LEFT JOIN users` returns NULL for `u.role` when other user doesn't exist in users table. Go `string` field cannot accept NULL → pgx scan error → handler returns generic "failed to list conversations".

### Exact Changes

**File:** `backend/go-services/internal/chat/repository/chat_repository.go`

| # | Lines | Current | New |
|---|-------|---------|-----|
| 1 | 241-242 | `u.role AS other_user_role, u.full_name AS other_user_name` | `COALESCE(u.role, 'unknown') AS other_user_role, COALESCE(u.full_name, 'Unknown') AS other_user_name` |
| 2 | 266 | `rows.Scan(..., &c.OtherUserRole, ...)` | Scan into `*string` pointer, then assign |

**File:** `backend/go-services/internal/chat/handlers/chat_handler.go`

| # | Lines | Current | New |
|---|-------|---------|-----|
| 3 | 177 | `c.JSON(500, ...)` | Add `log.Printf("[CHAT] ListConversations error for user %s: %v", userID, err)` before JSON |

**File:** `backend/go-services/internal/chat/models/chat.go`

| # | Lines | Current | New |
|---|-------|---------|-----|
| 4 | 36 | `OtherUserRole string` | `OtherUserRole *string` (pointer for NULL safety) |

### Endpoint Fixed

- `GET /api/v1/chat/conversations` → 200

---

## Additional Issues Found (Low Priority)

| # | Service | Issue | Severity | Action |
|---|---------|-------|----------|--------|
| 1 | product-service | gRPC port 50052 hardcoded, `log.Fatalf` if busy | Low | Log and continue |
| 2 | order-service | `log.Fatalf` on gRPC dial to product-service | Low | Add retry/backoff |
| 3 | websocket-gateway | Custom Redis init without Ping check | Low | Works but could fail silently |

---

## Implementation Order

1. Write Obsidian session doc (this file)
2. Fix admin-service Redis init (`cmd/admin-service/main.go`)
3. Fix chat NULL safety (`chat_repository.go` + `chat_handler.go` + `chat_models.go`)
4. Build and verify (`go build` all services)
5. Push to GitHub → Railway auto-deploys
6. Test all endpoints → all should return 200/201

---

## Summary

| Fix | File(s) | Endpoints Fixed |
|-----|---------|----------------|
| Admin Redis URL | `cmd/admin-service/main.go` | 25+ admin routes |
| Chat NULL safety | `chat_repository.go`, `chat_handler.go`, `chat.go` | Chat conversations |
| **Total** | **4 files** | **~27 endpoints** |
