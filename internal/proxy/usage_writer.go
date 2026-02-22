package proxy

import (
	"context"
	"log"
	"time"

	"github.com/ggmolly/claude-gate/internal/store"
)

const (
	usageFlushInterval = 1 * time.Second
	usageBatchSize     = 100
)

// UsageWriter reads usage entries from a channel and batch-writes them to the store.
type UsageWriter struct {
	ch    <-chan usageEntry
	store *store.Store
	buf   []usageEntry
}

// NewUsageWriter creates a UsageWriter. The channel should be created by the
// caller and shared with ProxyHandler.
func NewUsageWriter(ch <-chan usageEntry, s *store.Store) *UsageWriter {
	return &UsageWriter{
		ch:    ch,
		store: s,
		buf:   make([]usageEntry, 0, usageBatchSize),
	}
}

// Run reads from the channel until ctx is cancelled. It flushes on timer or
// when the batch buffer is full.
func (uw *UsageWriter) Run(ctx context.Context) {
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
			if len(uw.buf) > 0 {
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
	if len(uw.buf) == 0 {
		return
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

	if err := uw.store.InsertUsageLogs(logs); err != nil {
		log.Printf("failed to insert usage logs: %v", err)
	}

	// Update cumulative counters on gate and real tokens.
	for _, e := range uw.buf {
		if err := uw.store.UpdateGateTokenUsage(e.GateTokenID, e.Usage.InputTokens, e.Usage.OutputTokens); err != nil {
			log.Printf("failed to update gate token usage: %v", err)
		}
		if err := uw.store.UpdateRealTokenUsage(e.RealTokenID, e.Usage.InputTokens, e.Usage.OutputTokens); err != nil {
			log.Printf("failed to update real token usage: %v", err)
		}
	}

	uw.buf = uw.buf[:0]
}
