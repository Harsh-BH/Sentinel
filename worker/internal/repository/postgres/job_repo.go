package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Harsh-BH/Sentinel/worker/internal/domain"
	"github.com/Harsh-BH/Sentinel/worker/internal/repository"
)

var _ repository.JobRepository = (*pgJobRepo)(nil)

type pgJobRepo struct {
	pool *pgxpool.Pool
}

// NewPostgresJobRepository creates a new PostgreSQL-backed job repository for the worker.
func NewPostgresJobRepository(pool *pgxpool.Pool) repository.JobRepository {
	return &pgJobRepo{pool: pool}
}

func (r *pgJobRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.ExecutionStatus) error {
	query := `UPDATE execution_jobs SET status = $1, updated_at = $2 WHERE job_id = $3`
	tag, err := r.pool.Exec(ctx, query, status, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("postgres: update status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("postgres: job not found: %s", id)
	}
	return nil
}

func (r *pgJobRepo) SetResult(ctx context.Context, id uuid.UUID, result *domain.ExecutionResult) error {
	query := `
		UPDATE execution_jobs
		SET stdout = $1, stderr = $2, status = $3, exit_code = $4,
		    time_used_ms = $5, memory_used_kb = $6, updated_at = $7
		WHERE job_id = $8`

	tag, err := r.pool.Exec(ctx, query,
		result.Stdout, result.Stderr, result.Status, result.ExitCode,
		result.TimeUsedMs, result.MemoryUsedKB, time.Now().UTC(), id,
	)
	if err != nil {
		return fmt.Errorf("postgres: set result: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("postgres: job not found: %s", id)
	}
	return nil
}
