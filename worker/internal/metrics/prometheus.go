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

	// DuplicateDeliveries counts messages dropped because the job was already
	// handled. A non-zero rate here is normal for at-least-once delivery; a
	// sudden spike means redelivery is happening more than it should.
	DuplicateDeliveries = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sentinel_duplicate_deliveries_total",
			Help: "Queue messages skipped because the job was already handled",
		},
		[]string{"reason"},
	)

	// JobsReclaimed counts jobs re-submitted by the reaper after their lease
	// expired without a result. This is the "how much work is being lost and
	// recovered" signal — it should normally be zero.
	JobsReclaimed = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "sentinel_jobs_reclaimed_total",
			Help: "Jobs re-submitted by the reaper after an expired lease",
		},
	)

	// WorkerPanics counts recovered panics in pool goroutines. Should be zero;
	// any value at all is a bug worth paging on.
	WorkerPanics = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "sentinel_worker_panics_total",
			Help: "Recovered panics in worker pool goroutines",
		},
	)

	// SettleFailures counts failures to ack/nack a delivery. These are the
	// messages most at risk of being stuck unacked in the broker.
	SettleFailures = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sentinel_settle_failures_total",
			Help: "Failures acknowledging or rejecting a queue delivery",
		},
		[]string{"action"},
	)
)
