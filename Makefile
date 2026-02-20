# =============================================================================
# Project Sentinel — Makefile
# =============================================================================

.PHONY: help api worker frontend up down test lint migrate clean

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

up: ## Start infrastructure services (Postgres, RabbitMQ, Redis)
	docker-compose up -d postgres rabbitmq redis

up-all: ## Start all services including API, Worker, Frontend
	docker-compose up -d --build

down: ## Stop all services
	docker-compose down

down-clean: ## Stop all services and remove volumes
	docker-compose down -v

logs: ## Tail logs for all services
	docker-compose logs -f

# ---------- Database ----------

migrate: ## Apply database migrations (requires running Postgres)
	@echo "Applying migrations..."
	PGPASSWORD=sentinel_secret psql -h localhost -U sentinel -d sentinel -f migrations/001_initial_schema.up.sql

migrate-down: ## Rollback database migrations
	@echo "Rolling back migrations..."
	PGPASSWORD=sentinel_secret psql -h localhost -U sentinel -d sentinel -f migrations/001_initial_schema.down.sql

# ---------- Testing ----------

test: test-api test-worker ## Run all tests

test-api: ## Run API unit tests
	cd api && go test -v -race -count=1 ./...

test-worker: ## Run worker unit tests
	cd worker && go test -v -race -count=1 ./...

test-frontend: ## Run frontend tests
	cd frontend && npm test

test-integration: ## Run integration tests with Docker Compose
	docker-compose -f docker-compose.yml -f docker-compose.test.yml up --build --abort-on-container-exit

# ---------- Linting ----------

lint: lint-api lint-worker ## Run all linters

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

# ---------- Cleanup ----------

clean: ## Remove build artifacts
	rm -rf bin/
	rm -rf frontend/dist/
	cd api && go clean
	cd worker && go clean
