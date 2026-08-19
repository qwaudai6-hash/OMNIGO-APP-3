import yaml
import sys

with open("infrastructure/docker/docker-compose.yml", "r") as f:
    data = yaml.safe_load(f)

services_to_remove = [
    "payment-orchestrator",
    "websocket-gateway",
    "auth-service",
    "vendor-store-service",
    "product-service",
    "delivery-gig-service",
    "ride-service",
    "order-service",
    "admin-service"
]

for s in services_to_remove:
    if s in data["services"]:
        del data["services"][s]

monolith_service = {
    "build": {
        "context": "../../backend/go-services",
        "dockerfile": "Dockerfile",
        "args": {
            "SERVICE": "monolith"
        }
    },
    "container_name": "omnigo-monolith",
    "ports": ["8000:8000"],
    "env_file": ["../../.env"],
    "environment": [
        "DB_WRITER_DSN=postgres://omnigo_user:${POSTGRES_PASSWORD:-omnigo_password}@omnigo-postgres:5432/omnigo_db?sslmode=disable",
        "DB_READER_DSN=postgres://omnigo_user:${POSTGRES_PASSWORD:-omnigo_password}@omnigo-postgres:5432/omnigo_db?sslmode=disable",
        "REDIS_ADDRS=redis-node-1:6379,redis-node-2:6380,redis-node-3:6381",
        "KAFKA_BROKERS=kafka-cluster:29092",
        "MEILISEARCH_HOST=http://omnigo-meilisearch:7700",
        "NEO4J_URI=neo4j://neo4j-graph:7687",
        "NEO4J_USER=neo4j",
        "OSRM_URL=http://omnigo-osrm:5000",
        "TIGERBEETLE_ADDR=tigerbeetle:3000"
    ],
    "depends_on": {
        "omnigo-postgres": {"condition": "service_healthy"},
        "redis-node-1": {"condition": "service_healthy"},
        "kafka-cluster": {"condition": "service_healthy"}
    },
    "healthcheck": {
        "test": ["CMD-SHELL", "wget -q --spider http://localhost:8000/health || exit 1"],
        "interval": "15s",
        "timeout": "10s",
        "retries": 3,
        "start_period": "20s"
    },
    "deploy": {
        "resources": {
            "limits": {
                "memory": "1G",
                "cpus": "1"
            }
        }
    },
    "restart": "always"
}

data["services"]["monolith"] = monolith_service

class MyDumper(yaml.Dumper):
    def increase_indent(self, flow=False, indentless=False):
        return super(MyDumper, self).increase_indent(flow, False)

with open("infrastructure/docker/docker-compose.yml", "w") as f:
    yaml.dump(data, f, sort_keys=False, Dumper=MyDumper, default_flow_style=False)

print("Successfully updated docker-compose.yml")
