package usecase

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/worker/internal/domain"
	"github.com/Harsh-BH/Sentinel/worker/internal/metrics"
	"github.com/Harsh-BH/Sentinel/worker/internal/repository"
)

// ExecuteJobUsecase orchestrates the full job execution pipeline.
type ExecuteJobUsecase struct {
	repo       repository.JobRepository
	idempotent repository.IdempotencyStore
	executor   repository.Executor
	logger     *zap.Logger
}

// NewExecuteJobUsecase creates a new ExecuteJobUsecase.
func NewExecuteJobUsecase(
	repo repository.JobRepository,
	idempotent repository.IdempotencyStore,
	exec repository.Executor,
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
	lang := string(job.Language)
	start := time.Now()

	// Step 1: Idempotency check
	acquired, err := uc.idempotent.AcquireLock(ctx, job.JobID)
	if err != nil {
		uc.logger.Error("Failed to acquire idempotency lock", zap.Error(err), zap.String("job_id", job.JobID.String()))
		metrics.ExecutionsTotal.WithLabelValues(lang, "error").Inc()
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
		metrics.ExecutionsTotal.WithLabelValues(lang, "error").Inc()
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
		metrics.ExecutionsTotal.WithLabelValues(lang, string(domain.StatusInternalError)).Inc()
		metrics.SandboxFailures.Inc()
		return false, err
	}

	// Step 4: Store result
	if err := uc.repo.SetResult(ctx, job.JobID, result); err != nil {
		uc.logger.Error("Failed to store result", zap.Error(err), zap.String("job_id", job.JobID.String()))
		metrics.ExecutionsTotal.WithLabelValues(lang, "error").Inc()
		return false, err
	}

	// Step 5: Release idempotency lock (set TTL for eventual cleanup)
	_ = uc.idempotent.ReleaseLock(ctx, job.JobID)

	elapsed := time.Since(start).Seconds()
	metrics.ExecutionsTotal.WithLabelValues(lang, string(result.Status)).Inc()
	metrics.ExecutionDuration.WithLabelValues(lang).Observe(elapsed)

	uc.logger.Info("Job executed successfully",
		zap.String("job_id", job.JobID.String()),
		zap.String("status", string(result.Status)),
		zap.Int("time_ms", result.TimeUsedMs),
		zap.Float64("wall_seconds", elapsed),
	)

	return false, nil
}
