package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/worker/internal/config"
	amqpdelivery "github.com/Harsh-BH/Sentinel/worker/internal/delivery/amqp"
	"github.com/Harsh-BH/Sentinel/worker/internal/domain"
	"github.com/Harsh-BH/Sentinel/worker/internal/executor"
	"github.com/Harsh-BH/Sentinel/worker/internal/pool"
	"github.com/Harsh-BH/Sentinel/worker/internal/repository/postgres"
	redisrepo "github.com/Harsh-BH/Sentinel/worker/internal/repository/redis"
	"github.com/Harsh-BH/Sentinel/worker/internal/usecase"
)

func main() {
	// Initialize logger
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	logger.Info("Starting Sentinel Execution Worker")

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		logger.Fatal("Failed to load configuration", zap.Error(err))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Connect to PostgreSQL
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
	redisOpts, err := goredis.ParseURL(cfg.Redis.URL)
	if err != nil {
		logger.Fatal("Invalid Redis URL", zap.Error(err))
	}
	redisClient := goredis.NewClient(redisOpts)
	if err := redisClient.Ping(ctx).Err(); err != nil {
		logger.Fatal("Failed to connect to Redis", zap.Error(err))
	}
	defer redisClient.Close()
	logger.Info("Connected to Redis")

	// Initialize repositories
	jobRepo := postgres.NewPostgresJobRepository(dbPool)
	idempotencyStore := redisrepo.NewRedisIdempotencyStore(redisClient)

	// Initialize sandbox executor
	sandboxExec := executor.NewSandboxExecutor(cfg.Sandbox.NsjailPath, cfg.Sandbox.ConfigDir, logger)

	// Initialize use case
	executeUC := usecase.NewExecuteJobUsecase(jobRepo, idempotencyStore, sandboxExec, logger)

	// Create buffered job channel
	jobsChan := make(chan *domain.Job, cfg.Worker.PoolSize*2)

	// Initialize AMQP consumer
	consumer, err := amqpdelivery.NewConsumer(cfg.RabbitMQ.URL, jobsChan, logger)
	if err != nil {
		logger.Fatal("Failed to initialize AMQP consumer", zap.Error(err))
	}
	defer consumer.Close()
	logger.Info("Connected to RabbitMQ")

	// Start worker pool
	workerPool := pool.NewWorkerPool(cfg.Worker.PoolSize, jobsChan, executeUC, logger)
	workerPool.Start(ctx)

	// Start AMQP consumer in a goroutine
	go func() {
		if err := consumer.Start(ctx); err != nil {
			logger.Error("AMQP consumer error", zap.Error(err))
			cancel()
		}
	}()

	// Start Prometheus metrics server
	go func() {
		metricsAddr := fmt.Sprintf(":%d", cfg.Worker.MetricsPort)
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		logger.Info("Metrics server listening", zap.String("addr", metricsAddr))
		if err := http.ListenAndServe(metricsAddr, mux); err != nil {
			logger.Error("Metrics server error", zap.Error(err))
		}
	}()

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down worker...")
	cancel()

	// Wait for workers to finish in-flight jobs
	workerPool.Stop()

	logger.Info("Worker stopped")
}
