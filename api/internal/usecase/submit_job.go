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
)

// SubmitJobUsecase handles the business logic for submitting code execution jobs.
type SubmitJobUsecase struct {
	repo      repository.JobRepository
	publisher publisher.Publisher
	logger    *zap.Logger
}

// NewSubmitJobUsecase creates a new SubmitJobUsecase.
func NewSubmitJobUsecase(repo repository.JobRepository, pub publisher.Publisher, logger *zap.Logger) *SubmitJobUsecase {
	return &SubmitJobUsecase{
		repo:      repo,
		publisher: pub,
		logger:    logger,
	}
}

// Execute validates the submission, creates a job, publishes it, and returns the job ID.
func (uc *SubmitJobUsecase) Execute(ctx context.Context, req *domain.SubmitRequest) (*domain.SubmitResponse, error) {
	// Validate language
	if !req.Language.IsValid() {
		return nil, domain.ErrInvalidLanguage
	}

	// Validate source code
	if strings.TrimSpace(req.SourceCode) == "" {
		return nil, domain.ErrEmptySourceCode
	}
	if len(req.SourceCode) > maxSourceCodeSize {
		return nil, domain.ErrPayloadTooLarge
	}

	// Apply defaults
	timeLimitMs := defaultTimeLimitMs
	if req.TimeLimitMs != nil && *req.TimeLimitMs > 0 && *req.TimeLimitMs <= 30000 {
		timeLimitMs = *req.TimeLimitMs
	}
	memoryLimitKB := defaultMemoryLimitKB
	if req.MemoryLimitKB != nil && *req.MemoryLimitKB > 0 && *req.MemoryLimitKB <= 524288 {
		memoryLimitKB = *req.MemoryLimitKB
	}

	// Generate UUIDv7 (time-ordered)
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
		UpdatedAt:     time.Now().UTC(),
	}

	// Persist to PostgreSQL
	if err := uc.repo.Create(ctx, job); err != nil {
		uc.logger.Error("Failed to create job in database", zap.Error(err), zap.String("job_id", jobID.String()))
		return nil, fmt.Errorf("create job: %w", err)
	}

	// Publish to RabbitMQ
	if err := uc.publisher.Publish(ctx, job); err != nil {
		uc.logger.Error("Failed to publish job to queue", zap.Error(err), zap.String("job_id", jobID.String()))
		// Update status to INTERNAL_ERROR since the job won't be processed
		_ = uc.repo.UpdateStatus(ctx, jobID, domain.StatusInternalError)
		return nil, domain.ErrPublishFailed
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
