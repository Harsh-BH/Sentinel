package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config holds all configuration for the API server.
type Config struct {
	Server   ServerConfig
	Database DatabaseConfig
	RabbitMQ RabbitMQConfig
	Redis    RedisConfig
}

type ServerConfig struct {
	Port         int           `mapstructure:"API_PORT"`
	ReadTimeout  time.Duration `mapstructure:"API_READ_TIMEOUT"`
	WriteTimeout time.Duration `mapstructure:"API_WRITE_TIMEOUT"`
	RateLimit    int           `mapstructure:"API_RATE_LIMIT"`
	GinMode      string        `mapstructure:"GIN_MODE"`

	// AllowedOrigins is the CORS / WebSocket origin allowlist. "*" is accepted
	// for local development but is unsafe in production: this API returns a job's
	// source code on GET and has no authentication.
	AllowedOrigins []string `mapstructure:"API_ALLOWED_ORIGINS"`

	// TrustedProxies lists the peers permitted to set X-Forwarded-For. Empty
	// means trust nobody, which is the safe default — see router.go.
	TrustedProxies []string `mapstructure:"API_TRUSTED_PROXIES"`

	// ReapInterval controls how often the stranded-job sweep runs. Zero disables
	// the reaper entirely, which is only appropriate in tests.
	ReapInterval time.Duration `mapstructure:"API_REAP_INTERVAL"`
}

type DatabaseConfig struct {
	URL string `mapstructure:"DATABASE_URL"`
}

type RabbitMQConfig struct {
	URL string `mapstructure:"RABBITMQ_URL"`
}

type RedisConfig struct {
	URL string `mapstructure:"REDIS_URL"`
}

// Load reads configuration from environment variables and .env file.
func Load() (*Config, error) {
	viper.SetConfigFile(".env")
	viper.AutomaticEnv()

	viper.SetDefault("API_PORT", 8080)
	viper.SetDefault("API_READ_TIMEOUT", "10s")
	viper.SetDefault("API_WRITE_TIMEOUT", "30s")
	viper.SetDefault("API_RATE_LIMIT", 100)
	viper.SetDefault("GIN_MODE", "release")
	viper.SetDefault("API_ALLOWED_ORIGINS", "*")
	viper.SetDefault("API_TRUSTED_PROXIES", "")
	viper.SetDefault("API_REAP_INTERVAL", "30s")
	viper.SetDefault("DATABASE_URL", "postgres://sentinel:sentinel_secret@localhost:5432/sentinel?sslmode=disable")
	viper.SetDefault("RABBITMQ_URL", "amqp://sentinel:sentinel_secret@localhost:5672/")
	viper.SetDefault("REDIS_URL", "redis://localhost:6379/0")

	// Attempt to read .env file (non-fatal if missing)
	_ = viper.ReadInConfig()

	cfg := &Config{}
	cfg.Server.Port = viper.GetInt("API_PORT")
	cfg.Server.ReadTimeout = viper.GetDuration("API_READ_TIMEOUT")
	cfg.Server.WriteTimeout = viper.GetDuration("API_WRITE_TIMEOUT")
	cfg.Server.RateLimit = viper.GetInt("API_RATE_LIMIT")
	cfg.Server.GinMode = viper.GetString("GIN_MODE")
	cfg.Server.AllowedOrigins = splitList(viper.GetString("API_ALLOWED_ORIGINS"))
	cfg.Server.TrustedProxies = splitList(viper.GetString("API_TRUSTED_PROXIES"))
	cfg.Server.ReapInterval = viper.GetDuration("API_REAP_INTERVAL")
	cfg.Database.URL = viper.GetString("DATABASE_URL")
	cfg.RabbitMQ.URL = viper.GetString("RABBITMQ_URL")
	cfg.Redis.URL = viper.GetString("REDIS_URL")

	if cfg.Server.Port < 1 || cfg.Server.Port > 65535 {
		return nil, fmt.Errorf("config: API_PORT must be 1-65535, got %d", cfg.Server.Port)
	}
	if cfg.Server.RateLimit < 1 {
		return nil, fmt.Errorf("config: API_RATE_LIMIT must be >= 1, got %d", cfg.Server.RateLimit)
	}

	return cfg, nil
}

// splitList parses a comma-separated env value into a trimmed, non-empty slice.
func splitList(raw string) []string {
	var out []string
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
