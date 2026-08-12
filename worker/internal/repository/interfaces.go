package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/Harsh-BH/Sentinel/worker/internal/domain"
)

// ErrAlreadyTerminal is returned when a write is rejected because the job has
// already reached a final state. It means a duplicate delivery lost the race —
// not that anything went wrong.
var ErrAlreadyTerminal = errors.New("job already in terminal state")

// ClaimOutcome describes the result of trying to take ownership of a job.
type ClaimOutcome int

const (
	// ClaimAcquired means this worker now owns the job and must execute it.
	ClaimAcquired ClaimOutcome = iota
	// ClaimTerminal means the job already finished. A genuine duplicate delivery.
	ClaimTerminal
	// ClaimLeased means another worker holds a live lease on the job.
	ClaimLeased
	// ClaimNotFound means no such row exists — a poison message.
	ClaimNotFound
)

// JobRepository defines the interface for updating job state in the database.
//
// Every method takes createdAt alongside the job ID. That is not redundant:
// execution_jobs is range-partitioned on created_at, so a predicate on
// created_at is what lets Postgres prune to a single partition instead of
// probing the primary-key index of every one. Callers pass the value that came
// with the queue message.
type JobRepository interface {
	// Claim atomically takes ownership of a job by moving it to status and
	// setting a lease that expires after lease. It succeeds only if the job is
	// non-terminal AND unleased (or its previous lease has expired), which is
	// what makes a crashed worker's job reclaimable while still rejecting
	// concurrent duplicate deliveries.
	Claim(ctx context.Context, id uuid.UUID, createdAt time.Time, status domain.ExecutionStatus, lease time.Duration) (ClaimOutcome, error)

	// SetResult stores the execution result and clears the lease. It refuses to
	// overwrite a job that is already terminal, returning ErrAlreadyTerminal.
	SetResult(ctx context.Context, id uuid.UUID, createdAt time.Time, result *domain.ExecutionResult) error

	// SetStatus moves a non-terminal job to status and clears the lease. Like
	// SetResult it will not clobber a terminal state.
	SetStatus(ctx context.Context, id uuid.UUID, createdAt time.Time, status domain.ExecutionStatus) error
}

// Executor defines the interface for running code in a sandbox.
type Executor interface {
	Execute(ctx context.Context, req *domain.ExecutionRequest) (*domain.ExecutionResult, error)
}
