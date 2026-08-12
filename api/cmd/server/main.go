package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/api/internal/config"
	handler "github.com/Harsh-BH/Sentinel/api/internal/delivery/http"
	"github.com/Harsh-BH/Sentinel/api/internal/publisher"
	"github.com/Harsh-BH/Sentinel/api/internal/repository/postgres"
	"github.com/Harsh-BH/Sentinel/api/internal/usecase"
)

func main() {
	logger, _ := zap.NewProduction()
	defer func() { _ = logger.Sync() }()

	logger.Info("Starting Sentinel API Server")

	cfg, err := config.Load()
	if err != nil {
		logger.Fatal("Failed to load configuration", zap.Error(err))
	}

	gin.SetMode(cfg.Server.GinMode)

	// Bounded startup context so a dead dependency fails fast and visibly instead
	// of hanging the process with no explanation.
	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelStartup()

	dbPool, err := pgxpool.New(startupCtx, cfg.Database.URL)
	if err != nil {
		logger.Fatal("Failed to connect to PostgreSQL", zap.Error(err))
	}
	defer dbPool.Close()

	if err := dbPool.Ping(startupCtx); err != nil {
		logger.Fatal("Failed to ping PostgreSQL", zap.Error(err))
	}
	logger.Info("Connected to PostgreSQL")

	redisOpts, err := redis.ParseURL(cfg.Redis.URL)
	if err != nil {
		logger.Fatal("Failed to parse Redis URL", zap.Error(err))
	}
	rdb := redis.NewClient(redisOpts)
	defer rdb.Close()

	if err := rdb.Ping(startupCtx).Err(); err != nil {
		logger.Fatal("Failed to ping Redis", zap.Error(err))
	}
	logger.Info("Connected to Redis")

	pub, err := publisher.NewRabbitMQPublisher(cfg.RabbitMQ.URL, logger)
	if err != nil {
		logger.Fatal("Failed to initialize RabbitMQ publisher", zap.Error(err))
	}
	defer pub.Close()
	logger.Info("Connected to RabbitMQ")

	jobRepo := postgres.NewPostgresJobRepository(dbPool)
	outboxRepo := postgres.NewPostgresOutboxRepository(dbPool)

	submitUC := usecase.NewSubmitJobUsecase(outboxRepo, pub, logger)
	getJobUC := usecase.NewGetJobUsecase(jobRepo, logger)
	reaperUC := usecase.NewReapJobsUsecase(jobRepo, pub, logger)
	relayUC := usecase.NewRelayOutboxUsecase(outboxRepo, pub, logger)

	// The reaper is the backstop for the non-atomic dual write (Postgres INSERT
	// then RabbitMQ publish) and for workers that die mid-execution. It is safe to
	// run on every replica: candidate selection uses FOR UPDATE SKIP LOCKED.
	bgCtx, stopBackground := context.WithCancel(context.Background())
	defer stopBackground()
	if cfg.Server.ReapInterval > 0 {
		go reaperUC.Run(bgCtx, cfg.Server.ReapInterval)
	} else {
		logger.Warn("Reaper disabled (API_REAP_INTERVAL=0) — stranded jobs will not be recovered")
	}

	// The outbox relay is the delivery path for anything the inline publish could
	// not send. Without it a broker outage would leave committed jobs undelivered,
	// so this is not optional in the way the reaper arguably is.
	go relayUC.Run(bgCtx, usecase.DefaultRelayInterval)

	// One LISTEN connection per pod, fanned out in memory to WebSocket
	// subscribers. Replaces per-connection polling of the jobs table.
	notifier := handler.NewStatusNotifier(dbPool, logger)
	go notifier.Run(bgCtx)

	router := handler.NewRouter(&handler.RouterDeps{
		SubmitUC:        submitUC,
		GetJobUC:        getJobUC,
		Logger:          logger,
		RateLimitPerMin: cfg.Server.RateLimit,
		DBPool:          dbPool,
		Publisher:       pub,
		Redis:           rdb,
		Notifier:        notifier,
		AllowedOrigins:  cfg.Server.AllowedOrigins,
		TrustedProxies:  cfg.Server.TrustedProxies,
	})

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:      router,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	go func() {
		logger.Info("API server listening",
			zap.Int("port", cfg.Server.Port),
			zap.String("gin_mode", cfg.Server.GinMode),
			zap.Strings("allowed_origins", cfg.Server.AllowedOrigins),
		)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("Server failed", zap.Error(err))
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down API server...")
	stopBackground()

	// The write timeout is 30s and WebSocket streams can be long-lived, so give
	// Shutdown enough room to let in-flight requests finish rather than cutting
	// responses off mid-flight.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("Server forced to shutdown", zap.Error(err))
	}

	logger.Info("API server stopped")
}
