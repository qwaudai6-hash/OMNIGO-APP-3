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
	# Add test commands for all services
	@echo "Running tests..."

clean:
	docker compose -f $(COMPOSE_FILE) down -v
	rm -rf tmp/
