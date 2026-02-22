package token

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/ggmolly/claude-gate/internal/store"
)

// Manager combines a TokenPool with a StickyManager to resolve real tokens
// for incoming gate-token requests.
type Manager struct {
	pool   *TokenPool
	sticky *StickyManager
	store  *store.Store
	logger *slog.Logger

	stickyTTL   time.Duration
	maxFailures int

	ctx          context.Context
	refreshMu    sync.Mutex
	refreshTimer *time.Timer
}

// NewManager creates a Manager. Call Start to begin background goroutines.
func NewManager(s *store.Store, maxFailures int, stickyTTL time.Duration, logger *slog.Logger) *Manager {
	return &Manager{
		pool:        NewTokenPool(s, maxFailures, logger),
		sticky:      NewStickyManager(s, logger),
		store:       s,
		logger:      logger,
		stickyTTL:   stickyTTL,
		maxFailures: maxFailures,
	}
}

// Start initialises the token pool and starts the sticky cleanup goroutine.
func (m *Manager) Start(ctx context.Context) error {
	m.ctx = ctx
	if err := m.pool.Refresh(ctx); err != nil {
		return err
	}
	m.sticky.Start(ctx)
	return nil
}

// Stop terminates background goroutines.
func (m *Manager) Stop() {
	m.sticky.Stop()
}

// RefreshPool schedules a debounced pool refresh. Multiple calls within 100ms
// are coalesced into a single refresh.
func (m *Manager) RefreshPool() {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	if m.refreshTimer != nil {
		m.refreshTimer.Stop()
	}
	m.refreshTimer = time.AfterFunc(100*time.Millisecond, func() {
		if err := m.pool.Refresh(m.ctx); err != nil {
			m.logger.Error("failed to refresh token pool", "error", err)
		} else {
			m.logger.Debug("pool refreshed")
		}
	})
}

// ResolveToken returns the real token to use for the given gate token ID.
// It checks the sticky session first; on miss it performs round-robin
// selection and binds a new sticky session.
func (m *Manager) ResolveToken(ctx context.Context, gateTokenID string) (*store.RealToken, error) {
	// Check sticky session.
	if realTokenID, ok := m.sticky.Resolve(ctx, gateTokenID); ok {
		if t := m.pool.GetByID(realTokenID); t != nil {
			m.logger.Debug("token resolved", "gate", gateTokenID, "real", realTokenID, "real_name", t.Name, "method", "sticky")
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
	_ = m.sticky.Bind(ctx, gateTokenID, t.ID, m.stickyTTL)
	m.logger.Debug("token resolved", "gate", gateTokenID, "real", t.ID, "real_name", t.Name, "method", "round-robin")
	return t, nil
}

// RecordFailure increments the failure count for a real token in the database.
// If the count reaches maxFailures, the token is deactivated. The pool is
// refreshed afterward.
func (m *Manager) RecordFailure(ctx context.Context, realTokenID string) error {
	m.logger.Info("recording failure", "real_token", realTokenID)
	if err := m.store.IncrementRealTokenFailure(ctx, realTokenID); err != nil {
		return err
	}

	// Check if we should deactivate.
	t, err := m.store.GetRealToken(ctx, realTokenID)
	if err != nil {
		return err
	}
	if t.FailureCount >= m.maxFailures {
		m.logger.Warn("deactivating token due to failures", "real_token", realTokenID, "failures", t.FailureCount)
		if err := m.store.SetRealTokenActive(ctx, realTokenID, false); err != nil {
			return err
		}
	}

	m.RefreshPool()
	return nil
}
