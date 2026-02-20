package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Harsh-BH/Sentinel/api/internal/domain"
	"github.com/Harsh-BH/Sentinel/api/internal/repository"
)

// Ensure pgJobRepo implements repository.JobRepository.
var _ repository.JobRepository = (*pgJobRepo)(nil)

type pgJobRepo struct {
	pool *pgxpool.Pool
}

// NewPostgresJobRepository creates a new PostgreSQL-backed job repository.
func NewPostgresJobRepository(pool *pgxpool.Pool) repository.JobRepository {
	return &pgJobRepo{pool: pool}
}

func (r *pgJobRepo) Create(ctx context.Context, job *domain.Job) error {
	query := `
		INSERT INTO execution_jobs (job_id, language, source_code, stdin, status, time_limit_ms, memory_limit_kb, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

	now := time.Now().UTC()
	_, err := r.pool.Exec(ctx, query,
		job.JobID, job.Language, job.SourceCode, job.Stdin,
		job.Status, job.TimeLimitMs, job.MemoryLimitKB, now, now,
	)
	if err != nil {
		return fmt.Errorf("postgres: create job: %w", err)
	}
	job.CreatedAt = now
	job.UpdatedAt = now
	return nil
}

func (r *pgJobRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Job, error) {
	query := `
		SELECT job_id, language, source_code, stdin, stdout, stderr, status,
		       exit_code, time_used_ms, memory_used_kb, time_limit_ms, memory_limit_kb,
		       created_at, updated_at
		FROM execution_jobs
		WHERE job_id = $1`

	job := &domain.Job{}
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&job.JobID, &job.Language, &job.SourceCode, &job.Stdin,
		&job.Stdout, &job.Stderr, &job.Status,
		&job.ExitCode, &job.TimeUsedMs, &job.MemoryUsedKB,
		&job.TimeLimitMs, &job.MemoryLimitKB,
		&job.CreatedAt, &job.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: get job by id: %w", err)
	}
	return job, nil
}

func (r *pgJobRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.ExecutionStatus) error {
	query := `UPDATE execution_jobs SET status = $1, updated_at = $2 WHERE job_id = $3`
	tag, err := r.pool.Exec(ctx, query, status, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("postgres: update status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrJobNotFound
	}
	return nil
}

func (r *pgJobRepo) SetResult(ctx context.Context, id uuid.UUID, result *domain.Job) error {
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
		return domain.ErrJobNotFound
	}
	return nil
}
