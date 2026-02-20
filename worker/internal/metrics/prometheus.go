package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// ExecutionsTotal counts the total number of code executions by language and status.
	ExecutionsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sentinel_executions_total",
			Help: "Total number of code executions",
		},
		[]string{"language", "status"},
	)

	// ExecutionDuration tracks the duration of code executions in seconds.
	ExecutionDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "sentinel_execution_duration_seconds",
			Help:    "Duration of code executions in seconds",
			Buckets: prometheus.ExponentialBuckets(0.01, 2, 12), // 10ms to ~40s
		},
		[]string{"language"},
	)

	// WorkersActive tracks the number of currently active workers.
	WorkersActive = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "sentinel_workers_active",
			Help: "Number of currently active worker goroutines",
		},
	)

	// SandboxFailures counts sandbox infrastructure failures (not user code errors).
	SandboxFailures = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "sentinel_sandbox_failures_total",
			Help: "Total number of sandbox infrastructure failures",
		},
	)
)
