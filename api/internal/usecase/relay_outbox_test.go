package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/api/internal/domain"
	mockpub "github.com/Harsh-BH/Sentinel/api/internal/publisher/mock"
	mockrepo "github.com/Harsh-BH/Sentinel/api/internal/repository/mock"
)

// The end-to-end property of the outbox: a submission that could not be
// published survives and is delivered by the relay on a later pass.
func TestRelay_DeliversWhatTheBrokerRejected(t *testing.T) {
	outbox := mockrepo.NewMockOutboxRepository()
	pub := mockpub.NewMockPublisher()

	// Broker is down at submit time.
	pub.PublishFn = func(context.Context, *domain.Job) error {
		return errors.New("connection refused")
	}
	submit := NewSubmitJobUsecase(outbox, pub, zap.NewNop())
	if _, err := submit.Execute(context.Background(), &domain.SubmitRequest{
		Language:   domain.LangPython,
		SourceCode: "print('deferred')",
	}); err != nil {
		t.Fatalf("submission must succeed even with the broker down: %v", err)
	}
	if outbox.PendingCount() != 1 {
		t.Fatalf("expected the message to be held in the outbox, got %d", outbox.PendingCount())
	}

	// Broker recovers.
	pub.PublishFn = nil
	stats, err := NewRelayOutboxUsecase(outbox, pub, zap.NewNop()).Pass(context.Background())
	if err != nil {
		t.Fatalf("relay pass failed: %v", err)
	}
	if stats.Published != 1 {
		t.Errorf("expected the relay to publish 1 message, got %+v", stats)
	}
	if outbox.PendingCount() != 0 {
		t.Errorf("expected the outbox to be drained, got %d pending", outbox.PendingCount())
	}
	if len(pub.Published) != 1 {
		t.Errorf("expected exactly 1 delivery, got %d", len(pub.Published))
	}
}

// A still-broken broker must leave the entry for the next pass, not drop it.
func TestRelay_KeepsEntryWhenPublishStillFails(t *testing.T) {
	outbox := mockrepo.NewMockOutboxRepository()
	pub := mockpub.NewMockPublisher()
	pub.PublishFn = func(context.Context, *domain.Job) error {
		return errors.New("still down")
	}

	submit := NewSubmitJobUsecase(outbox, pub, zap.NewNop())
	if _, err := submit.Execute(context.Background(), &domain.SubmitRequest{
		Language: domain.LangPython, SourceCode: "print(1)",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	stats, err := NewRelayOutboxUsecase(outbox, pub, zap.NewNop()).Pass(context.Background())
	if err != nil {
		t.Fatalf("a publish failure must not fail the pass: %v", err)
	}
	if stats.Failed != 1 || stats.Published != 0 {
		t.Errorf("expected failed=1 published=0, got %+v", stats)
	}
	if outbox.PendingCount() != 1 {
		t.Error("the entry must survive for the next pass")
	}
}

// A message that can never be routed must eventually stop being retried, and
// the job must be told, rather than sitting QUEUED forever.
func TestRelay_ExhaustedEntryFailsTheJob(t *testing.T) {
	outbox := mockrepo.NewMockOutboxRepository()
	pub := mockpub.NewMockPublisher()
	pub.PublishFn = func(context.Context, *domain.Job) error {
		return errors.New("unroutable")
	}

	submit := NewSubmitJobUsecase(outbox, pub, zap.NewNop())
	if _, err := submit.Execute(context.Background(), &domain.SubmitRequest{
		Language: domain.LangPython, SourceCode: "print(1)",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	job := outbox.Jobs()[0]
	outbox.SetAttempts(job.JobID, DefaultRelayMaxAttempts)

	stats, err := NewRelayOutboxUsecase(outbox, pub, zap.NewNop()).Pass(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats.Exhausted != 1 {
		t.Errorf("expected 1 exhausted entry, got %+v", stats)
	}
	if outbox.PendingCount() != 0 {
		t.Error("an exhausted entry must be removed")
	}
	if job.Status != domain.StatusInternalError {
		t.Errorf("the job must be failed, got %s", job.Status)
	}
}

// An empty outbox is a no-op, which is the steady state.
func TestRelay_EmptyOutbox(t *testing.T) {
	outbox := mockrepo.NewMockOutboxRepository()
	pub := mockpub.NewMockPublisher()

	stats, err := NewRelayOutboxUsecase(outbox, pub, zap.NewNop()).Pass(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats.Claimed != 0 || stats.Published != 0 {
		t.Errorf("expected a no-op pass, got %+v", stats)
	}
	if len(pub.Published) != 0 {
		t.Error("nothing should be published from an empty outbox")
	}
}

// The relay must ask for entries older than minAge only. Without that it races
// the inline publish that Submit performs immediately after commit, and delivers
// the same message twice.
func TestRelay_RespectsMinAge(t *testing.T) {
	outbox := mockrepo.NewMockOutboxRepository()
	pub := mockpub.NewMockPublisher()

	var gotMinAge time.Duration
	outbox.ClaimUnpublishedFunc = func(_ context.Context, minAge time.Duration, _ int) ([]*domain.Job, error) {
		gotMinAge = minAge
		return nil, nil
	}

	if _, err := NewRelayOutboxUsecase(outbox, pub, zap.NewNop()).Pass(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMinAge <= 0 {
		t.Errorf("relay must not claim brand-new entries; minAge was %s", gotMinAge)
	}
	if gotMinAge != DefaultRelayMinAge {
		t.Errorf("expected minAge %s, got %s", DefaultRelayMinAge, gotMinAge)
	}
}

// A claim error surfaces rather than being swallowed into a silent no-op.
func TestRelay_ClaimErrorSurfaces(t *testing.T) {
	outbox := mockrepo.NewMockOutboxRepository()
	outbox.ClaimUnpublishedFunc = func(context.Context, time.Duration, int) ([]*domain.Job, error) {
		return nil, errors.New("db down")
	}

	_, err := NewRelayOutboxUsecase(outbox, mockpub.NewMockPublisher(), zap.NewNop()).Pass(context.Background())
	if err == nil {
		t.Error("expected the claim error to be returned")
	}
}

// Publishing but failing to clear the entry must not lose the message. A
// duplicate delivery is acceptable (the worker's claim dedupes it); a lost one is
// not, so this must not be treated as a hard failure.
func TestRelay_PublishedButNotClearedIsSurvivable(t *testing.T) {
	outbox := mockrepo.NewMockOutboxRepository()
	pub := mockpub.NewMockPublisher()

	submit := NewSubmitJobUsecase(outbox, pub, zap.NewNop())
	// Break MarkPublished only after the submission has stored its entry.
	if _, err := submit.Execute(context.Background(), &domain.SubmitRequest{
		Language: domain.LangPython, SourceCode: "print(1)",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Re-add a pending entry to relay, then make clearing fail.
	job := outbox.Jobs()[0]
	outbox.SetAttempts(job.JobID, 0)
	outbox.MarkPublishedFunc = func(context.Context, uuid.UUID) error {
		return errors.New("delete failed")
	}

	stats, err := NewRelayOutboxUsecase(outbox, pub, zap.NewNop()).Pass(context.Background())
	if err != nil {
		t.Fatalf("a failed clear must not fail the pass: %v", err)
	}
	if stats.Claimed != 1 {
		t.Errorf("expected the entry to be claimed, got %+v", stats)
	}
	if stats.Published != 0 {
		t.Errorf("an unconfirmed clear must not count as published, got %+v", stats)
	}
}
