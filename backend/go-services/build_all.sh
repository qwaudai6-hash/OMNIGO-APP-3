#!/bin/bash
# build_all.sh — Build all OMNIGO Go service binaries for Docker.
# Errors are NOT suppressed: a failing build exits non-zero so CI/Docker
# fails loudly instead of shipping a broken image.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_DIR="$SCRIPT_DIR/bin"

echo "=== Building OMNIGO Go Service Binaries ==="
mkdir -p "$BIN_DIR"

# CGO is required by uber/h3-go used in delivery-gig-service.
export CGO_ENABLED=1

SERVICES=(
  "auth-service:cmd/auth-service/main.go"
  "vendor-store-service:cmd/vendor-store-service/main.go"
  "product-service:cmd/product-service/main.go"
  "delivery-gig-service:cmd/delivery-gig-service/main.go"
  "ride-service:cmd/ride-service/main.go"
  "order-service:cmd/order-service/main.go"
  "payment-orchestrator:cmd/payment-orchestrator/main.go"
  "admin-service:cmd/admin-service/main.go"
  "websocket-gateway:cmd/websocket-gateway/main.go"
  "gateway:cmd/gateway/main.go"
  "graph-sync-worker:cmd/graph-sync-worker/main.go"
  "location-sync-worker:cmd/location-sync-worker/main.go"
  "map-service:cmd/map-service/main.go"
  "monolith:cmd/monolith/main.go"
)

cd "$SCRIPT_DIR"

FAILED=0
for entry in "${SERVICES[@]}"; do
  IFS=':' read -r name path <<< "$entry"
  printf "  Building %-25s ... " "$name"
  if go build -o "$BIN_DIR/$name" "./$path"; then
    echo "OK"
  else
    echo "FAILED"
    FAILED=$((FAILED + 1))
  fi
done

echo ""
if [ "$FAILED" -gt 0 ]; then
  echo "=== $FAILED service(s) failed to build ===" >&2
  exit 1
fi

echo "=== All ${#SERVICES[@]} binaries built in $BIN_DIR ==="
ls -lh "$BIN_DIR/"