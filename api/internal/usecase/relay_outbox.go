package usecase

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/api/internal/publisher"
	"github.com/Harsh-BH/Sentinel/api/internal/repository"
)

const (
	// DefaultRelayInterval is how often unpublished outbox entries are retried.
	// Much tighter than the reaper's sweep because this is the primary delivery
	// path when the broker has been unavailable, not a last-resort backstop.
	DefaultRelayInterval = 1 * time.Second

	// DefaultRelayMinAge keeps the relay clear of the inline publish that Submit
	// performs right after commit. Without it the relay could pick up a row while
	// that publish is still in flight and send the same message twice.
	DefaultRelayMinAge = 3 * time.Second

	// DefaultRelayBatch caps rows per pass so a large backlog drains steadily
	// rather than in one burst that could overwhelm the broker it just recovered.
	DefaultRelayBatch = 200

	// DefaultRelayMaxAttempts bounds retries for a message that can never be
	// routed. Past this the job is failed with an explanatory stderr.
	DefaultRelayMaxAttempts = 10
)

// RelayOutboxUsecase publishes queue messages that were committed to the outbox
// but not yet confirmed by the broker.
//
// This is the second half of the transactional outbox. Submit writes the job row
// and its message in one transaction and then tries to publish inline — the fast
// path, which is what happens virtually always. The relay exists for the cases
// the fast path cannot cover: the broker was down, the publish was nacked, or the
// API process died between commit and publish.
//
// Because delivery no longer depends on the broker being reachable at submit
// time, POST /submissions returns 202 even during a RabbitMQ outage.
type RelayOutboxUsecase struct {
	outbox      repository.OutboxRepository
	publisher   publisher.Publisher
	logger      *zap.Logger
	minAge      time.Duration
	batch       int
	maxAttempts int
}

// NewRelayOutboxUsecase creates a relay with the default tuning.
func NewRelayOutboxUsecase(outbox repository.OutboxRepository, pub publisher.Publisher, logger *zap.Logger) *RelayOutboxUsecase {
	return &RelayOutboxUsecase{
		outbox:      outbox,
		publisher:   pub,
		logger:      logger,
		minAge:      DefaultRelayMinAge,
		batch:       DefaultRelayBatch,
		maxAttempts: DefaultRelayMaxAttempts,
	}
}

// RelayStats summarises one relay pass.
type RelayStats struct {
	Claimed   int
	Published int
	Failed    int
	Exhausted int
}

// Pass runs a single relay pass.
func (uc *RelayOutboxUsecase) Pass(ctx context.Context) (RelayStats, error) {
	var stats RelayStats

	// Give up on hopeless entries first, so they cannot occupy the batch budget
	// on every pass and starve deliverable messages behind them.
	if n, err := uc.outbox.DropExhausted(ctx, uc.maxAttempts); err != nil {
		uc.logger.Error("Outbox relay failed to drop exhausted entries", zap.Error(err))
	} else if n > 0 {
		stats.Exhausted = n
		uc.logger.Error("Outbox entries exhausted their retries; jobs marked failed",
			zap.Int("count", n), zap.Int("max_attempts", uc.maxAttempts))
	}

	jobs, err := uc.outbox.ClaimUnpublished(ctx, uc.minAge, uc.batch)
	if err != nil {
		return stats, err
	}
	stats.Claimed = len(jobs)

	for _, job := range jobs {
		if err := uc.publisher.Publish(ctx, job); err != nil {
			stats.Failed++
			uc.logger.Warn("Outbox relay publish failed; will retry",
				zap.String("job_id", job.JobID.String()), zap.Error(err))
			continue
		}
		if err := uc.outbox.MarkPublished(ctx, job.JobID); err != nil {
			// The message is on the queue but we failed to record that. The worker's
			// claim is idempotent, so a later duplicate publish is harmless — better
			// than risking a message that was never delivered.
			uc.logger.Error("Outbox relay published but could not clear the entry",
				zap.String("job_id", job.JobID.String()), zap.Error(err))
			continue
		}
		stats.Published++
	}

	return stats, nil
}

// Run relays on a ticker until ctx is cancelled.
func (uc *RelayOutboxUsecase) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	uc.logger.Info("Outbox relay started",
		zap.Duration("interval", interval),
		zap.Duration("min_age", uc.minAge),
		zap.Int("max_attempts", uc.maxAttempts),
	)

	for {
		select {
		case <-ctx.Done():
			uc.logger.Info("Outbox relay stopped")
			return
		case <-ticker.C:
			stats, err := uc.Pass(ctx)
			if err != nil {
				uc.logger.Error("Outbox relay pass failed", zap.Error(err))
				continue
			}
			switch {
			case stats.Published > 0:
				uc.logger.Warn("Outbox relay delivered deferred messages",
					zap.Int("claimed", stats.Claimed),
					zap.Int("published", stats.Published),
					zap.Int("failed", stats.Failed),
				)
			case stats.Claimed > 0:
				// Claimed but none published: the broker is still refusing. Say that,
				// rather than logging "delivered" with published=0.
				uc.logger.Warn("Outbox relay could not deliver; will retry",
					zap.Int("claimed", stats.Claimed),
					zap.Int("failed", stats.Failed),
				)
			}
		}
	}
}
