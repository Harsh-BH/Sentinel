package usecase

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/api/internal/domain"
	"github.com/Harsh-BH/Sentinel/api/internal/publisher"
	"github.com/Harsh-BH/Sentinel/api/internal/repository"
)

// Reaper defaults. These are deliberately conservative: sweeping too eagerly
// re-runs jobs that were merely slow, and a job re-run is user-visible work.
const (
	// DefaultReapInterval is how often the sweep runs.
	DefaultReapInterval = 30 * time.Second

	// DefaultReapGrace is how long a job may sit untouched in a non-terminal
	// state before it is considered stranded. It must comfortably exceed the
	// worst case queue wait, or the reaper will duplicate jobs that were only
	// waiting their turn.
	DefaultReapGrace = 2 * time.Minute

	// DefaultReapBatch caps rows per sweep so a large backlog is worked through
	// gradually instead of flooding the queue in one burst.
	DefaultReapBatch = 100

	// DefaultMaxAttempts caps total delivery attempts (worker claims plus reaper
	// re-publishes) before the job is failed. Without a cap, a job that kills its
	// worker every time would be re-published forever.
	DefaultMaxAttempts = 5
)

// ReapJobsUsecase re-submits jobs that were stranded in a non-terminal state.
//
// This is the backstop for the system's two unavoidable gaps:
//
//  1. The submit path writes to Postgres and then publishes to RabbitMQ. Those
//     are two systems, so they cannot be one atomic operation — a crash in
//     between leaves a QUEUED row with no queue message.
//  2. A worker can die after claiming a job but before writing its result,
//     leaving the row in RUNNING with an expired lease.
//
// Both look identical from here: a non-terminal row nobody is working on. One
// sweep fixes both, which is why the lease lives in Postgres rather than in an
// evictable cache.
type ReapJobsUsecase struct {
	repo        repository.JobRepository
	publisher   publisher.Publisher
	logger      *zap.Logger
	grace       time.Duration
	batch       int
	maxAttempts int
}

// NewReapJobsUsecase creates a reaper with the default tuning.
func NewReapJobsUsecase(repo repository.JobRepository, pub publisher.Publisher, logger *zap.Logger) *ReapJobsUsecase {
	return &ReapJobsUsecase{
		repo:        repo,
		publisher:   pub,
		logger:      logger,
		grace:       DefaultReapGrace,
		batch:       DefaultReapBatch,
		maxAttempts: DefaultMaxAttempts,
	}
}

// ReapStats summarises one sweep.
type ReapStats struct {
	Found       int
	Republished int
	Failed      int
}

// Sweep runs a single reaper pass.
func (uc *ReapJobsUsecase) Sweep(ctx context.Context) (ReapStats, error) {
	var stats ReapStats

	jobs, err := uc.repo.ReclaimStuck(ctx, uc.grace, uc.batch)
	if err != nil {
		return stats, err
	}
	stats.Found = len(jobs)

	for _, job := range jobs {
		if job.Attempts >= uc.maxAttempts {
			// Out of retries. Give the user a definite answer instead of leaving the
			// job claiming to be RUNNING forever.
			if err := uc.repo.UpdateStatus(ctx, job.JobID, job.CreatedAt, domain.StatusInternalError); err != nil {
				uc.logger.Error("Reaper failed to mark job failed",
					zap.String("job_id", job.JobID.String()), zap.Error(err))
				continue
			}
			stats.Failed++
			uc.logger.Warn("Reaper gave up on job",
				zap.String("job_id", job.JobID.String()),
				zap.Int("attempts", job.Attempts),
			)
			continue
		}

		if err := uc.publisher.Publish(ctx, job); err != nil {
			// Leave it stranded; the next sweep will retry once this lease expires.
			uc.logger.Error("Reaper failed to re-publish job",
				zap.String("job_id", job.JobID.String()), zap.Error(err))
			continue
		}
		stats.Republished++
		uc.logger.Warn("Reaper re-published stranded job",
			zap.String("job_id", job.JobID.String()),
			zap.String("status", string(job.Status)),
			zap.Int("attempts", job.Attempts),
		)
	}

	return stats, nil
}

// Run sweeps on a ticker until ctx is cancelled.
func (uc *ReapJobsUsecase) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	uc.logger.Info("Reaper started",
		zap.Duration("interval", interval),
		zap.Duration("grace", uc.grace),
		zap.Int("max_attempts", uc.maxAttempts),
	)

	for {
		select {
		case <-ctx.Done():
			uc.logger.Info("Reaper stopped")
			return
		case <-ticker.C:
			stats, err := uc.Sweep(ctx)
			if err != nil {
				uc.logger.Error("Reaper sweep failed", zap.Error(err))
				continue
			}
			if stats.Found > 0 {
				uc.logger.Warn("Reaper sweep recovered stranded jobs",
					zap.Int("found", stats.Found),
					zap.Int("republished", stats.Republished),
					zap.Int("failed", stats.Failed),
				)
			}
		}
	}
}
