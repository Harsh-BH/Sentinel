package usecase

import (
	"context"

	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/worker/internal/domain"
	"github.com/Harsh-BH/Sentinel/worker/internal/executor"
	"github.com/Harsh-BH/Sentinel/worker/internal/repository"
)

// ExecuteJobUsecase orchestrates the full job execution pipeline.
type ExecuteJobUsecase struct {
	repo       repository.JobRepository
	idempotent repository.IdempotencyStore
	executor   *executor.SandboxExecutor
	logger     *zap.Logger
}

// NewExecuteJobUsecase creates a new ExecuteJobUsecase.
func NewExecuteJobUsecase(
	repo repository.JobRepository,
	idempotent repository.IdempotencyStore,
	exec *executor.SandboxExecutor,
	logger *zap.Logger,
) *ExecuteJobUsecase {
	return &ExecuteJobUsecase{
		repo:       repo,
		idempotent: idempotent,
		executor:   exec,
		logger:     logger,
	}
}

// Execute processes a single job: idempotency check → status update → sandbox run → store result.
// Returns (isDuplicate, error).
func (uc *ExecuteJobUsecase) Execute(ctx context.Context, job *domain.Job) (bool, error) {
	// Step 1: Idempotency check
	acquired, err := uc.idempotent.AcquireLock(ctx, job.JobID)
	if err != nil {
		uc.logger.Error("Failed to acquire idempotency lock", zap.Error(err), zap.String("job_id", job.JobID.String()))
		return false, err
	}
	if !acquired {
		uc.logger.Info("Duplicate message detected, skipping", zap.String("job_id", job.JobID.String()))
		return true, nil
	}

	// Step 2: Update status to COMPILING (C++) or RUNNING (Python)
	var initialStatus domain.ExecutionStatus
	if job.Language == domain.LangCpp {
		initialStatus = domain.StatusCompiling
	} else {
		initialStatus = domain.StatusRunning
	}
	if err := uc.repo.UpdateStatus(ctx, job.JobID, initialStatus); err != nil {
		uc.logger.Error("Failed to update job status", zap.Error(err), zap.String("job_id", job.JobID.String()))
		return false, err
	}

	// Step 3: Execute in sandbox
	req := &domain.ExecutionRequest{
		JobID:         job.JobID,
		Language:      job.Language,
		SourceCode:    job.SourceCode,
		Stdin:         job.Stdin,
		TimeLimitMs:   job.TimeLimitMs,
		MemoryLimitKB: job.MemoryLimitKB,
	}

	result, err := uc.executor.Execute(ctx, req)
	if err != nil {
		uc.logger.Error("Sandbox execution failed", zap.Error(err), zap.String("job_id", job.JobID.String()))
		// Set status to INTERNAL_ERROR
		_ = uc.repo.UpdateStatus(ctx, job.JobID, domain.StatusInternalError)
		return false, err
	}

	// Step 4: Store result
	if err := uc.repo.SetResult(ctx, job.JobID, result); err != nil {
		uc.logger.Error("Failed to store result", zap.Error(err), zap.String("job_id", job.JobID.String()))
		return false, err
	}

	// Step 5: Release idempotency lock (set TTL for eventual cleanup)
	_ = uc.idempotent.ReleaseLock(ctx, job.JobID)

	uc.logger.Info("Job executed successfully",
		zap.String("job_id", job.JobID.String()),
		zap.String("status", string(result.Status)),
		zap.Int("time_ms", result.TimeUsedMs),
	)

	return false, nil
}
