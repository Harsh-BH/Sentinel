# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

Sentinel is a distributed remote code execution engine. Untrusted user code (Python, C++) is queued via a Go API, consumed by a Go worker pool, and executed inside an `nsjail` sandbox. Results stream back to a React/Vite frontend over WebSocket.

The repo is a polyglot monorepo with three deployable units (`api/`, `worker/`, `frontend/`) plus shared sandbox configuration (`sandbox/`), DB migrations (`migrations/`), and infra (`infra/k8s/`, `docker-compose.yml`).

## Architecture

### Request flow
1. Frontend POSTs to `POST /api/v1/submissions` → API writes the `QUEUED` row **and its queue message** in one Postgres transaction (`execution_jobs` + `job_outbox`), then attempts an inline publish. If the publish fails the row stays in the outbox and a relay goroutine delivers it later — so **submission returns `202` even when RabbitMQ is down**. See [design doc 0006](./docs/design/0006-transactional-outbox.md).
2. API publishes to direct exchange `sentinel.direct` with routing key `execute`, bound to queue `execution_tasks` (quorum queue, with a DLX). Both API and worker independently `QueueDeclare` it idempotently — the declared arguments must match exactly on both sides or RabbitMQ returns a `PRECONDITION_FAILED` and the service crash-loops.
3. Worker consumer in `worker/internal/delivery/amqp` pushes `JobMessage` (with Ack/Nack callbacks) onto a buffered channel sized `2 * pool_size`. **AMQP prefetch is set to `WORKER_POOL_SIZE`** — because ACK happens after execution, prefetch is the real per-pod concurrency ceiling, so it must track pool size (it was hardcoded to 1, which serialised every pod).
4. `worker/internal/pool` goroutine pool pulls from the channel and runs `usecase.ExecuteJobUsecase`, which: **claims the job with a conditional UPDATE that sets a lease on the Postgres row** → invokes `executor.SandboxExecutor` → writes the result back with a `status NOT IN (terminal)` guard → ACKs the AMQP delivery (ACK-after-execute, never before). The worker does **not** use Redis; see [design doc 0005](./docs/design/0005-job-lease-and-recovery.md).
5. Frontend tracks results either by polling `GET /api/v1/submissions/:id` or via WebSocket at `GET /api/v1/submissions/:id/stream`. The WebSocket is **push-based**: a trigger on `execution_jobs` fires `pg_notify`, one listener per API process fans out to subscribers, and a 5 s poll remains only as a correctness backstop (`NOTIFY` is fire-and-forget). See [design doc 0007](./docs/design/0007-push-based-status-updates.md).

Note: the actual routes (see `api/internal/delivery/http/router.go`) are `/api/v1/health`, `/api/v1/livez`, `/api/v1/readyz` and `/api/v1/submissions/:id/stream`. Trust the router, not the README.

Probe wiring matters: **`livenessProbe` must point at `/livez`** (process-only). Pointing it at a dependency-checking endpoint means a RabbitMQ blip fails liveness on every replica at once and the kubelet restarts the whole tier.

### Clean architecture (Go services)
Both `api/` and `worker/` follow the same layout:
- `cmd/<service>/main.go` — composition root; wires config, pgxpool, redis, rabbitmq, then layers
- `internal/config` — Viper-style env config
- `internal/domain` — pure types (Job, Status, ExecutionRequest/Result, errors). No I/O imports here.
- `internal/repository/{postgres,redis}` — pgx + go-redis adapters. **Worker-side there is no `redis` package**: job deduplication is a Postgres lease, so the worker's only datastore is Postgres. Redis is API-only (rate limiting).
- `internal/usecase` — business logic; depends on domain + repository interfaces only
- `internal/delivery/{http,amqp}` — transport adapters (Gin handlers, AMQP consumer)
- API-only: `internal/publisher` (RabbitMQ producer)
- Worker-only: `internal/executor` (nsjail), `internal/pool` (goroutine pool), `internal/metrics` (Prometheus)

When adding a feature, the dependency direction is `delivery → usecase → repository/domain`. Don't reach across — for example, handlers must not import `repository/postgres` directly.

### Sandbox model
The worker shells out to `nsjail` (pinned to a commit in `worker/Dockerfile`) using protobuf configs in `sandbox/nsjail/{python,cpp}.cfg`. The Kafel seccomp policies in `sandbox/policies/{python,cpp}.policy` are **enforced** — `seccomp_policy_file` is set in both `.cfg` files, allowlist with `DEFAULT KILL`.

If you add a language or upgrade a runtime, expect to extend the allowlist. The method is in the policy header: capture the real syscalls with `strace -f`, then check the names against kafel's table (it rejects `fstat` and `uname`; use `newfstatat` and `newuname`). A seccomp kill surfaces as exit code 159 (SIGSYS) and the executor turns that into an explanatory stderr message.

**Memory limits need two knobs, not one.** `--cgroup_mem_max` alone does not bound memory: cgroup v2's `memory.max` excludes swap, so on a host with swap the process is paged out instead of OOM-killed and the job reports SUCCESS. The executor also passes `--cgroup_mem_swap_max 0`. Verified: without it, a 400 MB allocation under a 64 MB limit completes successfully. `executor.SandboxExecutor.Execute` writes source + stdin to an ephemeral `os.MkdirTemp` workdir, runs nsjail, captures stdout/stderr capped at 64 KB, then deletes the workdir. C++ is two-pass: a compile invocation followed by a run invocation; both are sandboxed separately. If you change the language pipeline you almost certainly need to touch all three of: the executor switch in `worker/internal/executor/sandbox.go`, the nsjail `.cfg`, and the kafel `.policy`.

### Database
`migrations/001_initial_schema.up.sql` defines a single partitioned table `execution_jobs` (range-partitioned by `created_at`, quarterly partitions through 2027 Q1 plus a DEFAULT catch-all). When adding fields, remember it's partitioned — DDL must be issued on the parent.

Two things to preserve:

- **Reads must carry a `created_at` predicate**, or Postgres cannot prune and probes every partition. `GetByID` reconstructs a window from the UUIDv7 timestamp; the worker uses the `created_at` that came with the queue message.
- **Every status/result write is guarded by `status NOT IN (terminal)`.** That guard, not delivery semantics, is what stops a duplicate execution overwriting a finished result.

Two other objects in that migration are load-bearing: **`job_outbox`** (the transactional-outbox table, [0006](./docs/design/0006-transactional-outbox.md)) and **`trg_execution_jobs_notify`** (the `pg_notify` trigger that makes the WebSocket a push, [0007](./docs/design/0007-push-based-status-updates.md)). Dropping either silently degrades a guarantee rather than breaking a test.

`idx_jobs_reap` is the only secondary index, and it exists for the reaper's sweep. The two indexes that used to be here (`idx_active_jobs`, `idx_jobs_status`) were unusable by any query in the codebase — a partial index can only be used when the query's predicate implies the index predicate, and nothing filtered on status.

The k8s Postgres init ConfigMap is **generated** from this file (`make k8s-sync-schema`); `make k8s-verify-schema` fails on drift. Do not hand-edit `infra/k8s/generated/`.

### Frontend
React 18 + Vite + TypeScript + Tailwind + Monaco.

`nginx.conf` is shipped as a **template**, not a final config: it is copied to `/etc/nginx/templates/sentinel.conf.template` and the nginx entrypoint runs `envsubst` over it. `API_UPSTREAM` and `DNS_RESOLVER` must therefore be set per environment (`api:8080` + `127.0.0.11` in compose; `sentinel-api:8080` + `kube-dns.kube-system.svc.cluster.local` in k8s). The upstream used to be hardcoded to the compose service name, which meant the frontend crash-looped in Kubernetes with `host not found in upstream "api"`. `proxy_pass` goes through a variable on purpose so the host resolves per request instead of once at startup.

Two things the code does differently from what `.env.example` implies: `src/services/api.ts` uses a **relative** `/api/v1` base URL and `window.location.host` for the WebSocket, so `VITE_API_BASE_URL`/`VITE_WS_BASE_URL` are currently unread — routing is nginx's job in the container and the Vite dev proxy's job locally (`vite.config.ts`, which needs `ws: true` on the `/api` rule for the stream to work).

The stream is a real push: worker writes → trigger `pg_notify` → API listener → WebSocket, with a 5 s safety poll behind it ([0007](./docs/design/0007-push-based-status-updates.md)).

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

Applying the base to a fresh cluster needs three things that are **not** in it: KEDA (for `ScaledObject`), cert-manager (for the two `ClusterIssuer`s — `kubectl apply -k` reports `no matches for kind "ClusterIssuer"` without it), and a node labelled `sentinel.io/role=worker`. The worker deployment also tolerates a `workload=untrusted:NoSchedule` taint, which assumes a dedicated node pool — on a single-node cluster do not apply that taint or nothing else will schedule.

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
   - [0003 — Scaling architecture](./docs/design/0003-scaling-architecture.md): KEDA + worker pool model, `prefetch == pool_size` backpressure, capacity math.
   - [0004 — Data model and partitioning](./docs/design/0004-data-model-and-partitioning.md): `execution_jobs` partitioning rationale, UUIDv7 choice, index design, what is deliberately *not* in the schema.
   - [0005 — Job leases and crash recovery](./docs/design/0005-job-lease-and-recovery.md): the Postgres lease that replaced the Redis dedup lock, guarded writes, and the reaper. **Read before touching the claim/ack path** — it records two mistakes that are easy to reintroduce.
   - [0006 — Transactional outbox](./docs/design/0006-transactional-outbox.md): why the job row and its queue message commit together, and why submission survives a broker outage. **Read before touching the submit path.**
   - [0007 — Push-based status updates](./docs/design/0007-push-based-status-updates.md): the `pg_notify` trigger, the per-process listener, and why a 5 s safety poll deliberately remains.

If you're about to make a change to nsjail config, queue declarations, schema, or scaling parameters, the corresponding design doc is mandatory reading. They explicitly call out trade-offs and rejected alternatives so you don't redo a debate that has already happened.

When making a non-obvious decision yourself, add a numbered design doc under `docs/design/` rather than burying the rationale in a commit message — see `docs/design/README.md` for conventions.

### Quick-reference response shape

`POST /api/v1/submissions` returns `202 Accepted` with body `{"job_id": "<uuidv7>", "status": "QUEUED"}`. The README and earlier docs sometimes drift on this — trust `api/internal/domain/job.go::SubmitResponse`.
