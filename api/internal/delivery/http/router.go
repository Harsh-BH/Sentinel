package http

import (
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/api/internal/delivery/http/middleware"
	"github.com/Harsh-BH/Sentinel/api/internal/usecase"
)

// NewRouter creates and configures the Gin router with all routes and middleware.
func NewRouter(
	submitUC *usecase.SubmitJobUsecase,
	getJobUC *usecase.GetJobUsecase,
	logger *zap.Logger,
	rateLimitPerMin int,
) *gin.Engine {
	router := gin.New()

	// Global middleware
	router.Use(gin.Recovery())
	router.Use(middleware.RequestID())
	router.Use(middleware.CORS())
	router.Use(middleware.Logger(logger))

	// Metrics endpoint (no rate limiting)
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// API v1 group
	v1 := router.Group("/api/v1")
	{
		// Health check (no rate limiting)
		healthHandler := NewHealthHandler(logger)
		v1.GET("/health", healthHandler.Health)

		// Languages
		langHandler := NewLanguageHandler()
		v1.GET("/languages", langHandler.List)

		// Submissions (with rate limiting)
		subHandler := NewSubmissionHandler(submitUC, getJobUC, logger)
		v1.POST("/submissions", subHandler.Submit)
		v1.GET("/submissions/:id", subHandler.GetByID)

		// WebSocket for real-time updates
		wsHandler := NewWebSocketHandler(getJobUC, logger)
		v1.GET("/submissions/:id/stream", wsHandler.Stream)
	}

	return router
}
