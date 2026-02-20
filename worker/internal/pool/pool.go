package pool

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/worker/internal/domain"
	"github.com/Harsh-BH/Sentinel/worker/internal/metrics"
	"github.com/Harsh-BH/Sentinel/worker/internal/usecase"
)

// WorkerPool manages a fixed-size pool of goroutines that process jobs.
type WorkerPool struct {
	size      int
	jobs      <-chan *domain.JobMessage
	executeUC *usecase.ExecuteJobUsecase
	logger    *zap.Logger
	wg        sync.WaitGroup
}

// NewWorkerPool creates a new fixed-size worker pool.
func NewWorkerPool(size int, jobs <-chan *domain.JobMessage, executeUC *usecase.ExecuteJobUsecase, logger *zap.Logger) *WorkerPool {
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
		case msg, ok := <-p.jobs:
			if !ok {
				p.logger.Debug("Job channel closed", zap.Int("worker_id", id))
				return
			}

			job := msg.Job

			p.logger.Info("Worker processing job",
				zap.Int("worker_id", id),
				zap.String("job_id", job.JobID.String()),
				zap.String("language", string(job.Language)),
			)

			// Track active workers gauge.
			metrics.WorkersActive.Inc()
			startTime := time.Now()

			isDuplicate, err := p.executeUC.Execute(ctx, job)
			elapsed := time.Since(startTime).Seconds()

			metrics.WorkersActive.Dec()

			if err != nil {
				p.logger.Error("Job execution failed",
					zap.Int("worker_id", id),
					zap.String("job_id", job.JobID.String()),
					zap.Error(err),
				)

				// Nack without requeue — failed jobs go to DLQ.
				// Requeuing a deterministic failure would cause an infinite loop.
				if nackErr := msg.Nack(false); nackErr != nil {
					p.logger.Error("Failed to NACK message",
						zap.String("job_id", job.JobID.String()),
						zap.Error(nackErr),
					)
				}

				metrics.ExecutionsTotal.WithLabelValues(string(job.Language), "error").Inc()
				metrics.ExecutionDuration.WithLabelValues(string(job.Language)).Observe(elapsed)
				continue
			}

			if isDuplicate {
				p.logger.Debug("Duplicate job skipped",
					zap.Int("worker_id", id),
					zap.String("job_id", job.JobID.String()),
				)
				// Duplicate → still ACK so the message is removed from the queue.
				if ackErr := msg.Ack(); ackErr != nil {
					p.logger.Error("Failed to ACK duplicate message",
						zap.String("job_id", job.JobID.String()),
						zap.Error(ackErr),
					)
				}
				continue
			}

			// Successful execution — ACK the message.
			if ackErr := msg.Ack(); ackErr != nil {
				p.logger.Error("Failed to ACK message after execution",
					zap.String("job_id", job.JobID.String()),
					zap.Error(ackErr),
				)
			}

			metrics.ExecutionDuration.WithLabelValues(string(job.Language)).Observe(elapsed)
		}
	}
}
