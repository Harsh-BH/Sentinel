# 🛡️ Sentinel

> **Distributed Remote Code Execution Engine** — Execute untrusted code safely at scale with sub-second latency.

[![CI](https://github.com/Harsh-BH/Sentinel/actions/workflows/ci.yml/badge.svg)](https://github.com/Harsh-BH/Sentinel/actions)
[![Go](https://img.shields.io/badge/Go-1.23+-00ADD8?logo=go)](https://go.dev)
[![React](https://img.shields.io/badge/React-18-61DAFB?logo=react)](https://react.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](./LICENSE)

---

## Architecture

```
┌──────────────┐     ┌──────────────┐     ┌──────────────────┐
│   Frontend   │────▶│   API GW     │────▶│    RabbitMQ      │
│  React/Vite  │◀────│  Go + Gin    │     │  Quorum Queues   │
│  Monaco Edit │  WS │  PostgreSQL  │     └────────┬─────────┘
└──────────────┘     └──────────────┘              │
                                                   ▼
                                          ┌──────────────────┐
                                          │   Worker Pool    │
                                          │  Go + nsjail     │
                                          │  Python │ C++    │
                                          │  Sandboxed RCE   │
                                          └──────────────────┘
```

### How It Works

1. **Submit** — User writes code in the Monaco editor and hits Run
2. **Queue** — API validates input, persists to PostgreSQL, publishes to RabbitMQ
3. **Execute** — Worker consumes the job, spins up an nsjail sandbox, runs the code
4. **Stream** — Results flow back via WebSocket (polling fallback) to the frontend

### Security Model (nsjail)

- **Filesystem**: Read-only `pivot_root` with tmpfs scratch space
- **Namespaces**: Full isolation (PID, NET, MNT, UTS, IPC, USER, CGROUP)
- **Cgroups v2**: Memory (256MB), PIDs (64), CPU (1 core) limits
- **Seccomp-BPF**: Allowlisted syscalls via Kafel policies — everything else is killed
- **Timeouts**: Hard wall-clock limit (10s default) + process-group kill

---

## Tech Stack

| Layer | Technology |
|-------|-----------|
| **Frontend** | React 18, TypeScript, Vite, Monaco Editor, Tailwind CSS |
| **API Gateway** | Go 1.23, Gin, pgx/v5, gorilla/websocket |
| **Message Broker** | RabbitMQ 3.13 (Quorum Queues, DLX) |
| **Worker** | Go 1.23, nsjail, os/exec with process groups |
| **Database** | PostgreSQL 16 (partitioned tables, UUIDv7) |
| **Cache** | Redis 7 (idempotency locks, rate limiting) |
| **Observability** | Prometheus + Grafana |
| **Infrastructure** | Docker Compose (dev), k3s + KEDA + cert-manager (prod) |
| **CI/CD** | GitHub Actions, GHCR |

---

## Project Structure

```
Sentinel/
├── api/                        # Go API Gateway
│   ├── cmd/server/             # Entrypoint
│   ├── internal/
│   │   ├── config/             # Viper configuration
│   │   ├── delivery/http/      # Gin handlers & middleware
│   │   ├── domain/             # Core types (Job, Status, errors)
│   │   ├── publisher/          # RabbitMQ publisher
│   │   ├── repository/         # PostgreSQL repository (pgx)
│   │   └── usecase/            # Business logic
│   └── Dockerfile
├── worker/                     # Go Worker Service
│   ├── cmd/worker/             # Entrypoint
│   ├── internal/
│   │   ├── config/             # Worker configuration
│   │   ├── delivery/amqp/      # RabbitMQ consumer
│   │   ├── domain/             # Execution types
│   │   ├── executor/           # nsjail sandbox executor
│   │   ├── metrics/            # Prometheus metrics
│   │   ├── pool/               # Goroutine worker pool
│   │   ├── repository/         # Postgres + Redis repos
│   │   └── usecase/            # Execution orchestration
│   └── Dockerfile
├── frontend/                   # React Frontend
│   ├── src/
│   │   ├── components/         # CodeEditor, ResultPanel, etc.
│   │   ├── hooks/              # useJobTracking
│   │   ├── services/           # API client + WebSocket
│   │   └── types/              # TypeScript types
│   └── Dockerfile
├── sandbox/                    # Sandbox Configuration
│   ├── nsjail/                 # nsjail protobuf configs
│   └── policies/               # Kafel seccomp policies
├── migrations/                 # PostgreSQL migrations
├── infra/k8s/                  # Kubernetes manifests
├── infra/k8s/monitoring/       # Prometheus + Grafana stack
├── infra/monitoring/           # Docker Compose observability configs
├── .github/workflows/          # CI pipeline
├── docker-compose.yml          # Local development stack
├── Makefile                    # Build & dev commands
└── MASTER_PLAN.md              # Full project specification
```

---

## Quick Start

### Prerequisites

- **Go 1.23+**
- **Node.js 20+** & npm
- **Docker** & Docker Compose
- **nsjail** (for local worker testing — [install guide](https://github.com/google/nsjail))

### 1. Clone & Configure

```bash
git clone https://github.com/Harsh-BH/Sentinel.git
cd Sentinel
cp .env.example .env
```

### 2. Start Infrastructure

```bash
# Start PostgreSQL, RabbitMQ, Redis
make up-infra

# Run database migrations
make migrate
```

### 3. Run Services

```bash
# Terminal 1 — API
make dev-api

# Terminal 2 — Worker
make dev-worker

# Terminal 3 — Frontend
make dev-frontend
```

### 4. Open

Navigate to [http://localhost:5173](http://localhost:5173)

### Docker Compose (all-in-one)

```bash
make up          # Build & start everything
make down        # Stop everything
make down-clean  # Stop & remove volumes
make logs        # Follow logs
make health      # Check service health
```

### Integration Tests

```bash
# Run full E2E tests against Docker Compose stack
make test-integration
```

---

## API Reference

### Submit Code

```http
POST /api/v1/submissions
Content-Type: application/json

{
  "source_code": "print('Hello, World!')",
  "language": "python",
  "stdin": ""
}
```

**Response (202 Accepted)**:
```json
{
  "id": "01912345-6789-7abc-def0-123456789abc",
  "status": "QUEUED",
  "created_at": "2026-02-20T10:00:00Z"
}
```

### Get Result

```http
GET /api/v1/submissions/:id
```

### WebSocket Stream

```
ws://localhost:8080/ws/submissions/:id
```

### Health Check

```http
GET /health
```

### List Languages

```http
GET /api/v1/languages
```

---

## Supported Languages

| Language | Version | Time Limit | Memory Limit |
|----------|---------|-----------|-------------|
| Python | 3.12 | 10s | 256 MB |
| C++ | 17 (g++ 13) | 10s (compile) + 10s (run) | 512 MB (compile), 256 MB (run) |

---

## Development

```bash
make help               # Show all available commands
make build              # Build all services
make test               # Run all unit tests
make test-integration   # Run E2E integration tests
make lint               # Lint Go + Frontend
make fmt                # Format Go code
make deps               # Install all dependencies
make clean              # Clean build artifacts
make docker-build       # Build all Docker images
make docker-push        # Push images to GHCR
make health             # Check health of running services
make monitoring-up      # Start Prometheus + Grafana
make monitoring-status  # Check monitoring endpoints
```

### Kubernetes (k3s) Deployment

```bash
# Full cluster setup (installs k3s, KEDA, cert-manager, nginx-ingress)
sudo make k8s-setup

# Or apply manifests only (if cluster already exists)
make k8s-apply

# Check status
make k8s-status

# Tail logs
make k8s-logs

# Teardown
sudo make k8s-teardown
```

#### Manifest Structure (`infra/k8s/`)

| File | Resources |
|------|-----------|
| `namespace.yaml` | Namespace, ResourceQuota, LimitRange |
| `secrets.yaml` | PostgreSQL, RabbitMQ, Redis, API, Worker secrets |
| `configmaps.yaml` | API config, Worker config, nsjail configs, RabbitMQ config, PG init scripts |
| `postgres-statefulset.yaml` | StatefulSet (1 replica), headless + client Service, PDB |
| `rabbitmq-statefulset.yaml` | StatefulSet (3 replicas, quorum queues), headless + client Service, PDB |
| `redis-statefulset.yaml` | StatefulSet (1 replica, AOF), headless + client Service |
| `api-deployment.yaml` | Deployment (3 replicas), Service, ServiceAccount, PDB |
| `worker-deployment.yaml` | Deployment (KEDA-managed, 2–50), Service, ServiceAccount, PDB |
| `ingress.yaml` | ClusterIssuer (Let's Encrypt), Ingress (TLS, WebSocket, CORS), Frontend Deployment + Service + PDB |
| `keda-scaledobject.yaml` | ScaledObject (queue-based), TriggerAuthentication, API HPA (CPU) |
| `network-policies.yaml` | Default deny-all, per-component ingress/egress rules |
| `kustomization.yaml` | Kustomize base (ordered resource application) |

#### Worker Auto-Scaling (KEDA)

Workers scale based on RabbitMQ `execution_tasks` queue depth:
- **Trigger**: Queue length > 15 messages per worker
- **Min replicas**: 2 | **Max replicas**: 50
- **Scale-up**: +5 pods or +100% every 30s (whichever is larger)
- **Scale-down**: -2 pods every 60s (120s stabilization window)

Verify with: `kubectl get pods -n sentinel -w`

#### Network Policies

- **Default**: Deny-all ingress + egress in `sentinel` namespace
- **API**: Receives from nginx-ingress → talks to PG, RabbitMQ, Redis
- **Worker**: Receives health probes → talks to PG, RabbitMQ, Redis (no internet)
- **PostgreSQL/Redis**: Accept only from API + Worker
- **RabbitMQ**: Accept AMQP from API + Worker, inter-node clustering, Prometheus scrape
- **Monitoring**: Prometheus can scrape all sentinel pods; Grafana reaches Prometheus; postgres-exporter reaches PG

### Observability (Prometheus + Grafana)

Sentinel ships a full observability stack for both local development and Kubernetes.

#### Local (Docker Compose)

```bash
make monitoring-up       # Start Prometheus + Grafana
make monitoring-status   # Check endpoints
make monitoring-down     # Stop monitoring stack
```

| Service | URL | Credentials |
|---------|-----|-------------|
| Prometheus | [http://localhost:9091](http://localhost:9091) | — |
| Grafana | [http://localhost:3001](http://localhost:3001) | admin / sentinel |

#### Kubernetes

```bash
make k8s-monitoring-apply      # Deploy monitoring stack
make k8s-monitoring-status     # Check pods/services
make k8s-monitoring-portforward  # Port-forward Grafana + Prometheus
make k8s-monitoring-delete     # Teardown monitoring
```

#### Metrics Exposed

| Component | Port | Path | Key Metrics |
|-----------|------|------|-------------|
| **API** | 8080 | `/metrics` | Default Go/Gin metrics, HTTP request counts |
| **Worker** | 9090 | `/metrics` | `sentinel_executions_total`, `sentinel_execution_duration_seconds`, `sentinel_workers_active`, `sentinel_sandbox_failures_total` |
| **RabbitMQ** | 15692 | `/metrics` | Queue depth, message rates, consumer counts |
| **PostgreSQL** | 9187 | `/metrics` | Connection counts, database size, query stats (via postgres-exporter) |

#### Grafana Dashboards

| Dashboard | Description |
|-----------|-------------|
| **Sentinel Overview** | Execution rates, error rates, active workers, p50/p90/p99 latencies, sandbox failures |
| **Worker Health** | Per-pod execution rates, duration heatmap, error rates, CPU/memory usage |
| **Infrastructure** | PostgreSQL connections/size, RabbitMQ queue depth/throughput, Redis clients/memory, node CPU/memory |

#### Alerting Rules

| Alert | Condition | Severity |
|-------|-----------|----------|
| `SentinelQueueBacklog` | Queue > 1000 messages for 5m | ⚠️ Warning |
| `SentinelWorkerDown` | Active workers < 1 for 3m | 🔴 Critical |
| `SentinelHighErrorRate` | Error rate > 10% for 5m | ⚠️ Warning |
| `SentinelSandboxFailures` | > 50 failures in 5m | 🔴 Critical |

#### Monitoring Manifest Structure (`infra/k8s/monitoring/`)

| File | Resources |
|------|-----------|
| `namespace.yaml` | Monitoring namespace |
| `prometheus-rbac.yaml` | ServiceAccount, ClusterRole, ClusterRoleBinding |
| `prometheus-config.yaml` | Prometheus ConfigMap (scrape configs for API, Worker, RabbitMQ, postgres-exporter, nodes) |
| `alerting-rules.yaml` | 4 alerting rules (queue backlog, worker down, error rate, sandbox failures) |
| `prometheus-deployment.yaml` | PVC (10Gi), Deployment, Service |
| `grafana-config.yaml` | Datasource + dashboard provider ConfigMaps |
| `grafana-dashboards.yaml` | 3 dashboard JSON ConfigMaps (Overview, Worker Health, Infrastructure) |
| `grafana-deployment.yaml` | PVC (2Gi), Deployment, Service |
| `postgres-exporter.yaml` | Deployment, Service (connects to sentinel-postgres) |
| `network-policies.yaml` | Prometheus scrape, Grafana→Prometheus, postgres-exporter→PG |
| `kustomization.yaml` | Monitoring Kustomize base |

### CI/CD

The CI pipeline (`.github/workflows/ci.yml`) runs automatically on every push and PR to `main`:

| Job | What it does |
|-----|-------------|
| `lint-api` / `lint-worker` | golangci-lint on Go code |
| `test-api` / `test-worker` | Unit tests with race detector + coverage |
| `lint-frontend` / `build-frontend` | ESLint + Vite production build |
| `integration-test` | Full Docker Compose stack + E2E test script |
| `build-images` | Build & push to GHCR (main only, SHA + latest tags) |

The deploy workflow (`.github/workflows/deploy.yml`) can be triggered manually or auto-fires after CI passes on `main`.

---

## Roadmap

See [MASTER_PLAN.md](./MASTER_PLAN.md) for the full 10-phase development plan:

- ✅ **Phase 0**: Project scaffolding & local dev environment
- ✅ **Phase 1**: Sandbox development (nsjail hardening)
- ✅ **Phase 2**: API gateway (REST + WebSocket + rate limiting)
- ✅ **Phase 3**: Execution worker (ACK-after-execute, Prometheus metrics)
- ✅ **Phase 4**: Frontend (Monaco editor, real-time results, history)
- ✅ **Phase 5**: Dockerization & Docker Compose integration
- ✅ **Phase 6**: CI/CD pipeline (GitHub Actions, GHCR, integration tests)
- ✅ **Phase 7**: Kubernetes deployment (k3s + KEDA + network policies)
- ✅ **Phase 8**: Observability (Prometheus + Grafana + alerting)
- 🔲 **Phase 9**: Documentation & launch

---

## License

[MIT](./LICENSE)