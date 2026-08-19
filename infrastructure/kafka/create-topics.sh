#!/bin/bash
# Wait for Kafka to be ready
echo "Waiting for Kafka to be ready..."
sleep 15

# Define topics and partitions
# Includes topics consumed by Rust WS gateway, Node workers, and Go order-service
TOPICS=(
  "orders.created:12:1"
  "orders.updated:12:1"
  "delivery.requested:12:1"
  "deliveries.broadcasted:12:1"
  "deliveries.status_updated:12:1"
  "deliveries.accepted:12:1"
  "ride.requested:12:1"
  "ride.broadcasted:12:1"
  "rider.location.updated:12:1"
  "payments.initiated:12:1"
  "analytics.events:6:1"
)

for ENTRY in "${TOPICS[@]}"; do
  IFS=':' read -r TOPIC PARTITIONS REPLICAS <<< "$ENTRY"
  echo "Creating topic: $TOPIC (Partitions: $PARTITIONS, Replicas: $REPLICAS)"
  
  kafka-topics --create \
    --if-not-exists \
    --bootstrap-server localhost:9092 \
    --partitions "$PARTITIONS" \
    --replication-factor "$REPLICAS" \
    --topic "$TOPIC"
done

echo "Kafka topics created successfully."
