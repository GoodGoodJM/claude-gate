package config

import (
	"errors"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Addr        string
	DBPath      string
	AdminSecret string
	UpstreamURL string
	StickyTTL   time.Duration
	MaxFailures int
	LogLevel    string
}

func Load() (*Config, error) {
	cfg := &Config{
		Addr:        envOr("CLAUDE_GATE_ADDR", ":8080"),
		DBPath:      envOr("CLAUDE_GATE_DB_PATH", "./claude-gate.db"),
		AdminSecret: os.Getenv("CLAUDE_GATE_ADMIN_SECRET"),
		UpstreamURL: envOr("CLAUDE_GATE_UPSTREAM_URL", "https://api.anthropic.com"),
		StickyTTL:   envDuration("CLAUDE_GATE_STICKY_TTL", 10*time.Minute),
		MaxFailures: envInt("CLAUDE_GATE_MAX_FAILURES", 5),
		LogLevel:    envOr("CLAUDE_GATE_LOG_LEVEL", "info"),
	}

	if cfg.AdminSecret == "" {
		return nil, errors.New("CLAUDE_GATE_ADMIN_SECRET is required")
	}

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		d, err := time.ParseDuration(v)
		if err == nil {
			return d
		}
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
	}
	return fallback
}
