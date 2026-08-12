-- =============================================================================
-- Project Sentinel — Rollback Initial Schema
-- =============================================================================

DROP TRIGGER IF EXISTS trg_execution_jobs_notify ON execution_jobs;
DROP TRIGGER IF EXISTS trg_execution_jobs_updated_at ON execution_jobs;
DROP FUNCTION IF EXISTS notify_job_status();
DROP FUNCTION IF EXISTS update_updated_at();
DROP TABLE IF EXISTS job_outbox CASCADE;
DROP TABLE IF EXISTS execution_jobs CASCADE;
DROP TYPE IF EXISTS execution_status;
DROP TYPE IF EXISTS language;
