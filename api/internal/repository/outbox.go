package repository

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Harsh-BH/Sentinel/api/internal/domain"
)

// OutboxRepository is the durable hand-off between "the job exists" and "the
// queue knows about it".
//
// The submit path cannot write Postgres and RabbitMQ atomically — they are
// different systems — so instead it writes the job row and its queue message in
// one Postgres transaction. Publishing happens after commit; anything that fails
// to publish is still recorded and gets picked up by the relay.
//
// The observable consequence is that a submission stops depending on broker
// availability: once the transaction commits, delivery is guaranteed eventually,
// so the API can return 202 even while RabbitMQ is down.
type OutboxRepository interface {
	// CreateJobWithOutbox inserts the job row and its outbox entry in a single
	// transaction. Either both land or neither does.
	CreateJobWithOutbox(ctx context.Context, job *domain.Job) error

	// MarkPublished removes the outbox entry after the broker has confirmed the
	// message. Deleting rather than flagging keeps the relay's partial index the
	// size of the backlog instead of the size of history.
	MarkPublished(ctx context.Context, jobID uuid.UUID) error

	// ClaimUnpublished locks and returns outbox entries that have not been
	// confirmed and are at least minAge old, incrementing their attempt count.
	//
	// minAge exists to stay out of the fast path's way: a submission publishes
	// inline immediately after commit, so sweeping a row that is milliseconds old
	// would race that publish and duplicate the message.
	//
	// Concurrent relays are safe via FOR UPDATE SKIP LOCKED.
	ClaimUnpublished(ctx context.Context, minAge time.Duration, limit int) ([]*domain.Job, error)

	// DropExhausted removes entries past maxAttempts and marks their job failed,
	// so a permanently unroutable message cannot be retried forever.
	DropExhausted(ctx context.Context, maxAttempts int) (int, error)
}
