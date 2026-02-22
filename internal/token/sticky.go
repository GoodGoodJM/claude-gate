package token

import (
	"context"
	"sync"
	"time"

	"github.com/ggmolly/claude-gate/internal/store"
)

type stickyEntry struct {
	realTokenID string
	expiresAt   time.Time
}

// StickyManager provides gate-token-to-real-token affinity with an in-memory
// cache backed by the database for persistence across restarts.
type StickyManager struct {
	store *store.Store
	cache sync.Map // map[string]*stickyEntry (gateTokenID -> entry)

	cancel context.CancelFunc
	done   chan struct{}
}

// NewStickyManager creates a new StickyManager.
func NewStickyManager(s *store.Store) *StickyManager {
	return &StickyManager{
		store: s,
		done:  make(chan struct{}),
	}
}

// Start begins the background cleanup goroutine that removes expired entries
// every 5 minutes. Pass a parent context; cancel it or call Stop to terminate.
func (sm *StickyManager) Start(ctx context.Context) {
	ctx, sm.cancel = context.WithCancel(ctx)
	go sm.cleanupLoop(ctx)
}

// Stop terminates the background cleanup goroutine and waits for it to exit.
func (sm *StickyManager) Stop() {
	if sm.cancel != nil {
		sm.cancel()
		<-sm.done
	}
}

// Resolve looks up the real token ID for a gate token. It checks the in-memory
// cache first, then falls back to the database. Returns ("", false) if no
// valid binding exists.
func (sm *StickyManager) Resolve(gateTokenID string) (string, bool) {
	// Check in-memory cache first.
	if v, ok := sm.cache.Load(gateTokenID); ok {
		entry, _ := v.(*stickyEntry)
		if entry != nil && time.Now().Before(entry.expiresAt) {
			return entry.realTokenID, true
		}
		sm.cache.Delete(gateTokenID)
	}

	// Fall back to DB.
	ss, err := sm.store.GetStickySession(gateTokenID)
	if err != nil || ss == nil {
		return "", false
	}

	// Populate cache.
	sm.cache.Store(gateTokenID, &stickyEntry{
		realTokenID: ss.RealTokenID,
		expiresAt:   ss.ExpiresAt,
	})
	return ss.RealTokenID, true
}

// Bind creates or updates a sticky binding in both memory and the database.
func (sm *StickyManager) Bind(gateTokenID, realTokenID string, ttl time.Duration) error {
	expiresAt := time.Now().UTC().Add(ttl)
	if err := sm.store.UpsertStickySession(gateTokenID, realTokenID, expiresAt); err != nil {
		return err
	}
	sm.cache.Store(gateTokenID, &stickyEntry{
		realTokenID: realTokenID,
		expiresAt:   expiresAt,
	})
	return nil
}

func (sm *StickyManager) cleanupLoop(ctx context.Context) {
	defer close(sm.done)
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sm.cleanup()
		}
	}
}

func (sm *StickyManager) cleanup() {
	now := time.Now()
	sm.cache.Range(func(key, value any) bool {
		entry, _ := value.(*stickyEntry)
		if entry != nil && now.After(entry.expiresAt) {
			sm.cache.Delete(key)
		}
		return true
	})
	_, _ = sm.store.DeleteExpiredStickySessions()
}
