# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

Sentinel is a distributed remote code execution engine. Untrusted user code (Python, C++) is queued via a Go API, consumed by a Go worker pool, and executed inside an `nsjail` sandbox. Results stream back to a React/Vite frontend over WebSocket.

The repo is a polyglot monorepo with three deployable units (`api/`, `worker/`, `frontend/`) plus shared sandbox configuration (`sandbox/`), DB migrations (`migrations/`), and infra (`infra/k8s/`, `docker-compose.yml`).

## Architecture

### Request flow
1. Frontend POSTs to `POST /api/v1/submissions` → API persists a `QUEUED` row in Postgres `execution_jobs` and publishes to RabbitMQ.
2. API publishes to direct exchange `sentinel.direct` with routing key `execute`, bound to queue `execution_tasks` (quorum queue, with a DLX). Both API and worker independently `QueueDeclare` it idempotently — the declared arguments must match exactly on both sides or RabbitMQ returns a `PRECONDITION_FAILED` and the service crash-loops.
3. Worker consumer in `worker/internal/delivery/amqp` pushes `JobMessage` (with Ack/Nack callbacks) onto a buffered channel sized `2 * pool_size`.
4. `worker/internal/pool` goroutine pool pulls from the channel and runs `usecase.ExecuteJobUsecase`, which: claims an idempotency lock in Redis → updates job status → invokes `executor.SandboxExecutor` → writes the result back to Postgres → ACKs the AMQP delivery (ACK-after-execute, never before).
5. Frontend tracks results either by polling `GET /api/v1/submissions/:id` or via WebSocket at `GET /api/v1/submissions/:id/stream`.

Note: the README mentions paths like `/health` and `/ws/submissions/:id` — the actual routes (see `api/internal/delivery/http/router.go`) are `/api/v1/health` and `/api/v1/submissions/:id/stream`. Trust the router, not the README.

### Clean architecture (Go services)
Both `api/` and `worker/` follow the same layout:
- `cmd/<service>/main.go` — composition root; wires config, pgxpool, redis, rabbitmq, then layers
- `internal/config` — Viper-style env config
- `internal/domain` — pure types (Job, Status, ExecutionRequest/Result, errors). No I/O imports here.
- `internal/repository/{postgres,redis}` — pgx + go-redis adapters
- `internal/usecase` — business logic; depends on domain + repository interfaces only
- `internal/delivery/{http,amqp}` — transport adapters (Gin handlers, AMQP consumer)
- API-only: `internal/publisher` (RabbitMQ producer)
- Worker-only: `internal/executor` (nsjail), `internal/pool` (goroutine pool), `internal/metrics` (Prometheus)

When adding a feature, the dependency direction is `delivery → usecase → repository/domain`. Don't reach across — for example, handlers must not import `repository/postgres` directly.

### Sandbox model
The worker shells out to `nsjail` using protobuf configs in `sandbox/nsjail/{python,cpp}.cfg` and Kafel seccomp policies in `sandbox/policies/{python,cpp}.policy`. `executor.SandboxExecutor.Execute` writes source + stdin to an ephemeral `os.MkdirTemp` workdir, runs nsjail, captures stdout/stderr capped at 64 KB, then deletes the workdir. C++ is two-pass: a compile invocation followed by a run invocation; both are sandboxed separately. If you change the language pipeline you almost certainly need to touch all three of: the executor switch in `worker/internal/executor/sandbox.go`, the nsjail `.cfg`, and the kafel `.policy`.

### Database
`migrations/001_initial_schema.up.sql` defines a single partitioned table `execution_jobs` (range-partitioned by `created_at`, quarterly partitions pre-created for 2026 Q1/Q2). When adding fields, remember it's partitioned — DDL must be issued on the parent. The active-jobs partial index on `status IN ('QUEUED','COMPILING','RUNNING')` is what keeps the polling path cheap; preserve it.

### Frontend
React 18 + Vite + TypeScript + Tailwind + Monaco. API base URL comes from `VITE_API_BASE_URL` / `VITE_WS_BASE_URL` (see `.env.example`). Real-time updates use the WebSocket route above with polling as a fallback.

## Common commands

All targets are documented under `make help`. The ones used most:

```bash
# Local dev (3 terminals, infra in Docker)
make up-infra                       # Postgres + RabbitMQ + Redis
make migrate                        # Apply 001_initial_schema.up.sql
make dev-api                        # cd api && go run ./cmd/server/
make dev-worker                     # cd worker && go run ./cmd/worker/
make dev-frontend                   # cd frontend && npm run dev

# Full Docker Compose stack
make up                             # build + start everything
make down-clean                     # stop and wipe volumes
make health                         # curl the health endpoints

# Tests
make test                           # api + worker unit tests with -race
make test-integration               # spins up docker-compose.test.yml; E2E
cd api && go test -run TestSubmit ./internal/usecase/...   # single test
cd worker && go test -tags=integration ./internal/executor/...  # sandbox tests need nsjail

# Lint / format
make lint                           # golangci-lint (api+worker) + eslint (frontend)
make fmt                            # go fmt only — frontend has no formatter target

# Docker images (worker uses repo root context — see Makefile)
make docker-build
```

The worker Docker image **must** be built with the repo root as build context (not `./worker`) because it copies `sandbox/` configs in. The `docker-build-worker` Makefile target does this correctly; if you invoke `docker build` directly, mirror the `-f worker/Dockerfile .` form.

`make migrate` shells out to a local `psql` against `localhost:5432` with hardcoded credentials matching `.env.example`. If your `.env` differs, run the SQL manually.

`make security-audit` and `make load-test` expect a running stack (the load test requires `k6` installed).

## Configuration

Both Go services read env vars listed in `.env.example`. Local dev expects `.env` at the repo root (not per-service). Defaults are dev-friendly (insecure passwords, debug logging, `nsjail` path `/usr/bin/nsjail`); these are overridden via secrets/configmaps for k8s (`infra/k8s/secrets.yaml`, `infra/k8s/configmaps.yaml`).

## Kubernetes

`infra/k8s/` is a Kustomize base. Apply order matters: namespace → secrets/configmaps → stateful infra (Postgres/RabbitMQ/Redis StatefulSets) → API/Worker deployments → ingress + KEDA. `make k8s-apply` handles ordering via the `kustomization.yaml`. Worker scaling is driven by KEDA against the `execution_tasks` queue depth (min 2, max 50, trigger at 15 msgs/worker). The monitoring stack is a separate kustomize base under `infra/k8s/monitoring/`.

A default deny-all NetworkPolicy is in effect inside the `sentinel` namespace — any new pod that needs to talk to Postgres/RabbitMQ/Redis or expose metrics needs an explicit allow rule in `network-policies.yaml`.

## Host requirements (matters for any worker change)

The worker only functions on a **Linux kernel with cgroup v2**. It needs `privileged: true` and `cgroup: host` in compose (already set) so nsjail can write to `/sys/fs/cgroup/cgroup.subtree_control`.

- Linux native: works directly.
- Windows: only via Docker Desktop's **WSL2** backend (Hyper-V backend is untested and likely lacks cgroup v2).
- macOS: Docker Desktop's xhyve/VZ VM does NOT expose cgroup v2 in a way nsjail can use. Use [Colima](https://github.com/abiosoft/colima) (`colima start --vm-type=vz`).

`make dev-worker` (native, non-container) is Linux-host-only because it shells out to `nsjail` directly. On Windows/macOS, run the worker in Docker even when iterating on Go code; native dev only makes sense for the API and frontend.

## Conventions

- Go modules are independent: `api/go.mod` and `worker/go.mod`. Don't try to combine them. After `go get` in one, run `go mod tidy` only in that module.
- Go code uses `go.uber.org/zap` (production logger) and structured fields. Mirror existing log style; don't introduce `log` or `slog`.
- Errors at boundaries are wrapped with `fmt.Errorf("context: %w", err)`. Domain errors live in `internal/domain`.
- Tests use the `-race` flag in CI; new tests must pass under it. Sandbox integration tests in `worker/internal/executor/sandbox_integration_test.go` require `nsjail` on PATH — they're skipped otherwise.
- The worker is intentionally written so that AMQP ack happens **after** the result is persisted. Don't refactor toward auto-ack or pre-ack — at-least-once delivery is the contract.

## Documentation map

The `docs/` tree splits into reference docs (what the system does) and design docs (why it does it that way). Read in this order when onboarding:

1. **High-level overview** — start with [README.md](./README.md), then [docs/architecture.md](./docs/architecture.md) for system diagrams, component breakdown, and network topology.
2. **API contract** — [docs/api.md](./docs/api.md) for endpoint shapes, status codes, WebSocket protocol, and the OpenAPI spec.
3. **Operations** — [docs/deployment.md](./docs/deployment.md) (Docker Compose + k3s), [docs/tuning.md](./docs/tuning.md) (knobs that matter under load).
4. **Design rationale** — [docs/design/](./docs/design/) holds RFC-style decision docs. Read these when you need to know *why* something is the way it is, or before changing a load-bearing piece:
   - [0001 — Sandbox security model](./docs/design/0001-sandbox-security.md): nsjail layering, kafel seccomp, threat model, alternatives (gVisor, Firecracker).
   - [0002 — Queue topology and delivery semantics](./docs/design/0002-queue-and-delivery-semantics.md): exchange/queue names, ACK-after-execute, idempotency, **and the known queue-arg mismatch between API and worker** — read this before touching either AMQP file.
   - [0003 — Scaling architecture](./docs/design/0003-scaling-architecture.md): KEDA + worker pool model, prefetch=1 backpressure, capacity math.
   - [0004 — Data model and partitioning](./docs/design/0004-data-model-and-partitioning.md): `execution_jobs` partitioning rationale, UUIDv7 choice, partial-index design, what is deliberately *not* in the schema.

If you're about to make a change to nsjail config, queue declarations, schema, or scaling parameters, the corresponding design doc is mandatory reading. They explicitly call out trade-offs and rejected alternatives so you don't redo a debate that has already happened.

When making a non-obvious decision yourself, add a numbered design doc under `docs/design/` rather than burying the rationale in a commit message — see `docs/design/README.md` for conventions.

### Quick-reference response shape

`POST /api/v1/submissions` returns `202 Accepted` with body `{"job_id": "<uuidv7>", "status": "QUEUED"}`. The README and earlier docs sometimes drift on this — trust `api/internal/domain/job.go::SubmitResponse`.
