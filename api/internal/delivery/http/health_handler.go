package http

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// HealthHandler handles health check requests.
type HealthHandler struct {
	logger  *zap.Logger
	dbPool  *pgxpool.Pool
	amqpURI string
	rdb     *redis.Client
}

// NewHealthHandler creates a new HealthHandler.
func NewHealthHandler(logger *zap.Logger, dbPool *pgxpool.Pool, amqpURI string, rdb *redis.Client) *HealthHandler {
	return &HealthHandler{
		logger:  logger,
		dbPool:  dbPool,
		amqpURI: amqpURI,
		rdb:     rdb,
	}
}

// Health handles GET /api/v1/health
func (h *HealthHandler) Health(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	pgStatus := "ok"
	if err := h.dbPool.Ping(ctx); err != nil {
		pgStatus = "error: " + err.Error()
		h.logger.Warn("PostgreSQL health check failed", zap.Error(err))
	}

	rabbitStatus := "ok"
	conn, err := amqp.Dial(h.amqpURI)
	if err != nil {
		rabbitStatus = "error: " + err.Error()
		h.logger.Warn("RabbitMQ health check failed", zap.Error(err))
	} else {
		conn.Close()
	}

	redisStatus := "ok"
	if err := h.rdb.Ping(ctx).Err(); err != nil {
		redisStatus = "error: " + err.Error()
		h.logger.Warn("Redis health check failed", zap.Error(err))
	}

	overallStatus := "ok"
	statusCode := http.StatusOK
	if pgStatus != "ok" || rabbitStatus != "ok" || redisStatus != "ok" {
		overallStatus = "degraded"
		statusCode = http.StatusServiceUnavailable
	}

	c.JSON(statusCode, gin.H{
		"status": overallStatus,
		"services": gin.H{
			"postgres": pgStatus,
			"rabbitmq": rabbitStatus,
			"redis":    redisStatus,
		},
	})
}
