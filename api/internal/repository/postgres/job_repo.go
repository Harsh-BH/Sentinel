package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Harsh-BH/Sentinel/api/internal/domain"
	"github.com/Harsh-BH/Sentinel/api/internal/repository"
)

// Ensure pgJobRepo implements repository.JobRepository.
var _ repository.JobRepository = (*pgJobRepo)(nil)

// partitionSlack widens the created_at predicate used for partition pruning. It
// only has to exceed the skew between a job's UUIDv7 timestamp and its stored
// created_at — microseconds in practice — so a day of slack is far more than
// enough while still pruning to one or two partitions.
const partitionSlack = 24 * time.Hour

const terminalStatuses = `('SUCCESS','COMPILATION_ERROR','RUNTIME_ERROR','TIMEOUT','MEMORY_LIMIT_EXCEEDED','INTERNAL_ERROR')`

const jobColumns = `job_id, language, source_code, stdin, stdout, stderr, status,
	       exit_code, time_used_ms, memory_used_kb, time_limit_ms, memory_limit_kb,
	       created_at, updated_at`

type pgJobRepo struct {
	pool *pgxpool.Pool
}

// NewPostgresJobRepository creates a new PostgreSQL-backed job repository.
func NewPostgresJobRepository(pool *pgxpool.Pool) repository.JobRepository {
	return &pgJobRepo{pool: pool}
}

// GetByID reads a job by primary key.
//
// execution_jobs is range-partitioned on created_at, so a query with only
// `WHERE job_id = $1` cannot be pruned — Postgres has to probe the index of
// every partition. UUIDv7 embeds the generation timestamp in its first 48 bits,
// so we can reconstruct a narrow created_at window from the ID itself and let
// the planner skip every other partition.
//
// If the ID is not a v7 (or the windowed read misses, which should not happen)
// we fall back to the unpruned query. Correctness never depends on the
// optimisation.
func (r *pgJobRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Job, error) {
	if ts, ok := uuidV7Time(id); ok {
		job, err := r.getByIDWindowed(ctx, id, ts.Add(-partitionSlack), ts.Add(partitionSlack))
		if err == nil {
			return job, nil
		}
		if !errors.Is(err, domain.ErrJobNotFound) {
			return nil, err
		}
	}

	query := `SELECT ` + jobColumns + ` FROM execution_jobs WHERE job_id = $1`
	return scanJob(r.pool.QueryRow(ctx, query, id))
}

func (r *pgJobRepo) getByIDWindowed(ctx context.Context, id uuid.UUID, lo, hi time.Time) (*domain.Job, error) {
	query := `SELECT ` + jobColumns + `
		FROM execution_jobs
		WHERE job_id = $1 AND created_at >= $2 AND created_at < $3`
	return scanJob(r.pool.QueryRow(ctx, query, id, lo, hi))
}

func scanJob(row pgx.Row) (*domain.Job, error) {
	job := &domain.Job{}
	err := row.Scan(
		&job.JobID, &job.Language, &job.SourceCode, &job.Stdin,
		&job.Stdout, &job.Stderr, &job.Status,
		&job.ExitCode, &job.TimeUsedMs, &job.MemoryUsedKB,
		&job.TimeLimitMs, &job.MemoryLimitKB,
		&job.CreatedAt, &job.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrJobNotFound
		}
		return nil, fmt.Errorf("postgres: get job by id: %w", err)
	}
	return job, nil
}

// uuidV7Time extracts the embedded millisecond timestamp from a UUIDv7.
// Returns ok=false for any other UUID version.
func uuidV7Time(id uuid.UUID) (time.Time, bool) {
	if id.Version() != 7 {
		return time.Time{}, false
	}
	ms := int64(id[0])<<40 | int64(id[1])<<32 | int64(id[2])<<24 |
		int64(id[3])<<16 | int64(id[4])<<8 | int64(id[5])
	return time.UnixMilli(ms).UTC(), true
}

func (r *pgJobRepo) UpdateStatus(ctx context.Context, id uuid.UUID, createdAt time.Time, status domain.ExecutionStatus) error {
	query := `
		UPDATE execution_jobs SET status = $1
		WHERE job_id = $2
		  AND created_at >= $3 AND created_at < $4
		  AND status NOT IN ` + terminalStatuses

	lo, hi := createdAt.Add(-partitionSlack), createdAt.Add(partitionSlack)
	tag, err := r.pool.Exec(ctx, query, status, id, lo, hi)
	if err != nil {
		return fmt.Errorf("postgres: update status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrJobNotFound
	}
	return nil
}

func (r *pgJobRepo) ReclaimStuck(ctx context.Context, grace time.Duration, limit int) ([]*domain.Job, error) {
	// Two distinct "stranded" cases, with different staleness signals:
	//
	//   * Lease present but expired — a worker claimed the job and then died. The
	//     expired lease IS the evidence; no extra grace period is needed, so this
	//     recovers as fast as lease-expiry + one sweep.
	//   * No lease at all — either never picked up (the dual-write gap) or already
	//     reaped once. Here only elapsed time can distinguish "stranded" from
	//     "legitimately still queued", so the grace period applies.
	//
	// The reaper CLEARS the lease rather than taking one. Taking a reservation
	// lease deadlocks recovery: the worker that receives the re-published message
	// cannot claim a job whose lease is live, so it drops the message as a
	// duplicate and the job stays stuck until the reaper's own lease lapses —
	// then repeats, forever, without ever incrementing attempts.
	//
	// Re-reaping the same row is instead suppressed by updated_at: this UPDATE
	// fires the updated_at trigger, so the row falls into the second branch above
	// and is ignored for a full grace period.
	//
	// attempts is incremented here as well as on worker claim, so it counts total
	// delivery attempts. Without that, a job re-published into a queue with no
	// live consumer never increments anything, so the retry cap never trips and
	// the reaper keeps stacking duplicate messages indefinitely.
	//
	// FOR UPDATE SKIP LOCKED keeps concurrent reapers on disjoint rows: a replica
	// that loses the race skips ahead instead of blocking behind the winner.
	query := `
		WITH candidates AS (
			SELECT job_id, created_at
			FROM execution_jobs
			WHERE status IN ('QUEUED', 'COMPILING', 'RUNNING')
			  AND (
			        (lease_expires_at IS NOT NULL AND lease_expires_at < now())
			     OR (lease_expires_at IS NULL AND updated_at < now() - make_interval(secs => $1))
			      )
			  -- Leave rows the outbox relay still owns alone. An unpublished outbox
			  -- entry means delivery is already someone else's job, and the relay
			  -- retries every second versus the reaper's minutes — both publishing
			  -- would just create duplicate messages for the worker to dedupe.
			  AND NOT EXISTS (
			        SELECT 1 FROM job_outbox o
			        WHERE o.job_id = execution_jobs.job_id AND o.published_at IS NULL
			      )
			ORDER BY created_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE execution_jobs j
		SET lease_expires_at = NULL, attempts = j.attempts + 1
		FROM candidates c
		WHERE j.job_id = c.job_id AND j.created_at = c.created_at
		RETURNING j.job_id, j.language, j.source_code, j.stdin, j.status,
		          j.time_limit_ms, j.memory_limit_kb, j.attempts, j.created_at, j.updated_at`

	rows, err := r.pool.Query(ctx, query, grace.Seconds(), limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: reclaim stuck jobs: %w", err)
	}
	defer rows.Close()

	var jobs []*domain.Job
	for rows.Next() {
		job := &domain.Job{}
		if err := rows.Scan(
			&job.JobID, &job.Language, &job.SourceCode, &job.Stdin, &job.Status,
			&job.TimeLimitMs, &job.MemoryLimitKB, &job.Attempts,
			&job.CreatedAt, &job.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan stuck job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate stuck jobs: %w", err)
	}
	return jobs, nil
}
