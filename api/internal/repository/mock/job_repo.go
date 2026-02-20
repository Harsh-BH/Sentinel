package mock

import (
	"context"
	"sync"

	"github.com/google/uuid"

	"github.com/Harsh-BH/Sentinel/api/internal/domain"
	"github.com/Harsh-BH/Sentinel/api/internal/repository"
)

// Ensure MockJobRepository implements repository.JobRepository.
var _ repository.JobRepository = (*MockJobRepository)(nil)

// MockJobRepository is an in-memory mock of the job repository for testing.
type MockJobRepository struct {
	mu   sync.RWMutex
	jobs map[uuid.UUID]*domain.Job

	// Hook functions for injecting errors
	CreateFunc       func(ctx context.Context, job *domain.Job) error
	GetByIDFunc      func(ctx context.Context, id uuid.UUID) (*domain.Job, error)
	UpdateStatusFunc func(ctx context.Context, id uuid.UUID, status domain.ExecutionStatus) error
	SetResultFunc    func(ctx context.Context, id uuid.UUID, result *domain.Job) error
}

// NewMockJobRepository creates a new mock repository.
func NewMockJobRepository() *MockJobRepository {
	return &MockJobRepository{
		jobs: make(map[uuid.UUID]*domain.Job),
	}
}

func (m *MockJobRepository) Create(ctx context.Context, job *domain.Job) error {
	if m.CreateFunc != nil {
		return m.CreateFunc(ctx, job)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobs[job.JobID] = job
	return nil
}

func (m *MockJobRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Job, error) {
	if m.GetByIDFunc != nil {
		return m.GetByIDFunc(ctx, id)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	job, ok := m.jobs[id]
	if !ok {
		return nil, domain.ErrJobNotFound
	}
	return job, nil
}

func (m *MockJobRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.ExecutionStatus) error {
	if m.UpdateStatusFunc != nil {
		return m.UpdateStatusFunc(ctx, id, status)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return domain.ErrJobNotFound
	}
	job.Status = status
	return nil
}

func (m *MockJobRepository) SetResult(ctx context.Context, id uuid.UUID, result *domain.Job) error {
	if m.SetResultFunc != nil {
		return m.SetResultFunc(ctx, id, result)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return domain.ErrJobNotFound
	}
	job.Stdout = result.Stdout
	job.Stderr = result.Stderr
	job.Status = result.Status
	job.ExitCode = result.ExitCode
	job.TimeUsedMs = result.TimeUsedMs
	job.MemoryUsedKB = result.MemoryUsedKB
	return nil
}

// GetAll returns all stored jobs (for test assertions).
func (m *MockJobRepository) GetAll() []*domain.Job {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*domain.Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		result = append(result, j)
	}
	return result
}
