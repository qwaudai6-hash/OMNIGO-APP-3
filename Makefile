.PHONY: up down build logs test clean bootstrap k8s-deploy k8s-delete dev-infra

COMPOSE_FILE := infrastructure/docker/docker-compose.yml
HELM_DIR := infrastructure/helm/omnigo

# Docker Compose Full Stack (Postgres, Redis, TigerBeetle, Kafka, Neo4j, Microservices)
up:
	docker compose -f $(COMPOSE_FILE) up -d

down:
	docker compose -f $(COMPOSE_FILE) down

build:
	docker compose -f $(COMPOSE_FILE) build

logs:
	docker compose -f $(COMPOSE_FILE) logs -f

# Start Core Infrastructure Only (Postgres, Redis, TigerBeetle, Kafka) for local development
dev-infra:
	docker compose -f $(COMPOSE_FILE) up -d omnigo-postgres redis-node-1 tigerbeetle zookeeper kafka

bootstrap:
	./scripts/bootstrap.sh

# Kubernetes Deployment via Helm
k8s-deploy:
	helm upgrade --install omnigo $(HELM_DIR) --namespace omnigo --create-namespace

k8s-delete:
	helm uninstall omnigo --namespace omnigo

test:
	@echo "=== Running Go tests ==="
	cd backend/go-services && go test ./... -v -count=1 -short 2>&1 || true
	@echo "=== Running Flutter analyze ==="
	cd frontend/omnigo_app && flutter analyze --no-pub 2>&1 || true
	@echo "=== All tests complete ==="

clean:
	docker compose -f $(COMPOSE_FILE) down -v
	rm -rf tmp/
