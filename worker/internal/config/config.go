package config

import (
	"github.com/spf13/viper"
)

// Config holds all configuration for the execution worker.
type Config struct {
	RabbitMQ RabbitMQConfig
	Database DatabaseConfig
	Redis    RedisConfig
	Worker   WorkerConfig
	Sandbox  SandboxConfig
}

type RabbitMQConfig struct {
	URL string `mapstructure:"RABBITMQ_URL"`
}

type DatabaseConfig struct {
	URL string `mapstructure:"DATABASE_URL"`
}

type RedisConfig struct {
	URL string `mapstructure:"REDIS_URL"`
}

type WorkerConfig struct {
	PoolSize    int `mapstructure:"WORKER_POOL_SIZE"`
	MetricsPort int `mapstructure:"WORKER_METRICS_PORT"`
}

type SandboxConfig struct {
	NsjailPath           string `mapstructure:"WORKER_NSJAIL_PATH"`
	ConfigDir            string `mapstructure:"WORKER_SANDBOX_CONFIG_DIR"`
	PolicyDir            string `mapstructure:"WORKER_POLICY_DIR"`
	DefaultTimeLimitMs   int    `mapstructure:"WORKER_DEFAULT_TIME_LIMIT_MS"`
	DefaultMemoryLimitKB int    `mapstructure:"WORKER_DEFAULT_MEMORY_LIMIT_KB"`
}

// Load reads worker configuration from environment variables.
func Load() (*Config, error) {
	viper.SetConfigFile(".env")
	viper.AutomaticEnv()

	// Defaults
	viper.SetDefault("RABBITMQ_URL", "amqp://sentinel:sentinel_secret@localhost:5672/")
	viper.SetDefault("DATABASE_URL", "postgres://sentinel:sentinel_secret@localhost:5432/sentinel?sslmode=disable")
	viper.SetDefault("REDIS_URL", "redis://localhost:6379/0")
	viper.SetDefault("WORKER_POOL_SIZE", 4)
	viper.SetDefault("WORKER_METRICS_PORT", 9090)
	viper.SetDefault("WORKER_NSJAIL_PATH", "/usr/bin/nsjail")
	viper.SetDefault("WORKER_SANDBOX_CONFIG_DIR", "./sandbox/nsjail")
	viper.SetDefault("WORKER_POLICY_DIR", "./sandbox/policies")
	viper.SetDefault("WORKER_DEFAULT_TIME_LIMIT_MS", 5000)
	viper.SetDefault("WORKER_DEFAULT_MEMORY_LIMIT_KB", 262144)

	_ = viper.ReadInConfig()

	cfg := &Config{}
	cfg.RabbitMQ.URL = viper.GetString("RABBITMQ_URL")
	cfg.Database.URL = viper.GetString("DATABASE_URL")
	cfg.Redis.URL = viper.GetString("REDIS_URL")
	cfg.Worker.PoolSize = viper.GetInt("WORKER_POOL_SIZE")
	cfg.Worker.MetricsPort = viper.GetInt("WORKER_METRICS_PORT")
	cfg.Sandbox.NsjailPath = viper.GetString("WORKER_NSJAIL_PATH")
	cfg.Sandbox.ConfigDir = viper.GetString("WORKER_SANDBOX_CONFIG_DIR")
	cfg.Sandbox.PolicyDir = viper.GetString("WORKER_POLICY_DIR")
	cfg.Sandbox.DefaultTimeLimitMs = viper.GetInt("WORKER_DEFAULT_TIME_LIMIT_MS")
	cfg.Sandbox.DefaultMemoryLimitKB = viper.GetInt("WORKER_DEFAULT_MEMORY_LIMIT_KB")

	return cfg, nil
}
