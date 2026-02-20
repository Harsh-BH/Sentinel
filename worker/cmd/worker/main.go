package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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

	// Create buffered job channel (carries JobMessage with ACK callbacks).
	jobsChan := make(chan *domain.JobMessage, cfg.Worker.PoolSize*2)

	// Initialize AMQP consumer
	consumer, err := amqpdelivery.NewConsumer(cfg.RabbitMQ.URL, jobsChan, logger)
	if err != nil {
		logger.Fatal("Failed to initialize AMQP consumer", zap.Error(err))
	}
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

	// Start HTTP server for Prometheus metrics + health check.
	metricsSrv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Worker.MetricsPort),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		// Quick liveness: check DB and Redis are reachable.
		pingCtx, pingCancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer pingCancel()
		if err := dbPool.Ping(pingCtx); err != nil {
			http.Error(w, "db unreachable", http.StatusServiceUnavailable)
			return
		}
		if err := redisClient.Ping(pingCtx).Err(); err != nil {
			http.Error(w, "redis unreachable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	metricsSrv.Handler = mux

	go func() {
		logger.Info("Metrics/health server listening", zap.String("addr", metricsSrv.Addr))
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Metrics server error", zap.Error(err))
		}
	}()

	// ---- Graceful shutdown ----
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down worker...")

	// 1. Stop the AMQP consumer first so no new messages are fetched.
	if err := consumer.Close(); err != nil {
		logger.Error("Error closing AMQP consumer", zap.Error(err))
	}

	// 2. Cancel the context so workers finish their current job and exit.
	cancel()

	// 3. Wait for workers to drain in-flight jobs.
	workerPool.Stop()

	// 4. Close the job channel.
	close(jobsChan)

	// 5. Shut down the metrics server.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("Metrics server shutdown error", zap.Error(err))
	}

	logger.Info("Worker stopped")
}
