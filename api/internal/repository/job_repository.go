package repository

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Harsh-BH/Sentinel/api/internal/domain"
)

// JobRepository defines the interface for job persistence operations.
// Implementations must be safe for concurrent use.
type JobRepository interface {
	// Job creation lives on OutboxRepository.CreateJobWithOutbox, because the row
	// and its queue message must be written in one transaction.

	// GetByID retrieves a job by its UUID.
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Job, error)

	// UpdateStatus moves a non-terminal job to status. It refuses to overwrite a
	// job that has already reached a final state, so a slow writer can never
	// resurrect a finished job.
	UpdateStatus(ctx context.Context, id uuid.UUID, createdAt time.Time, status domain.ExecutionStatus) error

	// ReclaimStuck clears the lease on, and returns, jobs stranded in a
	// non-terminal state: either never picked up (publish failed, or the API
	// crashed between the INSERT and the publish) or claimed by a worker that
	// died before writing a result.
	//
	// A job whose lease has expired is reclaimed immediately — the expired lease
	// is itself proof that its owner is gone. A job with no lease at all is only
	// reclaimed after grace, because nothing but elapsed time distinguishes
	// "stranded" from "still legitimately waiting in the queue".
	//
	// The lease is cleared rather than taken; see the implementation for why
	// taking a reservation lease deadlocks recovery.
	//
	// Concurrent reapers are safe: candidate selection uses FOR UPDATE SKIP
	// LOCKED, so two API replicas sweeping at the same instant get disjoint sets.
	ReclaimStuck(ctx context.Context, grace time.Duration, limit int) ([]*domain.Job, error)
}
