package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Harsh-BH/Sentinel/worker/internal/domain"
	"github.com/Harsh-BH/Sentinel/worker/internal/repository"
)

var _ repository.JobRepository = (*pgJobRepo)(nil)

// partitionSlack widens the created_at predicate used for partition pruning.
//
// We cannot match created_at exactly: Go timestamps carry nanoseconds and
// Postgres timestamptz stores microseconds, so the value that round-trips
// through the queue message is truncated relative to the one in the row. A
// window avoids that mismatch while still pruning to one or two partitions.
const partitionSlack = 24 * time.Hour

// terminalStatuses is the SQL list guarding every write against clobbering a
// finished job. Kept as a literal so the guard is visible in the query text.
const terminalStatuses = `('SUCCESS','COMPILATION_ERROR','RUNTIME_ERROR','TIMEOUT','MEMORY_LIMIT_EXCEEDED','INTERNAL_ERROR')`

type pgJobRepo struct {
	pool *pgxpool.Pool
}

// NewPostgresJobRepository creates a new PostgreSQL-backed job repository for the worker.
func NewPostgresJobRepository(pool *pgxpool.Pool) repository.JobRepository {
	return &pgJobRepo{pool: pool}
}

func window(createdAt time.Time) (time.Time, time.Time) {
	return createdAt.Add(-partitionSlack), createdAt.Add(partitionSlack)
}

func (r *pgJobRepo) Claim(
	ctx context.Context,
	id uuid.UUID,
	createdAt time.Time,
	status domain.ExecutionStatus,
	lease time.Duration,
) (repository.ClaimOutcome, error) {
	lo, hi := window(createdAt)

	// A single conditional UPDATE is the whole mutual-exclusion mechanism. Two
	// workers racing on the same row serialise on Postgres' row lock, and the
	// loser re-evaluates the WHERE against the winner's committed row — by which
	// point lease_expires_at is in the future, so it matches zero rows.
	const claimQuery = `
		UPDATE execution_jobs
		SET status = $1,
		    attempts = attempts + 1,
		    lease_expires_at = now() + make_interval(secs => $2)
		WHERE job_id = $3
		  AND created_at >= $4 AND created_at < $5
		  AND status NOT IN ` + terminalStatuses + `
		  AND (lease_expires_at IS NULL OR lease_expires_at < now())`

	tag, err := r.pool.Exec(ctx, claimQuery, status, lease.Seconds(), id, lo, hi)
	if err != nil {
		return repository.ClaimNotFound, fmt.Errorf("postgres: claim job: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return repository.ClaimAcquired, nil
	}

	// Zero rows. Work out which of the three reasons it was, so the caller can
	// tell "already done" (ack) from "no such job" (dead-letter).
	const reasonQuery = `
		SELECT status, (lease_expires_at IS NOT NULL AND lease_expires_at >= now())
		FROM execution_jobs
		WHERE job_id = $1 AND created_at >= $2 AND created_at < $3`

	var current domain.ExecutionStatus
	var leaseLive bool
	if err := r.pool.QueryRow(ctx, reasonQuery, id, lo, hi).Scan(&current, &leaseLive); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return repository.ClaimNotFound, nil
		}
		return repository.ClaimNotFound, fmt.Errorf("postgres: claim reason: %w", err)
	}
	if current.IsTerminal() {
		return repository.ClaimTerminal, nil
	}
	return repository.ClaimLeased, nil
}

func (r *pgJobRepo) SetResult(
	ctx context.Context,
	id uuid.UUID,
	createdAt time.Time,
	result *domain.ExecutionResult,
) error {
	lo, hi := window(createdAt)

	const query = `
		UPDATE execution_jobs
		SET stdout = $1, stderr = $2, status = $3, exit_code = $4,
		    time_used_ms = $5, memory_used_kb = $6, lease_expires_at = NULL
		WHERE job_id = $7
		  AND created_at >= $8 AND created_at < $9
		  AND status NOT IN ` + terminalStatuses

	tag, err := r.pool.Exec(ctx, query,
		result.Stdout, result.Stderr, result.Status, result.ExitCode,
		result.TimeUsedMs, result.MemoryUsedKB, id, lo, hi,
	)
	if err != nil {
		return fmt.Errorf("postgres: set result: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrAlreadyTerminal
	}
	return nil
}

func (r *pgJobRepo) SetStatus(
	ctx context.Context,
	id uuid.UUID,
	createdAt time.Time,
	status domain.ExecutionStatus,
) error {
	lo, hi := window(createdAt)

	const query = `
		UPDATE execution_jobs
		SET status = $1, lease_expires_at = NULL
		WHERE job_id = $2
		  AND created_at >= $3 AND created_at < $4
		  AND status NOT IN ` + terminalStatuses

	tag, err := r.pool.Exec(ctx, query, status, id, lo, hi)
	if err != nil {
		return fmt.Errorf("postgres: set status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrAlreadyTerminal
	}
	return nil
}
