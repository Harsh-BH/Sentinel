package config

import (
	"fmt"
	"time"

	"github.com/spf13/viper"
)

// Config holds all configuration for the execution worker.
type Config struct {
	RabbitMQ RabbitMQConfig
	Database DatabaseConfig
	Worker   WorkerConfig
	Sandbox  SandboxConfig
}

type RabbitMQConfig struct {
	URL string `mapstructure:"RABBITMQ_URL"`
}

type DatabaseConfig struct {
	URL string `mapstructure:"DATABASE_URL"`
}

type WorkerConfig struct {
	PoolSize    int `mapstructure:"WORKER_POOL_SIZE"`
	MetricsPort int `mapstructure:"WORKER_METRICS_PORT"`
	// DrainTimeout must stay below the orchestrator's termination grace period
	// (60s in infra/k8s/worker-deployment.yaml) or the kubelet SIGKILLs the pod
	// mid-drain and the point of draining is lost.
	DrainTimeout time.Duration `mapstructure:"WORKER_DRAIN_TIMEOUT"`
}

type SandboxConfig struct {
	NsjailPath string `mapstructure:"WORKER_NSJAIL_PATH"`
	ConfigDir  string `mapstructure:"WORKER_SANDBOX_CONFIG_DIR"`
}

// Load reads worker configuration from environment variables.
func Load() (*Config, error) {
	viper.SetConfigFile(".env")
	viper.AutomaticEnv()

	// Defaults
	viper.SetDefault("RABBITMQ_URL", "amqp://sentinel:sentinel_secret@localhost:5672/")
	viper.SetDefault("DATABASE_URL", "postgres://sentinel:sentinel_secret@localhost:5432/sentinel?sslmode=disable")
	viper.SetDefault("WORKER_POOL_SIZE", 4)
	viper.SetDefault("WORKER_METRICS_PORT", 9090)
	viper.SetDefault("WORKER_DRAIN_TIMEOUT", "45s")
	viper.SetDefault("WORKER_NSJAIL_PATH", "/usr/bin/nsjail")
	// Absolute path: this is where worker/Dockerfile installs the configs. The
	// old default was the relative "./sandbox/nsjail", which resolves against the
	// process CWD (/ in the container) and silently does not exist — so any
	// deployment that failed to set WORKER_SANDBOX_CONFIG_DIR broke every
	// execution with a config-not-found error.
	viper.SetDefault("WORKER_SANDBOX_CONFIG_DIR", "/etc/sentinel/nsjail")

	_ = viper.ReadInConfig()

	cfg := &Config{}
	cfg.RabbitMQ.URL = viper.GetString("RABBITMQ_URL")
	cfg.Database.URL = viper.GetString("DATABASE_URL")
	cfg.Worker.PoolSize = viper.GetInt("WORKER_POOL_SIZE")
	cfg.Worker.MetricsPort = viper.GetInt("WORKER_METRICS_PORT")
	cfg.Worker.DrainTimeout = viper.GetDuration("WORKER_DRAIN_TIMEOUT")
	cfg.Sandbox.NsjailPath = viper.GetString("WORKER_NSJAIL_PATH")
	cfg.Sandbox.ConfigDir = viper.GetString("WORKER_SANDBOX_CONFIG_DIR")

	// Fail loudly at boot rather than behaving strangely later. A pool size of 0
	// silently processes nothing; a zero drain timeout silently kills in-flight
	// jobs, which is the exact bug the two-context shutdown exists to prevent.
	if cfg.Worker.PoolSize < 1 {
		return nil, fmt.Errorf("config: WORKER_POOL_SIZE must be >= 1, got %d", cfg.Worker.PoolSize)
	}
	if cfg.Worker.DrainTimeout <= 0 {
		return nil, fmt.Errorf("config: WORKER_DRAIN_TIMEOUT must be > 0, got %s", cfg.Worker.DrainTimeout)
	}

	return cfg, nil
}
