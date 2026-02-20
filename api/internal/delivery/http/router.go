package http

import (
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/api/internal/delivery/http/middleware"
	"github.com/Harsh-BH/Sentinel/api/internal/usecase"
)

// RouterDeps holds all dependencies needed to construct the router.
type RouterDeps struct {
	SubmitUC        *usecase.SubmitJobUsecase
	GetJobUC        *usecase.GetJobUsecase
	Logger          *zap.Logger
	RateLimitPerMin int
	DBPool          *pgxpool.Pool
	AmqpURI         string
	Redis           *redis.Client
}

// NewRouter creates and configures the Gin router with all routes and middleware.
func NewRouter(deps *RouterDeps) *gin.Engine {
	router := gin.New()

	// Global middleware
	router.Use(gin.Recovery())
	router.Use(middleware.RequestID())
	router.Use(middleware.CORS())
	router.Use(middleware.Logger(deps.Logger))
	router.Use(middleware.BodySizeLimit(1 << 20)) // 1 MB max request body

	// Metrics endpoint (no rate limiting)
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// API v1 group
	v1 := router.Group("/api/v1")
	{
		// Health check (no rate limiting)
		healthHandler := NewHealthHandler(deps.Logger, deps.DBPool, deps.AmqpURI, deps.Redis)
		v1.GET("/health", healthHandler.Health)

		// Languages
		langHandler := NewLanguageHandler()
		v1.GET("/languages", langHandler.List)

		// Apply rate limiter to submission endpoints
		rateLimited := v1.Group("")
		rateLimited.Use(middleware.RateLimiter(deps.Redis, deps.RateLimitPerMin))
		{
			// Submissions
			subHandler := NewSubmissionHandler(deps.SubmitUC, deps.GetJobUC, deps.Logger)
			rateLimited.POST("/submissions", subHandler.Submit)
			rateLimited.GET("/submissions/:id", subHandler.GetByID)
		}

		// WebSocket for real-time updates (no rate limiting — one connection per job)
		wsHandler := NewWebSocketHandler(deps.GetJobUC, deps.Logger)
		v1.GET("/submissions/:id/stream", wsHandler.Stream)
	}

	return router
}
