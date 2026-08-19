# Session 21 — Execution Plan: Security, Debezium, Env Config, DB Optimization

> **Created:** July 13, 2026
> **Preceded by:** [[session_20_execution_log]]
> **Architecture:** [[OMNIGO_SuperApp_Architecture]]

---

## GOAL

Billion-dollar enterprise-grade hardening ke liye 5 tasks:

1. **Real JWT Signing** — Replace fake `jwt_token_session_` with HMAC-SHA256 signed JWTs
2. **CORS + Rate Limiting Middleware** — Production-grade API protection
3. **Debezium Connector Registration** — PostgreSQL CDC → Kafka pipeline config
4. **Environment Config Overhaul** — .env.example with all new vars, config.yaml alignment
5. **Advanced DB Optimization** — Composite indexes, connection pool tuning, partitioning strategy

**Deferred (pending credentials/infra):**
- JazzCash/EasyPaisa full merchant credentials
- Load testing (k6/locust for 9M users)

---

## EXECUTION SEQUENCE

| Step | Task | Time |
|------|------|------|
| 1 | Real JWT signing (Go auth-service) | 30 min |
| 2 | CORS middleware (shared package) | 15 min |
| 3 | Rate limiting middleware (Redis-backed) | 20 min |
| 4 | Wire CORS + rate limit into all service mains | 15 min |
| 5 | Debezium connector JSON config | 15 min |
| 6 | .env.example overhaul | 10 min |
| 7 | DB indexes + partitioning SQL | 20 min |
| 8 | Connection pool tuning verification | 10 min |
| 9 | Commit + push | 5 min |

**Total: ~2.5 hours**