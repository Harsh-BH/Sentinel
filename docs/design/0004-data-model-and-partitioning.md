# 0004 — Data model and partitioning

**Status:** Accepted
**Implements:** `migrations/001_initial_schema.up.sql`, `api/internal/domain/job.go`, `api/internal/repository/postgres/`

## Context

The system writes one row per submission and updates that row 2–3 times during the job lifecycle. The hot read path is "get the latest status of a specific job" — invoked by the WebSocket streamer and the `GET /api/v1/submissions/:id` poll endpoint. The cold read path is operational: dashboards counting executions by status, finding stuck jobs, audits.

Sentinel does not currently expose any per-user history queries (there is no user concept), so the schema is optimized for the two read patterns above and one write pattern: bounded-keyspace updates to recent rows.

## Schema

A single table, `execution_jobs`, **range-partitioned by `created_at`**:

```sql
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
```

Partitions are created **per quarter** (e.g. `execution_jobs_2026_q1`, `execution_jobs_2026_q2`, …).

Two enum types narrow the value space and avoid the cost of repeated string comparisons:

```sql
CREATE TYPE execution_status AS ENUM (
    'QUEUED', 'COMPILING', 'RUNNING', 'SUCCESS',
    'COMPILATION_ERROR', 'RUNTIME_ERROR', 'TIMEOUT',
    'MEMORY_LIMIT_EXCEEDED', 'INTERNAL_ERROR'
);
CREATE TYPE language AS ENUM ('python', 'cpp');
```

## Why partitioning

Partitioning is the single most impactful schema choice. Reasons:

1. **Drop is O(1).** Old quarters are dropped with `DROP TABLE execution_jobs_2025_q4` rather than `DELETE` + `VACUUM`. No long-running deletes, no bloat, no autovacuum thrash.
2. **Active partition stays small.** The hot read pattern hits jobs created within the last few minutes/hours — almost always in the current quarter. Postgres prunes inactive partitions at plan time, so query plans see a small relation.
3. **Indexes are partition-local.** The active-jobs partial index (see below) is scoped per partition, so it's tiny.
4. **Backups can be partition-scoped.** We can ship only recent partitions to standbys.

The tradeoff: cross-quarter queries pay the partition-pruning cost upfront. Acceptable — those queries are operational, not user-facing.

### Pre-creating partitions

`migrations/001_initial_schema.up.sql` ships partitions for 2026 Q1 and Q2. Future partitions must be created **before** rows for that range are inserted, otherwise the insert fails. We currently do this manually; a future cron job (or `pg_partman`) should automate it. Tracked as a known operational gap.

## UUIDv7 primary key

Job IDs are UUIDv7, generated **in the API usecase** (`uuid.NewV7()`), not in the database. Two reasons:
- **Time-ordered insertion** keeps B-tree pages from fragmenting (UUIDv4 produces random insertion points). Our write workload is append-only by `created_at`; UUIDv7 makes the PK index match that pattern.
- **Deterministic from the API.** The 202 response carries the ID before the row is committed, which simplifies the client-side WebSocket subscription contract.

We considered `BIGSERIAL` (smaller, even faster) but UUIDs are friendlier across services (no coordination needed if we ever shard) and impossible to enumerate.

## Indexing

```sql
-- Partial index for active (non-terminal) jobs — stays small in RAM
CREATE INDEX idx_active_jobs ON execution_jobs(job_id)
    WHERE status IN ('QUEUED', 'COMPILING', 'RUNNING');

-- Compound index for status + time ordering (operational queries)
CREATE INDEX idx_jobs_status ON execution_jobs(status, created_at);
```

### `idx_active_jobs` (partial)

The WebSocket polling path does:

```sql
SELECT status, exit_code, time_used_ms, ...
FROM execution_jobs
WHERE job_id = $1;
```

Even with the PK alone, this is a single index lookup. The partial index is **not** for this query — the PK already serves it. The partial index helps the operational query "find jobs that have been QUEUED for too long":

```sql
SELECT job_id FROM execution_jobs
WHERE status IN ('QUEUED', 'COMPILING', 'RUNNING')
  AND created_at < NOW() - INTERVAL '5 minutes';
```

Because the index only contains rows that match the WHERE clause, it stays tiny (≈ in-flight job count, typically <100 even under load) and effectively lives in shared_buffers. Without the partial predicate this index would grow with the entire job history.

### `idx_jobs_status` (compound)

Used by Grafana panels:

```sql
SELECT status, COUNT(*) FROM execution_jobs
WHERE created_at > NOW() - INTERVAL '1 hour'
GROUP BY status;
```

The `(status, created_at)` ordering supports both filtering on status alone and on status+time.

## Status state machine

```
                ┌──────────────────────────────────────┐
                │                                      │
                ▼                                      │
   ┌─────────┐    ┌─────────┐    ┌─────────────┐     │
   │ QUEUED  │───▶│COMPILING│───▶│   RUNNING   │     │
   └─────────┘    └────┬────┘    └──────┬──────┘     │
                       │                │             │
                       ▼                ├──▶ SUCCESS  │
                  COMPILATION           ├──▶ RUNTIME_ERROR
                    _ERROR              ├──▶ TIMEOUT  │
                                        ├──▶ MEMORY_LIMIT_EXCEEDED
                                        └──▶ INTERNAL_ERROR ──┘
                                                   (terminal, no retry)
```

`ExecutionStatus.IsTerminal()` (`api/internal/domain/job.go`) is the canonical predicate. Anything that wants to know "is this job done" should call it rather than open-coding the set; the set has been wrong in three different places in this repo's history.

The transitions are not enforced at the database level — there is no `CHECK (...)` or trigger. The application is the single writer of the status column (only the worker writes terminal statuses; only the API writes `QUEUED`); enforcing transitions in DB would just duplicate that.

## What is deliberately NOT in the schema

- **Soft deletes / `deleted_at`.** Deletion is by partition drop. A user-driven delete operation would require revisiting this.
- **`updated_at` trigger logic in app code.** A `BEFORE UPDATE` trigger sets `updated_at = NOW()` so writers can't forget it.
- **A separate `execution_results` table.** Splitting columns across two tables would force a join on the hot read path. Result columns are `NULL`-able and only populated on terminal statuses.
- **A row-level user FK.** No user concept exists in v1. When it's added, it goes on a separate `submissions` table that references this one, not by altering the partitioned table (altering partitioned tables is operationally painful).

## Alternatives considered

- **Time-series database (TimescaleDB, Influx).** Tempting because writes are append-mostly. Rejected because we *update* rows (status transitions); TSDBs are bad at update-in-place. Postgres native partitioning gets us most of the benefit without leaving the relational world.
- **Append-only event log + materialized current-state.** Cleaner conceptually (each status change is an event row), but doubles write volume and forces every read to either hit the materialized view (eventual consistency) or scan events (slow). The single-mutable-row model is fine because there's a single writer per row at a time.
- **Sharding by `job_id`.** Premature. Single-instance Postgres handles our throughput with margin (see [0003 capacity math](./0003-scaling-architecture.md)). When we outgrow it, the partitioned shape makes shard-by-time the natural step.
