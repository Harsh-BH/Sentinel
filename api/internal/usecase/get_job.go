package usecase

import (
	"context"
	"errors"
	"fmt"

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
//
// "Not found" and "the database is unreachable" are reported as different
// errors. This used to collapse every failure into ErrJobNotFound, so a Postgres
// outage answered every request with 404 "Job not found" — which tells the
// caller their job never existed, and points whoever is debugging at 3am at
// entirely the wrong subsystem.
func (uc *GetJobUsecase) Execute(ctx context.Context, id uuid.UUID) (*domain.Job, error) {
	job, err := uc.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, domain.ErrJobNotFound) {
			uc.logger.Debug("Job not found", zap.String("job_id", id.String()))
			return nil, domain.ErrJobNotFound
		}
		uc.logger.Error("Failed to read job", zap.String("job_id", id.String()), zap.Error(err))
		return nil, fmt.Errorf("%w: %v", domain.ErrDatabaseUnavailable, err)
	}
	return job, nil
}
