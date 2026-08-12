-- GENERATED FILE — DO NOT EDIT.
-- Source: migrations/001_initial_schema.up.sql
-- Regenerate: make k8s-sync-schema   (CI fails if this drifts)
--
-- Kustomize refuses file sources outside its root, so the schema is copied
-- here rather than referenced. The copy is generated and drift-checked so it
-- cannot rot the way the previous hand-maintained inline copy did — that one
-- had drifted into invalid DDL (PRIMARY KEY missing the partition column).

-- =============================================================================
-- Project Sentinel — Initial Database Schema
-- =============================================================================

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- Execution status enum
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

-- Supported languages enum
CREATE TYPE language AS ENUM ('python', 'cpp');

-- Main execution jobs table (partitioned by created_at)
-- The primary key of a partitioned table must include every partition-key
-- column, so it is (job_id, created_at) rather than job_id alone. job_id is
-- a UUID and remains effectively unique on its own.
CREATE TABLE execution_jobs (
    job_id          UUID NOT NULL,
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
    -- Execution lease. A worker "claims" a job by setting this to now()+slack.
    -- While the lease is in the future the job is considered actively owned; once
    -- it expires the job is reclaimable. This is what makes a crashed worker's
    -- job re-executable instead of stranded, and it lives in Postgres (durable)
    -- rather than Redis (evictable) on purpose.
    lease_expires_at TIMESTAMPTZ,
    -- Number of times a worker has claimed this job. Bounds reaper retries.
    attempts        SMALLINT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (job_id, created_at)
) PARTITION BY RANGE (created_at);

-- Initial partition: Q1 2026
CREATE TABLE execution_jobs_2026_q1 PARTITION OF execution_jobs
    FOR VALUES FROM ('2026-01-01') TO ('2026-04-01');

-- Q2 2026
CREATE TABLE execution_jobs_2026_q2 PARTITION OF execution_jobs
    FOR VALUES FROM ('2026-04-01') TO ('2026-07-01');

-- Q3 2026
CREATE TABLE execution_jobs_2026_q3 PARTITION OF execution_jobs
    FOR VALUES FROM ('2026-07-01') TO ('2026-10-01');

-- Q4 2026
CREATE TABLE execution_jobs_2026_q4 PARTITION OF execution_jobs
    FOR VALUES FROM ('2026-10-01') TO ('2027-01-01');

-- Q1 2027
CREATE TABLE execution_jobs_2027_q1 PARTITION OF execution_jobs
    FOR VALUES FROM ('2027-01-01') TO ('2027-04-01');

-- Safety net: rows for any quarter without a dedicated partition land here
-- instead of failing the INSERT (SQLSTATE 23514). Pre-created quarterly
-- partitions are a time-bomb without this — they expire into the past.
-- ponytail: DEFAULT partition, replace with pg_partman/cron auto-provisioning
-- if the default ever accumulates enough rows to hurt query plans.
CREATE TABLE execution_jobs_default PARTITION OF execution_jobs DEFAULT;

-- Reaper index. This is the ONLY secondary index, because it is the only one a
-- query in this codebase can actually use.
--
-- The two indexes that used to live here were dead weight:
--   * idx_active_jobs ON (job_id) WHERE status IN (non-terminal) — a partial
--     index is only usable when the query's own predicate implies the index
--     predicate. The read path is `WHERE job_id = $1` with no status clause, so
--     the planner could never prove a row qualified and never used it.
--   * idx_jobs_status ON (status, created_at) — no query filtered on status.
-- Both cost write amplification on every INSERT/UPDATE and bought nothing.
--
-- The reaper's sweep IS status-qualified and lease-ordered, so it matches:
--   WHERE status IN ('QUEUED','COMPILING','RUNNING')
--     AND (lease_expires_at IS NULL OR lease_expires_at < now())
-- Point reads are served by the (job_id, created_at) primary key.
CREATE INDEX idx_jobs_reap ON execution_jobs (lease_expires_at NULLS FIRST, created_at)
    WHERE status IN ('QUEUED', 'COMPILING', 'RUNNING');

-- =============================================================================
-- Transactional outbox
-- =============================================================================
-- Submitting a job used to be two writes to two systems: INSERT the row, then
-- publish to RabbitMQ. Those cannot be made atomic, so a crash in between left a
-- QUEUED row that no worker would ever see.
--
-- The outbox closes that gap: the job row and its queue message are written in
-- ONE transaction. Publishing then happens after commit, and anything not
-- confirmed is picked up by the relay. The message is durable the moment the
-- transaction commits, which is why POST /submissions can return 202 even while
-- the broker is unreachable.
--
-- Deliberately NOT partitioned: this table is a work queue that stays small
-- (rows are deleted once published), so partitioning would add planning cost for
-- no benefit. Contrast execution_jobs, which grows without bound.
CREATE TABLE job_outbox (
    job_id       UUID        NOT NULL,
    -- Carried so the worker can prune partitions on execution_jobs, and so the
    -- relay can rebuild the message without touching the jobs table.
    job_created_at TIMESTAMPTZ NOT NULL,
    payload      JSONB       NOT NULL,
    attempts     SMALLINT    NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Set once the broker has confirmed the publish. Rows are deleted on
    -- confirmation, so this exists mainly for debugging a stuck relay.
    published_at TIMESTAMPTZ,
    PRIMARY KEY (job_id)
);

-- The relay's only query: unpublished rows, oldest first. Partial index so it
-- stays the size of the backlog rather than the size of the table.
CREATE INDEX idx_outbox_unpublished ON job_outbox (created_at)
    WHERE published_at IS NULL;

-- =============================================================================
-- Status-change notifications (LISTEN/NOTIFY)
-- =============================================================================
-- Emitted inside the same transaction as the status write, so a listener can
-- never be told about a state that did not commit. The API's WebSocket handler
-- listens on this channel and pushes to connected clients, instead of polling
-- the table twice a second per connection.
--
-- Payload is deliberately tiny (job_id:status). NOTIFY payloads are capped at
-- 8000 bytes and stdout/stderr can be 64 KB, so the listener uses the payload as
-- a wake-up and reads the row itself.
CREATE OR REPLACE FUNCTION notify_job_status()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.status IS DISTINCT FROM OLD.status THEN
        PERFORM pg_notify('job_status', NEW.job_id::text || ':' || NEW.status::text);
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_execution_jobs_notify
    AFTER UPDATE ON execution_jobs
    FOR EACH ROW
    EXECUTE FUNCTION notify_job_status();

-- Trigger to auto-update updated_at
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_execution_jobs_updated_at
    BEFORE UPDATE ON execution_jobs
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();
