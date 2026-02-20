package usecase_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/worker/internal/domain"
	"github.com/Harsh-BH/Sentinel/worker/internal/repository/mock"
	"github.com/Harsh-BH/Sentinel/worker/internal/usecase"
)

func newTestUsecase(repo *mock.JobRepository, idem *mock.IdempotencyStore, exec *mock.Executor) *usecase.ExecuteJobUsecase {
	logger := zap.NewNop()
	return usecase.NewExecuteJobUsecase(repo, idem, exec, logger)
}

func newTestJob() *domain.Job {
	return &domain.Job{
		JobID:         uuid.New(),
		Language:      domain.LangPython,
		SourceCode:    "print('hello')",
		Stdin:         "",
		TimeLimitMs:   5000,
		MemoryLimitKB: 262144,
	}
}

// Test: successful python execution end-to-end.
func TestExecute_Success_Python(t *testing.T) {
	repo := &mock.JobRepository{}
	idem := &mock.IdempotencyStore{}
	exec := &mock.Executor{
		ExecuteFn: func(ctx context.Context, req *domain.ExecutionRequest) (*domain.ExecutionResult, error) {
			return &domain.ExecutionResult{
				Status:     domain.StatusSuccess,
				Stdout:     "hello\n",
				ExitCode:   0,
				TimeUsedMs: 50,
			}, nil
		},
	}

	uc := newTestUsecase(repo, idem, exec)
	job := newTestJob()

	isDup, err := uc.Execute(context.Background(), job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isDup {
		t.Fatal("expected not duplicate")
	}

	// Verify status was updated to RUNNING (Python).
	if len(repo.StatusUpdates) != 1 {
		t.Fatalf("expected 1 status update, got %d", len(repo.StatusUpdates))
	}
	if repo.StatusUpdates[0].Status != domain.StatusRunning {
		t.Errorf("expected RUNNING status, got %s", repo.StatusUpdates[0].Status)
	}

	// Verify result was stored.
	if len(repo.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(repo.Results))
	}
	if repo.Results[0].Result.Status != domain.StatusSuccess {
		t.Errorf("expected SUCCESS result, got %s", repo.Results[0].Result.Status)
	}

	// Verify lock was acquired and released.
	if len(idem.AcquireCalls) != 1 {
		t.Fatalf("expected 1 acquire call, got %d", len(idem.AcquireCalls))
	}
	if len(idem.ReleaseCalls) != 1 {
		t.Fatalf("expected 1 release call, got %d", len(idem.ReleaseCalls))
	}
}

// Test: C++ job sets initial status to COMPILING.
func TestExecute_Success_Cpp(t *testing.T) {
	repo := &mock.JobRepository{}
	idem := &mock.IdempotencyStore{}
	exec := &mock.Executor{
		ExecuteFn: func(ctx context.Context, req *domain.ExecutionRequest) (*domain.ExecutionResult, error) {
			return &domain.ExecutionResult{
				Status:     domain.StatusSuccess,
				Stdout:     "42\n",
				ExitCode:   0,
				TimeUsedMs: 120,
			}, nil
		},
	}

	uc := newTestUsecase(repo, idem, exec)
	job := newTestJob()
	job.Language = domain.LangCpp
	job.SourceCode = "#include <cstdio>\nint main(){printf(\"42\\n\");}"

	isDup, err := uc.Execute(context.Background(), job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isDup {
		t.Fatal("expected not duplicate")
	}

	if repo.StatusUpdates[0].Status != domain.StatusCompiling {
		t.Errorf("expected COMPILING status for C++, got %s", repo.StatusUpdates[0].Status)
	}
}

// Test: duplicate message is detected and skipped.
func TestExecute_Duplicate(t *testing.T) {
	repo := &mock.JobRepository{}
	idem := &mock.IdempotencyStore{
		AcquireLockFn: func(ctx context.Context, jobID uuid.UUID) (bool, error) {
			return false, nil // lock not acquired = duplicate
		},
	}
	exec := &mock.Executor{}

	uc := newTestUsecase(repo, idem, exec)
	job := newTestJob()

	isDup, err := uc.Execute(context.Background(), job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isDup {
		t.Fatal("expected duplicate")
	}

	// Verify no status update or execution happened.
	if len(repo.StatusUpdates) != 0 {
		t.Errorf("expected 0 status updates, got %d", len(repo.StatusUpdates))
	}
	if len(exec.ExecuteCalls) != 0 {
		t.Errorf("expected 0 execute calls, got %d", len(exec.ExecuteCalls))
	}
}

// Test: idempotency lock acquisition fails.
func TestExecute_LockError(t *testing.T) {
	repo := &mock.JobRepository{}
	idem := &mock.IdempotencyStore{
		AcquireLockFn: func(ctx context.Context, jobID uuid.UUID) (bool, error) {
			return false, errors.New("redis connection refused")
		},
	}
	exec := &mock.Executor{}

	uc := newTestUsecase(repo, idem, exec)
	job := newTestJob()

	_, err := uc.Execute(context.Background(), job)
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "redis connection refused" {
		t.Errorf("unexpected error: %v", err)
	}
}

// Test: sandbox execution returns infrastructure error.
func TestExecute_SandboxFailure(t *testing.T) {
	repo := &mock.JobRepository{}
	idem := &mock.IdempotencyStore{}
	exec := &mock.Executor{
		ExecuteFn: func(ctx context.Context, req *domain.ExecutionRequest) (*domain.ExecutionResult, error) {
			return nil, errors.New("nsjail binary not found")
		},
	}

	uc := newTestUsecase(repo, idem, exec)
	job := newTestJob()

	isDup, err := uc.Execute(context.Background(), job)
	if err == nil {
		t.Fatal("expected error from sandbox failure")
	}
	if isDup {
		t.Fatal("expected not duplicate")
	}

	// Should have set initial status AND then INTERNAL_ERROR.
	if len(repo.StatusUpdates) != 2 {
		t.Fatalf("expected 2 status updates, got %d", len(repo.StatusUpdates))
	}
	if repo.StatusUpdates[1].Status != domain.StatusInternalError {
		t.Errorf("expected INTERNAL_ERROR, got %s", repo.StatusUpdates[1].Status)
	}
}

// Test: UpdateStatus DB failure.
func TestExecute_DBUpdateStatusError(t *testing.T) {
	repo := &mock.JobRepository{
		UpdateStatusFn: func(ctx context.Context, id uuid.UUID, status domain.ExecutionStatus) error {
			return errors.New("connection refused")
		},
	}
	idem := &mock.IdempotencyStore{}
	exec := &mock.Executor{}

	uc := newTestUsecase(repo, idem, exec)
	job := newTestJob()

	_, err := uc.Execute(context.Background(), job)
	if err == nil {
		t.Fatal("expected error from DB failure")
	}
}

// Test: SetResult DB failure.
func TestExecute_DBSetResultError(t *testing.T) {
	repo := &mock.JobRepository{
		SetResultFn: func(ctx context.Context, id uuid.UUID, result *domain.ExecutionResult) error {
			return errors.New("disk full")
		},
	}
	idem := &mock.IdempotencyStore{}
	exec := &mock.Executor{}

	uc := newTestUsecase(repo, idem, exec)
	job := newTestJob()

	_, err := uc.Execute(context.Background(), job)
	if err == nil {
		t.Fatal("expected error from SetResult failure")
	}
	if err.Error() != "disk full" {
		t.Errorf("unexpected error: %v", err)
	}
}

// Test: executor receives correct request fields.
func TestExecute_CorrectRequestFields(t *testing.T) {
	repo := &mock.JobRepository{}
	idem := &mock.IdempotencyStore{}
	exec := &mock.Executor{}

	uc := newTestUsecase(repo, idem, exec)
	job := &domain.Job{
		JobID:         uuid.MustParse("01234567-89ab-cdef-0123-456789abcdef"),
		Language:      domain.LangPython,
		SourceCode:    "print(42)",
		Stdin:         "input data",
		TimeLimitMs:   3000,
		MemoryLimitKB: 131072,
	}

	_, err := uc.Execute(context.Background(), job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(exec.ExecuteCalls) != 1 {
		t.Fatalf("expected 1 execute call, got %d", len(exec.ExecuteCalls))
	}
	req := exec.ExecuteCalls[0]
	if req.JobID != job.JobID {
		t.Errorf("job ID mismatch")
	}
	if req.Language != job.Language {
		t.Errorf("language mismatch")
	}
	if req.SourceCode != job.SourceCode {
		t.Errorf("source code mismatch")
	}
	if req.Stdin != job.Stdin {
		t.Errorf("stdin mismatch")
	}
	if req.TimeLimitMs != job.TimeLimitMs {
		t.Errorf("time limit mismatch")
	}
	if req.MemoryLimitKB != job.MemoryLimitKB {
		t.Errorf("memory limit mismatch")
	}
}
