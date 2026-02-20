package config

import (
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

	// Set defaults
	viper.SetDefault("API_PORT", 8080)
	viper.SetDefault("API_READ_TIMEOUT", "10s")
	viper.SetDefault("API_WRITE_TIMEOUT", "30s")
	viper.SetDefault("API_RATE_LIMIT", 100)
	viper.SetDefault("GIN_MODE", "debug")
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
	cfg.Database.URL = viper.GetString("DATABASE_URL")
	cfg.RabbitMQ.URL = viper.GetString("RABBITMQ_URL")
	cfg.Redis.URL = viper.GetString("REDIS_URL")

	return cfg, nil
}
