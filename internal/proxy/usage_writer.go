package proxy

import (
	"context"
	"log/slog"
	"time"

	"github.com/ggmolly/claude-gate/internal/store"
)

const (
	usageFlushInterval = 1 * time.Second
	usageBatchSize     = 100
)

// UsageWriter reads usage entries from a channel and batch-writes them to the store.
type UsageWriter struct {
	ch         <-chan usageEntry
	store      *store.Store
	logger     *slog.Logger
	buf        []usageEntry
	retryBuf   []usageEntry
	retryCount int
	ctx        context.Context
}

// NewUsageWriter creates a UsageWriter. The channel should be created by the
// caller and shared with ProxyHandler.
func NewUsageWriter(ch <-chan usageEntry, s *store.Store, logger *slog.Logger) *UsageWriter {
	return &UsageWriter{
		ch:     ch,
		store:  s,
		logger: logger,
		buf:    make([]usageEntry, 0, usageBatchSize),
	}
}

// Run reads from the channel until ctx is cancelled. It flushes on timer or
// when the batch buffer is full.
func (uw *UsageWriter) Run(ctx context.Context) {
	uw.ctx = ctx
	ticker := time.NewTicker(usageFlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Drain remaining entries.
			uw.drain()
			uw.flush()
			return
		case entry, ok := <-uw.ch:
			if !ok {
				uw.flush()
				return
			}
			uw.buf = append(uw.buf, entry)
			if len(uw.buf) >= usageBatchSize {
				uw.flush()
			}
		case <-ticker.C:
			if len(uw.buf) > 0 || len(uw.retryBuf) > 0 {
				uw.flush()
			}
		}
	}
}

// drain reads remaining entries from the channel without blocking.
func (uw *UsageWriter) drain() {
	for {
		select {
		case entry, ok := <-uw.ch:
			if !ok {
				return
			}
			uw.buf = append(uw.buf, entry)
		default:
			return
		}
	}
}

func (uw *UsageWriter) flush() {
	if len(uw.buf) == 0 && len(uw.retryBuf) == 0 {
		return
	}

	// Prepend retry entries.
	if len(uw.retryBuf) > 0 {
		uw.buf = append(uw.retryBuf, uw.buf...)
		uw.retryBuf = nil
	}

	logs := make([]store.UsageLog, 0, len(uw.buf))
	for _, e := range uw.buf {
		logs = append(logs, store.UsageLog{
			GateTokenID:              e.GateTokenID,
			RealTokenID:              e.RealTokenID,
			Model:                    e.Usage.Model,
			InputTokens:              e.Usage.InputTokens,
			OutputTokens:             e.Usage.OutputTokens,
			CacheCreationInputTokens: e.Usage.CacheCreationInputTokens,
			CacheReadInputTokens:     e.Usage.CacheReadInputTokens,
			RequestPath:              e.RequestPath,
			StatusCode:               e.StatusCode,
		})
	}

	if err := uw.store.InsertUsageLogs(uw.ctx, logs); err != nil {
		uw.logger.Error("failed to insert usage logs", "error", err, "retry", uw.retryCount)
		if uw.retryCount < 3 {
			uw.retryBuf = append(uw.retryBuf[:0], uw.buf...)
			uw.retryCount++
		} else {
			uw.logger.Error("usage logs discarded after max retries", "entries", len(uw.buf))
			uw.retryCount = 0
		}
		uw.buf = uw.buf[:0]
		return
	}
	uw.retryCount = 0

	// Update cumulative counters on gate and real tokens.
	for _, e := range uw.buf {
		if err := uw.store.UpdateGateTokenUsage(uw.ctx, e.GateTokenID, e.Usage.InputTokens, e.Usage.OutputTokens); err != nil {
			uw.logger.Error("failed to update gate token usage", "error", err)
		}
		if err := uw.store.UpdateRealTokenUsage(uw.ctx, e.RealTokenID, e.Usage.InputTokens, e.Usage.OutputTokens); err != nil {
			uw.logger.Error("failed to update real token usage", "error", err)
		}
	}

	uw.logger.Debug("usage flushed", "entries", len(uw.buf))
	uw.buf = uw.buf[:0]
}
