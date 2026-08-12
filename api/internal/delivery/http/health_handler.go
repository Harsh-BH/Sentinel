package http

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/api/internal/publisher"
)

// HealthHandler serves liveness and readiness checks.
//
// These are deliberately different endpoints with different semantics:
//
//	/livez  — is this process functioning? No dependency checks. Used for the
//	          Kubernetes livenessProbe, whose only action is to restart the pod.
//	/readyz — can this process serve traffic right now? Checks dependencies.
//	          Used for the readinessProbe, whose only action is to pull the pod
//	          out of the Service's endpoint list.
//
// Both probes previously pointed at a single dependency-checking endpoint, so a
// RabbitMQ blip failed liveness on every replica simultaneously and the kubelet
// restarted the entire API tier — converting a partial degradation into a full
// outage. Restarting a process never fixes someone else's broker.
type HealthHandler struct {
	logger *zap.Logger
	dbPool *pgxpool.Pool
	pub    publisher.Publisher
	rdb    *redis.Client
}

// NewHealthHandler creates a new HealthHandler.
func NewHealthHandler(logger *zap.Logger, dbPool *pgxpool.Pool, pub publisher.Publisher, rdb *redis.Client) *HealthHandler {
	return &HealthHandler{
		logger: logger,
		dbPool: dbPool,
		pub:    pub,
		rdb:    rdb,
	}
}

// Live handles GET /api/v1/livez — process liveness only.
func (h *HealthHandler) Live(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Ready handles GET /api/v1/readyz — dependency readiness.
func (h *HealthHandler) Ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	services := gin.H{}
	ready := true

	// Postgres is required to accept a submission at all.
	if err := h.dbPool.Ping(ctx); err != nil {
		services["postgres"] = "error: " + err.Error()
		ready = false
		h.logger.Warn("PostgreSQL readiness check failed", zap.Error(err))
	} else {
		services["postgres"] = "ok"
	}

	// Ask the existing publisher connection instead of dialling a new one.
	//
	// This used to call amqp.Dial on every request: a fresh TCP connection plus a
	// full AMQP handshake per probe, on every replica, forever — and it ignored
	// the request context, so a hung broker blocked the handler well past the
	// probe timeout.
	if h.pub.Healthy() {
		services["rabbitmq"] = "ok"
	} else {
		services["rabbitmq"] = "error: publisher connection is down"
		ready = false
	}

	// Redis backs rate limiting only, and the limiter fails open — so its being
	// down degrades protection but does not stop the API doing its job. Reported,
	// not fatal.
	if err := h.rdb.Ping(ctx).Err(); err != nil {
		services["redis"] = "degraded: " + err.Error()
		h.logger.Warn("Redis readiness check failed", zap.Error(err))
	} else {
		services["redis"] = "ok"
	}

	status := "ok"
	code := http.StatusOK
	if !ready {
		status = "unavailable"
		code = http.StatusServiceUnavailable
	}

	c.JSON(code, gin.H{"status": status, "services": services})
}

// Health handles GET /api/v1/health, kept as an alias of the readiness check for
// existing clients and the compose healthcheck.
func (h *HealthHandler) Health(c *gin.Context) {
	h.Ready(c)
}
