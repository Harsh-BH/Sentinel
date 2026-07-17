# Architecture

> Deep dive into Sentinel's system design, data flow, security model, and component interactions.

---

## Table of Contents

- [System Overview](#system-overview)
- [Component Architecture](#component-architecture)
- [Data Flow](#data-flow)
- [Security Architecture](#security-architecture)
- [Scaling Architecture](#scaling-architecture)
- [Network Topology](#network-topology)
- [Data Model](#data-model)
- [Error Handling](#error-handling)
- [Observability](#observability)

---

## System Overview

Sentinel is a distributed remote code execution (RCE) engine designed to safely execute untrusted code submissions at scale. It follows an **event-driven architecture** with strict separation between the API layer (request ingestion) and the worker layer (execution).

```
                                ┌─────────────────────────────────────┐
                                │          Load Balancer              │
                                │    (nginx-ingress / k3s traefik)    │
                                └──────────────┬──────────────────────┘
                                               │
                        ┌──────────────────────┼──────────────────────┐
                        │                      │                      │
                        ▼                      ▼                      ▼
               ┌─────────────────┐   ┌─────────────────┐   ┌─────────────────┐
               │   Frontend      │   │    API Server    │   │    API Server    │
               │   React SPA     │   │    (replica 1)   │   │    (replica N)   │
               │   + Monaco      │   │                  │   │                  │
               └────────┬────────┘   └───────┬──────────┘   └───────┬──────────┘
                        │                    │                      │
                        │ HTTP/WS            │                      │
                        └────────────────────┼──────────────────────┘
                                             │
                    ┌────────────────────────┬┴───────────────────────┐
                    │                        │                        │
                    ▼                        ▼                        ▼
           ┌────────────────┐     ┌───────────────────┐    ┌──────────────────┐
           │  PostgreSQL 16 │     │   RabbitMQ 3.13    │    │    Redis 7       │
           │  (persistence) │     │  (quorum queues)   │    │  (cache/locks)   │
           └────────────────┘     └────────┬──────────┘    └──────────────────┘
                                           │
                        ┌──────────────────┬┴──────────────────┐
                        │                  │                    │
                        ▼                  ▼                    ▼
               ┌────────────────┐ ┌────────────────┐  ┌────────────────┐
               │   Worker Pod   │ │   Worker Pod   │  │   Worker Pod   │
               │  ┌──────────┐  │ │  ┌──────────┐  │  │  ┌──────────┐  │
               │  │ nsjail   │  │ │  │ nsjail   │  │  │  │ nsjail   │  │
               │  │ sandbox  │  │ │  │ sandbox  │  │  │  │ sandbox  │  │
               │  └──────────┘  │ │  └──────────┘  │  │  └──────────┘  │
               └────────────────┘ └────────────────┘  └────────────────┘
                        ▲                                      ▲
                        │              KEDA                    │
                        └──────── Auto-Scaling ────────────────┘
```

---

## Component Architecture

### Frontend (React + Vite)

```
frontend/src/
├── components/
│   ├── CodeEditor.tsx      ← Monaco Editor wrapper
│   ├── ResultPanel.tsx     ← Execution output display
│   ├── LanguageSelector.tsx
│   └── SubmissionHistory.tsx
├── hooks/
│   └── useJobTracking.ts   ← WebSocket + polling logic
├── services/
│   ├── api.ts              ← HTTP client (Axios)
│   └── websocket.ts        ← WebSocket manager
└── types/
    └── index.ts            ← Shared TypeScript types
```

**Key behaviors**:
- Monaco Editor provides syntax highlighting and intellisense for Python/C++
- `useJobTracking` hook connects via WebSocket first, falls back to polling
- Result streaming: WebSocket receives status updates as the job progresses through QUEUED → RUNNING → SUCCESS

### API Server (Go + Gin)

```
api/internal/
├── config/          ← Viper environment config
├── delivery/http/
│   ├── router.go               ← Route definitions
│   ├── submission_handler.go   ← Submit + Get handlers
│   ├── health_handler.go       ← Health check (DB, AMQP, Redis)
│   ├── language_handler.go     ← GET /languages
│   ├── websocket_handler.go    ← WebSocket upgrade + streaming
│   └── middleware/             ← CORS, logger, rate limiter, request ID, body size
├── domain/
│   ├── job.go                  ← Core types (Job, SubmitRequest, Status)
│   └── errors.go               ← Domain error types
├── publisher/
│   └── rabbitmq.go             ← AMQP publisher (quorum queue)
├── repository/
│   ├── job_repository.go       ← Repository interface
│   ├── postgres/               ← pgx implementation
│   └── mock/                   ← test doubles
└── usecase/
    ├── submit_job.go           ← Submit flow (validate → persist → publish)
    └── get_job.go              ← Fetch job + status
```

**Request flow**:
1. Gin receives POST `/api/v1/submissions`
2. Middleware chain: Recovery → RequestID → CORS → Logger → BodySize → RateLimiter
3. `SubmissionHandler.Submit()` validates and delegates to `SubmitJobUsecase`
4. Usecase: Generate UUIDv7 → Insert into PostgreSQL → Publish to RabbitMQ
5. Return 202 Accepted with `job_id`

### Worker (Go + nsjail)

```
worker/internal/
├── config/          ← Viper environment config
├── delivery/amqp/
│   └── consumer.go         ← RabbitMQ consumer (manual ACK after execute)
├── domain/
│   └── execution.go        ← Execution types (Job, JobMessage, ExecutionRequest/Result)
├── executor/
│   ├── sandbox.go          ← Sandbox execution (nsjail CLI wrapper, Python + C++)
│   └── sandbox_*_test.go   ← Unit + integration tests (latter requires nsjail on PATH)
├── metrics/
│   └── prometheus.go       ← Custom Prometheus metrics
├── pool/
│   └── pool.go             ← Goroutine worker pool
├── repository/
│   ├── interfaces.go       ← Repository + IdempotencyStore interfaces
│   ├── postgres/           ← pgx implementation (status + result updates)
│   ├── redis/              ← Idempotency lock implementation
│   └── mock/               ← test doubles
└── usecase/
    └── execute_job.go      ← Orchestrate: consume → idempotency → execute → persist → ACK
```

**Execution flow**:
1. Consumer receives message from `execution_tasks` queue
2. Pool assigns to a free goroutine
3. Usecase checks idempotency via Redis (prevent duplicate execution)
4. Updates job status to RUNNING in PostgreSQL
5. Executor spawns nsjail subprocess with language-specific config
6. Captures stdout/stderr, exit code, timing
7. Updates job with results in PostgreSQL
8. ACKs the message (ACK-after-execute pattern)
9. On failure: message is NACKed → requeued (3 retries) → DLX

---

## Data Flow

### Submission Lifecycle

```
 Client                    API                    RabbitMQ              Worker
   │                        │                        │                    │
   │  POST /submissions     │                        │                    │
   │───────────────────────▶│                        │                    │
   │                        │  INSERT job (QUEUED)   │                    │
   │                        │──────────▶ PostgreSQL  │                    │
   │                        │                        │                    │
   │                        │  PUBLISH message       │                    │
   │                        │───────────────────────▶│                    │
   │                        │                        │                    │
   │  202 {job_id, QUEUED}  │                        │                    │
   │◀───────────────────────│                        │                    │
   │                        │                        │  CONSUME message   │
   │                        │                        │───────────────────▶│
   │                        │                        │                    │
   │  WS /api/v1/submissions/:id/stream              │  UPDATE → RUNNING │
   │───────────────────────▶│                        │──────▶ PostgreSQL │
   │                        │                        │                    │
   │  {status: RUNNING}     │                        │    nsjail exec    │
   │◀───────────────────────│                        │   ┌────────────┐  │
   │                        │                        │   │ sandbox    │  │
   │                        │                        │   │ python/c++ │  │
   │                        │                        │   └────────────┘  │
   │                        │                        │                    │
   │                        │                        │  UPDATE → SUCCESS │
   │  {status: SUCCESS,     │                        │──────▶ PostgreSQL │
   │   stdout: "...",       │                        │                    │
   │   time_used_ms: 42}    │                        │  ACK message      │
   │◀───────────────────────│                        │◀───────────────────│
   │                        │                        │                    │
```

### Job Status State Machine

```
                     ┌──────────────────────────────────────┐
                     │                                      │
                     ▼                                      │
    ┌─────────┐    ┌─────────┐    ┌─────────────┐         │
    │ QUEUED  │───▶│COMPILING│───▶│   RUNNING   │         │
    └─────────┘    └────┬────┘    └──────┬──────┘         │
                        │                │                 │
                        ▼                ├────▶ SUCCESS    │
                   COMPILATION     ├────▶ RUNTIME_ERROR    │
                     _ERROR        ├────▶ TIMEOUT          │
                                   ├────▶ MEMORY_LIMIT     │
                                   └────▶ INTERNAL_ERROR ──┘
                                              (retry)
```

---

## Security Architecture

### Defense in Depth

Sentinel is designed for **7 layers of isolation** to contain untrusted code. **6 are currently active**; Layer 4 (seccomp-BPF) is authored but **not yet enforced** — its policy is commented out in `sandbox/nsjail/*.cfg` pending a kafel syscall-table audit (see [design doc 0001](design/0001-sandbox-security.md)):

```
Layer 7: │ Application  │  Input validation, size limits, rate limiting          [active]
         ├──────────────┤
Layer 6: │ Kubernetes   │  Network policies (deny-all default), pod security     [active]
         ├──────────────┤
Layer 5: │ Container    │  Read-only rootfs, non-root user, drop capabilities    [active]
         ├──────────────┤
Layer 4: │ Seccomp-BPF  │  Kafel policies: allowlisted syscalls only    [PLANNED — disabled]
         ├──────────────┤
Layer 3: │ Cgroups v2   │  Memory (256MB), PIDs (64), CPU (1 core)               [active]
         ├──────────────┤
Layer 2: │ Namespaces   │  PID, NET, MNT, UTS, IPC, USER, CGROUP                 [active]
         ├──────────────┤
Layer 1: │ pivot_root   │  Minimal rootfs, no host filesystem access            [active]
```

### nsjail Sandbox Details

**Mount namespace**:
- `pivot_root` to a minimal filesystem (only language runtime + libraries)
- `/tmp/work` tmpfs for user code (64MB, `noexec` for runtime but code is interpreted)
- All host paths are inaccessible

**Network namespace**:
- Empty network namespace (no `lo`, no `eth0`) — this is what enforces no-network today
- Socket syscalls would additionally be blocked by seccomp once the policy is enforced (see below); until then, an unreachable empty netns is the guarantee
- DNS resolution impossible

**PID namespace**:
- Process sees PID 1 (itself)
- Cannot signal or inspect host processes
- Fork bomb limited by `cgroup_pids_max: 64`

**Seccomp-BPF (Kafel DSL)** — *authored but not yet enforced; the policy below is the intended allowlist, currently commented out in the nsjail configs*:
```
// Python policy (simplified)
POLICY python {
  ALLOW { read, write, open, close, stat, fstat, mmap, mprotect,
          brk, munmap, rt_sigaction, rt_sigprocmask, ioctl,
          access, pipe, select, sched_yield, clone, execve,
          exit, exit_group, arch_prctl, ... }
  DENY { ptrace, mount, setuid, setgid, socket, connect, bind,
         listen, accept, sendto, recvfrom, ... }
  KILL_PROCESS  // Default: kill on any unlisted syscall
}
```

### Threat Model

| Threat | Mitigation | Verification |
|--------|-----------|-------------|
| Arbitrary code execution | nsjail sandbox with all 7 layers | `scripts/security-audit.sh` |
| Fork bomb / resource exhaustion | Cgroups v2 PID + memory + CPU limits | Security audit Test 4a-4e |
| Network exfiltration | Empty network namespace (active); seccomp socket block planned | Security audit Test 3a-3d |
| Filesystem escape | pivot_root + read-only mounts + no host paths | Security audit Test 2a-2e |
| Privilege escalation | User namespace (non-root, active); seccomp setuid/mount/ptrace block planned | Security audit Test 6a-6d |
| Denial of service | Rate limiting, KEDA auto-scaling, queue-based backpressure | Load test (`scripts/load-test.js`) |
| Replay attacks | Redis idempotency locks (ZADD NX) | Integration tests |

---

## Scaling Architecture

### Horizontal Scaling

```
                    ┌──────────────────────────────────┐
                    │        KEDA Controller            │
                    │   polls: RabbitMQ queue depth      │
                    │   metric: messages / worker        │
                    └──────────────┬───────────────────┘
                                   │
                    ┌──────────────▼───────────────────┐
                    │   Kubernetes HPA                  │
                    │   min: 2  │  max: 50  │  target: 15 │
                    └──────────────┬───────────────────┘
                                   │
              ┌────────────────────┼────────────────────┐
              │                    │                     │
    ┌─────────▼──────┐  ┌─────────▼──────┐  ┌─────────▼──────┐
    │  Worker Pod 1  │  │  Worker Pod 2  │  │  Worker Pod N  │
    │  pool_size=4   │  │  pool_size=4   │  │  pool_size=4   │
    │  4 goroutines  │  │  4 goroutines  │  │  4 goroutines  │
    └────────────────┘  └────────────────┘  └────────────────┘
```

**Scaling behavior**:
- Queue depth > 15 per worker → scale up (max +5 pods or +100% every 30s)
- Queue depth → 0 → scale down (-2 pods every 60s, 120s stabilization)
- Burst: 0 → 1000 messages → scales to ~67 pods within 2-3 minutes

### Capacity Planning

| Workers | Pool Size | Concurrent Executions | Sustained Throughput (est.) |
|---------|-----------|----------------------|---------------------------|
| 2 | 4 | 8 | ~100 submissions/min |
| 10 | 4 | 40 | ~500 submissions/min |
| 50 | 4 | 200 | ~2500 submissions/min |

---

## Network Topology

### Kubernetes (Production)

```
┌─── sentinel namespace ─────────────────────────────────────────────┐
│                                                                     │
│  ┌─────────┐    ┌─────────┐    ┌─────────┐    ┌─────────┐         │
│  │   API   │───▶│   PG    │    │  AMQP   │◀───│ Worker  │         │
│  │ :8080   │───▶│  :5432  │    │  :5672  │    │ :9090   │         │
│  │         │───▶│         │    │ :15672  │    │         │         │
│  │         │───▶│  Redis  ├────│ :15692  │───▶│         │         │
│  │         │    │  :6379  │    │         │    │         │         │
│  └────▲────┘    └─────────┘    └─────────┘    └─────────┘         │
│       │                                                             │
│  ┌────┴────┐                                                        │
│  │ Ingress │                                                        │
│  │ nginx   │                                                        │
│  └────▲────┘                                                        │
└───────┼─────────────────────────────────────────────────────────────┘
        │
┌───────┼─── monitoring namespace ────────────────────────────────────┐
│       │                                                              │
│  ┌────┴──────┐    ┌────────────┐    ┌──────────────┐               │
│  │Prometheus │───▶│  Grafana   │    │ PG Exporter  │               │
│  │  :9090    │    │   :3000    │    │   :9187      │               │
│  └───────────┘    └────────────┘    └──────────────┘               │
└──────────────────────────────────────────────────────────────────────┘
```

### Network Policies

| Rule | From | To | Ports |
|------|------|----|-------|
| Default | * | * | **DENY ALL** |
| API Ingress | nginx-ingress | API | 8080 |
| API → PG | API | PostgreSQL | 5432 |
| API → AMQP | API | RabbitMQ | 5672 |
| API → Redis | API | Redis | 6379 |
| Worker → PG | Worker | PostgreSQL | 5432 |
| Worker → AMQP | Worker | RabbitMQ | 5672 |
| Worker → Redis | Worker | Redis | 6379 |
| Prometheus scrape | monitoring/prometheus | sentinel/* | 8080, 9090, 15692 |
| PG Exporter | monitoring/pg-exporter | sentinel/pg | 5432 |

---

## Data Model

### PostgreSQL Schema

See `migrations/001_initial_schema.up.sql` for the canonical DDL. Summary:

```sql
-- ENUM types
CREATE TYPE execution_status AS ENUM (
    'QUEUED', 'COMPILING', 'RUNNING', 'SUCCESS',
    'COMPILATION_ERROR', 'RUNTIME_ERROR', 'TIMEOUT',
    'MEMORY_LIMIT_EXCEEDED', 'INTERNAL_ERROR'
);
CREATE TYPE language AS ENUM ('python', 'cpp');

-- Range-partitioned by created_at (quarterly partitions)
CREATE TABLE execution_jobs (
    job_id          UUID PRIMARY KEY,
    language        language NOT NULL,
    source_code     TEXT NOT NULL,
    stdin           TEXT DEFAULT '',
    stdout          TEXT DEFAULT '',
    stderr          TEXT DEFAULT '',
    status          execution_status NOT NULL DEFAULT 'QUEUED',
    exit_code       INT,
    time_used_ms    INT,
    memory_used_kb  INT,
    time_limit_ms   INT NOT NULL DEFAULT 5000,
    memory_limit_kb INT NOT NULL DEFAULT 262144,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
) PARTITION BY RANGE (created_at);

-- Quarterly partitions pre-created (2026 Q1, Q2, ...)

-- Partial index keeps the active-job lookup hot in RAM
CREATE INDEX idx_active_jobs ON execution_jobs(job_id)
    WHERE status IN ('QUEUED', 'COMPILING', 'RUNNING');

-- Compound index for status-based polling
CREATE INDEX idx_jobs_status ON execution_jobs(status, created_at);
```

Job IDs are UUIDv7 (time-ordered) generated in the API usecase, not by the database.

### RabbitMQ Message Schema

The full `Job` struct (see `api/internal/domain/job.go`) is JSON-serialized as the message body. Minimum fields needed by the worker:

```json
{
  "job_id": "01912345-6789-7abc-def0-123456789abc",
  "language": "python",
  "source_code": "print('hello')",
  "stdin": "",
  "status": "QUEUED",
  "time_limit_ms": 5000,
  "memory_limit_kb": 262144,
  "created_at": "2026-02-20T10:00:00Z",
  "updated_at": "2026-02-20T10:00:00Z"
}
```

- **Exchange**: `sentinel.direct` (direct, durable)
- **Routing key**: `execute`
- **Main queue**: `execution_tasks` (quorum, durable, prefetch=1, manual ack)
- **DLX**: `sentinel.dlx` (direct, durable) → bound to `dead_letter_queue`
- **Delivery mode**: persistent | **Content-Type**: `application/json`
- **Publisher confirms**: enabled (API waits for broker ack before returning 202)

---

## Error Handling

### Retry Strategy

```
  Message consumed
       │
       ▼
  Execute in sandbox
       │
  ┌────┴────┐
  │ Success? │
  └────┬────┘
   Yes │    No
       ▼     ▼
   ACK msg  Check retry count
              │
         ┌────┴────┐
         │ < 3?    │
         └────┬────┘
          Yes │    No
              ▼     ▼
         NACK+requeue  NACK → DLX
```

### Error Classification

| Error Type | Status | Retryable | Action |
|-----------|--------|-----------|--------|
| Compilation failure | `COMPILATION_ERROR` | No | Return to user |
| Runtime exception | `RUNTIME_ERROR` | No | Return to user |
| Wall-clock timeout | `TIMEOUT` | No | Return to user |
| OOM kill | `MEMORY_LIMIT_EXCEEDED` | No | Return to user |
| nsjail crash | `INTERNAL_ERROR` | Yes (3x) | Retry, then DLX |
| DB connection lost | `INTERNAL_ERROR` | Yes (3x) | Retry, then DLX |
| AMQP disconnected | — | Yes (auto) | AMQP reconnect |

---

## Observability

### Metrics Pipeline

```
  API Pod (:8080/metrics) ───┐
                              │
  Worker Pod (:9090/metrics) ─┼───▶ Prometheus (:9090) ───▶ Grafana (:3000)
                              │         │
  RabbitMQ (:15692/metrics) ──┤         │
                              │         ▼
  PG Exporter (:9187/metrics)─┘   Alerting Rules
                                       │
                                       ▼
                                  Alertmanager (optional)
```

### Key Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `sentinel_executions_total` | Counter | language, status | Total executions |
| `sentinel_execution_duration_seconds` | Histogram | language | Execution time distribution |
| `sentinel_workers_active` | Gauge | — | Currently active worker goroutines |
| `sentinel_sandbox_failures_total` | Counter | — | nsjail spawn failures |

### Dashboards

See [Observability section in README](../README.md#observability-prometheus--grafana) for dashboard descriptions and screenshots.
