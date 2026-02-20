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
	repo := mockrepo.NewMockJobRepository()
	pub := mockpub.NewMockPublisher()
	logger := zap.NewNop()

	uc := NewSubmitJobUsecase(repo, pub, logger)

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
	jobs := repo.GetAll()
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
	repo := mockrepo.NewMockJobRepository()
	pub := mockpub.NewMockPublisher()
	logger := zap.NewNop()

	uc := NewSubmitJobUsecase(repo, pub, logger)

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
	repo := mockrepo.NewMockJobRepository()
	pub := mockpub.NewMockPublisher()
	logger := zap.NewNop()

	uc := NewSubmitJobUsecase(repo, pub, logger)

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
	repo := mockrepo.NewMockJobRepository()
	pub := mockpub.NewMockPublisher()
	logger := zap.NewNop()

	uc := NewSubmitJobUsecase(repo, pub, logger)

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
	repo := mockrepo.NewMockJobRepository()
	pub := mockpub.NewMockPublisher()
	logger := zap.NewNop()

	uc := NewSubmitJobUsecase(repo, pub, logger)

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

	jobs := repo.GetAll()
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

func TestSubmitJob_PublishFailure(t *testing.T) {
	repo := mockrepo.NewMockJobRepository()
	pub := mockpub.NewMockPublisher()
	pub.PublishFn = func(ctx context.Context, job *domain.Job) error {
		return errors.New("connection refused")
	}
	logger := zap.NewNop()

	uc := NewSubmitJobUsecase(repo, pub, logger)

	req := &domain.SubmitRequest{
		Language:   domain.LangPython,
		SourceCode: "print('hello')",
	}

	_, err := uc.Execute(context.Background(), req)
	if !errors.Is(err, domain.ErrPublishFailed) {
		t.Errorf("expected ErrPublishFailed, got %v", err)
	}

	// Job should be in repo with INTERNAL_ERROR status
	jobs := repo.GetAll()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].Status != domain.StatusInternalError {
		t.Errorf("expected INTERNAL_ERROR status, got %s", jobs[0].Status)
	}
}

func TestSubmitJob_RepoCreateFailure(t *testing.T) {
	repo := mockrepo.NewMockJobRepository()
	repo.CreateFunc = func(ctx context.Context, job *domain.Job) error {
		return errors.New("database unavailable")
	}
	pub := mockpub.NewMockPublisher()
	logger := zap.NewNop()

	uc := NewSubmitJobUsecase(repo, pub, logger)

	req := &domain.SubmitRequest{
		Language:   domain.LangPython,
		SourceCode: "print('hello')",
	}

	_, err := uc.Execute(context.Background(), req)
	if err == nil {
		t.Error("expected error on repo failure")
	}
	// Should NOT have published
	if len(pub.Published) != 0 {
		t.Error("should not publish when repo create fails")
	}
}

func TestGetJob_Success(t *testing.T) {
	repo := mockrepo.NewMockJobRepository()
	logger := zap.NewNop()

	// Pre-populate a job
	pub := mockpub.NewMockPublisher()
	submitUC := NewSubmitJobUsecase(repo, pub, logger)

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
