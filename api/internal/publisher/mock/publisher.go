package mock

import (
	"context"

	"github.com/Harsh-BH/Sentinel/api/internal/domain"
	"github.com/Harsh-BH/Sentinel/api/internal/publisher"
)

// Ensure MockPublisher implements publisher.Publisher.
var _ publisher.Publisher = (*MockPublisher)(nil)

// MockPublisher is a mock message publisher for testing.
type MockPublisher struct {
	Published []*domain.Job
	PublishFn func(ctx context.Context, job *domain.Job) error
}

// NewMockPublisher creates a new mock publisher.
func NewMockPublisher() *MockPublisher {
	return &MockPublisher{}
}

func (m *MockPublisher) Publish(ctx context.Context, job *domain.Job) error {
	if m.PublishFn != nil {
		return m.PublishFn(ctx, job)
	}
	m.Published = append(m.Published, job)
	return nil
}

func (m *MockPublisher) Close() error {
	return nil
}
