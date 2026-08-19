#!/bin/bash
# Register the Debezium PostgreSQL connector with Debezium Connect.
# Run this after `docker-compose up` has started Kafka + Debezium Connect.
#
# Usage: ./scripts/register_debezium_connector.sh

set -e

CONNECT_URL="http://localhost:8083/connectors"
CONNECTOR_FILE="infrastructure/debezium/omnigo-postgres-connector.json"

echo "Registering Debezium PostgreSQL connector..."
echo "  Connect URL: $CONNECT_URL"
echo "  Config file: $CONNECTOR_FILE"

# Check if connector already exists
EXISTING=$(curl -s -o /dev/null -w "%{http_code}" "$CONNECT_URL/omnigo-postgres-connector")
if [ "$EXISTING" = "200" ]; then
    echo "Connector already exists. Updating config..."
    METHOD="PUT"
    URL="$CONNECT_URL/omnigo-postgres-connector/config"
    # Extract just the config block for PUT
    cat "$CONNECTOR_FILE" | python3 -c "import sys,json; print(json.dumps(json.load(sys.stdin)['config']))" > /tmp/debezium_config.json
    BODY_FILE="/tmp/debezium_config.json"
else
    echo "Creating new connector..."
    METHOD="POST"
    URL="$CONNECT_URL"
    BODY_FILE="$CONNECTOR_FILE"
fi

RESPONSE=$(curl -s -X "$METHOD" "$URL" \
    -H "Content-Type: application/json" \
    -d @"$BODY_FILE")

if echo "$RESPONSE" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    echo "✅ Connector registered successfully!"
    echo "$RESPONSE" | python3 -m json.tool
else
    echo "❌ Failed to register connector:"
    echo "$RESPONSE"
    exit 1
fi

echo ""
echo "CDC topics will be available at: dbstream.public.<table_name>"
echo "Graph sync worker consumes: dbstream.public.orders"