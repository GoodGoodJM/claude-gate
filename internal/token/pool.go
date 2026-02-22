package token

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/ggmolly/claude-gate/internal/store"
)

var ErrNoAvailableTokens = errors.New("no available tokens")

// TokenPool manages a cached list of active real tokens and provides
// round-robin selection with optional exclusion.
type TokenPool struct {
	store       *store.Store
	maxFailures int
	logger      *slog.Logger

	mu     sync.RWMutex
	tokens []store.RealToken

	counter atomic.Uint64
}

// NewTokenPool creates a new TokenPool. Call Refresh to load initial tokens.
func NewTokenPool(s *store.Store, maxFailures int, logger *slog.Logger) *TokenPool {
	return &TokenPool{
		store:       s,
		maxFailures: maxFailures,
		logger:      logger,
	}
}

// Refresh reloads the token list from the database.
func (p *TokenPool) Refresh(ctx context.Context) error {
	tokens, err := p.store.ListActiveRealTokens(ctx, p.maxFailures)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.tokens = tokens
	p.mu.Unlock()
	p.logger.Debug("pool refreshed", "active_tokens", len(tokens))
	return nil
}

// Select picks the next token using round-robin, skipping any token whose ID
// is in the excludeIDs set. Returns ErrNoAvailableTokens if none are available.
func (p *TokenPool) Select(excludeIDs map[string]bool) (*store.RealToken, error) {
	p.mu.RLock()
	tokens := p.tokens
	p.mu.RUnlock()

	n := len(tokens)
	if n == 0 {
		return nil, ErrNoAvailableTokens
	}

	start := p.counter.Add(1) - 1
	for i := range n {
		idx := int((start + uint64(i)) % uint64(n))
		t := &tokens[idx]
		if excludeIDs != nil && excludeIDs[t.ID] {
			continue
		}
		return t, nil
	}
	return nil, ErrNoAvailableTokens
}

// Len returns the number of cached active tokens.
func (p *TokenPool) Len() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.tokens)
}

// GetByID returns a token from the cache by ID, or nil if not found.
func (p *TokenPool) GetByID(id string) *store.RealToken {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for i := range p.tokens {
		if p.tokens[i].ID == id {
			t := p.tokens[i]
			return &t
		}
	}
	return nil
}
