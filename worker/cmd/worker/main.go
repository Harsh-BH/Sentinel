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
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/worker/internal/config"
	amqpdelivery "github.com/Harsh-BH/Sentinel/worker/internal/delivery/amqp"
	"github.com/Harsh-BH/Sentinel/worker/internal/domain"
	"github.com/Harsh-BH/Sentinel/worker/internal/executor"
	"github.com/Harsh-BH/Sentinel/worker/internal/pool"
	"github.com/Harsh-BH/Sentinel/worker/internal/repository/postgres"
	"github.com/Harsh-BH/Sentinel/worker/internal/usecase"
)

func main() {
	logger, _ := zap.NewProduction()
	defer func() { _ = logger.Sync() }()

	logger.Info("Starting Sentinel Execution Worker")

	cfg, err := config.Load()
	if err != nil {
		logger.Fatal("Failed to load configuration", zap.Error(err))
	}

	// Two independent lifetimes, not one:
	//
	//   acceptCtx — cancelled on SIGTERM. Stops pulling new work.
	//   jobCtx    — cancelled only after the drain deadline. Anything already
	//               executing keeps its context alive so nsjail is not SIGKILLed
	//               out from under a job whose result has not been written yet.
	acceptCtx, stopAccepting := context.WithCancel(context.Background())
	defer stopAccepting()
	jobCtx, cancelJobs := context.WithCancel(context.Background())
	defer cancelJobs()

	// Startup uses its own bounded context so a dead dependency fails fast
	// instead of hanging the pod until the kubelet's startup probe gives up.
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

	// The worker no longer talks to Redis. Deduplication moved into the
	// execution_jobs row (a conditional UPDATE with a lease), so Redis is not a
	// dependency of the execute path any more — an evicted key can no longer
	// cause a double execution, and a Redis outage can no longer fail jobs.
	jobRepo := postgres.NewPostgresJobRepository(dbPool)
	sandboxExec := executor.NewSandboxExecutor(cfg.Sandbox.NsjailPath, cfg.Sandbox.ConfigDir, logger)
	executeUC := usecase.NewExecuteJobUsecase(jobRepo, sandboxExec, logger)

	jobsChan := make(chan *domain.JobMessage, cfg.Worker.PoolSize*2)

	// Prefetch tracks pool size: because ack happens after execution, prefetch is
	// the real concurrency ceiling of this process.
	consumer, err := amqpdelivery.NewConsumer(cfg.RabbitMQ.URL, jobsChan, cfg.Worker.PoolSize, logger)
	if err != nil {
		logger.Fatal("Failed to initialize AMQP consumer", zap.Error(err))
	}
	logger.Info("Connected to RabbitMQ", zap.Int("prefetch", cfg.Worker.PoolSize))

	workerPool := pool.NewWorkerPool(cfg.Worker.PoolSize, jobsChan, executeUC, logger)
	workerPool.Start(acceptCtx, jobCtx)

	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		if err := consumer.Start(acceptCtx); err != nil {
			logger.Error("AMQP consumer error", zap.Error(err))
			stopAccepting()
		}
	}()

	// ---- Metrics + health server ----
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	// Liveness: is THIS process alive? Nothing else. A dependency check here
	// turns a transient Postgres blip into a kubelet-driven restart storm across
	// every worker at once, which is strictly worse than the blip.
	mux.HandleFunc("/livez", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Readiness: can this process do useful work right now? Dependency checks
	// belong here, because failing readiness only removes the pod from service.
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		pingCtx, pingCancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer pingCancel()
		if err := dbPool.Ping(pingCtx); err != nil {
			http.Error(w, "db unreachable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})

	// Kept for backwards compatibility with existing probes and compose
	// healthchecks that point at /healthz.
	mux.Handle("/healthz", http.RedirectHandler("/readyz", http.StatusTemporaryRedirect))

	metricsSrv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Worker.MetricsPort),
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
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

	logger.Info("Shutting down worker...", zap.Duration("drain_timeout", cfg.Worker.DrainTimeout))

	// 1. Stop accepting new work. In-flight jobs keep running on jobCtx.
	stopAccepting()
	<-consumerDone

	// 2. Let in-flight jobs finish and persist their results.
	drained := workerPool.Stop(cfg.Worker.DrainTimeout)
	if !drained {
		logger.Warn("Drain deadline exceeded — cancelling in-flight executions")
		cancelJobs()
	}

	// 3. Only now close the AMQP connection. Closing it earlier would invalidate
	//    the ack callbacks of jobs that were still finishing, so their results
	//    would be written but the messages redelivered anyway.
	if err := consumer.Close(); err != nil {
		logger.Error("Error closing AMQP consumer", zap.Error(err))
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("Metrics server shutdown error", zap.Error(err))
	}

	logger.Info("Worker stopped", zap.Bool("clean_drain", drained))
}
