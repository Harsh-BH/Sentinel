package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/api/internal/domain"
)

const (
	exchangeName = "sentinel.direct"
	exchangeType = "direct"
	routingKey   = "execute"

	// Reconnection settings
	reconnectDelay    = 2 * time.Second
	maxReconnectDelay = 30 * time.Second

	// Publish timeout
	publishTimeout = 5 * time.Second
)

// Publisher defines the interface for publishing jobs to the message broker.
type Publisher interface {
	Publish(ctx context.Context, job *domain.Job) error
	Close() error
}

type rabbitPublisher struct {
	url     string
	conn    *amqp.Connection
	channel *amqp.Channel
	logger  *zap.Logger
	mu      sync.RWMutex
	closed  bool
}

// NewRabbitMQPublisher creates a new RabbitMQ publisher with exchange and queue setup.
func NewRabbitMQPublisher(url string, logger *zap.Logger) (Publisher, error) {
	p := &rabbitPublisher{
		url:    url,
		logger: logger,
	}

	if err := p.connect(); err != nil {
		return nil, err
	}

	// Watch for connection closures and reconnect
	go p.watchConnection()

	return p, nil
}

func (p *rabbitPublisher) connect() error {
	conn, err := amqp.Dial(p.url)
	if err != nil {
		return fmt.Errorf("rabbitmq: dial: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return fmt.Errorf("rabbitmq: channel: %w", err)
	}

	// Enable publisher confirms
	if err := ch.Confirm(false); err != nil {
		ch.Close()
		conn.Close()
		return fmt.Errorf("rabbitmq: enable confirms: %w", err)
	}

	// Declare the direct exchange
	if err := ch.ExchangeDeclare(exchangeName, exchangeType, true, false, false, false, nil); err != nil {
		ch.Close()
		conn.Close()
		return fmt.Errorf("rabbitmq: declare exchange: %w", err)
	}

	// Declare dead letter exchange
	if err := ch.ExchangeDeclare("sentinel.dlx", "direct", true, false, false, false, nil); err != nil {
		ch.Close()
		conn.Close()
		return fmt.Errorf("rabbitmq: declare DLX: %w", err)
	}

	// Declare dead letter queue
	if _, err := ch.QueueDeclare("dead_letter_queue", true, false, false, false, nil); err != nil {
		ch.Close()
		conn.Close()
		return fmt.Errorf("rabbitmq: declare DLQ: %w", err)
	}
	if err := ch.QueueBind("dead_letter_queue", "", "sentinel.dlx", false, nil); err != nil {
		ch.Close()
		conn.Close()
		return fmt.Errorf("rabbitmq: bind DLQ: %w", err)
	}

	// Declare main execution queue with DLX
	args := amqp.Table{
		"x-dead-letter-exchange": "sentinel.dlx",
		"x-queue-type":           "quorum",
	}
	if _, err := ch.QueueDeclare("execution_tasks", true, false, false, false, args); err != nil {
		ch.Close()
		conn.Close()
		return fmt.Errorf("rabbitmq: declare queue: %w", err)
	}
	if err := ch.QueueBind("execution_tasks", routingKey, exchangeName, false, nil); err != nil {
		ch.Close()
		conn.Close()
		return fmt.Errorf("rabbitmq: bind queue: %w", err)
	}

	p.mu.Lock()
	p.conn = conn
	p.channel = ch
	p.mu.Unlock()

	p.logger.Info("RabbitMQ publisher initialized",
		zap.String("exchange", exchangeName),
		zap.String("queue", "execution_tasks"),
	)

	return nil
}

// watchConnection monitors the connection and reconnects on failure.
func (p *rabbitPublisher) watchConnection() {
	for {
		p.mu.RLock()
		if p.closed {
			p.mu.RUnlock()
			return
		}
		conn := p.conn
		p.mu.RUnlock()

		if conn == nil {
			time.Sleep(reconnectDelay)
			continue
		}

		// Block until the connection closes
		reason, ok := <-conn.NotifyClose(make(chan *amqp.Error))
		if !ok {
			// Channel closed normally
			return
		}

		p.logger.Warn("RabbitMQ connection lost, reconnecting...",
			zap.String("reason", reason.Error()),
		)

		delay := reconnectDelay
		for {
			p.mu.RLock()
			if p.closed {
				p.mu.RUnlock()
				return
			}
			p.mu.RUnlock()

			time.Sleep(delay)

			if err := p.connect(); err != nil {
				p.logger.Warn("RabbitMQ reconnect failed", zap.Error(err), zap.Duration("retry_in", delay))
				delay = delay * 2
				if delay > maxReconnectDelay {
					delay = maxReconnectDelay
				}
				continue
			}

			p.logger.Info("RabbitMQ reconnected successfully")
			break
		}
	}
}

func (p *rabbitPublisher) Publish(ctx context.Context, job *domain.Job) error {
	body, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("rabbitmq: marshal job: %w", err)
	}

	p.mu.RLock()
	ch := p.channel
	p.mu.RUnlock()

	if ch == nil {
		return fmt.Errorf("rabbitmq: channel not available (reconnecting)")
	}

	// Get confirmation channel
	confirm := ch.NotifyPublish(make(chan amqp.Confirmation, 1))

	publishCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()

	err = ch.PublishWithContext(publishCtx,
		exchangeName,
		routingKey,
		false, // mandatory
		false, // immediate
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			MessageId:    job.JobID.String(),
			Timestamp:    time.Now(),
			Body:         body,
		},
	)
	if err != nil {
		return fmt.Errorf("rabbitmq: publish: %w", err)
	}

	// Wait for broker confirmation
	select {
	case ack := <-confirm:
		if !ack.Ack {
			return fmt.Errorf("rabbitmq: broker nacked message (job_id=%s)", job.JobID)
		}
	case <-publishCtx.Done():
		return fmt.Errorf("rabbitmq: publish confirmation timeout (job_id=%s)", job.JobID)
	}

	p.logger.Debug("Published job to RabbitMQ",
		zap.String("job_id", job.JobID.String()),
		zap.Int("body_size", len(body)),
	)
	return nil
}

func (p *rabbitPublisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.closed = true

	if p.channel != nil {
		p.channel.Close()
	}
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}
