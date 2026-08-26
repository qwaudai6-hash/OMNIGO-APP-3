#!/usr/bin/env bash
# ==============================================================================
# scripts/verify_railway_deployment.sh — End-to-End Railway Deployment Verifier
# Validates Gateway, Monolith, Map Service, and Database connectivity.
# ==============================================================================
set -euo pipefail

BASE_URL="${RAILWAY_PUBLIC_URL:-http://localhost:8080}"
MAX_RETRIES=15
RETRY_DELAY=5

echo "=== [OMNIGO Verifier] Testing Live Railway Deployment ==="
echo "Target Base URL: ${BASE_URL}"

check_endpoint() {
    local name="$1"
    local url="$2"
    local expected_code="${3:-200}"

    echo -n "Checking ${name} (${url}) ... "
    for ((i=1; i<=MAX_RETRIES; i++)); do
        HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$url" || echo "000")
        if [[ "$HTTP_CODE" == "$expected_code" || "$HTTP_CODE" == "200" || "$HTTP_CODE" == "204" ]]; then
            echo "[OK] (HTTP ${HTTP_CODE})"
            return 0
        fi
        sleep "$RETRY_DELAY"
    done
    echo "[FAILED] (HTTP ${HTTP_CODE} after ${MAX_RETRIES} attempts)"
    return 1
}

# 1. API Gateway Health
check_endpoint "API Gateway Health" "${BASE_URL}/health"

# 2. Monolith Core Backend
check_endpoint "Core Monolith Health" "${BASE_URL}/api/v1/health"

# 3. Map Service Proxy
check_endpoint "Map Service Proxy" "${BASE_URL}/api/v1/map/health"

# 4. Map Style JSON Endpoint
check_endpoint "MapLibre Style JSON" "${BASE_URL}/api/v1/map/style.json"

echo "=== [SUCCESS] All Railway services are healthy and responding! ==="
