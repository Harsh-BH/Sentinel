package usecase

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/api/internal/domain"
	"github.com/Harsh-BH/Sentinel/api/internal/publisher"
	"github.com/Harsh-BH/Sentinel/api/internal/repository"
)

const (
	maxSourceCodeSize    = 1 << 20 // 1 MB
	defaultTimeLimitMs   = 5000
	defaultMemoryLimitKB = 262144 // 256 MB

	// Hard ceilings on what a caller may request.
	maxTimeLimitMs   = 30000
	maxMemoryLimitKB = 524288 // 512 MB
)

// SubmitJobUsecase handles the business logic for submitting code execution jobs.
type SubmitJobUsecase struct {
	outbox    repository.OutboxRepository
	publisher publisher.Publisher
	logger    *zap.Logger
}

// NewSubmitJobUsecase creates a new SubmitJobUsecase.
func NewSubmitJobUsecase(outbox repository.OutboxRepository, pub publisher.Publisher, logger *zap.Logger) *SubmitJobUsecase {
	return &SubmitJobUsecase{
		outbox:    outbox,
		publisher: pub,
		logger:    logger,
	}
}

// Execute validates the submission, creates a job, publishes it, and returns the job ID.
func (uc *SubmitJobUsecase) Execute(ctx context.Context, req *domain.SubmitRequest) (*domain.SubmitResponse, error) {
	if !req.Language.IsValid() {
		return nil, domain.ErrInvalidLanguage
	}

	if strings.TrimSpace(req.SourceCode) == "" {
		return nil, domain.ErrEmptySourceCode
	}
	if len(req.SourceCode) > maxSourceCodeSize {
		return nil, domain.ErrPayloadTooLarge
	}

	// Out-of-range limits are rejected, not silently replaced.
	//
	// This used to fall back to the default whenever the requested value was out
	// of range, so asking for a 60 s limit quietly got you 5 s and your job was
	// reported as TIMEOUT. A caller has no way to detect that, which makes the
	// API's behaviour unexplainable from the outside.
	timeLimitMs := defaultTimeLimitMs
	if req.TimeLimitMs != nil {
		if *req.TimeLimitMs <= 0 || *req.TimeLimitMs > maxTimeLimitMs {
			return nil, fmt.Errorf("%w: time_limit_ms must be between 1 and %d, got %d",
				domain.ErrInvalidLimit, maxTimeLimitMs, *req.TimeLimitMs)
		}
		timeLimitMs = *req.TimeLimitMs
	}

	memoryLimitKB := defaultMemoryLimitKB
	if req.MemoryLimitKB != nil {
		if *req.MemoryLimitKB <= 0 || *req.MemoryLimitKB > maxMemoryLimitKB {
			return nil, fmt.Errorf("%w: memory_limit_kb must be between 1 and %d, got %d",
				domain.ErrInvalidLimit, maxMemoryLimitKB, *req.MemoryLimitKB)
		}
		memoryLimitKB = *req.MemoryLimitKB
	}

	// UUIDv7: time-ordered, so inserts land at the end of the index instead of
	// scattering across it, and the embedded timestamp lets reads prune to a
	// single partition. See repository/postgres.uuidV7Time.
	jobID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate UUIDv7: %w", err)
	}

	job := &domain.Job{
		JobID:         jobID,
		Language:      req.Language,
		SourceCode:    req.SourceCode,
		Stdin:         req.Stdin,
		Status:        domain.StatusQueued,
		TimeLimitMs:   timeLimitMs,
		MemoryLimitKB: memoryLimitKB,
		CreatedAt:     time.Now().UTC(),
	}

	// One transaction writes both the job row and its queue message (the outbox
	// entry). There is no window in which the job exists but its message does not,
	// which is what the plain "INSERT then publish" sequence could not guarantee.
	if err := uc.outbox.CreateJobWithOutbox(ctx, job); err != nil {
		uc.logger.Error("Failed to create job", zap.Error(err), zap.String("job_id", jobID.String()))
		return nil, fmt.Errorf("create job: %w", err)
	}

	// Fast path: publish inline so the common case has no added latency. A failure
	// here is NOT a submission failure — the message is already durable, and the
	// outbox relay will deliver it. That is why this returns 202 rather than 503
	// when the broker is unreachable.
	if err := uc.publisher.Publish(ctx, job); err != nil {
		uc.logger.Warn("Inline publish failed; outbox relay will deliver",
			zap.Error(err), zap.String("job_id", jobID.String()))
	} else if err := uc.outbox.MarkPublished(ctx, job.JobID); err != nil {
		// Published but not recorded. The relay may publish it again; the worker's
		// claim is idempotent, so a duplicate is harmless.
		uc.logger.Warn("Published but failed to clear outbox entry",
			zap.Error(err), zap.String("job_id", jobID.String()))
	}

	uc.logger.Info("Job submitted successfully",
		zap.String("job_id", jobID.String()),
		zap.String("language", string(req.Language)),
	)

	return &domain.SubmitResponse{
		JobID:  jobID,
		Status: string(domain.StatusQueued),
	}, nil
}
