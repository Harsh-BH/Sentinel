package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/worker/internal/domain"
	"github.com/Harsh-BH/Sentinel/worker/internal/repository"
	"github.com/Harsh-BH/Sentinel/worker/internal/repository/mock"
	"github.com/Harsh-BH/Sentinel/worker/internal/usecase"
)

func newTestUsecase(repo *mock.JobRepository, exec *mock.Executor) *usecase.ExecuteJobUsecase {
	return usecase.NewExecuteJobUsecase(repo, exec, zap.NewNop())
}

func newTestJob() *domain.Job {
	return &domain.Job{
		JobID:         uuid.New(),
		Language:      domain.LangPython,
		SourceCode:    "print('hello')",
		TimeLimitMs:   5000,
		MemoryLimitKB: 262144,
		CreatedAt:     time.Now().UTC(),
	}
}

// Test: successful python execution end-to-end.
func TestExecute_Success_Python(t *testing.T) {
	repo := &mock.JobRepository{}
	exec := &mock.Executor{}
	uc := newTestUsecase(repo, exec)

	outcome, err := uc.Execute(context.Background(), newTestJob())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome != usecase.OutcomeExecuted {
		t.Errorf("expected OutcomeExecuted, got %v", outcome)
	}
	if repo.ClaimCallCount() != 1 {
		t.Errorf("expected 1 claim, got %d", repo.ClaimCallCount())
	}
	if repo.Claims[0].Status != domain.StatusRunning {
		t.Errorf("python should claim as RUNNING, got %s", repo.Claims[0].Status)
	}
	if repo.ResultCount() != 1 {
		t.Errorf("expected 1 result write, got %d", repo.ResultCount())
	}
}

// Test: C++ claims as COMPILING and its lease covers the compile pass too.
func TestExecute_Cpp_ClaimsCompilingWithLongerLease(t *testing.T) {
	repo := &mock.JobRepository{}
	exec := &mock.Executor{}
	uc := newTestUsecase(repo, exec)

	job := newTestJob()
	job.Language = domain.LangCpp

	if _, err := uc.Execute(context.Background(), job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.Claims[0].Status != domain.StatusCompiling {
		t.Errorf("cpp should claim as COMPILING, got %s", repo.Claims[0].Status)
	}

	// The lease must cover job runtime + compile budget, otherwise a slow compile
	// lets the lease expire and the reaper re-submits a job that is still running.
	minLease := time.Duration(job.TimeLimitMs)*time.Millisecond + domain.CompileTimeLimit
	if repo.Claims[0].Lease <= minLease {
		t.Errorf("cpp lease %s must exceed runtime+compile budget %s", repo.Claims[0].Lease, minLease)
	}
}

// Test: an already-terminal job is a duplicate — ack it, never re-run it.
func TestExecute_DuplicateTerminal_DoesNotExecute(t *testing.T) {
	repo := &mock.JobRepository{
		ClaimFn: func(context.Context, uuid.UUID, time.Time, domain.ExecutionStatus, time.Duration) (repository.ClaimOutcome, error) {
			return repository.ClaimTerminal, nil
		},
	}
	exec := &mock.Executor{}
	uc := newTestUsecase(repo, exec)

	outcome, err := uc.Execute(context.Background(), newTestJob())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome != usecase.OutcomeDuplicate {
		t.Errorf("expected OutcomeDuplicate, got %v", outcome)
	}
	if exec.ExecuteCallCount() != 0 {
		t.Error("a terminal job must not be executed again")
	}
	if repo.ResultCount() != 0 {
		t.Error("a terminal job must not have its result overwritten")
	}
}

// Test: a job leased by a live worker is skipped, not executed.
func TestExecute_LeasedByOther_DoesNotExecute(t *testing.T) {
	repo := &mock.JobRepository{
		ClaimFn: func(context.Context, uuid.UUID, time.Time, domain.ExecutionStatus, time.Duration) (repository.ClaimOutcome, error) {
			return repository.ClaimLeased, nil
		},
	}
	exec := &mock.Executor{}
	uc := newTestUsecase(repo, exec)

	outcome, _ := uc.Execute(context.Background(), newTestJob())
	if outcome != usecase.OutcomeDuplicate {
		t.Errorf("expected OutcomeDuplicate, got %v", outcome)
	}
	if exec.ExecuteCallCount() != 0 {
		t.Error("a leased job must not be executed concurrently")
	}
}

// Test: a missing job row is poison — dead-letter it rather than retrying forever.
func TestExecute_NotFound_IsPoison(t *testing.T) {
	repo := &mock.JobRepository{
		ClaimFn: func(context.Context, uuid.UUID, time.Time, domain.ExecutionStatus, time.Duration) (repository.ClaimOutcome, error) {
			return repository.ClaimNotFound, nil
		},
	}
	uc := newTestUsecase(repo, &mock.Executor{})

	outcome, _ := uc.Execute(context.Background(), newTestJob())
	if outcome != usecase.OutcomePoison {
		t.Errorf("expected OutcomePoison, got %v", outcome)
	}
}

// Test: a claim error is retryable, so the reaper gets a chance to re-submit.
func TestExecute_ClaimError_IsRetryable(t *testing.T) {
	dbErr := errors.New("connection refused")
	repo := &mock.JobRepository{
		ClaimFn: func(context.Context, uuid.UUID, time.Time, domain.ExecutionStatus, time.Duration) (repository.ClaimOutcome, error) {
			return repository.ClaimNotFound, dbErr
		},
	}
	uc := newTestUsecase(repo, &mock.Executor{})

	outcome, err := uc.Execute(context.Background(), newTestJob())
	if outcome != usecase.OutcomeRetryable {
		t.Errorf("expected OutcomeRetryable, got %v", outcome)
	}
	if !errors.Is(err, dbErr) {
		t.Errorf("expected the underlying error to propagate, got %v", err)
	}
}

// Test: a sandbox failure marks the job INTERNAL_ERROR instead of leaving it
// stuck in RUNNING until the reaper notices.
func TestExecute_SandboxFailure_MarksInternalError(t *testing.T) {
	repo := &mock.JobRepository{}
	exec := &mock.Executor{
		ExecuteFn: func(context.Context, *domain.ExecutionRequest) (*domain.ExecutionResult, error) {
			return nil, errors.New("nsjail exploded")
		},
	}
	uc := newTestUsecase(repo, exec)

	outcome, err := uc.Execute(context.Background(), newTestJob())
	if outcome != usecase.OutcomeRetryable {
		t.Errorf("expected OutcomeRetryable, got %v", outcome)
	}
	if err == nil {
		t.Error("expected an error")
	}
	if len(repo.StatusUpdates) != 1 || repo.StatusUpdates[0].Status != domain.StatusInternalError {
		t.Errorf("expected INTERNAL_ERROR status write, got %+v", repo.StatusUpdates)
	}
}

// Test: losing the result race is NOT an error. The guard did its job.
func TestExecute_ResultRace_IsDuplicateNotError(t *testing.T) {
	repo := &mock.JobRepository{
		SetResultFn: func(context.Context, uuid.UUID, time.Time, *domain.ExecutionResult) error {
			return repository.ErrAlreadyTerminal
		},
	}
	uc := newTestUsecase(repo, &mock.Executor{})

	outcome, err := uc.Execute(context.Background(), newTestJob())
	if err != nil {
		t.Fatalf("a lost result race must not surface as an error: %v", err)
	}
	if outcome != usecase.OutcomeDuplicate {
		t.Errorf("expected OutcomeDuplicate, got %v", outcome)
	}
}

// Test: a result-write infrastructure error IS retryable.
func TestExecute_ResultWriteError_IsRetryable(t *testing.T) {
	repo := &mock.JobRepository{
		SetResultFn: func(context.Context, uuid.UUID, time.Time, *domain.ExecutionResult) error {
			return errors.New("disk full")
		},
	}
	uc := newTestUsecase(repo, &mock.Executor{})

	outcome, err := uc.Execute(context.Background(), newTestJob())
	if outcome != usecase.OutcomeRetryable {
		t.Errorf("expected OutcomeRetryable, got %v", outcome)
	}
	if err == nil {
		t.Error("expected an error")
	}
}

// Test: the executor receives the job's real limits, not defaults.
func TestExecute_PassesRequestFieldsThrough(t *testing.T) {
	repo := &mock.JobRepository{}
	exec := &mock.Executor{}
	uc := newTestUsecase(repo, exec)

	job := newTestJob()
	job.SourceCode = "x = 1"
	job.Stdin = "42\n"
	job.TimeLimitMs = 1234
	job.MemoryLimitKB = 65536

	if _, err := uc.Execute(context.Background(), job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := exec.ExecuteCalls[0]
	if got.SourceCode != job.SourceCode || got.Stdin != job.Stdin ||
		got.TimeLimitMs != job.TimeLimitMs || got.MemoryLimitKB != job.MemoryLimitKB {
		t.Errorf("executor got %+v, want fields from %+v", got, job)
	}
}
