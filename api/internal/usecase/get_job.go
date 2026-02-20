package usecase

import (
	"context"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/api/internal/domain"
	"github.com/Harsh-BH/Sentinel/api/internal/repository"
)

// GetJobUsecase handles fetching job status and results.
type GetJobUsecase struct {
	repo   repository.JobRepository
	logger *zap.Logger
}

// NewGetJobUsecase creates a new GetJobUsecase.
func NewGetJobUsecase(repo repository.JobRepository, logger *zap.Logger) *GetJobUsecase {
	return &GetJobUsecase{
		repo:   repo,
		logger: logger,
	}
}

// Execute retrieves a job by its ID.
func (uc *GetJobUsecase) Execute(ctx context.Context, id uuid.UUID) (*domain.Job, error) {
	job, err := uc.repo.GetByID(ctx, id)
	if err != nil {
		uc.logger.Debug("Job not found", zap.String("job_id", id.String()), zap.Error(err))
		return nil, domain.ErrJobNotFound
	}
	return job, nil
}
