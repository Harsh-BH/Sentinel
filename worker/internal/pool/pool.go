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

	// inFlight holds the message currently being processed. On a panic we use
	// it to requeue the message instead of stranding it in the broker's
	// unacked set until the connection drops.
	var inFlight *domain.JobMessage

	defer func() {
		if r := recover(); r != nil {
			p.logger.Error("Worker panic recovered",
				zap.Int("worker_id", id),
				zap.Any("panic", r),
			)

			// Requeue the in-flight message so it is redelivered rather than
			// lost with the crashed goroutine.
			if inFlight != nil {
				if nackErr := inFlight.Nack(true); nackErr != nil {
					p.logger.Error("Failed to NACK in-flight message after panic",
						zap.Int("worker_id", id),
						zap.Error(nackErr),
					)
				}
			}

			// Relaunch the worker so the pool does not permanently shrink.
			// Add(1) happens before this goroutine's deferred Done, while the
			// WaitGroup counter is still non-zero, so Stop() stays correct.
			p.wg.Add(1)
			go p.worker(ctx, id)
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

			// Mark the message in-flight for the duration of processing so a
			// panic in processJob can requeue it, then clear it on success.
			inFlight = msg
			p.processJob(ctx, id, msg)
			inFlight = nil
		}
	}
}

// processJob runs a single job to completion and acknowledges it. A panic here
// propagates to worker's deferred recover, which requeues the message and
// relaunches the goroutine.
func (p *WorkerPool) processJob(ctx context.Context, id int, msg *domain.JobMessage) {
	job := msg.Job

	p.logger.Info("Worker processing job",
		zap.Int("worker_id", id),
		zap.String("job_id", job.JobID.String()),
		zap.String("language", string(job.Language)),
	)

	// Track active workers gauge. Deferred so it is balanced even on panic.
	metrics.WorkersActive.Inc()
	defer metrics.WorkersActive.Dec()

	startTime := time.Now()
	isDuplicate, err := p.executeUC.Execute(ctx, job)
	elapsed := time.Since(startTime).Seconds()

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
		return
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
		return
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
