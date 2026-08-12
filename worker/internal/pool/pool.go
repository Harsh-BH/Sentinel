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

// Start launches all worker goroutines.
//
// Two contexts, deliberately:
//
//   - acceptCtx is cancelled first on shutdown. Workers stop picking up NEW
//     messages but keep running whatever they already have.
//   - jobCtx is passed into execution and is cancelled only as a last resort,
//     once the drain deadline expires.
//
// Collapsing these into one context is what made SIGTERM kill in-flight
// executions: cancelling the context the sandbox was launched with SIGKILLs
// nsjail immediately, so the result was never persisted and the job was lost.
func (p *WorkerPool) Start(acceptCtx, jobCtx context.Context) {
	p.logger.Info("Starting worker pool", zap.Int("pool_size", p.size))

	for i := 0; i < p.size; i++ {
		p.wg.Add(1)
		go p.worker(acceptCtx, jobCtx, i)
	}
}

// Stop waits up to timeout for workers to finish their current jobs.
// It reports whether the pool drained cleanly within the deadline.
func (p *WorkerPool) Stop(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		p.logger.Info("Worker pool drained cleanly")
		return true
	case <-time.After(timeout):
		p.logger.Warn("Worker pool drain timed out", zap.Duration("timeout", timeout))
		return false
	}
}

func (p *WorkerPool) worker(acceptCtx, jobCtx context.Context, id int) {
	defer p.wg.Done()

	// inFlight holds the message currently being processed. On a panic we use it
	// to requeue the message instead of stranding it in the broker's unacked set.
	// Double-settling is impossible: the consumer wraps Ack/Nack in a sync.Once,
	// so whichever path fires first wins and the other is a no-op.
	var inFlight *domain.JobMessage

	defer func() {
		if r := recover(); r != nil {
			p.logger.Error("Worker panic recovered",
				zap.Int("worker_id", id),
				zap.Any("panic", r),
			)
			metrics.WorkerPanics.Inc()

			if inFlight != nil {
				if nackErr := inFlight.Nack(true); nackErr != nil {
					p.logger.Error("Failed to NACK in-flight message after panic",
						zap.Int("worker_id", id),
						zap.Error(nackErr),
					)
				}
			}

			// Relaunch so the pool does not permanently shrink. Add(1) happens
			// before this goroutine's deferred Done, while the WaitGroup counter is
			// still non-zero, so Stop() stays correct.
			p.wg.Add(1)
			go p.worker(acceptCtx, jobCtx, id)
		}
	}()

	p.logger.Debug("Worker started", zap.Int("worker_id", id))

	for {
		select {
		case <-acceptCtx.Done():
			p.logger.Debug("Worker stopping: no longer accepting work", zap.Int("worker_id", id))
			return
		case msg, ok := <-p.jobs:
			if !ok {
				p.logger.Debug("Job channel closed", zap.Int("worker_id", id))
				return
			}

			inFlight = msg
			p.processJob(jobCtx, id, msg)
			inFlight = nil
		}
	}
}

// processJob runs a single job to completion and settles the queue message.
// A panic here propagates to worker's deferred recover, which requeues the
// message and relaunches the goroutine.
func (p *WorkerPool) processJob(jobCtx context.Context, id int, msg *domain.JobMessage) {
	job := msg.Job

	p.logger.Info("Worker processing job",
		zap.Int("worker_id", id),
		zap.String("job_id", job.JobID.String()),
		zap.String("language", string(job.Language)),
	)

	metrics.WorkersActive.Inc()
	defer metrics.WorkersActive.Dec()

	outcome, err := p.executeUC.Execute(jobCtx, job)

	// Metrics are recorded by the usecase only. They used to be recorded here as
	// well, which double-counted every execution: the histogram's _count was 2x
	// reality and every latency percentile derived from it was wrong.
	switch outcome {
	case usecase.OutcomeExecuted, usecase.OutcomeDuplicate:
		p.settle(job, "ack", msg.Ack)

	case usecase.OutcomePoison:
		// Can never succeed. Dead-letter it without requeue.
		p.logger.Error("Dead-lettering poison message",
			zap.String("job_id", job.JobID.String()),
			zap.Error(err),
		)
		p.settle(job, "dlq", func() error { return msg.Nack(false) })

	case usecase.OutcomeRetryable:
		// Infrastructure failed. Dead-letter rather than requeue — an immediate
		// requeue spins a hot loop against the broken dependency. The job row is
		// left non-terminal with an expired lease, so the reaper re-submits it.
		p.logger.Error("Job failed, leaving for reaper",
			zap.Int("worker_id", id),
			zap.String("job_id", job.JobID.String()),
			zap.Error(err),
		)
		p.settle(job, "dlq", func() error { return msg.Nack(false) })
	}
}

func (p *WorkerPool) settle(job *domain.Job, action string, fn func() error) {
	if err := fn(); err != nil {
		p.logger.Error("Failed to settle message",
			zap.String("action", action),
			zap.String("job_id", job.JobID.String()),
			zap.Error(err),
		)
		metrics.SettleFailures.WithLabelValues(action).Inc()
	}
}
