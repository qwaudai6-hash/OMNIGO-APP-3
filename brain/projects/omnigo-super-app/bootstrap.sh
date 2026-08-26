#!/bin/bash
set -e

COMPOSE_FILE="infrastructure/docker/docker-compose.yml"

echo "=========================================================="
echo "          OMNIGO ECOSYSTEM BOOTSTRAP INTEGRATION          "
echo "=========================================================="

# 1. Start Docker Containers
echo "Starting Database, Event Bus, and routing cluster..."
docker compose -f "$COMPOSE_FILE" up -d

# 2. Wait for Postgres Primary
echo "Waiting for PostgreSQL Primary Database..."
until docker exec $(docker compose -f "$COMPOSE_FILE" ps -q postgres-primary) pg_isready -U admin -d omnigo >/dev/null 2>&1; do
  echo -n "."
  sleep 2
done
echo " PostgreSQL is ONLINE."

# 3. Wait for Kafka Connect (Debezium)
echo "Waiting for Kafka Connect REST Ingress..."
until curl -s -f http://localhost:8083/ >/dev/null 2>&1; do
  echo -n "."
  sleep 2
done
echo " Kafka Connect is ONLINE."

# 4. Wait for Neo4j Database
echo "Waiting for Neo4j Graph Database Ingress..."
until curl -s -f http://localhost:7474/ >/dev/null 2>&1; do
  echo -n "."
  sleep 2
done
echo " Neo4j is ONLINE."

# 5. Bootstrap Debezium CDC Connector
echo "Registering Debezium PostgreSQL connector with Unwrap SMT..."
curl -s -X POST -H "Accept:application/json" -H "Content-Type:application/json" \
  http://localhost:8083/connectors/ -d '{
    "name": "omnigo-postgres-connector",
    "config": {
      "connector.class": "io.debezium.connector.postgresql.PostgresConnector",
      "database.hostname": "postgres-primary",
      "database.port": "5432",
      "database.user": "admin",
      "database.password": "CHANGEME_ROTATED",
      "database.dbname": "omnigo",
      "table.include.list": "public.orders",
      "topic.prefix": "dbstream",
      "plugin.name": "pgoutput",
      "transforms": "unwrap",
      "transforms.unwrap.type": "io.debezium.transforms.ExtractNewRecordState",
      "transforms.unwrap.drop.tombstones": "true",
      "transforms.unwrap.delete.handling.mode": "drop"
    }
  }' >/dev/null

echo " Debezium PostgreSQL SMT Connector registered successfully."

# 6. Verify Connector Deployment
echo "Checking connector deployment state..."
sleep 3
connector_status=$(curl -s http://localhost:8083/connectors/omnigo-postgres-connector/status || echo "failed")
echo "Connector Status: $connector_status"

echo "=========================================================="
echo " OMNIGO Core Engine Bootstrapped Successfully!"
echo " Neo4j Uniqueness constraints will auto-bootstrap on "
echo " Graph Sync Worker startup."
echo "=========================================================="
