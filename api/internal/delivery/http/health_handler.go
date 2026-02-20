package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// HealthHandler handles health check requests.
type HealthHandler struct {
	logger *zap.Logger
	// TODO: Add dependency health checkers (db pool, rabbitmq conn, redis client)
}

// NewHealthHandler creates a new HealthHandler.
func NewHealthHandler(logger *zap.Logger) *HealthHandler {
	return &HealthHandler{logger: logger}
}

// Health handles GET /api/v1/health
func (h *HealthHandler) Health(c *gin.Context) {
	// TODO: Check PostgreSQL, RabbitMQ, Redis connectivity
	// For now, return a basic health response
	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
		"services": gin.H{
			"postgres": "ok",
			"rabbitmq": "ok",
			"redis":    "ok",
		},
	})
}
