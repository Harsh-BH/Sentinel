package pool_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/worker/internal/domain"
	"github.com/Harsh-BH/Sentinel/worker/internal/pool"
	"github.com/Harsh-BH/Sentinel/worker/internal/repository"
	"github.com/Harsh-BH/Sentinel/worker/internal/repository/mock"
	"github.com/Harsh-BH/Sentinel/worker/internal/usecase"
)

type testPool struct {
	ch            chan *domain.JobMessage
	wp            *pool.WorkerPool
	stopAccepting context.CancelFunc
	cancelJobs    context.CancelFunc
}

func newTestPool(t *testing.T, poolSize int, repo *mock.JobRepository, exec *mock.Executor) *testPool {
	t.Helper()

	logger := zap.NewNop()
	uc := usecase.NewExecuteJobUsecase(repo, exec, logger)

	ch := make(chan *domain.JobMessage, 16)
	acceptCtx, stopAccepting := context.WithCancel(context.Background())
	jobCtx, cancelJobs := context.WithCancel(context.Background())

	wp := pool.NewWorkerPool(poolSize, ch, uc, logger)
	wp.Start(acceptCtx, jobCtx)

	t.Cleanup(func() {
		stopAccepting()
		cancelJobs()
	})

	return &testPool{ch: ch, wp: wp, stopAccepting: stopAccepting, cancelJobs: cancelJobs}
}

// settleRecorder mimics the consumer's sync.Once wrapping so tests exercise the
// same "first settle wins" contract the real Ack/Nack closures provide.
type settleRecorder struct {
	acked    atomic.Int32
	nacked   atomic.Int32
	requeued atomic.Int32
}

func (s *settleRecorder) message() *domain.JobMessage {
	var once sync.Once
	return &domain.JobMessage{
		Job: &domain.Job{
			JobID:         uuid.New(),
			Language:      domain.LangPython,
			SourceCode:    "print('test')",
			TimeLimitMs:   5000,
			MemoryLimitKB: 262144,
			CreatedAt:     time.Now().UTC(),
		},
		Ack: func() error {
			once.Do(func() { s.acked.Add(1) })
			return nil
		},
		Nack: func(requeue bool) error {
			once.Do(func() {
				s.nacked.Add(1)
				if requeue {
					s.requeued.Add(1)
				}
			})
			return nil
		},
	}
}

// Test: pool processes jobs and ACKs them.
func TestPool_ProcessAndAck(t *testing.T) {
	var rec settleRecorder
	tp := newTestPool(t, 2, &mock.JobRepository{}, &mock.Executor{})

	tp.ch <- rec.message()
	time.Sleep(200 * time.Millisecond)

	if rec.acked.Load() != 1 {
		t.Errorf("expected 1 ACK, got %d", rec.acked.Load())
	}
	if rec.nacked.Load() != 0 {
		t.Errorf("expected 0 NACKs, got %d", rec.nacked.Load())
	}
}

// Test: a duplicate is ACKed (removed from the queue), not NACKed.
func TestPool_DuplicateIsAcked(t *testing.T) {
	var rec settleRecorder
	repo := &mock.JobRepository{
		ClaimFn: func(context.Context, uuid.UUID, time.Time, domain.ExecutionStatus, time.Duration) (repository.ClaimOutcome, error) {
			return repository.ClaimTerminal, nil
		},
	}
	tp := newTestPool(t, 1, repo, &mock.Executor{})

	tp.ch <- rec.message()
	time.Sleep(200 * time.Millisecond)

	if rec.acked.Load() != 1 {
		t.Errorf("expected duplicate to be ACKed, got %d acks", rec.acked.Load())
	}
}

// Test: an infrastructure failure dead-letters WITHOUT requeue. Requeuing would
// spin a hot loop against the broken dependency; the reaper re-submits instead.
func TestPool_RetryableFailure_DeadLettersWithoutRequeue(t *testing.T) {
	var rec settleRecorder
	exec := &mock.Executor{
		ExecuteFn: func(context.Context, *domain.ExecutionRequest) (*domain.ExecutionResult, error) {
			return nil, errors.New("sandbox down")
		},
	}
	tp := newTestPool(t, 1, &mock.JobRepository{}, exec)

	tp.ch <- rec.message()
	time.Sleep(200 * time.Millisecond)

	if rec.nacked.Load() != 1 {
		t.Errorf("expected 1 NACK, got %d", rec.nacked.Load())
	}
	if rec.requeued.Load() != 0 {
		t.Errorf("expected NACK without requeue, got %d requeues", rec.requeued.Load())
	}
}

// Test: a panic requeues the in-flight message and relaunches the worker.
//
// This is the regression test for the panic path having been disabled with
// `if false && inFlight != nil`, which left the message unacked forever — and
// because prefetch caps unacked deliveries, a single panic could stall the pod.
func TestPool_PanicRequeuesAndRelaunches(t *testing.T) {
	var panicOnce atomic.Bool
	exec := &mock.Executor{
		ExecuteFn: func(context.Context, *domain.ExecutionRequest) (*domain.ExecutionResult, error) {
			if panicOnce.CompareAndSwap(false, true) {
				panic("boom")
			}
			return &domain.ExecutionResult{Status: domain.StatusSuccess}, nil
		},
	}
	tp := newTestPool(t, 1, &mock.JobRepository{}, exec)

	var first, second settleRecorder

	tp.ch <- first.message()
	time.Sleep(200 * time.Millisecond)

	// A second job proves the single worker was relaunched and still processes.
	tp.ch <- second.message()
	time.Sleep(300 * time.Millisecond)

	if first.nacked.Load() != 1 {
		t.Errorf("expected the panicking job to be NACKed, got %d", first.nacked.Load())
	}
	if first.requeued.Load() != 1 {
		t.Errorf("expected the panicking job to be requeued, got %d", first.requeued.Load())
	}
	if second.acked.Load() != 1 {
		t.Errorf("expected the post-relaunch job to be ACKed, got %d", second.acked.Load())
	}
}

// Test: an in-flight job finishes during shutdown instead of being killed.
//
// This is the regression test for the shutdown bug: collapsing "stop accepting"
// and "abandon work" into one context SIGKILLed nsjail mid-execution, so every
// job running during a rolling deploy was lost.
func TestPool_ShutdownDrainsInFlightJob(t *testing.T) {
	var rec settleRecorder
	started := make(chan struct{})
	var completed atomic.Bool

	exec := &mock.Executor{
		ExecuteFn: func(ctx context.Context, _ *domain.ExecutionRequest) (*domain.ExecutionResult, error) {
			close(started)
			// Simulate a job still running when SIGTERM arrives. If the job context
			// were cancelled at "stop accepting" time, this would return early.
			select {
			case <-time.After(300 * time.Millisecond):
				completed.Store(true)
				return &domain.ExecutionResult{Status: domain.StatusSuccess}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}
	tp := newTestPool(t, 1, &mock.JobRepository{}, exec)

	tp.ch <- rec.message()
	<-started

	// SIGTERM equivalent: stop accepting, then drain with a generous deadline.
	tp.stopAccepting()
	if drained := tp.wp.Stop(5 * time.Second); !drained {
		t.Fatal("pool failed to drain within the deadline")
	}

	if !completed.Load() {
		t.Error("in-flight job was killed by shutdown instead of being allowed to finish")
	}
	if rec.acked.Load() != 1 {
		t.Errorf("expected the drained job to be ACKed, got %d", rec.acked.Load())
	}
}

// Test: Stop reports failure when a job outlasts the drain deadline, which is
// what tells main() to cancel the job context as a last resort.
func TestPool_StopReportsDrainTimeout(t *testing.T) {
	var rec settleRecorder
	started := make(chan struct{})

	exec := &mock.Executor{
		ExecuteFn: func(ctx context.Context, _ *domain.ExecutionRequest) (*domain.ExecutionResult, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	tp := newTestPool(t, 1, &mock.JobRepository{}, exec)

	tp.ch <- rec.message()
	<-started

	tp.stopAccepting()
	if drained := tp.wp.Stop(200 * time.Millisecond); drained {
		t.Error("expected Stop to report a drain timeout for a hung job")
	}

	// The escape hatch: cancelling the job context unblocks the straggler.
	tp.cancelJobs()
	if drained := tp.wp.Stop(2 * time.Second); !drained {
		t.Error("expected the pool to drain once the job context was cancelled")
	}
}

// Test: pool with no work shuts down promptly.
func TestPool_GracefulShutdown(t *testing.T) {
	tp := newTestPool(t, 4, &mock.JobRepository{}, &mock.Executor{})

	tp.stopAccepting()
	if drained := tp.wp.Stop(2 * time.Second); !drained {
		t.Error("idle pool failed to shut down")
	}
}
