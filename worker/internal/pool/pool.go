package pool

import (
	"context"
	"sync"

	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/worker/internal/domain"
	"github.com/Harsh-BH/Sentinel/worker/internal/usecase"
)

// WorkerPool manages a fixed-size pool of goroutines that process jobs.
type WorkerPool struct {
	size      int
	jobs      <-chan *domain.Job
	executeUC *usecase.ExecuteJobUsecase
	logger    *zap.Logger
	wg        sync.WaitGroup
}

// NewWorkerPool creates a new fixed-size worker pool.
func NewWorkerPool(size int, jobs <-chan *domain.Job, executeUC *usecase.ExecuteJobUsecase, logger *zap.Logger) *WorkerPool {
	return &WorkerPool{
		size:      size,
		jobs:      jobs,
		executeUC: executeUC,
		logger:    logger,
	}
}

// Start launches all worker goroutines. Call Stop to wait for them to finish.
func (p *WorkerPool) Start(ctx context.Context) {
	p.logger.Info("Starting worker pool", zap.Int("pool_size", p.size))

	for i := 0; i < p.size; i++ {
		p.wg.Add(1)
		go p.worker(ctx, i)
	}
}

// Stop waits for all workers to finish their current jobs and exit.
func (p *WorkerPool) Stop() {
	p.wg.Wait()
	p.logger.Info("Worker pool stopped")
}

func (p *WorkerPool) worker(ctx context.Context, id int) {
	defer p.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			p.logger.Error("Worker panic recovered",
				zap.Int("worker_id", id),
				zap.Any("panic", r),
			)
		}
	}()

	p.logger.Debug("Worker started", zap.Int("worker_id", id))

	for {
		select {
		case <-ctx.Done():
			p.logger.Debug("Worker shutting down", zap.Int("worker_id", id))
			return
		case job, ok := <-p.jobs:
			if !ok {
				p.logger.Debug("Job channel closed", zap.Int("worker_id", id))
				return
			}

			p.logger.Info("Worker processing job",
				zap.Int("worker_id", id),
				zap.String("job_id", job.JobID.String()),
				zap.String("language", string(job.Language)),
			)

			isDuplicate, err := p.executeUC.Execute(ctx, job)
			if err != nil {
				p.logger.Error("Job execution failed",
					zap.Int("worker_id", id),
					zap.String("job_id", job.JobID.String()),
					zap.Error(err),
				)
				continue
			}

			if isDuplicate {
				p.logger.Debug("Duplicate job skipped",
					zap.Int("worker_id", id),
					zap.String("job_id", job.JobID.String()),
				)
			}
		}
	}
}
