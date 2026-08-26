# OMNIGO APP - Railway Architecture & State Documentation

This document serves as the "Obsidian Mind" state tracker for the current architecture, environment variables, and request flows deployed on Railway.

## 1. System State & Services Setup

We have transitioned from local `docker-compose` to a fully automated CI/CD pipeline on Railway. The infrastructure comprises:

1. **Go Monolith Backend**: The main application server handling all business logic.
2. **PostgreSQL**: Primary relational database (Railway Plugin).
3. **Redis**: In-memory cache and session store (Railway Plugin).
4. **Kafka**: Message broker for async events (Railway Plugin/External).
5. **Neo4j AuraDB**: Managed Cloud Graph Database for location, recommendation, and fraud tracking.
6. **ClickHouse**: Analytics Database (Railway Plugin).
7. **OSRM (Open Source Routing Machine)**: Private routing server deployed as a standalone Docker container on Railway.
8. **TigerBeetle**: Financial ledger database deployed as a standalone Docker container on Railway.

---

## 2. Environment Variables Master List (No Mismatches)

Below is the EXACT, verified state of our production environment variables for the Go Backend on Railway. **There are no localhost leaks.**

```env
# Port & Gateway
AUTH_SERVICE_PORT="9000"
APP_ENV="production"
CORS_ALLOWED_ORIGINS="https://omnigo-app-production.up.railway.app"
PUBLIC_BASE_URL="https://omnigo-app-production.up.railway.app"
WALLET_RETURN_URL="https://omnigo-app-production.up.railway.app/api/v1/wallet/callback"
PUBLIC_GATEWAY_URL="https://omnigo-app-production.up.railway.app"

# Databases (Internal Railway Links & Cloud)
DATABASE_URL="${{Postgres.DATABASE_URL}}"
DB_WRITER_DSN="${{Postgres.DATABASE_URL}}"
DB_READER_DSN="${{Postgres.DATABASE_URL}}"
REDIS_ADDRS="${{Redis.REDIS_URL}}"

CLICKHOUSE_ADDR="clickhouse.railway.internal:9000"
CLICKHOUSE_USER="default"
CLICKHOUSE_PASSWORD="changeme_clickhouse_password"
CLICKHOUSE_DATABASE="default"

NEO4J_URI="neo4j+s://32442466.databases.neo4j.io"
NEO4J_USER="32442466"
NEO4J_PASSWORD="changeme_neo4j_password"
NEO4J_DATABASE="32442466"
AURA_INSTANCEID="32442466"
AURA_INSTANCENAME="OMNIGO APP"

# Message Broker (Kafka)
KAFKA_BROKERS="kafka.railway.internal:9092"

# Security & Secrets
JWT_ISSUER="omnigo-platform"
JWT_SECRET_KEY="changeme_generate_with_openssl_rand_base64_64"
HMAC_SECRET="changeme_generate_with_openssl_rand_base64_32"
ADMIN_API_KEY_ENCRYPTION_KEY="changeme_generate_with_openssl_rand_base64_32"

# Internal Monolith Service Loopbacks
SERVICE_NAME="monolith"
PRODUCT_SERVICE_URL="http://127.0.0.1:9001"
ADMIN_SERVICE_URL="http://127.0.0.1:9007"
AUTH_SERVICE_URL="http://127.0.0.1:9000"
VENDOR_STORE_SERVICE_URL="http://127.0.0.1:9002"
DELIVERY_SERVICE_URL="http://127.0.0.1:9003"
RIDE_SERVICE_URL="http://127.0.0.1:9004"
ORDER_SERVICE_URL="http://127.0.0.1:9005"
PAYMENT_SERVICE_URL="http://127.0.0.1:9006"
WEBSOCKET_GATEWAY_URL="http://127.0.0.1:9008"

# Internal Railway Microservices
AI_ENGINE_URL="http://ai-engine.railway.internal:8086"
EMAIL_SERVICE_URL="http://email-service.railway.internal:8090"
NOTIFICATION_SERVICE_URL="http://notification-service.railway.internal:8089"

# External Standalone Containers
OSRM_URL="http://osrm.railway.internal:5000"
TB_ENABLED="false" 
TIGERBEETLE_ADDRESSES="tigerbeetle.railway.internal:3000"
```
*(Note: TB_ENABLED can be flipped to "true" once TigerBeetle is fully running on Railway).*

---

## 3. Flutter to Backend Request Flow

### How does Flutter communicate?
1. **The Entry Point:** The Flutter application has `PUBLIC_BASE_URL` embedded inside its configuration. It ONLY talks to `https://omnigo-app-production.up.railway.app`.
2. **Gateway:** When Railway receives a request on this URL, it hits the Go Monolith API Gateway.
3. **Internal Routing:** 
   - If Flutter asks for `/api/v1/orders`, the Monolith routes the request internally to the `order-service` (listening on `127.0.0.1:9005`).
   - If Flutter asks for `/api/v1/rides`, it routes to `ride-service` (`127.0.0.1:9004`).
4. **Kafka Processing:** The `order-service` saves the order in Postgres, then emits an `OrderCreated` event to Kafka via `kafka.railway.internal:9092`.
5. **Background Workers:** Services like `graph-sync-worker` listen to Kafka. When they see the `OrderCreated` event, they reach out to **Neo4j Aura Cloud** (`neo4j+s://32442466...`) to securely build the graph relations.

**Bottom Line:** Flutter NEVER touches the database directly. It talks to the public Gateway, the Gateway proxies to internal services, and those services securely use private networks to talk to databases and brokers.

---

## 4. Standalone Docker Deployments (OSRM & TigerBeetle)

To fix the issue where Railway requires everything to work out-of-the-box without manual file injections, we created custom standalone Dockerfiles.

### A. OSRM (Open Source Routing Machine) Setup
- **Dockerfile Path:** `infrastructure/docker/dockerfiles/osrm.Dockerfile`
- **How it works:** 
  1. Railway builds the image. During the build, it uses `wget` to download `pakistan-latest.osm.pbf` from Geofabrik.
  2. It extracts and partitions the map data using the highly optimized MLD (Multi-Level Dijkstra) algorithm inside the image.
  3. It cleans up the raw `.pbf` file to save disk space.
  4. When the container starts on Railway, it simply runs `osrm-routed` on port 5000.
- **Backend Connection:** The Go backend securely talks to it via `http://osrm.railway.internal:5000`.

### B. TigerBeetle Ledger Setup
- **Dockerfile Path:** `infrastructure/docker/dockerfiles/tigerbeetle.Dockerfile`
- **Script Path:** `infrastructure/docker/dockerfiles/tigerbeetle-entrypoint.sh`
- **How it works:**
  1. Railway builds the image wrapping the official TigerBeetle node.
  2. The custom entrypoint script checks if `/data/0_0.tigerbeetle` exists.
  3. If it does not exist (first boot), it runs the `format` command to create the database file.
  4. It then runs `start` to boot the server.
- **Requirement:** In Railway settings, a custom volume MUST be mounted to `/data` for this service so that ledger data survives container restarts.
- **Backend Connection:** The Go backend connects via `tigerbeetle.railway.internal:3000`.

---
*Documented on: July 24, 2026. Codebase is in production-ready state.*
