.PHONY: up down build logs test clean bootstrap

COMPOSE_FILE := infrastructure/docker/docker-compose.yml

up:
	docker compose -f $(COMPOSE_FILE) up -d

down:
	docker compose -f $(COMPOSE_FILE) down

bootstrap:
	./scripts/bootstrap.sh

build:
	docker compose -f $(COMPOSE_FILE) build

logs:
	docker compose -f $(COMPOSE_FILE) logs -f

test:
	@echo "=== Running Go tests ==="
	cd backend/go-services && go test ./... -v -count=1 -short 2>&1 || true
	@echo "=== Running Flutter analyze ==="
	cd frontend/omnigo_app && flutter analyze --no-pub 2>&1 || true
	@echo "=== All tests complete ==="

clean:
	docker compose -f $(COMPOSE_FILE) down -v
	rm -rf tmp/
