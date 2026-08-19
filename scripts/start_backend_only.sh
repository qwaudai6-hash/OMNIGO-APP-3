#!/bin/bash
# start_backend_only.sh - Starts Go, Rust, and Node backends in background and exits.
# Does NOT trap EXIT so processes remain running.

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
echo "Project Root: $PROJECT_ROOT"

# 1. Kill existing ports
echo "Releasing existing OMNIGO ports..."
ALL_PORTS=(8080 8081 8082 8084 8085 8087 8088 8089 8090 8091 8092)
for port in "${ALL_PORTS[@]}"; do
  leaked_pid=$(lsof -ti :"$port" 2>/dev/null || true)
  if [ -n "$leaked_pid" ]; then
    echo "  Killing PID $leaked_pid on port $port"
    kill -9 $leaked_pid 2>/dev/null || true
  fi
done

# 2. Bootstrap docker infrastructure
echo "Bootstrapping Docker containers..."
cd "$PROJECT_ROOT"
./scripts/bootstrap.sh

# 3. Start Go Backend microservices
echo "Starting Go Services..."
cd "$PROJECT_ROOT/backend/go-services"
GO_SERVICES=(
  "auth-service:8080:cmd/auth-service/main.go"
  "vendor-store-service:8081:cmd/vendor-store-service/main.go"
  "product-service:8082:cmd/product-service/main.go"
  "delivery-gig-service:8084:cmd/delivery-gig-service/main.go"
  "ride-service:8085:cmd/ride-service/main.go"
  "order-service:8088:cmd/order-service/main.go"
  "admin-service:8091:cmd/admin-service/main.go"
  "graph-sync-worker::cmd/graph-sync-worker/main.go"
  "location-sync-worker::cmd/location-sync-worker/main.go"
)

for svc in "${GO_SERVICES[@]}"; do
  IFS=':' read -r name port path <<< "$svc"
  if [ -n "$port" ]; then
    echo "  Starting Go $name on port $port"
  else
    echo "  Starting Go worker $name"
  fi
  REDIS_ADDRS="127.0.0.1:6379,127.0.0.1:6380,127.0.0.1:6381" \
  KAFKA_BROKERS="localhost:9092" \
  go run "$path" > "/tmp/omnigo-$name.log" 2>&1 &
  disown
  sleep 2
done

# 4. WebSocket Gateway
# Note: The historical Rust websocket-gateway implementation was removed
# in session 54. The Go implementation (cmd/websocket-gateway on :8087)
# is canonical and is started above. This section is intentionally a
# no-op to keep step numbering stable for ops runbooks.

# 5. Start Node Services
echo "Starting Node Services..."
NODE_SERVICES=(
  "notification-service:8089:backend/node-services/notification-service"
  "email-service:8090:backend/node-services/email-service"
)

for svc in "${NODE_SERVICES[@]}"; do
  IFS=':' read -r name port path <<< "$svc"
  echo "  Starting Node $name on port $port"
  cd "$PROJECT_ROOT/$path"
  KAFKA_BROKERS="localhost:9092" \
  DB_DSN="postgres://omnigo_user:omnigo_password@localhost:5433/omnigo_db" \
  node src/index.js > "/tmp/omnigo-$name.log" 2>&1 &
  disown
done

echo "Backend services launched successfully in the background!"
