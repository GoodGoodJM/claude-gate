package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// SetupLogger creates a slog.Logger configured with the given level string.
// Valid levels: "debug", "info", "warn", "error". Defaults to "info".
func SetupLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

// Discard returns a logger that discards all output. Useful for tests.
func Discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
