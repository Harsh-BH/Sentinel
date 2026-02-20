package repository

import (
	"context"

	"github.com/google/uuid"

	"github.com/Harsh-BH/Sentinel/worker/internal/domain"
)

// JobRepository defines the interface for updating job state in the database.
type JobRepository interface {
	// UpdateStatus atomically updates the status of a job.
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.ExecutionStatus) error

	// SetResult stores the execution result for a completed job.
	SetResult(ctx context.Context, id uuid.UUID, result *domain.ExecutionResult) error
}

// IdempotencyStore defines the interface for distributed deduplication locks.
type IdempotencyStore interface {
	// AcquireLock attempts to acquire an exclusive processing lock for a job.
	// Returns true if the lock was acquired (first time), false if already locked (duplicate).
	AcquireLock(ctx context.Context, jobID uuid.UUID) (bool, error)

	// ReleaseLock releases the processing lock with a TTL for eventual cleanup.
	ReleaseLock(ctx context.Context, jobID uuid.UUID) error
}
