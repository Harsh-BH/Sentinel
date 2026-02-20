package amqp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"

	amqplib "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/worker/internal/domain"
)

const (
	queueName = "execution_tasks"

	// Reconnection parameters
	maxReconnectDelay  = 30 * time.Second
	baseReconnectDelay = 1 * time.Second
)

// Consumer listens to RabbitMQ and dispatches JobMessage (with ACK callbacks) to a channel.
type Consumer struct {
	url     string
	conn    *amqplib.Connection
	channel *amqplib.Channel
	logger  *zap.Logger
	jobs    chan<- *domain.JobMessage

	mu      sync.Mutex
	closed  bool
	closeCh chan struct{}
}

// NewConsumer creates a new RabbitMQ consumer.
// Unlike Phase 0, the consumer does NOT auto-ACK after dispatch.
// Instead, it wraps each delivery in a JobMessage with Ack/Nack callbacks
// that the worker pool calls after execution completes.
func NewConsumer(url string, jobs chan<- *domain.JobMessage, logger *zap.Logger) (*Consumer, error) {
	c := &Consumer{
		url:     url,
		logger:  logger,
		jobs:    jobs,
		closeCh: make(chan struct{}),
	}

	if err := c.connect(); err != nil {
		return nil, err
	}

	return c, nil
}

// connect establishes the AMQP connection and channel with prefetch=1.
func (c *Consumer) connect() error {
	conn, err := amqplib.Dial(c.url)
	if err != nil {
		return fmt.Errorf("amqp dial: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return fmt.Errorf("amqp channel: %w", err)
	}

	// Set prefetch to 1: only deliver one unacknowledged message per consumer.
	if err := ch.Qos(1, 0, false); err != nil {
		ch.Close()
		conn.Close()
		return fmt.Errorf("amqp qos: %w", err)
	}

	// Declare the queue (idempotent) — ensures it exists with quorum type.
	_, err = ch.QueueDeclare(
		queueName,
		true,  // durable
		false, // auto-delete
		false, // exclusive
		false, // no-wait
		amqplib.Table{
			"x-queue-type":              "quorum",
			"x-dead-letter-exchange":    "dlx.execution_tasks",
			"x-dead-letter-routing-key": "execution_tasks.dlq",
		},
	)
	if err != nil {
		ch.Close()
		conn.Close()
		return fmt.Errorf("amqp queue declare: %w", err)
	}

	c.mu.Lock()
	c.conn = conn
	c.channel = ch
	c.mu.Unlock()

	return nil
}

// Start begins consuming messages. It blocks until the context is cancelled.
// On connection loss it automatically reconnects with exponential backoff.
func (c *Consumer) Start(ctx context.Context) error {
	for {
		err := c.consume(ctx)
		if err == nil {
			// Context was cancelled — clean shutdown.
			return nil
		}

		// Check if we were explicitly closed.
		select {
		case <-c.closeCh:
			return nil
		case <-ctx.Done():
			return nil
		default:
		}

		c.logger.Warn("AMQP consumer lost connection, reconnecting...", zap.Error(err))

		// Exponential backoff reconnection loop.
		for attempt := 0; ; attempt++ {
			select {
			case <-c.closeCh:
				return nil
			case <-ctx.Done():
				return nil
			default:
			}

			delay := time.Duration(math.Min(
				float64(baseReconnectDelay)*math.Pow(2, float64(attempt)),
				float64(maxReconnectDelay),
			))
			c.logger.Info("Reconnect attempt",
				zap.Int("attempt", attempt+1),
				zap.Duration("delay", delay),
			)
			time.Sleep(delay)

			if err := c.connect(); err != nil {
				c.logger.Error("Reconnect failed", zap.Error(err))
				continue
			}

			c.logger.Info("Reconnected to RabbitMQ")
			break
		}
	}
}

// consume runs one consume session until the delivery channel closes or ctx is cancelled.
func (c *Consumer) consume(ctx context.Context) error {
	c.mu.Lock()
	ch := c.channel
	c.mu.Unlock()

	if ch == nil {
		return fmt.Errorf("channel is nil")
	}

	deliveries, err := ch.Consume(
		queueName,
		"",    // auto-generated consumer tag
		false, // auto-ack disabled (manual ack)
		false, // exclusive
		false, // no-local
		false, // no-wait
		nil,
	)
	if err != nil {
		return fmt.Errorf("amqp consume: %w", err)
	}

	c.logger.Info("AMQP consumer started", zap.String("queue", queueName))

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("AMQP consumer stopping (context cancelled)")
			return nil
		case delivery, ok := <-deliveries:
			if !ok {
				return fmt.Errorf("delivery channel closed")
			}

			var job domain.Job
			if err := json.Unmarshal(delivery.Body, &job); err != nil {
				c.logger.Error("Failed to unmarshal job",
					zap.Error(err),
					zap.String("body", string(delivery.Body)),
				)
				delivery.Nack(false, false) // reject → DLQ
				continue
			}

			c.logger.Debug("Received job from queue",
				zap.String("job_id", job.JobID.String()),
				zap.String("language", string(job.Language)),
			)

			// Create a local copy of the delivery tag so the closures are safe.
			tag := delivery.DeliveryTag
			localCh := ch

			msg := &domain.JobMessage{
				Job: &job,
				Ack: func() error {
					return localCh.Ack(tag, false)
				},
				Nack: func(requeue bool) error {
					return localCh.Nack(tag, false, requeue)
				},
			}

			// Dispatch to worker pool. This blocks if the channel is full,
			// which is desirable: back-pressure via prefetch=1.
			select {
			case c.jobs <- msg:
			case <-ctx.Done():
				// Shutting down — nack so the message is requeued.
				delivery.Nack(false, true)
				return nil
			}
		}
	}
}

// Close gracefully shuts down the consumer.
func (c *Consumer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true
	close(c.closeCh)

	var firstErr error
	if c.channel != nil {
		if err := c.channel.Close(); err != nil {
			firstErr = err
		}
	}
	if c.conn != nil {
		if err := c.conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
