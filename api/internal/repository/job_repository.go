package repository

import (
	"context"

	"github.com/google/uuid"

	"github.com/Harsh-BH/Sentinel/api/internal/domain"
)

// JobRepository defines the interface for job persistence operations.
// Implementations must be safe for concurrent use.
type JobRepository interface {
	// Create inserts a new job into the data store.
	Create(ctx context.Context, job *domain.Job) error

	// GetByID retrieves a job by its UUID.
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Job, error)

	// UpdateStatus atomically updates the status of a job.
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.ExecutionStatus) error

	// SetResult stores the execution result for a completed job.
	SetResult(ctx context.Context, id uuid.UUID, result *domain.Job) error
}
