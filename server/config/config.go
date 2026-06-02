package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds all runtime configuration loaded from environment variables.
// Every field has a sane default so the server starts with zero env vars set
// (useful for local go run outside Docker).
type Config struct {
	// Database
	DatabaseURL string

	// Redis
	RedisAddr     string
	RedisPassword string
	RedisDB       int

	// HTTP server
	ServerPort string

	// Prometheus metrics server (separate port so it's not exposed publicly)
	MetricsPort string

	// Application
	LogLevel string
	Env      string // development | staging | production
}

// Load reads all config from environment variables with sensible defaults.
func Load() (*Config, error) {
	cfg := &Config{
		DatabaseURL: getEnv("DATABASE_URL", "postgres://ffee:ffee_secret@localhost:5432/featureflags?sslmode=disable"),
		RedisAddr:   getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword: getEnv("REDIS_PASSWORD", ""),
		RedisDB:     getEnvInt("REDIS_DB", 0),
		ServerPort:  getEnv("SERVER_PORT", "8080"),
		MetricsPort: getEnv("METRICS_PORT", "9090"),
		LogLevel:    getEnv("LOG_LEVEL", "info"),
		Env:         getEnv("ENV", "development"),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}

	return cfg, nil
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
	}
	return defaultVal
}
