# Project Sentinel — Final Master Prompt & Phase-Wise Implementation Plan

---

## Feature: Distributed Remote Code Execution Engine (Full System)

### Context

This is a **greenfield project** — the entire system is being built from scratch. The repository currently contains only planning documents (`plan.md`, `prompt-refine.md`). The system will be structured as a **monorepo** containing:

- **`/api`** — Go API Gateway (Gin framework)
- **`/worker`** — Go Execution Worker (Clean Architecture)
- **`/frontend`** — Web client (React + TypeScript + Vite)
- **`/sandbox`** — nsjail configs, Kafel policies, language runtimes
- **`/infra`** — Kubernetes manifests, Helm charts, KEDA configs
- **`/migrations`** — PostgreSQL schema + migration files
- **`/scripts`** — Build, deploy, and load-testing scripts
- **`/.github`** — GitHub Actions CI/CD pipelines
- **`/docs`** — Architecture diagrams, API documentation
- **`docker-compose.yml`** — Local development stack

**Target Deployment:** Self-hosted Kubernetes cluster (k3s)  
**CI/CD:** GitHub Actions  
**Languages Supported:** Python 3.12 & C++17 (both from Phase 1)

---

### Requirements

#### Core RCE Engine (Priority 1)
- [ ] Accept code submissions via REST API with language, source code, stdin, time limit, memory limit
- [ ] Generate UUIDv7 per submission, return `202 Accepted` immediately
- [ ] Publish execution tasks to RabbitMQ Quorum Queues
- [ ] Go workers consume tasks with `prefetch=1` backpressure
- [ ] Execute untrusted code inside nsjail sandboxes with:
  - `pivot_root` filesystem isolation (read-only tmpfs)
  - cgroups v2 memory, PID, and CPU limits
  - seccomp-bpf via Kafel allowlist policies
- [ ] Capture stdout, stderr, exit code, execution time, memory usage
- [ ] Store results in PostgreSQL with state machine: `QUEUED → COMPILING → RUNNING → SUCCESS | RUNTIME_ERROR | COMPILATION_ERROR | TIMEOUT | MEMORY_LIMIT_EXCEEDED`
- [ ] Support both Python 3.12 and C++17 (g++) from day one
- [ ] Redis-based idempotency via `ZADD NX` to prevent duplicate execution
- [ ] Dead Letter Exchange (DLX) for poisoned/failed messages

#### API Gateway
- [ ] `POST /api/v1/submissions` — Submit code
- [ ] `GET /api/v1/submissions/:id` — Poll result by UUID
- [ ] `GET /api/v1/submissions/:id/stream` — WebSocket for real-time status updates
- [ ] `GET /api/v1/languages` — List supported languages with version info
- [ ] `GET /api/v1/health` — Health check (RabbitMQ, PostgreSQL, Redis connectivity)
- [ ] Rate limiting via Redis sliding window (100 req/min per IP)

#### Frontend Client
- [ ] React + TypeScript + Vite SPA
- [ ] Monaco Editor (VS Code editor component) with syntax highlighting for Python and C++
- [ ] Language selector dropdown
- [ ] stdin input textarea
- [ ] Submit button → shows real-time status via WebSocket
- [ ] Results panel showing: stdout, stderr, execution time, memory used, verdict
- [ ] Submission history list (polling `/submissions?user_id=...`)
- [ ] Responsive layout, dark theme

#### Infrastructure & Deployment
- [ ] Docker Compose for local development (API, Worker, RabbitMQ, PostgreSQL, Redis)
- [ ] Multi-stage Dockerfiles for API and Worker (Worker includes nsjail binary)
- [ ] k3s Kubernetes manifests:
  - Deployments for API, Worker
  - StatefulSets for RabbitMQ, PostgreSQL, Redis
  - Nginx Ingress with TLS
  - Node taints for worker isolation (`workload=untrusted:NoSchedule`)
  - KEDA ScaledObject (scale on RabbitMQ queue depth, 15 msgs = +1 pod)
- [ ] Prometheus + Grafana observability stack

#### CI/CD & Testing
- [ ] GitHub Actions pipeline:
  - Go lint (`golangci-lint`) + test on every PR
  - Frontend lint (`eslint`) + build on every PR
  - Integration tests with Docker Compose (spin up full stack, submit code, assert results)
  - Docker image build + push to container registry on merge to `main`
- [ ] Unit tests for Go API handlers, worker use cases, repository layer (mock interfaces)
- [ ] Integration tests: submit Python + C++ code, verify correct stdout, verify timeout enforcement, verify memory limit enforcement
- [ ] Load test script (k6 or vegeta) simulating 1000+ concurrent submissions

---

### Technical Approach

#### Go Backend
- **Framework:** Gin v1.10+ for HTTP routing
- **AMQP Client:** `github.com/rabbitmq/amqp091-go`
- **PostgreSQL Driver:** `github.com/jackc/pgx/v5` with connection pooling
- **Redis Client:** `github.com/redis/go-redis/v9`
- **UUID Generation:** `github.com/google/uuid` (UUIDv7)
- **WebSocket:** `github.com/gorilla/websocket`
- **Metrics:** `github.com/prometheus/client_golang`
- **Logging:** `go.uber.org/zap` (structured JSON logging)
- **Config:** `github.com/spf13/viper` (env vars + config files)
- **Architecture:** Clean Architecture (domain → repository → usecase → delivery)
- **Process Management:** `os/exec` with `SysProcAttr{Setpgid: true}`, `context.WithTimeout`, `syscall.Kill(-pid, SIGKILL)` for process group cleanup

#### Sandbox (nsjail)
- Compile nsjail from source (include in Worker Docker image)
- Two Kafel policies: `python.policy` and `cpp.policy`
- Python policy: allow `read, write, openat, close, fstat, mmap, mprotect, munmap, brk, ioctl, access, execve, arch_prctl, set_tid_address, set_robust_list, prlimit64, getrandom, futex, clone, exit_group, rt_sigaction, rt_sigprocmask, getpid, getuid, getgid, geteuid, getegid, getcwd, readlink, sysinfo, sigaltstack`
- C++ policy: similar but also allow `vfork, wait4, pipe2` for compilation
- cgroups v2 limits: `memory.max=256M`, `pids.max=64`, `cpu.max=100000 100000` (1 CPU)
- Default time limit: 5 seconds (configurable per submission)
- Mount: read-only bind `/usr`, `/lib`, `/lib64`; read-write tmpfs at `/tmp/work`

#### Frontend
- **Build Tool:** Vite 5+
- **Framework:** React 18+ with TypeScript
- **Editor:** `@monaco-editor/react`
- **HTTP Client:** Axios
- **WebSocket:** Native WebSocket API
- **Styling:** Tailwind CSS
- **State Management:** React hooks (useState, useEffect, useRef)

#### Database Schema
```sql
-- Enable pgcrypto for UUIDv7 generation
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TYPE execution_status AS ENUM (
    'QUEUED',
    'COMPILING',
    'RUNNING',
    'SUCCESS',
    'COMPILATION_ERROR',
    'RUNTIME_ERROR',
    'TIMEOUT',
    'MEMORY_LIMIT_EXCEEDED',
    'INTERNAL_ERROR'
);

CREATE TYPE language AS ENUM ('python', 'cpp');

CREATE TABLE execution_jobs (
    job_id       UUID PRIMARY KEY,         -- UUIDv7
    language     language NOT NULL,
    source_code  TEXT NOT NULL,
    stdin        TEXT DEFAULT '',
    stdout       TEXT DEFAULT '',
    stderr       TEXT DEFAULT '',
    status       execution_status NOT NULL DEFAULT 'QUEUED',
    exit_code    INT,
    time_used_ms INT,                      -- wall-clock ms
    memory_used_kb INT,                    -- peak RSS in KB
    time_limit_ms  INT NOT NULL DEFAULT 5000,
    memory_limit_kb INT NOT NULL DEFAULT 262144, -- 256 MB
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
) PARTITION BY RANGE (created_at);

-- Create initial partition
CREATE TABLE execution_jobs_2026_q1 PARTITION OF execution_jobs
    FOR VALUES FROM ('2026-01-01') TO ('2026-04-01');

-- Partial index for active jobs (stays small in RAM)
CREATE INDEX idx_active_jobs ON execution_jobs(job_id)
    WHERE status IN ('QUEUED', 'COMPILING', 'RUNNING');

-- Index for polling by status
CREATE INDEX idx_jobs_status ON execution_jobs(status, created_at);
```

#### RabbitMQ Topology
```
Exchange: sentinel.direct (type: direct, durable: true)
  ├── Queue: execution_tasks (quorum, durable, routing_key: "execute")
  │     └── DLX: sentinel.dlx
  └── Queue: dead_letter_queue (quorum, durable, bound to sentinel.dlx)
```

---

### Edge Cases

| Edge Case | How to Handle |
|-----------|---------------|
| **Fork bomb** (`while(1) fork()`) | nsjail `pids.max=64` in cgroups v2; kernel refuses new PIDs beyond limit |
| **Infinite loop** | `context.WithTimeout` in Go worker → `syscall.Kill(-pgid, SIGKILL)` terminates entire process group |
| **Memory exhaustion** (`malloc` in loop) | cgroups v2 `memory.max=256M` → OOM killer terminates process; worker reports `MEMORY_LIMIT_EXCEEDED` |
| **Network access attempt** (`socket()` syscall) | seccomp-bpf Kafel policy blocks `socket`, `connect`, `bind`, `listen` → `SECCOMP_RET_KILL` |
| **Filesystem escape** (`../../etc/passwd`) | `pivot_root` + read-only mounts; old root is unmounted, no path back |
| **RabbitMQ goes down** | Publisher retries with exponential backoff; messages durably persisted in Quorum Queues survive broker restarts |
| **Duplicate message delivery** | Redis `ZADD NX` idempotency check; duplicate is ACKed and discarded without re-execution |
| **Worker crash mid-execution** | RabbitMQ heartbeat detects disconnect → message is requeued → another worker picks it up |
| **Database connection failure** | pgx connection pool with retry + exponential backoff; worker does NOT ack message until DB write succeeds |
| **Compilation error in C++** | `g++` stderr is captured; status set to `COMPILATION_ERROR`; no execution phase is attempted |
| **Empty source code submitted** | API validates request body; returns `400 Bad Request` with descriptive error |
| **Oversized source code (>1MB)** | API enforces `max_body_size` via Gin middleware; returns `413 Payload Too Large` |
| **Rapid API abuse** | Redis sliding window rate limiter (100 req/min/IP); returns `429 Too Many Requests` |

---

### Acceptance Criteria

- [ ] A user can submit Python code via the frontend, see real-time status updates, and view correct stdout/stderr/verdict
- [ ] A user can submit C++ code via the frontend, it compiles and runs correctly, showing compilation errors if present
- [ ] Fork bombs, infinite loops, and memory bombs are killed within configured limits without affecting other submissions
- [ ] Submitting code that attempts `socket()` or filesystem escape is killed immediately by seccomp
- [ ] System handles 100 concurrent submissions in integration tests without dropping any
- [ ] Worker crash during execution results in automatic retry (message requeued by RabbitMQ)
- [ ] Duplicate messages are detected and skipped via Redis idempotency
- [ ] GitHub Actions CI passes: lint, unit tests, integration tests, Docker build
- [ ] Full stack runs locally via `docker-compose up`
- [ ] Kubernetes manifests deploy correctly to k3s with KEDA autoscaling functional
- [ ] Prometheus metrics are exposed and scrapeable; Grafana dashboards show execution latency, queue depth, worker utilization
- [ ] API returns proper HTTP status codes: `202` for submission, `200` for results, `404` for unknown ID, `400` for bad input, `429` for rate limit

---

---

# Phase-Wise Implementation Plan

## Phase 0: Project Scaffolding & Local Dev Environment
**Duration:** 1–2 days  
**Goal:** Set up the monorepo structure, tooling, and local Docker Compose stack.

### Tasks
1. Initialize Go modules for `api/` and `worker/` (`go mod init github.com/Harsh-BH/Sentinel/api`)
2. Initialize Vite + React + TypeScript project in `frontend/`
3. Create `docker-compose.yml` with:
   - RabbitMQ 3.13 (management plugin enabled, port 5672 + 15672)
   - PostgreSQL 16 (port 5432, init script for schema)
   - Redis 7 (port 6379)
4. Create initial PostgreSQL migration files in `migrations/`
5. Set up `.github/workflows/ci.yml` skeleton (lint + test steps)
6. Create `Makefile` with targets: `api`, `worker`, `frontend`, `up`, `down`, `test`, `lint`
7. Create `.env.example` with all configuration variables
8. Write project `README.md` with architecture overview and setup instructions

### Directory Structure After Phase 0
```
Sentinel/
├── api/
│   ├── cmd/
│   │   └── server/
│   │       └── main.go
│   ├── internal/
│   │   ├── config/
│   │   ├── delivery/
│   │   │   └── http/
│   │   ├── domain/
│   │   ├── repository/
│   │   └── usecase/
│   ├── go.mod
│   └── go.sum
├── worker/
│   ├── cmd/
│   │   └── worker/
│   │       └── main.go
│   ├── internal/
│   │   ├── config/
│   │   ├── delivery/
│   │   │   └── amqp/
│   │   ├── domain/
│   │   ├── executor/
│   │   ├── repository/
│   │   └── usecase/
│   ├── go.mod
│   └── go.sum
├── frontend/
│   ├── src/
│   │   ├── components/
│   │   ├── hooks/
│   │   ├── services/
│   │   ├── types/
│   │   ├── App.tsx
│   │   └── main.tsx
│   ├── index.html
│   ├── package.json
│   ├── tailwind.config.js
│   ├── tsconfig.json
│   └── vite.config.ts
├── sandbox/
│   ├── nsjail/
│   │   ├── python.cfg
│   │   └── cpp.cfg
│   └── policies/
│       ├── python.policy   (Kafel)
│       └── cpp.policy      (Kafel)
├── infra/
│   └── k8s/
│       ├── namespace.yaml
│       ├── api-deployment.yaml
│       ├── worker-deployment.yaml
│       ├── rabbitmq-statefulset.yaml
│       ├── postgres-statefulset.yaml
│       ├── redis-statefulset.yaml
│       ├── ingress.yaml
│       └── keda-scaledobject.yaml
├── migrations/
│   ├── 001_initial_schema.up.sql
│   └── 001_initial_schema.down.sql
├── scripts/
│   ├── load-test.sh
│   └── setup-k3s.sh
├── .github/
│   └── workflows/
│       └── ci.yml
├── docker-compose.yml
├── Makefile
├── .env.example
├── .gitignore
└── README.md
```

### Deliverables
- [ ] `docker-compose up` starts RabbitMQ, PostgreSQL, Redis successfully
- [ ] Database schema is applied on startup
- [ ] Go modules compile with `go build ./...`
- [ ] Frontend dev server starts with `npm run dev`

---

## Phase 1: Sandbox Development (nsjail + Kafel Policies)
**Duration:** 3–4 days  
**Goal:** Build and validate the nsjail sandbox independently, before any Go integration.

### Tasks
1. Write nsjail protobuf config for Python execution (`sandbox/nsjail/python.cfg`):
   - `pivot_root` to ephemeral tmpfs
   - Read-only bind mounts: `/usr`, `/lib`, `/lib64`, `/bin` (for Python interpreter)
   - Read-write tmpfs at `/tmp/work` (where user code lives)
   - cgroups v2: `memory.max=256M`, `pids.max=64`, `cpu.max=100000 100000`
   - Time limit: 5 seconds (via nsjail `--time_limit`)
2. Write nsjail protobuf config for C++ execution (`sandbox/nsjail/cpp.cfg`):
   - Same isolation as Python but also bind `/usr/bin/g++` and required libs
   - Two-phase execution: compile phase (10s limit) → run phase (5s limit)
3. Write Kafel seccomp policy for Python (`sandbox/policies/python.policy`):
   - Allowlist: `read, write, openat, close, fstat, mmap, mprotect, munmap, brk, rt_sigaction, rt_sigprocmask, rt_sigreturn, ioctl, access, execve, arch_prctl, clone, exit_group, getpid, getuid, getgid, geteuid, getegid, futex, set_tid_address, set_robust_list, prlimit64, getrandom, getcwd, readlink, sysinfo, sigaltstack, clock_gettime, clock_getres, gettimeofday, nanosleep, lseek, pipe, dup, dup2, fcntl, getdents64, newfstatat, statx`
   - Default action: `KILL`
4. Write Kafel seccomp policy for C++ (`sandbox/policies/cpp.policy`):
   - Extends Python policy with: `vfork, wait4, pipe2, rename, unlink, mkdir`
   - Default action: `KILL`
5. Create test scripts to validate sandbox:
   - `scripts/test-sandbox-python.sh`: Run "Hello World", fork bomb, infinite loop, memory bomb, network attempt
   - `scripts/test-sandbox-cpp.sh`: Compile + run "Hello World", compilation error, runtime segfault
6. Add `Dockerfile.nsjail` that builds nsjail from source (used as a build stage in Worker Dockerfile later)

### Deliverables
- [ ] `nsjail --config python.cfg -- /usr/bin/python3 /tmp/work/code.py` executes Hello World and returns stdout
- [ ] Fork bomb is killed (pids.max exceeded)
- [ ] Infinite loop is killed after time limit
- [ ] Memory bomb is killed (OOM)
- [ ] `socket()` call is killed by seccomp
- [ ] C++ code compiles and runs correctly in sandbox
- [ ] C++ compilation errors are captured in stderr

---

## Phase 2: Go API Gateway
**Duration:** 3–4 days  
**Goal:** Build the stateless API gateway that validates, persists, and publishes submissions.

### Tasks
1. **Domain layer** (`api/internal/domain/`):
   - `job.go`: `Job` struct (JobID, Language, SourceCode, Stdin, Status, Stdout, Stderr, ExitCode, TimeUsedMs, MemoryUsedKB, TimeLimitMs, MemoryLimitKB, CreatedAt, UpdatedAt)
   - `errors.go`: Custom error types (ErrJobNotFound, ErrInvalidLanguage, ErrPayloadTooLarge)
2. **Repository layer** (`api/internal/repository/`):
   - `job_repository.go`: Interface `JobRepository` with `Create(ctx, Job) error`, `GetByID(ctx, uuid) (Job, error)`, `UpdateStatus(ctx, uuid, status) error`
   - `postgres/job_repo.go`: pgx implementation of `JobRepository`
3. **Usecase layer** (`api/internal/usecase/`):
   - `submit_job.go`: Validate input → Generate UUIDv7 → Insert `QUEUED` row in PostgreSQL → Publish to RabbitMQ → Return UUID
   - `get_job.go`: Fetch job by ID from PostgreSQL
4. **Delivery layer** (`api/internal/delivery/http/`):
   - `router.go`: Gin router setup with middleware chain
   - `submission_handler.go`: `POST /api/v1/submissions`, `GET /api/v1/submissions/:id`
   - `websocket_handler.go`: `GET /api/v1/submissions/:id/stream` (WebSocket upgrade, poll DB every 500ms until terminal state)
   - `health_handler.go`: `GET /api/v1/health`
   - `language_handler.go`: `GET /api/v1/languages`
   - `middleware/rate_limiter.go`: Redis sliding window rate limiter
   - `middleware/cors.go`: CORS config for frontend
   - `middleware/request_id.go`: Inject X-Request-ID header
5. **Config** (`api/internal/config/`):
   - `config.go`: Viper-based config loading from env vars
6. **Publisher** (`api/internal/publisher/`):
   - `rabbitmq.go`: AMQP connection management, channel pooling, publish to `sentinel.direct` exchange with routing key `execute`
7. **Main** (`api/cmd/server/main.go`):
   - Wire up all dependencies, start Gin server
8. **Unit tests**:
   - Mock `JobRepository` interface
   - Test `SubmitJob` usecase with mock repo
   - Test HTTP handlers with `httptest`

### Deliverables
- [ ] `POST /api/v1/submissions` returns `202` with `{ "job_id": "uuid" }`
- [ ] `GET /api/v1/submissions/:id` returns current job state
- [ ] WebSocket streams real-time status updates
- [ ] Rate limiter returns `429` after 100 requests/min
- [ ] Health check reports connectivity to RabbitMQ, PostgreSQL, Redis
- [ ] Unit tests pass with `go test ./...`
- [ ] Messages appear in RabbitMQ management UI after submission

---

## Phase 3: Go Execution Worker
**Duration:** 4–5 days  
**Goal:** Build the worker that consumes from RabbitMQ, executes code in nsjail, and updates results.

### Tasks
1. **Domain layer** (`worker/internal/domain/`):
   - Shared domain types (can reference `api/internal/domain` or duplicate for decoupling)
   - `execution.go`: `ExecutionRequest`, `ExecutionResult` structs
2. **Executor** (`worker/internal/executor/`):
   - `sandbox.go`: Core execution logic:
     - Write source code to ephemeral tmpfs directory
     - For C++: invoke nsjail with `g++` to compile → check exit code → invoke nsjail to run binary
     - For Python: invoke nsjail with `python3` to run script
     - Use `os/exec.CommandContext` with `SysProcAttr{Setpgid: true}`
     - Capture stdout, stderr via pipes
     - Parse nsjail output for time/memory usage
     - On timeout: `syscall.Kill(-pgid, SIGKILL)`
     - Cleanup: remove tmpfs files
   - `sandbox_test.go`: Test executor with real nsjail (integration test, requires nsjail binary)
3. **Repository layer** (`worker/internal/repository/`):
   - `job_repository.go`: Interface for `UpdateStatus`, `SetResult`
   - `postgres/job_repo.go`: pgx implementation
   - `redis/idempotency.go`: `ZADD NX` lock acquisition and release
4. **Usecase layer** (`worker/internal/usecase/`):
   - `execute_job.go`: Orchestration flow:
     1. Acquire Redis idempotency lock (`ZADD NX`)
     2. If duplicate → ACK and return
     3. Update status to `COMPILING` (for C++) or `RUNNING` (for Python)
     4. Invoke executor
     5. Update status to terminal state with result
     6. Release Redis lock (set TTL expiry)
     7. ACK RabbitMQ message
5. **Delivery layer** (`worker/internal/delivery/amqp/`):
   - `consumer.go`: AMQP consumer with `prefetch=1`, message deserialization
   - `dispatcher.go`: Fan-out to buffered Go channel
6. **Worker pool** (`worker/internal/pool/`):
   - `pool.go`: Fixed-size goroutine pool reading from buffered channel
   - Graceful shutdown on `SIGTERM`/`SIGINT` (drain in-flight jobs before exit)
7. **Config** (`worker/internal/config/`):
   - `config.go`: Pool size, nsjail binary path, sandbox configs path, timeouts
8. **Main** (`worker/cmd/worker/main.go`):
   - Wire up dependencies, start consumer, start worker pool, block on signal
9. **Metrics** (`worker/internal/metrics/`):
   - Prometheus counters: `sentinel_executions_total{language, status}`
   - Prometheus histogram: `sentinel_execution_duration_seconds{language}`
   - Prometheus gauge: `sentinel_workers_active`

### Deliverables
- [ ] Worker consumes messages from RabbitMQ
- [ ] Python "Hello World" executes and result appears in database
- [ ] C++ "Hello World" compiles, runs, result in database
- [ ] Fork bomb / infinite loop / memory bomb results in correct error status
- [ ] Duplicate message is detected and skipped
- [ ] Worker gracefully shuts down on SIGTERM (finishes in-flight, then exits)
- [ ] Prometheus metrics exposed at `/metrics` on worker HTTP port
- [ ] Process group cleanup verified (no orphan processes after timeout kill)

---

## Phase 4: Frontend Client
**Duration:** 3–4 days  
**Goal:** Build the React frontend with Monaco editor, real-time results, and submission history.

### Tasks
1. **Project setup**: Vite + React + TypeScript + Tailwind CSS + Monaco Editor
2. **Components**:
   - `CodeEditor.tsx`: Monaco editor with language switching (Python/C++)
   - `LanguageSelector.tsx`: Dropdown to select Python or C++
   - `StdinInput.tsx`: Textarea for standard input
   - `SubmitButton.tsx`: Submit with loading state
   - `ResultPanel.tsx`: Display stdout, stderr, verdict badge, time, memory
   - `StatusBadge.tsx`: Color-coded status badge (green=success, red=error, yellow=running)
   - `SubmissionHistory.tsx`: List of past submissions with click-to-view
   - `Header.tsx`: App title + branding
   - `Layout.tsx`: Responsive split-pane layout
3. **Services**:
   - `api.ts`: Axios client for `POST /submissions`, `GET /submissions/:id`, `GET /languages`
   - `websocket.ts`: WebSocket connection manager for real-time status
4. **Types**:
   - `submission.ts`: TypeScript interfaces matching API response shapes
5. **Hooks**:
   - `useSubmission.ts`: Submit code, manage loading/error state
   - `useWebSocket.ts`: Connect to WebSocket, receive status updates
   - `useSubmissionHistory.ts`: Fetch and cache recent submissions
6. **Styling**:
   - Dark theme (slate/zinc color palette)
   - Responsive: editor on left, results on right (stacked on mobile)
   - Smooth transitions for status changes
7. **Config**:
   - `vite.config.ts`: Proxy `/api` to Go backend in development
   - Environment variable for API base URL

### Deliverables
- [ ] User can write Python/C++ code in Monaco editor
- [ ] User can submit code and see real-time status updates via WebSocket
- [ ] Results panel shows stdout, stderr, verdict, time, memory
- [ ] Submission history shows past submissions
- [ ] Responsive layout works on desktop and mobile
- [ ] Dark theme applied consistently
- [ ] Frontend builds successfully (`npm run build`)

---

## Phase 5: Dockerization & Docker Compose Integration
**Duration:** 2–3 days  
**Goal:** Containerize all services and validate the full stack runs locally.

### Tasks
1. **`api/Dockerfile`**: Multi-stage build (Go build → scratch/alpine runtime)
2. **`worker/Dockerfile`**: Multi-stage build:
   - Stage 1: Build nsjail from source (Ubuntu base, install protobuf, libnl, flex, bison)
   - Stage 2: Build Go worker binary
   - Stage 3: Runtime with nsjail binary, Python 3.12, g++, required libs
   - Must run with `--privileged` or specific capabilities (`SYS_ADMIN`, `SYS_PTRACE`, `NET_ADMIN`) for namespace creation
3. **`frontend/Dockerfile`**: Multi-stage (node build → nginx serve)
4. **`docker-compose.yml`** (complete):
   - `api` service (depends_on: rabbitmq, postgres, redis)
   - `worker` service (privileged: true, depends_on: rabbitmq, postgres, redis)
   - `frontend` service (depends_on: api)
   - `rabbitmq` service (management plugin, health check)
   - `postgres` service (init script mounts `migrations/`)
   - `redis` service
   - Shared network, named volumes for data persistence
5. **`docker-compose.test.yml`**: Override for CI — no volume persistence, exit after tests
6. Health check scripts for all services

### Deliverables
- [ ] `docker-compose up --build` starts the entire stack
- [ ] Submit code via frontend → results appear correctly
- [ ] `docker-compose down -v` cleanly tears down
- [ ] All containers have health checks
- [ ] Worker container can run nsjail (namespace creation works)

---

## Phase 6: CI/CD Pipeline (GitHub Actions)
**Duration:** 2–3 days  
**Goal:** Automated testing, linting, and Docker image builds on every PR and merge.

### Tasks
1. **`.github/workflows/ci.yml`**:
   ```yaml
   on: [push, pull_request]
   jobs:
     lint-go:
       - golangci-lint run (api/ and worker/)
     test-go:
       - go test ./... -v -race -coverprofile=coverage.out (api/ and worker/)
       - Upload coverage to Codecov
     lint-frontend:
       - npm run lint (frontend/)
     build-frontend:
       - npm run build (frontend/)
     integration-test:
       - docker-compose -f docker-compose.test.yml up --build --abort-on-container-exit
       - Run test script: submit Python + C++ code, assert results via API
       - Verify timeout enforcement, memory limit, compilation error handling
     build-images:
       - (only on merge to main)
       - Build and push Docker images to GHCR (ghcr.io/harsh-bh/sentinel-api, sentinel-worker, sentinel-frontend)
   ```
2. **`scripts/integration-test.sh`**:
   - Wait for API health check
   - Submit Python "print('hello')" → assert stdout == "hello\n"
   - Submit C++ hello world → assert stdout
   - Submit Python infinite loop → assert status == "TIMEOUT"
   - Submit C++ with syntax error → assert status == "COMPILATION_ERROR"
   - Submit Python memory bomb → assert status == "MEMORY_LIMIT_EXCEEDED"
3. **`.github/workflows/deploy.yml`** (optional, for k3s auto-deploy):
   - SSH to k3s node, pull latest images, `kubectl rollout restart`

### Deliverables
- [ ] CI runs on every PR: lint + test + build
- [ ] Integration tests verify end-to-end correctness
- [ ] Docker images are pushed to GHCR on merge to `main`
- [ ] CI badge in README

---

## Phase 7: Kubernetes Deployment (k3s)
**Duration:** 3–5 days  
**Goal:** Deploy the full system to a self-hosted k3s cluster with production-grade configs.

### Tasks
1. **Namespace**: `sentinel` namespace for all resources
2. **ConfigMaps & Secrets**:
   - Database credentials, RabbitMQ credentials, Redis password
   - nsjail configs mounted as ConfigMap volumes
3. **API Deployment** (`infra/k8s/api-deployment.yaml`):
   - 3 replicas, resource limits (256Mi RAM, 500m CPU)
   - Readiness/liveness probes on `/api/v1/health`
   - Environment variables from ConfigMap/Secret
4. **Worker Deployment** (`infra/k8s/worker-deployment.yaml`):
   - `securityContext: privileged: true` (required for nsjail namespaces)
   - Tolerations for `workload=untrusted:NoSchedule` taint
   - Resource limits (1Gi RAM, 2 CPU per pod)
   - nsjail config volume mounts
5. **RabbitMQ StatefulSet** (3 replicas, Quorum Queues, persistent volumes)
6. **PostgreSQL StatefulSet** (1 primary + 1 replica, persistent volume)
7. **Redis Deployment** (single instance, or Sentinel for HA)
8. **Nginx Ingress** with TLS (cert-manager + Let's Encrypt)
9. **KEDA ScaledObject** (`infra/k8s/keda-scaledobject.yaml`):
   - Scale `sentinel-worker-deployment` based on `execution_tasks` queue length
   - `value: "15"`, `minReplicaCount: 2`, `maxReplicaCount: 50`
10. **Node Taints**: Apply `workload=untrusted:NoSchedule` to worker nodes
11. **Network Policies**: Deny worker pods from accessing PostgreSQL/RabbitMQ internal cluster IPs directly (only via service DNS)
12. **k3s setup script** (`scripts/setup-k3s.sh`):
    - Install k3s
    - Install KEDA via Helm
    - Install cert-manager
    - Apply all manifests

### Deliverables
- [ ] Full system runs on k3s cluster
- [ ] KEDA scales workers based on queue depth (verifiable via `kubectl get pods -w`)
- [ ] Worker pods only scheduled on tainted nodes
- [ ] Ingress serves frontend and API over HTTPS
- [ ] System survives worker pod kill (message is requeued and reprocessed)

---

## Phase 8: Observability (Prometheus + Grafana)
**Duration:** 2–3 days  
**Goal:** Full metrics, dashboards, and alerting.

### Tasks
1. **Prometheus deployment** (k8s or Helm chart `kube-prometheus-stack`)
2. **ServiceMonitor** CRDs for:
   - API pods (`/metrics`)
   - Worker pods (`/metrics`)
   - RabbitMQ (rabbitmq-prometheus plugin)
   - PostgreSQL (postgres-exporter)
3. **Grafana Dashboards** (JSON provisioned):
   - **Sentinel Overview**: Submissions/sec, queue depth, active workers, p95 execution time
   - **Worker Health**: Goroutine count, active/idle workers, sandbox failures
   - **Infrastructure**: RabbitMQ queue depth, PostgreSQL connections, Redis memory
4. **Alerting Rules** (PrometheusRule CRD):
   - `SentinelQueueBacklog`: Queue depth > 1000 for 5 minutes
   - `SentinelWorkerDown`: Active workers < minReplicaCount for 3 minutes
   - `SentinelHighErrorRate`: Error rate > 10% for 5 minutes
   - `SentinelSandboxFailures`: Sandbox failure spike (> 50 in 5 minutes)
5. **Go metrics instrumentation** (already partially done in Phase 3):
   - Ensure bounded cardinality (labels: `language`, `status` only)

### Deliverables
- [ ] Prometheus scrapes all services
- [ ] Grafana dashboards visualize system health
- [ ] Alerts fire correctly (test by killing worker pods)
- [ ] No unbounded cardinality in metrics

---

## Phase 9: Load Testing & Hardening
**Duration:** 2–3 days  
**Goal:** Validate system under stress, fix bottlenecks, document.

### Tasks
1. **Load test script** (`scripts/load-test.sh` using k6 or vegeta):
   - Ramp from 10 to 1000 concurrent submissions over 5 minutes
   - Mix of Python and C++ submissions
   - Assert: 0 dropped requests, p99 < 30s, all results eventually correct
2. **Observe and tune**:
   - pgx connection pool size
   - Worker pool goroutine count
   - RabbitMQ prefetch and channel count
   - KEDA scaling thresholds
3. **Security audit**:
   - Verify no syscall leaks in seccomp policies
   - Verify no filesystem escape possible
   - Verify no network access from sandbox
4. **Documentation**:
   - Update `README.md` with full setup guide
   - `docs/architecture.md` with system diagrams
   - `docs/api.md` with OpenAPI spec
   - `docs/deployment.md` with k3s deployment guide

### Deliverables
- [ ] System handles 1000 concurrent submissions without data loss
- [ ] KEDA scales workers up during load spike and back down after
- [ ] All security tests pass (fork bomb, memory bomb, network, filesystem)
- [ ] Documentation is complete and accurate
- [ ] Project is ready for production use

---

## Summary Timeline

| Phase | Name | Duration | Cumulative |
|-------|------|----------|------------|
| 0 | Project Scaffolding | 1–2 days | 1–2 days |
| 1 | Sandbox Development | 3–4 days | 4–6 days |
| 2 | Go API Gateway | 3–4 days | 7–10 days |
| 3 | Go Execution Worker | 4–5 days | 11–15 days |
| 4 | Frontend Client | 3–4 days | 14–19 days |
| 5 | Dockerization | 2–3 days | 16–22 days |
| 6 | CI/CD Pipeline | 2–3 days | 18–25 days |
| 7 | Kubernetes Deployment | 3–5 days | 21–30 days |
| 8 | Observability | 2–3 days | 23–33 days |
| 9 | Load Testing & Hardening | 2–3 days | 25–36 days |

**Total estimated duration: 25–36 days (5–7 weeks)**

---

## Tech Stack Summary

| Component | Technology |
|-----------|-----------|
| API Gateway | Go 1.22+, Gin, pgx, go-redis, amqp091-go |
| Execution Worker | Go 1.22+, os/exec, nsjail, Clean Architecture |
| Message Broker | RabbitMQ 3.13+ (Quorum Queues, DLX) |
| Database | PostgreSQL 16 (partitioned, SKIP LOCKED) |
| Cache / Locks | Redis 7 (ZADD NX idempotency, rate limiting) |
| Sandbox | nsjail (pivot_root, cgroups v2, seccomp-bpf + Kafel) |
| Frontend | React 18, TypeScript, Vite, Monaco Editor, Tailwind CSS |
| Container Runtime | Docker (multi-stage builds) |
| Orchestration | k3s (self-hosted Kubernetes) |
| Autoscaling | KEDA (RabbitMQ queue-depth trigger) |
| CI/CD | GitHub Actions |
| Observability | Prometheus + Grafana |
| ID Generation | UUIDv7 |
| Logging | zap (structured JSON) |
