package token

import (
	"context"
	"log/slog"
	"time"

	"github.com/ggmolly/claude-gate/internal/store"
	"github.com/jellydator/ttlcache/v3"
)

// StickyManager provides gate-token-to-real-token affinity with an in-memory
// cache backed by the database for persistence across restarts.
type StickyManager struct {
	store  *store.Store
	logger *slog.Logger
	cache  *ttlcache.Cache[string, string]

	cancel context.CancelFunc
	done   chan struct{}
}

// NewStickyManager creates a new StickyManager.
func NewStickyManager(s *store.Store, logger *slog.Logger) *StickyManager {
	cache := ttlcache.New[string, string]()
	return &StickyManager{
		store:  s,
		logger: logger,
		cache:  cache,
		done:   make(chan struct{}),
	}
}

// Start begins the ttlcache auto-eviction goroutine and a background DB
// cleanup goroutine. Pass a parent context; cancel it or call Stop to terminate.
func (sm *StickyManager) Start(ctx context.Context) {
	ctx, sm.cancel = context.WithCancel(ctx)
	go sm.cache.Start()
	go sm.dbCleanupLoop(ctx)
}

// Stop terminates the ttlcache auto-eviction goroutine and the background DB
// cleanup goroutine, then waits for the DB cleanup goroutine to exit.
func (sm *StickyManager) Stop() {
	sm.cache.Stop()
	if sm.cancel != nil {
		sm.cancel()
		<-sm.done
	}
}

// Resolve looks up the real token ID for a gate token. It checks the in-memory
// cache first, then falls back to the database. Returns ("", false) if no
// valid binding exists.
func (sm *StickyManager) Resolve(ctx context.Context, gateTokenID string) (string, bool) {
	// Check in-memory cache first.
	item := sm.cache.Get(gateTokenID)
	if item != nil {
		return item.Value(), true
	}

	// Fall back to DB.
	ss, err := sm.store.GetStickySession(ctx, gateTokenID)
	if err != nil || ss == nil {
		return "", false
	}

	// Populate cache with remaining TTL.
	ttl := time.Until(ss.ExpiresAt)
	if ttl <= 0 {
		return "", false
	}
	sm.cache.Set(gateTokenID, ss.RealTokenID, ttl)
	return ss.RealTokenID, true
}

// Bind creates or updates a sticky binding in both memory and the database.
func (sm *StickyManager) Bind(ctx context.Context, gateTokenID, realTokenID string, ttl time.Duration) error {
	expiresAt := time.Now().UTC().Add(ttl)
	if err := sm.store.UpsertStickySession(ctx, gateTokenID, realTokenID, expiresAt); err != nil {
		return err
	}
	sm.cache.Set(gateTokenID, realTokenID, ttl)
	sm.logger.Debug("sticky bind", "gate", gateTokenID, "real", realTokenID, "ttl", ttl)
	return nil
}

func (sm *StickyManager) dbCleanupLoop(ctx context.Context) {
	defer close(sm.done)
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = sm.store.DeleteExpiredStickySessions(ctx)
		}
	}
}
