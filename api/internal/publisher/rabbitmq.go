package publisher

import (
	"context"
	"encoding/json"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/api/internal/domain"
)

const (
	exchangeName = "sentinel.direct"
	exchangeType = "direct"
	routingKey   = "execute"
)

// Publisher defines the interface for publishing jobs to the message broker.
type Publisher interface {
	Publish(ctx context.Context, job *domain.Job) error
	Close() error
}

type rabbitPublisher struct {
	conn    *amqp.Connection
	channel *amqp.Channel
	logger  *zap.Logger
}

// NewRabbitMQPublisher creates a new RabbitMQ publisher with exchange and queue setup.
func NewRabbitMQPublisher(url string, logger *zap.Logger) (Publisher, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("rabbitmq: dial: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("rabbitmq: channel: %w", err)
	}

	// Declare the direct exchange
	if err := ch.ExchangeDeclare(exchangeName, exchangeType, true, false, false, false, nil); err != nil {
		ch.Close()
		conn.Close()
		return nil, fmt.Errorf("rabbitmq: declare exchange: %w", err)
	}

	// Declare dead letter exchange
	if err := ch.ExchangeDeclare("sentinel.dlx", "direct", true, false, false, false, nil); err != nil {
		ch.Close()
		conn.Close()
		return nil, fmt.Errorf("rabbitmq: declare DLX: %w", err)
	}

	// Declare dead letter queue
	if _, err := ch.QueueDeclare("dead_letter_queue", true, false, false, false, nil); err != nil {
		ch.Close()
		conn.Close()
		return nil, fmt.Errorf("rabbitmq: declare DLQ: %w", err)
	}
	if err := ch.QueueBind("dead_letter_queue", "", "sentinel.dlx", false, nil); err != nil {
		ch.Close()
		conn.Close()
		return nil, fmt.Errorf("rabbitmq: bind DLQ: %w", err)
	}

	// Declare main execution queue with DLX
	args := amqp.Table{
		"x-dead-letter-exchange": "sentinel.dlx",
		"x-queue-type":           "quorum",
	}
	if _, err := ch.QueueDeclare("execution_tasks", true, false, false, false, args); err != nil {
		ch.Close()
		conn.Close()
		return nil, fmt.Errorf("rabbitmq: declare queue: %w", err)
	}
	if err := ch.QueueBind("execution_tasks", routingKey, exchangeName, false, nil); err != nil {
		ch.Close()
		conn.Close()
		return nil, fmt.Errorf("rabbitmq: bind queue: %w", err)
	}

	logger.Info("RabbitMQ publisher initialized",
		zap.String("exchange", exchangeName),
		zap.String("queue", "execution_tasks"),
	)

	return &rabbitPublisher{conn: conn, channel: ch, logger: logger}, nil
}

func (p *rabbitPublisher) Publish(ctx context.Context, job *domain.Job) error {
	body, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("rabbitmq: marshal job: %w", err)
	}

	err = p.channel.PublishWithContext(ctx,
		exchangeName,
		routingKey,
		false, // mandatory
		false, // immediate
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Body:         body,
		},
	)
	if err != nil {
		return fmt.Errorf("rabbitmq: publish: %w", err)
	}

	p.logger.Debug("Published job to RabbitMQ", zap.String("job_id", job.JobID.String()))
	return nil
}

func (p *rabbitPublisher) Close() error {
	if p.channel != nil {
		p.channel.Close()
	}
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}
