package config

import (
	"errors"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
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

// Validate checks configuration values and logs warnings for any issues.
func (c *Config) Validate(logger *slog.Logger) {
	if u, err := url.Parse(c.UpstreamURL); err != nil || u.Scheme == "" {
		logger.Warn("invalid upstream URL", "url", c.UpstreamURL)
	}

	dir := filepath.Dir(c.DBPath)
	if dir != "." && dir != "" {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			logger.Warn("database directory does not exist", "dir", dir)
		}
	}

	if v := os.Getenv("CLAUDE_GATE_STICKY_TTL"); v != "" {
		if _, err := time.ParseDuration(v); err != nil {
			logger.Warn("failed to parse CLAUDE_GATE_STICKY_TTL, using default", "value", v, "default", c.StickyTTL)
		}
	}

	if v := os.Getenv("CLAUDE_GATE_MAX_FAILURES"); v != "" {
		if _, err := strconv.Atoi(v); err != nil {
			logger.Warn("failed to parse CLAUDE_GATE_MAX_FAILURES, using default", "value", v, "default", c.MaxFailures)
		}
	}
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
