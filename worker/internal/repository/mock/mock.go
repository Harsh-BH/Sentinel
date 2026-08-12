package mock

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Harsh-BH/Sentinel/worker/internal/domain"
	"github.com/Harsh-BH/Sentinel/worker/internal/repository"
)

// ---- JobRepository mock ----

var _ repository.JobRepository = (*JobRepository)(nil)

// JobRepository is a test double for repository.JobRepository.
type JobRepository struct {
	mu sync.Mutex

	ClaimFn     func(ctx context.Context, id uuid.UUID, createdAt time.Time, status domain.ExecutionStatus, lease time.Duration) (repository.ClaimOutcome, error)
	SetResultFn func(ctx context.Context, id uuid.UUID, createdAt time.Time, result *domain.ExecutionResult) error
	SetStatusFn func(ctx context.Context, id uuid.UUID, createdAt time.Time, status domain.ExecutionStatus) error

	// Recorded calls for assertions.
	Claims        []ClaimCall
	StatusUpdates []StatusUpdate
	Results       []ResultUpdate
}

type ClaimCall struct {
	ID     uuid.UUID
	Status domain.ExecutionStatus
	Lease  time.Duration
}

type StatusUpdate struct {
	ID     uuid.UUID
	Status domain.ExecutionStatus
}

type ResultUpdate struct {
	ID     uuid.UUID
	Result *domain.ExecutionResult
}

func (m *JobRepository) Claim(
	ctx context.Context,
	id uuid.UUID,
	createdAt time.Time,
	status domain.ExecutionStatus,
	lease time.Duration,
) (repository.ClaimOutcome, error) {
	m.mu.Lock()
	m.Claims = append(m.Claims, ClaimCall{ID: id, Status: status, Lease: lease})
	m.mu.Unlock()
	if m.ClaimFn != nil {
		return m.ClaimFn(ctx, id, createdAt, status, lease)
	}
	return repository.ClaimAcquired, nil
}

func (m *JobRepository) SetResult(
	ctx context.Context,
	id uuid.UUID,
	createdAt time.Time,
	result *domain.ExecutionResult,
) error {
	m.mu.Lock()
	m.Results = append(m.Results, ResultUpdate{ID: id, Result: result})
	m.mu.Unlock()
	if m.SetResultFn != nil {
		return m.SetResultFn(ctx, id, createdAt, result)
	}
	return nil
}

func (m *JobRepository) SetStatus(
	ctx context.Context,
	id uuid.UUID,
	createdAt time.Time,
	status domain.ExecutionStatus,
) error {
	m.mu.Lock()
	m.StatusUpdates = append(m.StatusUpdates, StatusUpdate{ID: id, Status: status})
	m.mu.Unlock()
	if m.SetStatusFn != nil {
		return m.SetStatusFn(ctx, id, createdAt, status)
	}
	return nil
}

// ClaimCallCount returns how many times Claim was invoked.
func (m *JobRepository) ClaimCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Claims)
}

// ResultCount returns how many times SetResult was invoked.
func (m *JobRepository) ResultCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Results)
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

// ExecuteCallCount returns how many times Execute was invoked.
func (m *Executor) ExecuteCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.ExecuteCalls)
}
