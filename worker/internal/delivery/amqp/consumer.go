package amqp

import (
	"context"
	"encoding/json"
	"fmt"

	amqplib "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/worker/internal/domain"
)

const queueName = "execution_tasks"

// Consumer listens to RabbitMQ and dispatches jobs to a channel.
type Consumer struct {
	conn    *amqplib.Connection
	channel *amqplib.Channel
	logger  *zap.Logger
	jobs    chan<- *domain.Job
}

// NewConsumer creates a new RabbitMQ consumer with prefetch=1.
func NewConsumer(url string, jobs chan<- *domain.Job, logger *zap.Logger) (*Consumer, error) {
	conn, err := amqplib.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("amqp dial: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("amqp channel: %w", err)
	}

	// Set prefetch to 1: only deliver one unacknowledged message per consumer at a time.
	if err := ch.Qos(1, 0, false); err != nil {
		ch.Close()
		conn.Close()
		return nil, fmt.Errorf("amqp qos: %w", err)
	}

	return &Consumer{
		conn:    conn,
		channel: ch,
		logger:  logger,
		jobs:    jobs,
	}, nil
}

// Start begins consuming messages. It blocks until the context is cancelled.
func (c *Consumer) Start(ctx context.Context) error {
	deliveries, err := c.channel.Consume(
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
			c.logger.Info("AMQP consumer stopping")
			return nil
		case delivery, ok := <-deliveries:
			if !ok {
				c.logger.Warn("AMQP delivery channel closed")
				return fmt.Errorf("delivery channel closed")
			}

			var job domain.Job
			if err := json.Unmarshal(delivery.Body, &job); err != nil {
				c.logger.Error("Failed to unmarshal job", zap.Error(err))
				delivery.Nack(false, false) // reject, don't requeue → goes to DLQ
				continue
			}

			c.logger.Debug("Received job from queue",
				zap.String("job_id", job.JobID.String()),
				zap.String("language", string(job.Language)),
			)

			// Dispatch to worker pool via buffered channel
			c.jobs <- &job

			// Acknowledge the message after dispatching
			// NOTE: In production, ack should happen AFTER execution completes.
			// This is simplified for Phase 0 scaffolding.
			delivery.Ack(false)
		}
	}
}

// Close gracefully shuts down the consumer.
func (c *Consumer) Close() error {
	if c.channel != nil {
		c.channel.Close()
	}
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
