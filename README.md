# 🛡️ Sentinel

> **Distributed Remote Code Execution Engine** — Execute untrusted code safely at scale with sub-second latency.

[![CI](https://github.com/Harsh-BH/Sentinel/actions/workflows/ci.yml/badge.svg)](https://github.com/Harsh-BH/Sentinel/actions)
[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go)](https://go.dev)
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
| **API Gateway** | Go 1.22, Gin, pgx/v5, gorilla/websocket |
| **Message Broker** | RabbitMQ 3.13 (Quorum Queues, DLX) |
| **Worker** | Go 1.22, nsjail, os/exec with process groups |
| **Database** | PostgreSQL 16 (partitioned tables, UUIDv7) |
| **Cache** | Redis 7 (idempotency locks, rate limiting) |
| **Observability** | Prometheus + Grafana |
| **Infrastructure** | Docker Compose (dev), k3s + KEDA (prod) |
| **CI/CD** | GitHub Actions |

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
├── .github/workflows/          # CI pipeline
├── docker-compose.yml          # Local development stack
├── Makefile                    # Build & dev commands
└── MASTER_PLAN.md              # Full project specification
```

---

## Quick Start

### Prerequisites

- **Go 1.22+**
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
make dev-infra

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
make up        # Start everything
make down      # Stop everything
make logs      # Follow logs
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
make help          # Show all available commands
make build         # Build all services
make test          # Run all tests
make lint          # Lint Go + Frontend
make fmt           # Format Go code
make deps          # Install all dependencies
make clean         # Clean build artifacts
```

---

## Roadmap

See [MASTER_PLAN.md](./MASTER_PLAN.md) for the full 10-phase development plan:

- ✅ **Phase 0**: Project scaffolding & local dev environment
- 🔲 **Phase 1**: Sandbox development (nsjail hardening)
- 🔲 **Phase 2**: Worker core (execution pipeline)
- 🔲 **Phase 3**: API gateway (REST + WebSocket)
- 🔲 **Phase 4**: Frontend (Monaco + results UI)
- 🔲 **Phase 5**: Integration testing
- 🔲 **Phase 6**: Observability (Prometheus + Grafana)
- 🔲 **Phase 7**: Kubernetes deployment (k3s + KEDA)
- 🔲 **Phase 8**: Performance & hardening
- 🔲 **Phase 9**: Documentation & launch

---

## License

[MIT](./LICENSE)