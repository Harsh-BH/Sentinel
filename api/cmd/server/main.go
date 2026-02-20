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
	// Initialize logger
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	logger.Info("Starting Sentinel API Server")

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		logger.Fatal("Failed to load configuration", zap.Error(err))
	}

	// Set Gin mode
	gin.SetMode(cfg.Server.GinMode)

	// Connect to PostgreSQL
	ctx := context.Background()
	dbPool, err := pgxpool.New(ctx, cfg.Database.URL)
	if err != nil {
		logger.Fatal("Failed to connect to PostgreSQL", zap.Error(err))
	}
	defer dbPool.Close()

	if err := dbPool.Ping(ctx); err != nil {
		logger.Fatal("Failed to ping PostgreSQL", zap.Error(err))
	}
	logger.Info("Connected to PostgreSQL")

	// Connect to Redis
	redisOpts, err := redis.ParseURL(cfg.Redis.URL)
	if err != nil {
		logger.Fatal("Failed to parse Redis URL", zap.Error(err))
	}
	rdb := redis.NewClient(redisOpts)
	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		logger.Fatal("Failed to ping Redis", zap.Error(err))
	}
	logger.Info("Connected to Redis")

	// Initialize RabbitMQ publisher
	pub, err := publisher.NewRabbitMQPublisher(cfg.RabbitMQ.URL, logger)
	if err != nil {
		logger.Fatal("Failed to initialize RabbitMQ publisher", zap.Error(err))
	}
	defer pub.Close()
	logger.Info("Connected to RabbitMQ")

	// Initialize repository
	jobRepo := postgres.NewPostgresJobRepository(dbPool)

	// Initialize use cases
	submitUC := usecase.NewSubmitJobUsecase(jobRepo, pub, logger)
	getJobUC := usecase.NewGetJobUsecase(jobRepo, logger)

	// Initialize router
	router := handler.NewRouter(&handler.RouterDeps{
		SubmitUC:        submitUC,
		GetJobUC:        getJobUC,
		Logger:          logger,
		RateLimitPerMin: cfg.Server.RateLimit,
		DBPool:          dbPool,
		AmqpURI:         cfg.RabbitMQ.URL,
		Redis:           rdb,
	})

	// Create HTTP server
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:      router,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	// Start server in a goroutine
	go func() {
		logger.Info("API server listening", zap.Int("port", cfg.Server.Port))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("Server failed", zap.Error(err))
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down API server...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Fatal("Server forced to shutdown", zap.Error(err))
	}

	logger.Info("API server stopped")
}
