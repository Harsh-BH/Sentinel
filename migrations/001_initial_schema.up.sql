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

-- Initial partition: Q1 2026
CREATE TABLE execution_jobs_2026_q1 PARTITION OF execution_jobs
    FOR VALUES FROM ('2026-01-01') TO ('2026-04-01');

-- Q2 2026
CREATE TABLE execution_jobs_2026_q2 PARTITION OF execution_jobs
    FOR VALUES FROM ('2026-04-01') TO ('2026-07-01');

-- Partial index for active (non-terminal) jobs — stays small in RAM
CREATE INDEX idx_active_jobs ON execution_jobs(job_id)
    WHERE status IN ('QUEUED', 'COMPILING', 'RUNNING');

-- Index for polling by status + time ordering
CREATE INDEX idx_jobs_status ON execution_jobs(status, created_at);

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
