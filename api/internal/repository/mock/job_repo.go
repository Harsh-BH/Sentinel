package mock

import (
	"context"
	"sync"
	"time"

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
	UpdateStatusFunc func(ctx context.Context, id uuid.UUID, createdAt time.Time, status domain.ExecutionStatus) error
	ReclaimStuckFunc func(ctx context.Context, grace time.Duration, limit int) ([]*domain.Job, error)
}

// NewMockJobRepository creates a new mock repository.
func NewMockJobRepository() *MockJobRepository {
	return &MockJobRepository{
		jobs: make(map[uuid.UUID]*domain.Job),
	}
}

// Seed stages a job row directly, standing in for the outbox's transactional
// insert in tests that only care about reads.
func (m *MockJobRepository) Seed(job *domain.Job) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}
	m.jobs[job.JobID] = job
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

func (m *MockJobRepository) UpdateStatus(ctx context.Context, id uuid.UUID, createdAt time.Time, status domain.ExecutionStatus) error {
	if m.UpdateStatusFunc != nil {
		return m.UpdateStatusFunc(ctx, id, createdAt, status)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return domain.ErrJobNotFound
	}
	// Mirror the SQL guard: never clobber a terminal state.
	if job.Status.IsTerminal() {
		return domain.ErrJobNotFound
	}
	job.Status = status
	return nil
}

func (m *MockJobRepository) ReclaimStuck(ctx context.Context, grace time.Duration, limit int) ([]*domain.Job, error) {
	if m.ReclaimStuckFunc != nil {
		return m.ReclaimStuckFunc(ctx, grace, limit)
	}
	return nil, nil
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
