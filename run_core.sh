#!/bin/bash
# run_core.sh - Lightweight runner for low-RAM laptops
# Only runs Postgres, Redis, Auth Service, and Flutter App

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

echo -e "${CYAN}======================================================${NC}"
echo -e "${CYAN}    🚀 OMNIGO LITE LAUNCHER (Low RAM Mode)            ${NC}"
echo -e "${CYAN}======================================================${NC}"

# 1. Start only Postgres and Redis via Docker Compose
echo -e "\n${YELLOW}[1/3] Starting Database & Cache (Postgres + Redis)...${NC}"
cd "$PROJECT_ROOT/infrastructure/docker"
docker compose up -d omnigo-postgres redis-node-1
cd "$PROJECT_ROOT"

# Wait for DB to be ready
echo -e "Waiting 5 seconds for databases to initialize..."
sleep 5

# 2. Start only the Auth Service
echo -e "\n${YELLOW}[2/3] Starting Auth Service...${NC}"
cd "$PROJECT_ROOT/backend/go-services"

# Set environment variables just for this run
export DB_WRITER_DSN="postgres://omnigo_user:omnigo_password@localhost:5433/omnigo_db?sslmode=disable"
export DB_READER_DSN="postgres://omnigo_user:omnigo_password@localhost:5433/omnigo_db?sslmode=disable"
export REDIS_ADDRS="127.0.0.1:6379"
export PORT=8000

go run cmd/auth-service/main.go > /tmp/omnigo-auth-service.log 2>&1 &
AUTH_PID=$!
echo -e "${GREEN}✓ Auth Service started (PID: $AUTH_PID)${NC}"

# 2.5 Start Notification, Email & SMS Node Services
echo -e "\n${YELLOW}[2.5/3] Starting Notification, Email & SMS Microservices...${NC}"
NODE_PIDS=()

if command -v node &>/dev/null; then
  export DATABASE_URL="postgres://omnigo_user:omnigo_password@localhost:5433/omnigo_db?sslmode=disable"
  
  if [ -d "$PROJECT_ROOT/backend/node-services/email-service" ]; then
    (cd "$PROJECT_ROOT/backend/node-services/email-service" && PORT=8090 node src/index.js > /tmp/omnigo-email-service.log 2>&1) &
    NODE_PIDS+=($!)
    echo -e "${GREEN}✓ Email Service started (Port 8090)${NC}"
  fi

  if [ -d "$PROJECT_ROOT/backend/node-services/sms-service" ]; then
    (cd "$PROJECT_ROOT/backend/node-services/sms-service" && PORT=8091 node src/index.js > /tmp/omnigo-sms-service.log 2>&1) &
    NODE_PIDS+=($!)
    echo -e "${GREEN}✓ SMS Service started (Port 8091)${NC}"
  fi

  if [ -d "$PROJECT_ROOT/backend/node-services/notification-service" ]; then
    (cd "$PROJECT_ROOT/backend/node-services/notification-service" && PORT=8092 node src/index.js > /tmp/omnigo-notification-service.log 2>&1) &
    NODE_PIDS+=($!)
    echo -e "${GREEN}✓ FCM Notification Service started (Port 8092)${NC}"
  fi
fi

cd "$PROJECT_ROOT"

# 3. Start Flutter App
echo -e "\n${YELLOW}[3/3] Starting Flutter App...${NC}"
cd "$PROJECT_ROOT/frontend/omnigo_app"

echo -e "${GREEN}Starting Flutter! Press Ctrl+C in terminal to stop everything.${NC}"

# Cleanup function to kill background services when Flutter stops
cleanup() {
    echo -e "\n${YELLOW}Shutting down background services...${NC}"
    kill $AUTH_PID 2>/dev/null || true
    for pid in "${NODE_PIDS[@]}"; do
      kill $pid 2>/dev/null || true
    done
    echo -e "${GREEN}Cleanup complete.${NC}"
}
trap cleanup EXIT INT TERM

# Run Flutter
if command -v flutter &>/dev/null; then
  flutter run -d linux
else
  echo -e "${YELLOW}Flutter not found in path. Please run the flutter app manually.${NC}"
  wait $AUTH_PID
fi
