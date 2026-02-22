package proxy

import (
	"context"
	"log/slog"

	"github.com/ggmolly/claude-gate/internal/store"
	"github.com/ggmolly/claude-gate/internal/token"
)

const usageChannelSize = 1024

// Setup creates a ProxyHandler and starts the background UsageWriter.
// Returns the handler and a cancel function that stops the writer.
func Setup(ctx context.Context, s *store.Store, mgr *token.Manager, upstream string, logger *slog.Logger) (*ProxyHandler, context.CancelFunc, error) {
	ch := make(chan usageEntry, usageChannelSize)

	handler, err := NewProxyHandler(s, mgr, upstream, ch, logger)
	if err != nil {
		return nil, nil, err
	}

	writerCtx, cancel := context.WithCancel(ctx)
	writer := NewUsageWriter(ch, s, logger)
	go writer.Run(writerCtx)

	return handler, cancel, nil
}
