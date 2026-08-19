#!/bin/bash
# verify_system.sh - Starts Go microservices temporarily, tests API endpoints, verifies DB mapping, and exits cleanly.

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
echo "Project Root: $PROJECT_ROOT"

echo "=========================================================="
echo "          OMNIGO ECOSYSTEM AUTOMATED VERIFICATION          "
echo "=========================================================="

# 1. Cleanup existing ports
echo "Releasing existing ports..."
ALL_PORTS=(8080 8081 8082 8084 8085 8087 8088 8089 8090 8091 8092)
for port in "${ALL_PORTS[@]}"; do
  leaked_pid=$(lsof -ti :"$port" 2>/dev/null || true)
  if [ -n "$leaked_pid" ]; then
    kill -9 $leaked_pid 2>/dev/null || true
  fi
done

# 2. Start Go microservices in background
echo "Starting Go Backend Services..."
cd "$PROJECT_ROOT/backend/go-services"

GO_SERVICES=(
  "auth-service:8080:cmd/auth-service/main.go"
  "vendor-store-service:8081:cmd/vendor-store-service/main.go"
  "product-service:8082:cmd/product-service/main.go"
)

PIDS=()
for svc in "${GO_SERVICES[@]}"; do
  IFS=':' read -r name port path <<< "$svc"
  echo "  Launching $name on port $port..."
  REDIS_ADDRS="127.0.0.1:6379,127.0.0.1:6380,127.0.0.1:6381" \
  KAFKA_BROKERS="localhost:9092" \
  go run "$path" > "/tmp/omnigo-verify-$name.log" 2>&1 &
  PIDS+=("$!")
  sleep 2
done

echo "Waiting 12 seconds for Auth and Vendor services to initialize..."
sleep 12

# 3. Test Register Endpoint
echo "Testing /api/v1/auth/register..."
UNIQUE_EMAIL="verify_vendor_$(date +%s)@example.com"
REGISTER_PAYLOAD='{
  "name": "Ecosystem Verifier",
  "email": "'"$UNIQUE_EMAIL"'",
  "password": "SecurePassword123!",
  "role": "vendor",
  "region": "PK",
  "phone": "+923009876543",
  "business_name": "Verified Automated Store",
  "address": "Lahore Cantonment Main Mall Road",
  "latitude": 31.52041234,
  "longitude": 74.35875678,
  "entity_type": "individual"
}'

REGISTER_RESPONSE=$(curl -s -X POST http://localhost:8080/api/v1/auth/register \
  -H "Content-Type: application/json" \
  -d "$REGISTER_PAYLOAD")

echo "Register Response:"
echo "$REGISTER_RESPONSE"

# 4. Test Login Endpoint
echo "Testing /api/v1/auth/login..."
LOGIN_PAYLOAD='{
  "email": "'"$UNIQUE_EMAIL"'",
  "password": "SecurePassword123!"
}'

LOGIN_RESPONSE=$(curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d "$LOGIN_PAYLOAD")

echo "Login Response:"
echo "$LOGIN_RESPONSE"

# 5. Query PostgreSQL database to check coordinate mapping
echo "Querying PostgreSQL database table 'users'..."
DB_CHECK=$(docker exec omnigo-postgres psql -U omnigo_user -d omnigo_db -t -c "
  SELECT tracking_id, email, business_name, latitude, longitude, entity_type 
  FROM users 
  WHERE email = '$UNIQUE_EMAIL';
")

echo "Database Record for $UNIQUE_EMAIL:"
echo "$DB_CHECK"

# 6. Check Debezium replication task status
echo "Checking Debezium task state..."
DEBEZIUM_STATUS=$(curl -s http://localhost:8083/connectors/omnigo-postgres-connector/status || echo "failed")
echo "$DEBEZIUM_STATUS"

# 7. Cleanup processes
echo "Cleaning up verification backend processes..."
for pid in "${PIDS[@]}"; do
  kill -9 "$pid" 2>/dev/null || true
done

echo "=========================================================="
echo "          VERIFICATION PROCESS COMPLETED                  "
echo "=========================================================="
