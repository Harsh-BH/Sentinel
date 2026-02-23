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
│   ├── router.go           ← Route definitions
│   ├── submission.go       ← Submit + Get handlers
│   ├── health.go           ← Health check (DB, AMQP, Redis)
│   ├── language.go         ← GET /languages
│   ├── websocket.go        ← WebSocket upgrade + streaming
│   └── middleware/
│       ├── cors.go         ← CORS headers
│       ├── logger.go       ← Structured request logging (zap)
│       ├── ratelimiter.go  ← Redis sliding window
│       ├── requestid.go    ← X-Request-ID header
│       └── bodysize.go     ← 1MB body limit
├── domain/
│   ├── job.go              ← Core types (Job, SubmitRequest, Status)
│   └── errors.go           ← Domain error types
├── publisher/
│   └── rabbitmq.go         ← AMQP publisher (quorum queue)
├── repository/
│   └── postgres.go         ← pgx CRUD operations
└── usecase/
    ├── submit.go           ← Submit flow (validate → persist → publish)
    └── getjob.go           ← Fetch job + status
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
│   └── consumer.go         ← RabbitMQ consumer (ACK-after-execute)
├── domain/
│   └── execution.go        ← Execution types
├── executor/
│   └── nsjail.go           ← Sandbox execution (nsjail CLI wrapper)
├── metrics/
│   └── prometheus.go       ← Custom Prometheus metrics
├── pool/
│   └── pool.go             ← Goroutine worker pool
├── repository/
│   ├── postgres.go         ← Update job results
│   └── redis.go            ← Idempotency checks
└── usecase/
    └── execute.go          ← Orchestrate: consume → execute → persist → ACK
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
   │  WS /submissions/:id/stream                     │  UPDATE → RUNNING │
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

Sentinel employs **7 layers of isolation** to contain untrusted code:

```
Layer 7: │ Application │  Input validation, size limits, rate limiting
         ├─────────────┤
Layer 6: │ Kubernetes   │  Network policies (deny-all default), pod security
         ├─────────────┤
Layer 5: │ Container    │  Read-only rootfs, non-root user, drop capabilities
         ├─────────────┤
Layer 4: │ Seccomp-BPF  │  Kafel policies: allowlisted syscalls only
         ├─────────────┤
Layer 3: │ Cgroups v2   │  Memory (256MB), PIDs (64), CPU (1 core)
         ├─────────────┤
Layer 2: │ Namespaces   │  PID, NET, MNT, UTS, IPC, USER, CGROUP
         ├─────────────┤
Layer 1: │ pivot_root   │  Minimal rootfs, no host filesystem access
```

### nsjail Sandbox Details

**Mount namespace**:
- `pivot_root` to a minimal filesystem (only language runtime + libraries)
- `/tmp/work` tmpfs for user code (64MB, `noexec` for runtime but code is interpreted)
- All host paths are inaccessible

**Network namespace**:
- Empty network namespace (no `lo`, no `eth0`)
- All socket syscalls blocked by seccomp
- DNS resolution impossible

**PID namespace**:
- Process sees PID 1 (itself)
- Cannot signal or inspect host processes
- Fork bomb limited by `cgroup_pids_max: 64`

**Seccomp-BPF (Kafel DSL)**:
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
| Network exfiltration | Empty network namespace + seccomp socket block | Security audit Test 3a-3d |
| Filesystem escape | pivot_root + read-only mounts + no host paths | Security audit Test 2a-2e |
| Privilege escalation | User namespace (non-root), seccomp blocks setuid/mount/ptrace | Security audit Test 6a-6d |
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

```sql
-- Core submissions table (partitioned by month)
CREATE TABLE submissions (
    job_id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    language        VARCHAR(20) NOT NULL,
    source_code     TEXT NOT NULL,
    stdin           TEXT DEFAULT '',
    stdout          TEXT DEFAULT '',
    stderr          TEXT DEFAULT '',
    status          VARCHAR(30) NOT NULL DEFAULT 'QUEUED',
    exit_code       INTEGER,
    time_used_ms    INTEGER,
    memory_used_kb  INTEGER,
    time_limit_ms   INTEGER NOT NULL DEFAULT 5000,
    memory_limit_kb INTEGER NOT NULL DEFAULT 262144,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
) PARTITION BY RANGE (created_at);

-- Indexes for common queries
CREATE INDEX idx_submissions_status ON submissions (status);
CREATE INDEX idx_submissions_created ON submissions (created_at DESC);
CREATE INDEX idx_submissions_language ON submissions (language);
```

### RabbitMQ Message Schema

```json
{
  "job_id": "01912345-6789-7abc-def0-123456789abc",
  "language": "python",
  "source_code": "print('hello')",
  "stdin": "",
  "time_limit_ms": 5000,
  "memory_limit_kb": 262144
}
```

- **Exchange**: `execution_tasks` (direct)
- **Queue**: `execution_tasks` (quorum, durable)
- **DLX**: `execution_tasks.dlx` (dead-letter after 3 retries)
- **Content-Type**: `application/json`

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
