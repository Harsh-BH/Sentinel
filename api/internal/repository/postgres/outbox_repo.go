package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Harsh-BH/Sentinel/api/internal/domain"
	"github.com/Harsh-BH/Sentinel/api/internal/repository"
)

var _ repository.OutboxRepository = (*pgOutboxRepo)(nil)

type pgOutboxRepo struct {
	pool *pgxpool.Pool
}

// NewPostgresOutboxRepository creates a PostgreSQL-backed outbox repository.
func NewPostgresOutboxRepository(pool *pgxpool.Pool) repository.OutboxRepository {
	return &pgOutboxRepo{pool: pool}
}

func (r *pgOutboxRepo) CreateJobWithOutbox(ctx context.Context, job *domain.Job) error {
	now := time.Now().UTC()
	job.CreatedAt = now
	job.UpdatedAt = now

	// The queue message is serialised from the same struct the publisher would
	// send, so the relay reproduces a byte-identical message later.
	payload, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("postgres: marshal outbox payload: %w", err)
	}

	// One transaction, two inserts. This is the whole point of the outbox: there
	// is no window in which the job exists but its message does not.
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin outbox tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful commit

	const insertJob = `
		INSERT INTO execution_jobs (job_id, language, source_code, stdin, status, time_limit_ms, memory_limit_kb, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)`
	if _, err := tx.Exec(ctx, insertJob,
		job.JobID, job.Language, job.SourceCode, job.Stdin,
		job.Status, job.TimeLimitMs, job.MemoryLimitKB, now,
	); err != nil {
		return fmt.Errorf("postgres: insert job: %w", err)
	}

	const insertOutbox = `
		INSERT INTO job_outbox (job_id, job_created_at, payload, created_at)
		VALUES ($1, $2, $3, $2)`
	if _, err := tx.Exec(ctx, insertOutbox, job.JobID, now, payload); err != nil {
		return fmt.Errorf("postgres: insert outbox: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit outbox tx: %w", err)
	}
	return nil
}

func (r *pgOutboxRepo) MarkPublished(ctx context.Context, jobID uuid.UUID) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM job_outbox WHERE job_id = $1`, jobID); err != nil {
		return fmt.Errorf("postgres: mark outbox published: %w", err)
	}
	return nil
}

func (r *pgOutboxRepo) ClaimUnpublished(ctx context.Context, minAge time.Duration, limit int) ([]*domain.Job, error) {
	// minAge keeps the relay off the fast path. Submit publishes inline right
	// after commit; without this the relay could grab a row while that publish is
	// still in flight and send the message twice.
	const query = `
		WITH candidates AS (
			SELECT job_id FROM job_outbox
			WHERE published_at IS NULL
			  AND created_at < now() - make_interval(secs => $1)
			ORDER BY created_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE job_outbox o
		SET attempts = o.attempts + 1
		FROM candidates c
		WHERE o.job_id = c.job_id
		RETURNING o.payload`

	rows, err := r.pool.Query(ctx, query, minAge.Seconds(), limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: claim unpublished outbox rows: %w", err)
	}
	defer rows.Close()

	var jobs []*domain.Job
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("postgres: scan outbox payload: %w", err)
		}
		job := &domain.Job{}
		if err := json.Unmarshal(payload, job); err != nil {
			// A payload we cannot parse will never publish. Skip it rather than
			// stalling the whole relay; DropExhausted eventually clears it.
			continue
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate outbox rows: %w", err)
	}
	return jobs, nil
}

func (r *pgOutboxRepo) DropExhausted(ctx context.Context, maxAttempts int) (int, error) {
	// Mark the job failed and drop the entry in one statement, so a job can never
	// be left with no outbox row and no terminal status.
	const query = `
		WITH doomed AS (
			DELETE FROM job_outbox
			WHERE published_at IS NULL AND attempts >= $1
			RETURNING job_id, job_created_at
		)
		UPDATE execution_jobs j
		SET status = 'INTERNAL_ERROR',
		    stderr = 'Submission could not be delivered to the execution queue after ' || $1 || ' attempts.'
		FROM doomed d
		WHERE j.job_id = d.job_id
		  AND j.created_at = d.job_created_at
		  AND j.status NOT IN ` + terminalStatuses

	tag, err := r.pool.Exec(ctx, query, maxAttempts)
	if err != nil {
		return 0, fmt.Errorf("postgres: drop exhausted outbox rows: %w", err)
	}
	// Counts jobs newly marked failed. A job that had already reached a terminal
	// state still has its outbox row removed but is not counted here.
	return int(tag.RowsAffected()), nil
}
