package usecase

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/worker/internal/domain"
	"github.com/Harsh-BH/Sentinel/worker/internal/metrics"
	"github.com/Harsh-BH/Sentinel/worker/internal/repository"
)

// leaseSlack is added on top of a job's own limits when claiming it, to cover
// process startup, nsjail setup and the result write. Short enough that a
// crashed worker's job becomes reclaimable quickly; long enough that a healthy
// worker never loses its own lease mid-execution.
const leaseSlack = 30 * time.Second

// Outcome tells the caller how to settle the queue message. Keeping this
// explicit (rather than returning a bare error) is what stops the transport
// layer from having to guess whether a failure is retryable.
type Outcome int

const (
	// OutcomeExecuted — the job ran and its result is persisted. Ack.
	OutcomeExecuted Outcome = iota
	// OutcomeDuplicate — another delivery already handled this job. Ack.
	OutcomeDuplicate
	// OutcomePoison — the message can never succeed (no such job). Dead-letter it.
	OutcomePoison
	// OutcomeRetryable — infrastructure failed. Dead-letter it; the job row is
	// left non-terminal with an expired lease so the reaper re-submits it. We
	// deliberately do NOT requeue: RabbitMQ redelivers immediately, which would
	// spin a hot loop against whatever is already broken.
	OutcomeRetryable
)

// ExecuteJobUsecase orchestrates the full job execution pipeline.
type ExecuteJobUsecase struct {
	repo     repository.JobRepository
	executor repository.Executor
	logger   *zap.Logger
}

// NewExecuteJobUsecase creates a new ExecuteJobUsecase.
func NewExecuteJobUsecase(
	repo repository.JobRepository,
	exec repository.Executor,
	logger *zap.Logger,
) *ExecuteJobUsecase {
	return &ExecuteJobUsecase{
		repo:     repo,
		executor: exec,
		logger:   logger,
	}
}

// leaseFor sizes the lease to what this job is actually allowed to consume.
func leaseFor(job *domain.Job) time.Duration {
	d := time.Duration(job.TimeLimitMs) * time.Millisecond
	if job.Language == domain.LangCpp {
		d += domain.CompileTimeLimit
	}
	return d + leaseSlack
}

// Execute processes a single job: claim → sandbox run → persist result.
//
// There is exactly one source of truth for "has this job been handled" — the
// execution_jobs row. Deduplication used to live in a Redis SETNX lock, which
// could not distinguish "in progress" from "finished" and so silently dropped
// any job whose worker crashed. The conditional UPDATE in Claim answers both
// questions from durable state instead.
func (uc *ExecuteJobUsecase) Execute(ctx context.Context, job *domain.Job) (Outcome, error) {
	lang := string(job.Language)
	start := time.Now()

	// Step 1: claim the job. For C++ the first phase is compilation, so the
	// claimed status reflects that.
	initialStatus := domain.StatusRunning
	if job.Language == domain.LangCpp {
		initialStatus = domain.StatusCompiling
	}

	outcome, err := uc.repo.Claim(ctx, job.JobID, job.CreatedAt, initialStatus, leaseFor(job))
	if err != nil {
		uc.logger.Error("Failed to claim job", zap.Error(err), zap.String("job_id", job.JobID.String()))
		metrics.ExecutionsTotal.WithLabelValues(lang, "error").Inc()
		return OutcomeRetryable, err
	}

	switch outcome {
	case repository.ClaimAcquired:
		// Ours to run.
	case repository.ClaimTerminal:
		uc.logger.Info("Duplicate delivery: job already terminal", zap.String("job_id", job.JobID.String()))
		metrics.DuplicateDeliveries.WithLabelValues("terminal").Inc()
		return OutcomeDuplicate, nil
	case repository.ClaimLeased:
		uc.logger.Warn("Duplicate delivery: job leased by another worker", zap.String("job_id", job.JobID.String()))
		metrics.DuplicateDeliveries.WithLabelValues("leased").Inc()
		return OutcomeDuplicate, nil
	case repository.ClaimNotFound:
		uc.logger.Error("Job row not found — dead-lettering", zap.String("job_id", job.JobID.String()))
		metrics.ExecutionsTotal.WithLabelValues(lang, "poison").Inc()
		return OutcomePoison, nil
	}

	// Step 2: run in the sandbox.
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
		// Best effort: surface the failure to the user rather than leaving the
		// job sitting in RUNNING until the reaper notices.
		if serr := uc.repo.SetStatus(ctx, job.JobID, job.CreatedAt, domain.StatusInternalError); serr != nil &&
			!errors.Is(serr, repository.ErrAlreadyTerminal) {
			uc.logger.Error("Failed to mark job INTERNAL_ERROR", zap.Error(serr), zap.String("job_id", job.JobID.String()))
		}
		metrics.ExecutionsTotal.WithLabelValues(lang, string(domain.StatusInternalError)).Inc()
		metrics.SandboxFailures.Inc()
		return OutcomeRetryable, err
	}

	// Step 3: persist the result.
	if err := uc.repo.SetResult(ctx, job.JobID, job.CreatedAt, result); err != nil {
		if errors.Is(err, repository.ErrAlreadyTerminal) {
			// A concurrent delivery got there first. Our execution was wasted work,
			// but no data was corrupted — that is exactly what the guard is for.
			uc.logger.Warn("Result discarded: job already terminal", zap.String("job_id", job.JobID.String()))
			metrics.DuplicateDeliveries.WithLabelValues("result_race").Inc()
			return OutcomeDuplicate, nil
		}
		uc.logger.Error("Failed to store result", zap.Error(err), zap.String("job_id", job.JobID.String()))
		metrics.ExecutionsTotal.WithLabelValues(lang, "error").Inc()
		return OutcomeRetryable, err
	}

	elapsed := time.Since(start).Seconds()
	metrics.ExecutionsTotal.WithLabelValues(lang, string(result.Status)).Inc()
	metrics.ExecutionDuration.WithLabelValues(lang).Observe(elapsed)

	uc.logger.Info("Job executed",
		zap.String("job_id", job.JobID.String()),
		zap.String("status", string(result.Status)),
		zap.Int("time_ms", result.TimeUsedMs),
		zap.Int("memory_kb", result.MemoryUsedKB),
		zap.Float64("wall_seconds", elapsed),
	)

	return OutcomeExecuted, nil
}
