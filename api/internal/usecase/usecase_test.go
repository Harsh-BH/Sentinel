package usecase

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/api/internal/domain"
	mockpub "github.com/Harsh-BH/Sentinel/api/internal/publisher/mock"
	mockrepo "github.com/Harsh-BH/Sentinel/api/internal/repository/mock"
)

func TestSubmitJob_Success(t *testing.T) {
	outbox := mockrepo.NewMockOutboxRepository()
	pub := mockpub.NewMockPublisher()
	logger := zap.NewNop()

	uc := NewSubmitJobUsecase(outbox, pub, logger)

	req := &domain.SubmitRequest{
		Language:   domain.LangPython,
		SourceCode: "print('hello')",
		Stdin:      "test input",
	}

	resp, err := uc.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.JobID.String() == "" {
		t.Error("expected non-empty job ID")
	}
	if resp.Status != string(domain.StatusQueued) {
		t.Errorf("expected status QUEUED, got %s", resp.Status)
	}

	// Verify job was stored in repo
	jobs := outbox.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job in repo, got %d", len(jobs))
	}
	if jobs[0].Language != domain.LangPython {
		t.Errorf("expected language python, got %s", jobs[0].Language)
	}
	if jobs[0].TimeLimitMs != defaultTimeLimitMs {
		t.Errorf("expected default time limit %d, got %d", defaultTimeLimitMs, jobs[0].TimeLimitMs)
	}
	if jobs[0].MemoryLimitKB != defaultMemoryLimitKB {
		t.Errorf("expected default memory limit %d, got %d", defaultMemoryLimitKB, jobs[0].MemoryLimitKB)
	}

	// Verify job was published
	if len(pub.Published) != 1 {
		t.Fatalf("expected 1 published job, got %d", len(pub.Published))
	}
}

func TestSubmitJob_InvalidLanguage(t *testing.T) {
	outbox := mockrepo.NewMockOutboxRepository()
	pub := mockpub.NewMockPublisher()
	logger := zap.NewNop()

	uc := NewSubmitJobUsecase(outbox, pub, logger)

	req := &domain.SubmitRequest{
		Language:   domain.Language("ruby"),
		SourceCode: "puts 'hello'",
	}

	_, err := uc.Execute(context.Background(), req)
	if !errors.Is(err, domain.ErrInvalidLanguage) {
		t.Errorf("expected ErrInvalidLanguage, got %v", err)
	}
}

func TestSubmitJob_EmptySourceCode(t *testing.T) {
	outbox := mockrepo.NewMockOutboxRepository()
	pub := mockpub.NewMockPublisher()
	logger := zap.NewNop()

	uc := NewSubmitJobUsecase(outbox, pub, logger)

	req := &domain.SubmitRequest{
		Language:   domain.LangPython,
		SourceCode: "   ",
	}

	_, err := uc.Execute(context.Background(), req)
	if !errors.Is(err, domain.ErrEmptySourceCode) {
		t.Errorf("expected ErrEmptySourceCode, got %v", err)
	}
}

func TestSubmitJob_PayloadTooLarge(t *testing.T) {
	outbox := mockrepo.NewMockOutboxRepository()
	pub := mockpub.NewMockPublisher()
	logger := zap.NewNop()

	uc := NewSubmitJobUsecase(outbox, pub, logger)

	// Create source code larger than 1MB
	largeCode := make([]byte, maxSourceCodeSize+1)
	for i := range largeCode {
		largeCode[i] = 'x'
	}

	req := &domain.SubmitRequest{
		Language:   domain.LangPython,
		SourceCode: string(largeCode),
	}

	_, err := uc.Execute(context.Background(), req)
	if !errors.Is(err, domain.ErrPayloadTooLarge) {
		t.Errorf("expected ErrPayloadTooLarge, got %v", err)
	}
}

func TestSubmitJob_CustomLimits(t *testing.T) {
	outbox := mockrepo.NewMockOutboxRepository()
	pub := mockpub.NewMockPublisher()
	logger := zap.NewNop()

	uc := NewSubmitJobUsecase(outbox, pub, logger)

	timeLimit := 10000
	memLimit := 131072
	req := &domain.SubmitRequest{
		Language:      domain.LangCpp,
		SourceCode:    "int main() {}",
		TimeLimitMs:   &timeLimit,
		MemoryLimitKB: &memLimit,
	}

	resp, err := uc.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	jobs := outbox.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].TimeLimitMs != 10000 {
		t.Errorf("expected time limit 10000, got %d", jobs[0].TimeLimitMs)
	}
	if jobs[0].MemoryLimitKB != 131072 {
		t.Errorf("expected memory limit 131072, got %d", jobs[0].MemoryLimitKB)
	}
	_ = resp
}

// A broker outage must NOT fail the submission.
//
// This test previously asserted the opposite — that a publish failure returned
// ErrPublishFailed and flipped the job to INTERNAL_ERROR. That was correct for
// the old design, where the message existed nowhere but in the failed publish
// call. With the transactional outbox the message is already durable when the
// transaction commits, so the honest answer to the client is "accepted", and
// delivery becomes the relay's responsibility.
func TestSubmitJob_BrokerDown_StillAccepts(t *testing.T) {
	outbox := mockrepo.NewMockOutboxRepository()
	pub := mockpub.NewMockPublisher()
	pub.PublishFn = func(ctx context.Context, job *domain.Job) error {
		return errors.New("connection refused")
	}

	uc := NewSubmitJobUsecase(outbox, pub, zap.NewNop())

	resp, err := uc.Execute(context.Background(), &domain.SubmitRequest{
		Language:   domain.LangPython,
		SourceCode: "print('hello')",
	})
	if err != nil {
		t.Fatalf("a broker outage must not fail submission: %v", err)
	}
	if resp.Status != string(domain.StatusQueued) {
		t.Errorf("expected QUEUED, got %s", resp.Status)
	}

	jobs := outbox.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("expected the job row to be committed, got %d", len(jobs))
	}
	if jobs[0].Status != domain.StatusQueued {
		t.Errorf("job must stay QUEUED for the relay to deliver, got %s", jobs[0].Status)
	}
	// The outbox entry must survive so the relay has something to deliver.
	if outbox.PendingCount() != 1 {
		t.Errorf("expected 1 unpublished outbox entry, got %d", outbox.PendingCount())
	}
}

// The happy path must clear the outbox entry, or the relay would publish a
// duplicate for every successful submission.
func TestSubmitJob_SuccessClearsOutboxEntry(t *testing.T) {
	outbox := mockrepo.NewMockOutboxRepository()
	pub := mockpub.NewMockPublisher()

	uc := NewSubmitJobUsecase(outbox, pub, zap.NewNop())
	if _, err := uc.Execute(context.Background(), &domain.SubmitRequest{
		Language:   domain.LangPython,
		SourceCode: "print('hello')",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(pub.Published) != 1 {
		t.Errorf("expected an inline publish, got %d", len(pub.Published))
	}
	if outbox.PendingCount() != 0 {
		t.Errorf("expected the outbox entry to be cleared, got %d pending", outbox.PendingCount())
	}
}

// If the transaction fails, nothing is published and nothing is stored.
func TestSubmitJob_TransactionFailure(t *testing.T) {
	outbox := mockrepo.NewMockOutboxRepository()
	outbox.CreateFunc = func(ctx context.Context, job *domain.Job) error {
		return errors.New("database unavailable")
	}
	pub := mockpub.NewMockPublisher()

	uc := NewSubmitJobUsecase(outbox, pub, zap.NewNop())
	_, err := uc.Execute(context.Background(), &domain.SubmitRequest{
		Language:   domain.LangPython,
		SourceCode: "print('hello')",
	})
	if err == nil {
		t.Error("expected an error when the transaction fails")
	}
	if len(pub.Published) != 0 {
		t.Error("must not publish a job that was never committed")
	}
}

func TestGetJob_Success(t *testing.T) {
	repo := mockrepo.NewMockJobRepository()
	logger := zap.NewNop()

	// Pre-populate a job. The outbox is backed by the same job store, mirroring
	// the single transaction that writes both in production.
	outbox := mockrepo.NewMockOutboxRepository().BackedBy(repo)
	pub := mockpub.NewMockPublisher()
	submitUC := NewSubmitJobUsecase(outbox, pub, logger)

	req := &domain.SubmitRequest{
		Language:   domain.LangPython,
		SourceCode: "print('hello')",
	}
	resp, _ := submitUC.Execute(context.Background(), req)

	getUC := NewGetJobUsecase(repo, logger)
	job, err := getUC.Execute(context.Background(), resp.JobID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.JobID != resp.JobID {
		t.Errorf("expected job ID %s, got %s", resp.JobID, job.JobID)
	}
}

func TestGetJob_NotFound(t *testing.T) {
	repo := mockrepo.NewMockJobRepository()
	logger := zap.NewNop()

	getUC := NewGetJobUsecase(repo, logger)

	_, err := getUC.Execute(context.Background(), [16]byte{})
	if !errors.Is(err, domain.ErrJobNotFound) {
		t.Errorf("expected ErrJobNotFound, got %v", err)
	}
}
