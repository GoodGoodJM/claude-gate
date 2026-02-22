package token

import (
	"context"
	"time"

	"github.com/ggmolly/claude-gate/internal/store"
)

// Manager combines a TokenPool with a StickyManager to resolve real tokens
// for incoming gate-token requests.
type Manager struct {
	pool   *TokenPool
	sticky *StickyManager
	store  *store.Store

	stickyTTL   time.Duration
	maxFailures int
}

// NewManager creates a Manager. Call Start to begin background goroutines.
func NewManager(s *store.Store, maxFailures int, stickyTTL time.Duration) *Manager {
	return &Manager{
		pool:        NewTokenPool(s, maxFailures),
		sticky:      NewStickyManager(s),
		store:       s,
		stickyTTL:   stickyTTL,
		maxFailures: maxFailures,
	}
}

// Start initialises the token pool and starts the sticky cleanup goroutine.
func (m *Manager) Start(ctx context.Context) error {
	if err := m.pool.Refresh(); err != nil {
		return err
	}
	m.sticky.Start(ctx)
	return nil
}

// Stop terminates background goroutines.
func (m *Manager) Stop() {
	m.sticky.Stop()
}

// RefreshPool reloads the token pool from the database. Call this after
// creating, updating, or deleting real tokens.
func (m *Manager) RefreshPool() error {
	return m.pool.Refresh()
}

// ResolveToken returns the real token to use for the given gate token ID.
// It checks the sticky session first; on miss it performs round-robin
// selection and binds a new sticky session.
func (m *Manager) ResolveToken(gateTokenID string) (*store.RealToken, error) {
	// Check sticky session.
	if realTokenID, ok := m.sticky.Resolve(gateTokenID); ok {
		if t := m.pool.GetByID(realTokenID); t != nil {
			return t, nil
		}
		// Sticky target is no longer active; fall through to round-robin.
	}

	// Round-robin selection.
	t, err := m.pool.Select(nil)
	if err != nil {
		return nil, err
	}

	// Bind sticky session.
	_ = m.sticky.Bind(gateTokenID, t.ID, m.stickyTTL)
	return t, nil
}

// RecordFailure increments the failure count for a real token in the database.
// If the count reaches maxFailures, the token is deactivated. The pool is
// refreshed afterward.
func (m *Manager) RecordFailure(realTokenID string) error {
	if err := m.store.IncrementRealTokenFailure(realTokenID); err != nil {
		return err
	}

	// Check if we should deactivate.
	t, err := m.store.GetRealToken(realTokenID)
	if err != nil {
		return err
	}
	if t.FailureCount >= m.maxFailures {
		if err := m.store.SetRealTokenActive(realTokenID, false); err != nil {
			return err
		}
	}

	return m.pool.Refresh()
}

// Pool returns the underlying TokenPool (useful for inspection in handlers).
func (m *Manager) Pool() *TokenPool {
	return m.pool
}
