package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/api/internal/domain"
	mockpub "github.com/Harsh-BH/Sentinel/api/internal/publisher/mock"
	mockrepo "github.com/Harsh-BH/Sentinel/api/internal/repository/mock"
)

func stuckJob(attempts int, status domain.ExecutionStatus) *domain.Job {
	id, _ := uuid.NewV7()
	return &domain.Job{
		JobID:         id,
		Language:      domain.LangPython,
		SourceCode:    "print(1)",
		Status:        status,
		Attempts:      attempts,
		TimeLimitMs:   5000,
		MemoryLimitKB: 262144,
		CreatedAt:     time.Now().UTC().Add(-10 * time.Minute),
	}
}

// Test: a job stranded in QUEUED (the dual-write gap) is re-published.
func TestReaper_RepublishesStrandedQueuedJob(t *testing.T) {
	job := stuckJob(0, domain.StatusQueued)
	repo := mockrepo.NewMockJobRepository()
	repo.ReclaimStuckFunc = func(context.Context, time.Duration, int) ([]*domain.Job, error) {
		return []*domain.Job{job}, nil
	}
	pub := mockpub.NewMockPublisher()

	stats, err := NewReapJobsUsecase(repo, pub, zap.NewNop()).Sweep(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats.Republished != 1 {
		t.Errorf("expected 1 republish, got %d", stats.Republished)
	}
	if len(pub.Published) != 1 || pub.Published[0].JobID != job.JobID {
		t.Errorf("expected the stranded job to be published, got %+v", pub.Published)
	}
}

// Test: a job stranded in RUNNING (worker died mid-execution) is re-published.
// This is the case the old Redis lock silently dropped.
func TestReaper_RepublishesStrandedRunningJob(t *testing.T) {
	repo := mockrepo.NewMockJobRepository()
	repo.ReclaimStuckFunc = func(context.Context, time.Duration, int) ([]*domain.Job, error) {
		return []*domain.Job{stuckJob(1, domain.StatusRunning)}, nil
	}
	pub := mockpub.NewMockPublisher()

	stats, _ := NewReapJobsUsecase(repo, pub, zap.NewNop()).Sweep(context.Background())
	if stats.Republished != 1 {
		t.Errorf("expected a crashed job to be recovered, got %d republishes", stats.Republished)
	}
}

// Test: retries are capped. A job that keeps killing its worker must eventually
// be failed rather than re-published forever.
func TestReaper_GivesUpAfterMaxAttempts(t *testing.T) {
	job := stuckJob(DefaultMaxAttempts, domain.StatusRunning)
	repo := mockrepo.NewMockJobRepository()
	repo.ReclaimStuckFunc = func(context.Context, time.Duration, int) ([]*domain.Job, error) {
		return []*domain.Job{job}, nil
	}
	var marked domain.ExecutionStatus
	repo.UpdateStatusFunc = func(_ context.Context, _ uuid.UUID, _ time.Time, s domain.ExecutionStatus) error {
		marked = s
		return nil
	}
	pub := mockpub.NewMockPublisher()

	stats, _ := NewReapJobsUsecase(repo, pub, zap.NewNop()).Sweep(context.Background())
	if stats.Failed != 1 {
		t.Errorf("expected 1 failure, got %d", stats.Failed)
	}
	if stats.Republished != 0 {
		t.Errorf("expected no republish past the attempt cap, got %d", stats.Republished)
	}
	if marked != domain.StatusInternalError {
		t.Errorf("expected INTERNAL_ERROR, got %s", marked)
	}
	if len(pub.Published) != 0 {
		t.Error("an exhausted job must not be re-published")
	}
}

// Test: a publish failure leaves the job for the next sweep rather than
// abandoning it or crashing the reaper.
func TestReaper_PublishFailureIsSurvivable(t *testing.T) {
	repo := mockrepo.NewMockJobRepository()
	repo.ReclaimStuckFunc = func(context.Context, time.Duration, int) ([]*domain.Job, error) {
		return []*domain.Job{stuckJob(0, domain.StatusQueued)}, nil
	}
	pub := mockpub.NewMockPublisher()
	pub.PublishFn = func(context.Context, *domain.Job) error {
		return errors.New("broker down")
	}

	stats, err := NewReapJobsUsecase(repo, pub, zap.NewNop()).Sweep(context.Background())
	if err != nil {
		t.Fatalf("a single publish failure must not fail the sweep: %v", err)
	}
	if stats.Found != 1 || stats.Republished != 0 {
		t.Errorf("expected found=1 republished=0, got %+v", stats)
	}
}

// Test: an empty sweep is a no-op, not an error.
func TestReaper_NothingStuck(t *testing.T) {
	repo := mockrepo.NewMockJobRepository()
	pub := mockpub.NewMockPublisher()

	stats, err := NewReapJobsUsecase(repo, pub, zap.NewNop()).Sweep(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats.Found != 0 {
		t.Errorf("expected nothing found, got %+v", stats)
	}
}

// Test: out-of-range limits are rejected, not silently replaced with defaults.
func TestSubmitJob_RejectsOutOfRangeLimits(t *testing.T) {
	cases := []struct {
		name   string
		timeMs *int
		memKB  *int
	}{
		{"time limit above ceiling", ptr(60000), nil},
		{"time limit zero", ptr(0), nil},
		{"time limit negative", ptr(-1), nil},
		{"memory above ceiling", nil, ptr(1 << 20)},
		{"memory zero", nil, ptr(0)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outbox := mockrepo.NewMockOutboxRepository()
			pub := mockpub.NewMockPublisher()
			uc := NewSubmitJobUsecase(outbox, pub, zap.NewNop())

			_, err := uc.Execute(context.Background(), &domain.SubmitRequest{
				Language:      domain.LangPython,
				SourceCode:    "print(1)",
				TimeLimitMs:   tc.timeMs,
				MemoryLimitKB: tc.memKB,
			})
			if !errors.Is(err, domain.ErrInvalidLimit) {
				t.Errorf("expected ErrInvalidLimit, got %v", err)
			}
			if len(outbox.Jobs()) != 0 {
				t.Error("a rejected submission must not create a job row")
			}
			if outbox.PendingCount() != 0 {
				t.Error("a rejected submission must not create an outbox entry")
			}
		})
	}
}

// Test: a database outage surfaces as ErrDatabaseUnavailable, not as "not found".
func TestGetJob_DatabaseErrorIsNotNotFound(t *testing.T) {
	repo := mockrepo.NewMockJobRepository()
	repo.GetByIDFunc = func(context.Context, uuid.UUID) (*domain.Job, error) {
		return nil, errors.New("connection refused")
	}
	uc := NewGetJobUsecase(repo, zap.NewNop())

	_, err := uc.Execute(context.Background(), uuid.New())
	if errors.Is(err, domain.ErrJobNotFound) {
		t.Error("a database outage must not be reported as ErrJobNotFound")
	}
	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Errorf("expected ErrDatabaseUnavailable, got %v", err)
	}
}

// Test: a genuinely missing job still reports not-found.
func TestGetJob_MissingJobIsNotFound(t *testing.T) {
	repo := mockrepo.NewMockJobRepository()
	uc := NewGetJobUsecase(repo, zap.NewNop())

	_, err := uc.Execute(context.Background(), uuid.New())
	if !errors.Is(err, domain.ErrJobNotFound) {
		t.Errorf("expected ErrJobNotFound, got %v", err)
	}
}

func ptr(v int) *int { return &v }
