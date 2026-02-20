# =============================================================================
# Project Sentinel — Makefile
# =============================================================================

.PHONY: help api worker frontend build \
        dev-api dev-worker dev-frontend \
        up up-all up-infra down down-clean logs \
        migrate migrate-down \
        test test-api test-worker test-frontend test-integration \
        lint lint-api lint-worker lint-frontend \
        fmt deps deps-frontend clean \
        docker-build docker-build-api docker-build-worker docker-build-frontend \
        docker-push

# Default target
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

# ---------- Build ----------

api: ## Build the API server binary
	cd api && go build -o ../bin/api ./cmd/server/

worker: ## Build the worker binary
	cd worker && go build -o ../bin/worker ./cmd/worker/

frontend: ## Build the frontend for production
	cd frontend && npm run build

build: api worker frontend ## Build all components

# ---------- Development ----------

dev-api: ## Run API server in development mode
	cd api && go run ./cmd/server/

dev-worker: ## Run worker in development mode
	cd worker && go run ./cmd/worker/

dev-frontend: ## Run frontend dev server
	cd frontend && npm run dev

# ---------- Docker Compose ----------

up-infra: ## Start infrastructure only (Postgres, RabbitMQ, Redis)
	docker compose up -d postgres rabbitmq redis

up: ## Start all services (build if needed)
	docker compose up -d --build

up-all: up ## Alias for 'up'

down: ## Stop all services
	docker compose down

down-clean: ## Stop all services and remove volumes
	docker compose down -v

logs: ## Tail logs for all services
	docker compose logs -f

logs-api: ## Tail API logs
	docker compose logs -f api

logs-worker: ## Tail Worker logs
	docker compose logs -f worker

# ---------- Docker Build (for CI / GHCR push) ----------

REGISTRY ?= ghcr.io/harsh-bh
TAG ?= latest

docker-build-api: ## Build API Docker image
	docker build -t $(REGISTRY)/sentinel-api:$(TAG) ./api

docker-build-worker: ## Build Worker Docker image (uses repo root context)
	docker build -t $(REGISTRY)/sentinel-worker:$(TAG) -f worker/Dockerfile .

docker-build-frontend: ## Build Frontend Docker image
	docker build -t $(REGISTRY)/sentinel-frontend:$(TAG) ./frontend

docker-build: docker-build-api docker-build-worker docker-build-frontend ## Build all Docker images

docker-push: ## Push all images to registry
	docker push $(REGISTRY)/sentinel-api:$(TAG)
	docker push $(REGISTRY)/sentinel-worker:$(TAG)
	docker push $(REGISTRY)/sentinel-frontend:$(TAG)

# ---------- Database ----------

migrate: ## Apply database migrations (requires running Postgres)
	@echo "Applying migrations..."
	PGPASSWORD=sentinel_secret psql -h localhost -U sentinel -d sentinel -f migrations/001_initial_schema.up.sql

migrate-down: ## Rollback database migrations
	@echo "Rolling back migrations..."
	PGPASSWORD=sentinel_secret psql -h localhost -U sentinel -d sentinel -f migrations/001_initial_schema.down.sql

# ---------- Testing ----------

test: test-api test-worker ## Run all unit tests

test-api: ## Run API unit tests
	cd api && go test -v -race -count=1 ./...

test-worker: ## Run worker unit tests
	cd worker && go test -v -race -count=1 ./...

test-frontend: ## Run frontend tests
	cd frontend && npm test

test-integration: ## Run integration tests (full docker-compose stack)
	docker compose -f docker-compose.yml -f docker-compose.test.yml \
		up --build --abort-on-container-exit --exit-code-from integration-test
	@docker compose -f docker-compose.yml -f docker-compose.test.yml down -v

# ---------- Linting ----------

lint: lint-api lint-worker lint-frontend ## Run all linters

lint-api: ## Lint API code
	cd api && golangci-lint run ./...

lint-worker: ## Lint worker code
	cd worker && golangci-lint run ./...

lint-frontend: ## Lint frontend code
	cd frontend && npm run lint

# ---------- Formatting ----------

fmt: ## Format Go code
	cd api && go fmt ./...
	cd worker && go fmt ./...

# ---------- Dependencies ----------

deps: ## Install/update Go dependencies
	cd api && go mod tidy
	cd worker && go mod tidy

deps-frontend: ## Install frontend dependencies
	cd frontend && npm install

# ---------- Health Check ----------

health: ## Check health of running services
	@echo "API:    $$(curl -sf http://localhost:8080/api/v1/health && echo ' ✅' || echo ' ❌')"
	@echo "Worker: $$(curl -sf http://localhost:9090/healthz && echo ' ✅' || echo ' ❌')"
	@echo "Frontend: $$(curl -sf http://localhost:3000/nginx-health && echo ' ✅' || echo ' ❌')"

# ---------- Cleanup ----------

clean: ## Remove build artifacts
	rm -rf bin/
	rm -rf frontend/dist/
	cd api && go clean
	cd worker && go clean
