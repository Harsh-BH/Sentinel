package http

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// notifyChannel must match the channel name used by the notify_job_status()
// trigger in migrations/001_initial_schema.up.sql.
const notifyChannel = "job_status"

// StatusNotifier turns Postgres LISTEN/NOTIFY events into in-process fan-out for
// WebSocket subscribers.
//
// Why this exists: the job stream used to be the API querying the jobs table
// every 500 ms, per open connection. That is 2 queries/second/viewer for work
// that changes maybe three times in a job's life, and it scales with viewers
// rather than with events — 500 concurrent viewers meant 1000 queries/second of
// almost entirely redundant reads.
//
// Now the status-change trigger fires pg_notify inside the same transaction as
// the write, and each API pod holds ONE dedicated connection listening on that
// channel. Events are fanned out to subscribers in memory, so cost scales with
// status changes, and the number of database connections used for streaming is
// one per pod instead of one per request.
//
// The notification is only a wake-up: NOTIFY payloads are capped at 8000 bytes
// while stdout/stderr can be 64 KB, so subscribers read the row themselves.
type StatusNotifier struct {
	pool   *pgxpool.Pool
	logger *zap.Logger

	mu   sync.Mutex
	subs map[uuid.UUID]map[chan struct{}]struct{}
}

// NewStatusNotifier creates a notifier. Call Run to start listening.
func NewStatusNotifier(pool *pgxpool.Pool, logger *zap.Logger) *StatusNotifier {
	return &StatusNotifier{
		pool:   pool,
		logger: logger,
		subs:   make(map[uuid.UUID]map[chan struct{}]struct{}),
	}
}

// Subscribe registers interest in a job and returns a channel that receives a
// signal on each status change, plus a cancel func that must be called.
//
// The channel is buffered with capacity 1 and sends are non-blocking: a
// notification is a "something changed, go look" edge, not a queue of states, so
// coalescing two events into one wake-up loses nothing. This is also what stops
// one slow WebSocket client from blocking the shared listener goroutine.
func (n *StatusNotifier) Subscribe(jobID uuid.UUID) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)

	n.mu.Lock()
	if n.subs[jobID] == nil {
		n.subs[jobID] = make(map[chan struct{}]struct{})
	}
	n.subs[jobID][ch] = struct{}{}
	n.mu.Unlock()

	return ch, func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		if set, ok := n.subs[jobID]; ok {
			delete(set, ch)
			if len(set) == 0 {
				// Drop the empty map so a long-lived process does not accumulate one
				// entry per job it has ever streamed.
				delete(n.subs, jobID)
			}
		}
	}
}

// Subscribers reports how many channels are currently registered. Test hook.
func (n *StatusNotifier) Subscribers(jobID uuid.UUID) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.subs[jobID])
}

func (n *StatusNotifier) publish(jobID uuid.UUID) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for ch := range n.subs[jobID] {
		select {
		case ch <- struct{}{}:
		default: // already has a pending wake-up; coalesce
		}
	}
}

// Run holds a dedicated connection listening for status changes until ctx is
// cancelled, reconnecting with backoff on failure.
//
// A dedicated connection is required, not a pool acquisition: LISTEN
// registrations are per-session, so a pooled connection could be handed to
// another caller and reset, silently unsubscribing us.
func (n *StatusNotifier) Run(ctx context.Context) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}

		if err := n.listen(ctx); err != nil && ctx.Err() == nil {
			n.logger.Warn("Status listener disconnected, reconnecting",
				zap.Error(err), zap.Duration("retry_in", backoff))

			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		// Clean exit (context cancelled).
		return
	}
}

func (n *StatusNotifier) listen(ctx context.Context) error {
	conn, err := n.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	// Hijack the connection out of the pool for the lifetime of this listener, so
	// the pool cannot recycle a session that holds a LISTEN registration.
	defer conn.Release()

	if _, err := conn.Exec(ctx, "LISTEN "+notifyChannel); err != nil {
		return err
	}
	n.logger.Info("Listening for job status changes", zap.String("channel", notifyChannel))

	for {
		notification, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}

		// Payload is "<job_id>:<status>". The status is logged but not trusted for
		// state: subscribers re-read the row, which is authoritative.
		jobIDStr, status, _ := strings.Cut(notification.Payload, ":")
		jobID, parseErr := uuid.Parse(jobIDStr)
		if parseErr != nil {
			n.logger.Warn("Unparseable status notification",
				zap.String("payload", notification.Payload), zap.Error(parseErr))
			continue
		}

		n.logger.Debug("Job status change notification",
			zap.String("job_id", jobID.String()), zap.String("status", status))
		n.publish(jobID)
	}
}
