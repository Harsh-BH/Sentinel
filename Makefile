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
        docker-push \
        k8s-apply k8s-delete k8s-status k8s-logs k8s-setup k8s-teardown \
        monitoring-up monitoring-down monitoring-status

# Default target
help: ## Show this help
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
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
	@echo "API:        $$(curl -sf http://localhost:8080/api/v1/health && echo ' ✅' || echo ' ❌')"
	@echo "Worker:     $$(curl -sf http://localhost:9090/healthz && echo ' ✅' || echo ' ❌')"
	@echo "Frontend:   $$(curl -sf http://localhost:3000/nginx-health && echo ' ✅' || echo ' ❌')"
	@echo "Prometheus: $$(curl -sf http://localhost:9091/-/healthy && echo ' ✅' || echo ' ❌')"
	@echo "Grafana:    $$(curl -sf http://localhost:3001/api/health && echo ' ✅' || echo ' ❌')"

# ---------- Kubernetes (k3s) ----------

NAMESPACE ?= sentinel

k8s-setup: ## Full k3s cluster setup (requires sudo)
	sudo ./scripts/setup-k3s.sh

k8s-apply: ## Apply all K8s manifests via Kustomize
	kubectl apply -k infra/k8s/

k8s-delete: ## Delete all K8s resources
	kubectl delete -k infra/k8s/ --ignore-not-found

k8s-status: ## Show status of all Sentinel resources in k8s
	@echo "── Pods ──"
	@kubectl get pods -n $(NAMESPACE) -o wide
	@echo ""
	@echo "── Services ──"
	@kubectl get svc -n $(NAMESPACE)
	@echo ""
	@echo "── Deployments / StatefulSets ──"
	@kubectl get deploy,sts -n $(NAMESPACE)
	@echo ""
	@echo "── HPA / ScaledObjects ──"
	@kubectl get hpa,scaledobject -n $(NAMESPACE) 2>/dev/null || true
	@echo ""
	@echo "── Ingress ──"
	@kubectl get ingress -n $(NAMESPACE)
	@echo ""
	@echo "── PVCs ──"
	@kubectl get pvc -n $(NAMESPACE)

k8s-logs: ## Tail logs for all Sentinel pods
	kubectl logs -n $(NAMESPACE) -l app.kubernetes.io/part-of=sentinel -f --max-log-requests=10

k8s-teardown: ## Full teardown of Sentinel from k3s (requires sudo)
	sudo ./scripts/setup-k3s.sh --teardown

# ---------- Monitoring / Observability ----------

monitoring-up: ## Start Prometheus + Grafana (docker-compose)
	docker compose up -d prometheus grafana
	@echo ""
	@echo "Prometheus: http://localhost:9091"
	@echo "Grafana:    http://localhost:3001 (admin / sentinel)"

monitoring-down: ## Stop Prometheus + Grafana
	docker compose rm -sf prometheus grafana

monitoring-status: ## Check monitoring endpoints
	@echo "Prometheus: $$(curl -sf http://localhost:9091/-/healthy && echo ' ✅' || echo ' ❌')"
	@echo "Grafana:    $$(curl -sf http://localhost:3001/api/health && echo ' ✅' || echo ' ❌')"

k8s-monitoring-apply: ## Apply monitoring stack to k3s
	kubectl apply -k infra/k8s/monitoring/

k8s-monitoring-delete: ## Delete monitoring stack from k3s
	kubectl delete -k infra/k8s/monitoring/ --ignore-not-found

k8s-monitoring-status: ## Show monitoring stack status
	@echo "── Monitoring Pods ──"
	@kubectl get pods -n monitoring -o wide
	@echo ""
	@echo "── Monitoring Services ──"
	@kubectl get svc -n monitoring
	@echo ""
	@echo "── Monitoring PVCs ──"
	@kubectl get pvc -n monitoring

k8s-monitoring-portforward: ## Port-forward Grafana + Prometheus
	@echo "Prometheus → localhost:9091  |  Grafana → localhost:3001"
	@echo "Press Ctrl+C to stop"
	@kubectl port-forward -n monitoring svc/prometheus 9091:9090 &
	@kubectl port-forward -n monitoring svc/grafana 3001:3000

# ---------- Cleanup ----------

clean: ## Remove build artifacts
	rm -rf bin/
	rm -rf frontend/dist/
	cd api && go clean
	cd worker && go clean
