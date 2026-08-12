package http

import (
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/api/internal/delivery/http/middleware"
	"github.com/Harsh-BH/Sentinel/api/internal/publisher"
	"github.com/Harsh-BH/Sentinel/api/internal/usecase"
)

// RouterDeps holds all dependencies needed to construct the router.
type RouterDeps struct {
	SubmitUC        *usecase.SubmitJobUsecase
	GetJobUC        *usecase.GetJobUsecase
	Logger          *zap.Logger
	RateLimitPerMin int
	DBPool          *pgxpool.Pool
	Publisher       publisher.Publisher
	Redis           *redis.Client
	Notifier        *StatusNotifier
	AllowedOrigins  []string
	TrustedProxies  []string
}

// NewRouter creates and configures the Gin router with all routes and middleware.
func NewRouter(deps *RouterDeps) *gin.Engine {
	router := gin.New()

	// Declare which peers may set X-Forwarded-For.
	//
	// Gin trusts all proxies by default, which makes c.ClientIP() attacker
	// controlled: send your own X-Forwarded-For and the per-IP rate limiter hands
	// you a brand-new bucket on every request. Restricting the trust list is what
	// makes the limiter's key meaningful.
	//
	// An empty list means "trust nobody" — ClientIP() then always reports the
	// direct peer, which is the correct behaviour when the API is exposed
	// directly rather than through a proxy.
	if err := router.SetTrustedProxies(deps.TrustedProxies); err != nil {
		deps.Logger.Fatal("Invalid API_TRUSTED_PROXIES", zap.Error(err))
	}

	// Global middleware
	router.Use(gin.Recovery())
	router.Use(middleware.RequestID())
	router.Use(middleware.CORS(deps.AllowedOrigins))
	router.Use(middleware.Logger(deps.Logger))
	router.Use(middleware.BodySizeLimit(1 << 20)) // 1 MB max request body

	// Metrics endpoint (no rate limiting)
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// API v1 group
	v1 := router.Group("/api/v1")
	{
		// Health probes (no rate limiting). See health_handler.go for why
		// liveness and readiness are separate endpoints.
		healthHandler := NewHealthHandler(deps.Logger, deps.DBPool, deps.Publisher, deps.Redis)
		v1.GET("/livez", healthHandler.Live)
		v1.GET("/readyz", healthHandler.Ready)
		v1.GET("/health", healthHandler.Health)

		// Languages
		langHandler := NewLanguageHandler()
		v1.GET("/languages", langHandler.List)

		// Apply rate limiter to submission endpoints
		rateLimited := v1.Group("")
		rateLimited.Use(middleware.RateLimiter(deps.Redis, deps.RateLimitPerMin))
		{
			subHandler := NewSubmissionHandler(deps.SubmitUC, deps.GetJobUC, deps.Logger)
			rateLimited.POST("/submissions", subHandler.Submit)
			rateLimited.GET("/submissions/:id", subHandler.GetByID)
		}

		// WebSocket for real-time updates (no rate limiting — one connection per job)
		wsHandler := NewWebSocketHandler(deps.GetJobUC, deps.Notifier, deps.Logger, deps.AllowedOrigins)
		v1.GET("/submissions/:id/stream", wsHandler.Stream)
	}

	return router
}
