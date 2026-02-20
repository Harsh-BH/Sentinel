package pool_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/worker/internal/domain"
	"github.com/Harsh-BH/Sentinel/worker/internal/pool"
	"github.com/Harsh-BH/Sentinel/worker/internal/repository/mock"
	"github.com/Harsh-BH/Sentinel/worker/internal/usecase"
)

func newTestPool(t *testing.T, poolSize int, exec *mock.Executor) (chan *domain.JobMessage, *pool.WorkerPool, context.CancelFunc) {
	t.Helper()

	logger := zap.NewNop()
	repo := &mock.JobRepository{}
	idem := &mock.IdempotencyStore{}
	uc := usecase.NewExecuteJobUsecase(repo, idem, exec, logger)

	ch := make(chan *domain.JobMessage, 16)
	ctx, cancel := context.WithCancel(context.Background())
	wp := pool.NewWorkerPool(poolSize, ch, uc, logger)
	wp.Start(ctx)

	return ch, wp, cancel
}

func sendJob(ch chan<- *domain.JobMessage, acked *atomic.Int32, nacked *atomic.Int32) {
	ch <- &domain.JobMessage{
		Job: &domain.Job{
			JobID:         uuid.New(),
			Language:      domain.LangPython,
			SourceCode:    "print('test')",
			TimeLimitMs:   5000,
			MemoryLimitKB: 262144,
		},
		Ack: func() error {
			acked.Add(1)
			return nil
		},
		Nack: func(requeue bool) error {
			nacked.Add(1)
			return nil
		},
	}
}

// Test: pool processes jobs and ACKs them.
func TestPool_ProcessAndAck(t *testing.T) {
	exec := &mock.Executor{}
	ch, wp, cancel := newTestPool(t, 2, exec)

	var acked, nacked atomic.Int32

	for i := 0; i < 5; i++ {
		sendJob(ch, &acked, &nacked)
	}

	// Give workers time to process.
	time.Sleep(200 * time.Millisecond)

	cancel()
	wp.Stop()

	if acked.Load() != 5 {
		t.Errorf("expected 5 ACKs, got %d", acked.Load())
	}
	if nacked.Load() != 0 {
		t.Errorf("expected 0 NACKs, got %d", nacked.Load())
	}
}

// Test: pool NACKs jobs that fail execution.
func TestPool_NacksOnFailure(t *testing.T) {
	exec := &mock.Executor{
		ExecuteFn: func(ctx context.Context, req *domain.ExecutionRequest) (*domain.ExecutionResult, error) {
			return nil, context.DeadlineExceeded
		},
	}
	ch, wp, cancel := newTestPool(t, 1, exec)

	var acked, nacked atomic.Int32
	sendJob(ch, &acked, &nacked)

	time.Sleep(200 * time.Millisecond)

	cancel()
	wp.Stop()

	if nacked.Load() != 1 {
		t.Errorf("expected 1 NACK, got %d", nacked.Load())
	}
	if acked.Load() != 0 {
		t.Errorf("expected 0 ACKs, got %d", acked.Load())
	}
}

// Test: pool shuts down gracefully (context cancellation).
func TestPool_GracefulShutdown(t *testing.T) {
	exec := &mock.Executor{}
	ch, wp, cancel := newTestPool(t, 4, exec)

	// Send some jobs then immediately cancel.
	var acked, nacked atomic.Int32
	sendJob(ch, &acked, &nacked)
	sendJob(ch, &acked, &nacked)

	// Small delay so at least one job gets picked up.
	time.Sleep(50 * time.Millisecond)
	cancel()
	wp.Stop()
	close(ch)

	// All sent jobs should be ACKed (they were in the buffer before cancel).
	total := acked.Load() + nacked.Load()
	if total < 1 {
		t.Errorf("expected at least 1 processed job, got %d", total)
	}
}

// Test: pool handles duplicate jobs (ACKs them, not NACKs).
func TestPool_DuplicateIsAcked(t *testing.T) {
	exec := &mock.Executor{}

	logger := zap.NewNop()
	repo := &mock.JobRepository{}
	idem := &mock.IdempotencyStore{
		AcquireLockFn: func(ctx context.Context, jobID uuid.UUID) (bool, error) {
			return false, nil // duplicate
		},
	}
	uc := usecase.NewExecuteJobUsecase(repo, idem, exec, logger)

	ch := make(chan *domain.JobMessage, 8)
	ctx, cancel := context.WithCancel(context.Background())
	wp := pool.NewWorkerPool(1, ch, uc, logger)
	wp.Start(ctx)

	var acked, nacked atomic.Int32
	sendJob(ch, &acked, &nacked)

	time.Sleep(200 * time.Millisecond)
	cancel()
	wp.Stop()

	if acked.Load() != 1 {
		t.Errorf("expected 1 ACK for duplicate, got %d", acked.Load())
	}
	if nacked.Load() != 0 {
		t.Errorf("expected 0 NACKs, got %d", nacked.Load())
	}
}
