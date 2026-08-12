package mock

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Harsh-BH/Sentinel/api/internal/domain"
	"github.com/Harsh-BH/Sentinel/api/internal/repository"
)

var _ repository.OutboxRepository = (*MockOutboxRepository)(nil)

// MockOutboxRepository is an in-memory outbox for testing. It models the one
// property that matters: the job row and its outbox entry appear together, and
// the entry survives until something confirms the publish.
type MockOutboxRepository struct {
	mu      sync.Mutex
	jobs    map[uuid.UUID]*domain.Job
	pending map[uuid.UUID]int // job_id -> attempts

	// In production the job row and the outbox entry are written to the same
	// database in one transaction, so a job created here is immediately readable
	// through JobRepository. Attaching a job store reproduces that; without it a
	// test would submit into one store and read from another and never see its own
	// write — which says nothing about the real system.
	jobStore *MockJobRepository

	CreateFunc           func(ctx context.Context, job *domain.Job) error
	MarkPublishedFunc    func(ctx context.Context, jobID uuid.UUID) error
	ClaimUnpublishedFunc func(ctx context.Context, minAge time.Duration, limit int) ([]*domain.Job, error)
	DropExhaustedFunc    func(ctx context.Context, maxAttempts int) (int, error)
}

func NewMockOutboxRepository() *MockOutboxRepository {
	return &MockOutboxRepository{
		jobs:    make(map[uuid.UUID]*domain.Job),
		pending: make(map[uuid.UUID]int),
	}
}

// BackedBy links this outbox to a job repository so created jobs are readable
// through it, mirroring the single-transaction insert in production.
func (m *MockOutboxRepository) BackedBy(repo *MockJobRepository) *MockOutboxRepository {
	m.jobStore = repo
	return m
}

func (m *MockOutboxRepository) CreateJobWithOutbox(ctx context.Context, job *domain.Job) error {
	if m.CreateFunc != nil {
		return m.CreateFunc(ctx, job)
	}

	m.mu.Lock()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}
	job.UpdatedAt = job.CreatedAt
	m.jobs[job.JobID] = job
	m.pending[job.JobID] = 0
	store := m.jobStore
	m.mu.Unlock()

	// Seeded outside our lock: Seed takes the job store's own mutex, and holding
	// two mutexes across a call is how lock-ordering bugs start.
	if store != nil {
		store.Seed(job)
	}
	return nil
}

func (m *MockOutboxRepository) MarkPublished(ctx context.Context, jobID uuid.UUID) error {
	if m.MarkPublishedFunc != nil {
		return m.MarkPublishedFunc(ctx, jobID)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.pending, jobID)
	return nil
}

func (m *MockOutboxRepository) ClaimUnpublished(ctx context.Context, minAge time.Duration, limit int) ([]*domain.Job, error) {
	if m.ClaimUnpublishedFunc != nil {
		return m.ClaimUnpublishedFunc(ctx, minAge, limit)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*domain.Job
	for id := range m.pending {
		if len(out) >= limit {
			break
		}
		m.pending[id]++
		out = append(out, m.jobs[id])
	}
	return out, nil
}

func (m *MockOutboxRepository) DropExhausted(ctx context.Context, maxAttempts int) (int, error) {
	if m.DropExhaustedFunc != nil {
		return m.DropExhaustedFunc(ctx, maxAttempts)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, attempts := range m.pending {
		if attempts >= maxAttempts {
			delete(m.pending, id)
			if j, ok := m.jobs[id]; ok {
				j.Status = domain.StatusInternalError
			}
			n++
		}
	}
	return n, nil
}

// ---- test helpers ----

// Jobs returns every created job.
func (m *MockOutboxRepository) Jobs() []*domain.Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*domain.Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, j)
	}
	return out
}

// PendingCount returns how many outbox entries are still unconfirmed.
func (m *MockOutboxRepository) PendingCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pending)
}

// SetAttempts forces an entry's attempt count, for exercising the retry cap.
func (m *MockOutboxRepository) SetAttempts(jobID uuid.UUID, n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending[jobID] = n
}
