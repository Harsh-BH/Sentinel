package mock

import (
	"context"
	"sync"

	"github.com/google/uuid"

	"github.com/Harsh-BH/Sentinel/worker/internal/domain"
	"github.com/Harsh-BH/Sentinel/worker/internal/repository"
)

// ---- JobRepository mock ----

var _ repository.JobRepository = (*JobRepository)(nil)

// JobRepository is a test double for repository.JobRepository.
type JobRepository struct {
	mu sync.Mutex

	UpdateStatusFn func(ctx context.Context, id uuid.UUID, status domain.ExecutionStatus) error
	SetResultFn    func(ctx context.Context, id uuid.UUID, result *domain.ExecutionResult) error

	// Recorded calls for assertions.
	StatusUpdates []StatusUpdate
	Results       []ResultUpdate
}

type StatusUpdate struct {
	ID     uuid.UUID
	Status domain.ExecutionStatus
}

type ResultUpdate struct {
	ID     uuid.UUID
	Result *domain.ExecutionResult
}

func (m *JobRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.ExecutionStatus) error {
	m.mu.Lock()
	m.StatusUpdates = append(m.StatusUpdates, StatusUpdate{ID: id, Status: status})
	m.mu.Unlock()
	if m.UpdateStatusFn != nil {
		return m.UpdateStatusFn(ctx, id, status)
	}
	return nil
}

func (m *JobRepository) SetResult(ctx context.Context, id uuid.UUID, result *domain.ExecutionResult) error {
	m.mu.Lock()
	m.Results = append(m.Results, ResultUpdate{ID: id, Result: result})
	m.mu.Unlock()
	if m.SetResultFn != nil {
		return m.SetResultFn(ctx, id, result)
	}
	return nil
}

// ---- IdempotencyStore mock ----

var _ repository.IdempotencyStore = (*IdempotencyStore)(nil)

// IdempotencyStore is a test double for repository.IdempotencyStore.
type IdempotencyStore struct {
	mu sync.Mutex

	AcquireLockFn func(ctx context.Context, jobID uuid.UUID) (bool, error)
	ReleaseLockFn func(ctx context.Context, jobID uuid.UUID) error

	AcquireCalls []uuid.UUID
	ReleaseCalls []uuid.UUID
}

func (m *IdempotencyStore) AcquireLock(ctx context.Context, jobID uuid.UUID) (bool, error) {
	m.mu.Lock()
	m.AcquireCalls = append(m.AcquireCalls, jobID)
	m.mu.Unlock()
	if m.AcquireLockFn != nil {
		return m.AcquireLockFn(ctx, jobID)
	}
	return true, nil // default: lock acquired
}

func (m *IdempotencyStore) ReleaseLock(ctx context.Context, jobID uuid.UUID) error {
	m.mu.Lock()
	m.ReleaseCalls = append(m.ReleaseCalls, jobID)
	m.mu.Unlock()
	if m.ReleaseLockFn != nil {
		return m.ReleaseLockFn(ctx, jobID)
	}
	return nil
}

// ---- Executor mock ----

var _ repository.Executor = (*Executor)(nil)

// Executor is a test double for repository.Executor.
type Executor struct {
	mu sync.Mutex

	ExecuteFn func(ctx context.Context, req *domain.ExecutionRequest) (*domain.ExecutionResult, error)

	ExecuteCalls []*domain.ExecutionRequest
}

func (m *Executor) Execute(ctx context.Context, req *domain.ExecutionRequest) (*domain.ExecutionResult, error) {
	m.mu.Lock()
	m.ExecuteCalls = append(m.ExecuteCalls, req)
	m.mu.Unlock()
	if m.ExecuteFn != nil {
		return m.ExecuteFn(ctx, req)
	}
	return &domain.ExecutionResult{
		Status:     domain.StatusSuccess,
		Stdout:     "Hello, World!\n",
		ExitCode:   0,
		TimeUsedMs: 42,
	}, nil
}
