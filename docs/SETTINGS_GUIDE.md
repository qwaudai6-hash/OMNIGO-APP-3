# OMNIGO — Environment & App Settings Guide

## Overview — 3 jagah values set karni hain:

```
1. Railway (backend)     → service env vars (production secrets)
2. .env file (local dev) → local development values
3. Flutter app           → --dart-define flags (API_HOST)
```

---

## 1. RAILWAY (Production Backend) — Environment Variables

Railway pe har service ke liye ye env vars set karo:

### Gateway Service (PUBLIC — only this is exposed)
```
PORT=8000
APP_ENV=production
CORS_ALLOWED_ORIGINS=https://omnigo-app-3-production.up.railway.app
PUBLIC_BASE_URL=https://omnigo-app-3-production.up.railway.app
WALLET_RETURN_URL=https://omnigo-app-3-production.up.railway.app/api/v1/wallet/callback
REDIS_ADDRS=<railway-redis-private-url:6379>
```

### Auth Service (PRIVATE)
```
PORT=8080
DATABASE_URL=<railway-postgres-private-url>
DB_READER_DSN=<railway-postgres-private-url>  (same as writer if no replica)
REDIS_ADDRS=<railway-redis-private-url:6379>
KAFKA_BROKERS=<railway-kafka-private-url:9092>
JWT_SECRET_KEY=<generate with: openssl rand -base64 64>
JWT_ISSUER=omnigo-platform
APP_ENV=production
```

### Product Service (PRIVATE)
```
PORT=8082
DATABASE_URL=<railway-postgres-private-url>
DB_READER_DSN=<railway-postgres-private-url>
REDIS_ADDRS=<railway-redis-private-url:6379>
KAFKA_BROKERS=<railway-kafka-private-url:9092>
AI_ENGINE_URL=http://ai-engine.railway.internal:8086
MEILISEARCH_URL=<railway-meili-private-url>
APP_ENV=production
```

### Order Service (PRIVATE)
```
PORT=8088
DATABASE_URL=<railway-postgres-private-url>
DB_READER_DSN=<railway-postgres-private-url>
REDIS_ADDRS=<railway-redis-private-url:6379>
KAFKA_BROKERS=<railway-kafka-private-url:9092>
PRODUCT_SERVICE_URL=http://product-service.railway.internal:8082
APP_ENV=production
```

### Payment Orchestrator (PRIVATE)
```
PORT=8092
DATABASE_URL=<railway-postgres-private-url>
DB_READER_DSN=<railway-postgres-private-url>
REDIS_ADDRS=<railway-redis-private-url:6379>
KAFKA_BROKERS=<railway-kafka-private-url:9092>
PAYFAST_MERCHANT_ID=<your-payfast-id>
PAYFAST_SECURED_KEY=<your-payfast-key>
PAYFAST_API_URL=https://www.gopayfast.com
JAZZCASH_SALT=<your-jazzcash-salt>
EASYPAISA_SALT=<your-easypaisa-salt>
TB_ENABLED=false
APP_ENV=production
```

### Admin Service (PRIVATE)
```
PORT=8091
DATABASE_URL=<railway-postgres-private-url>
REDIS_ADDRS=<railway-redis-private-url:6379>
KAFKA_BROKERS=<railway-kafka-private-url:9092>
NEO4J_URI=<railway-neo4j-private-url>
NEO4J_USER=neo4j
NEO4J_PASSWORD=<your-neo4j-password>
AI_ENGINE_URL=http://ai-engine.railway.internal:8086
CLICKHOUSE_ADDR=<railway-clickhouse-private-url:9000>
ADMIN_API_KEY_ENCRYPTION_KEY=<openssl rand -base64 32>
APP_ENV=production
```

### AI Engine (PRIVATE — Python)
```
PORT=8086
DATABASE_URL=<railway-postgres-private-url>
DB_SSL=require
```

### WebSocket Gateway (PRIVATE)
```
PORT=8087
REDIS_ADDRS=<railway-redis-private-url:6379>
KAFKA_BROKERS=<railway-kafka-private-url:9092>
```

### Notification Service (PRIVATE — Node)
```
PORT=8089
REDIS_ADDRS=<railway-redis-private-url:6379>
KAFKA_BROKERS=<railway-kafka-private-url:9092>
FIREBASE_PROJECT_ID=<your-firebase-project>
FIREBASE_PRIVATE_KEY=<your-firebase-key>
FIREBASE_CLIENT_EMAIL=<your-firebase-email>
```

### Email Service (PRIVATE — Node)
```
PORT=8090
SMTP_HOST=smtp.gmail.com
SMTP_PORT=587
SMTP_USER=<your-email>
SMTP_PASS=<your-app-password>
```

### Delivery Gig Service (PRIVATE)
```
PORT=8084
DATABASE_URL=<railway-postgres-private-url>
REDIS_ADDRS=<railway-redis-private-url:6379>
KAFKA_BROKERS=<railway-kafka-private-url:9092>
OSRM_URL=<railway-osrm-private-url:5000>
APP_ENV=production
```

### Ride Service (PRIVATE)
```
PORT=8085
DATABASE_URL=<railway-postgres-private-url>
REDIS_ADDRS=<railway-redis-private-url:6379>
KAFKA_BROKERS=<railway-kafka-private-url:9092>
APP_ENV=production
```

### Vendor Store Service (PRIVATE)
```
PORT=8081
DATABASE_URL=<railway-postgres-private-url>
REDIS_ADDRS=<railway-redis-private-url:6379>
KAFKA_BROKERS=<railway-kafka-private-url:9092>
APP_ENV=production
```

---

## 2. Railway Private Networking — Service URLs

Railway pe har service ka private URL format:
```
http://<service-name>.railway.internal:<port>
```

Gateway ke env vars mein ye set karo:
```
AUTH_SERVICE_URL=http://auth-service.railway.internal:8080
VENDOR_STORE_SERVICE_URL=http://vendor-store-service.railway.internal:8081
PRODUCT_SERVICE_URL=http://product-service.railway.internal:8082
ADMIN_SERVICE_URL=http://admin-service.railway.internal:8091
DELIVERY_SERVICE_URL=http://delivery-gig-service.railway.internal:8084
RIDE_SERVICE_URL=http://ride-service.railway.internal:8085
ORDER_SERVICE_URL=http://order-service.railway.internal:8088
PAYMENT_SERVICE_URL=http://payment-orchestrator.railway.internal:8092
WEBSOCKET_GATEWAY_URL=http://websocket-gateway.railway.internal:8087
AI_ENGINE_URL=http://ai-engine.railway.internal:8086
```

---

## 3. FLUTTER APP — Build Configuration

### Production build (Play Store / App Store):
```bash
flutter build apk --release \
  --dart-define=API_HOST=omnigo-app-3-production.up.railway.app
```

Flutter automatically uses:
- `https://omnigo-app-3-production.up.railway.app` for all API calls
- `wss://omnigo-app-3-production.up.railway.app/ws` for WebSocket

### Local development (Android emulator):
```bash
flutter run --dart-define=API_HOST=10.0.2.2
```

Flutter uses:
- `http://10.0.2.2:8000` for all API calls (local gateway)
- `ws://10.0.2.2:8000/ws` for WebSocket

### Local development (Physical device / Desktop):
```bash
flutter run --dart-define=API_HOST=127.0.0.1
```

### IMPORTANT: Flutter code mein SIRF 1 value set hai

`api_endpoints.dart` line 32:
```dart
return 'omnigo-app-3-production.up.railway.app';
```

Yeh tab use hota hai jab `--dart-define=API_HOST` set NAHI kiya gaya.
Production build mein `--dart-define=API_HOST=omnigo-app-3-production.up.railway.app`
pass karte ho, toh wahi use hota hai.

---

## 4. LOCAL DEVELOPMENT — .env file

Root directory mein `.env` file banao (`.env.example` se copy karo):

```bash
cp .env.example .env
```

Key values for local dev:
```
DATABASE_URL=postgres://omnigo_user:omnigo_password@localhost:5433/omnigo_db
DB_READER_DSN=postgres://omnigo_user:omnigo_password@localhost:5433/omnigo_db
REDIS_ADDRS=127.0.0.1:6379
KAFKA_BROKERS=localhost:9092
JWT_SECRET_KEY=<generate: openssl rand -base64 64>
JWT_ISSUER=omnigo-platform
APP_ENV=development
CORS_ALLOWED_ORIGINS=  (empty = "*" in dev mode)

AUTH_SERVICE_URL=http://localhost:8080
VENDOR_STORE_SERVICE_URL=http://localhost:8081
PRODUCT_SERVICE_URL=http://localhost:8082
ADMIN_SERVICE_URL=http://localhost:8091
DELIVERY_SERVICE_URL=http://localhost:8084
RIDE_SERVICE_URL=http://localhost:8085
ORDER_SERVICE_URL=http://localhost:8088
PAYMENT_SERVICE_URL=http://localhost:8092
WEBSOCKET_GATEWAY_URL=http://localhost:8087
AI_ENGINE_URL=http://localhost:8086
```

---

## 5. DB Pool Tuning (Production)

```
DB_MAX_CONNS_WRITER=10
DB_MIN_CONNS_WRITER=2
DB_MAX_CONNS_READER=20
DB_MIN_CONNS_READER=4
DB_CONN_MAX_LIFETIME=30m
DB_CONN_MAX_IDLE_TIME=5m
```

Agar high traffic hai, writer ko 20-30 kar sakte ho.

---

## 6. Security Checklist

| Setting | Value |
|---|---|
| `JWT_SECRET_KEY` | `openssl rand -base64 64` se generate |
| `ADMIN_API_KEY_ENCRYPTION_KEY` | `openssl rand -base64 32` se generate |
| `APP_ENV` | `production` (NOT `development`) |
| `CORS_ALLOWED_ORIGINS` | `https://omnigo-app-3-production.up.railway.app` |
| `TB_ENABLED` | `false` jab tak TigerBeetle setup na ho |
| `PUBLIC_BASE_URL` | `https://omnigo-app-3-production.up.railway.app` |

---

## 7. Railway Service Exposure

| Service | Public? | Why |
|---|---|---|
| Gateway | ✅ YES | Single public entry point for Flutter |
| Auth | ❌ Private | Internal only |
| Product | ❌ Private | Internal only |
| Order | ❌ Private | Internal only |
| Payment | ❌ Private | Internal only |
| Admin | ❌ Private | Internal only |
| AI Engine | ❌ Private | Internal only |
| WebSocket | ❌ Private | Gateway proxies WS |
| Notification | ❌ Private | Internal only |
| Email | ❌ Private | Internal only |
| PostgreSQL | ❌ Private | Internal only |
| Redis | ❌ Private | Internal only |
| Kafka | ❌ Private | Internal only |

Railway dashboard → Settings → Networking:
- Gateway: "Public Networking" = ON
- Baqi sab: "Public Networking" = OFF (private only)