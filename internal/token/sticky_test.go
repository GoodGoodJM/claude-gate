package token

import (
	"context"
	"testing"
	"time"

	"github.com/ggmolly/claude-gate/testutil"
)

func TestStickyManager_BindAndResolve(t *testing.T) {
	s := testutil.NewTestStore(t)
	sm := NewStickyManager(s)

	gt, _ := s.CreateGateToken("gate1")
	rt, _ := s.CreateRealToken("real1", "acc1", "ref1")

	err := sm.Bind(gt.ID, rt.ID, 10*time.Minute)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}

	realID, ok := sm.Resolve(gt.ID)
	if !ok {
		t.Fatal("expected resolve to succeed")
	}
	if realID != rt.ID {
		t.Errorf("expected %s, got %s", rt.ID, realID)
	}
}

func TestStickyManager_Expiry(t *testing.T) {
	s := testutil.NewTestStore(t)
	sm := NewStickyManager(s)

	gt, _ := s.CreateGateToken("gate1")
	rt, _ := s.CreateRealToken("real1", "acc1", "ref1")

	// Bind normally first.
	err := sm.Bind(gt.ID, rt.ID, 10*time.Minute)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}

	// Force cache to expired state.
	sm.cache.Store(gt.ID, &stickyEntry{
		realTokenID: rt.ID,
		expiresAt:   time.Now().Add(-1 * time.Hour),
	})
	// Remove DB entry so the DB fallback also returns nothing.
	_ = s.DeleteStickySession(gt.ID)

	_, ok := sm.Resolve(gt.ID)
	if ok {
		t.Error("expected expired session to not resolve")
	}
}

func TestStickyManager_DBFallback(t *testing.T) {
	s := testutil.NewTestStore(t)
	sm := NewStickyManager(s)

	gt, _ := s.CreateGateToken("gate1")
	rt, _ := s.CreateRealToken("real1", "acc1", "ref1")

	// Write to DB directly (use UTC to match SQLite datetime('now')).
	err := s.UpsertStickySession(gt.ID, rt.ID, time.Now().UTC().Add(10*time.Minute))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// sm's cache is empty, so it should fall back to DB.
	realID, ok := sm.Resolve(gt.ID)
	if !ok {
		t.Fatal("expected DB fallback to succeed")
	}
	if realID != rt.ID {
		t.Errorf("expected %s, got %s", rt.ID, realID)
	}

	// Second call should hit cache.
	realID2, ok2 := sm.Resolve(gt.ID)
	if !ok2 || realID2 != rt.ID {
		t.Error("expected cached result")
	}
}

func TestStickyManager_Rebind(t *testing.T) {
	s := testutil.NewTestStore(t)
	sm := NewStickyManager(s)

	gt, _ := s.CreateGateToken("gate1")
	rt1, _ := s.CreateRealToken("real1", "acc1", "ref1")
	rt2, _ := s.CreateRealToken("real2", "acc2", "ref2")

	_ = sm.Bind(gt.ID, rt1.ID, 10*time.Minute)
	_ = sm.Bind(gt.ID, rt2.ID, 10*time.Minute)

	realID, ok := sm.Resolve(gt.ID)
	if !ok {
		t.Fatal("expected resolve to succeed after rebind")
	}
	if realID != rt2.ID {
		t.Errorf("expected %s after rebind, got %s", rt2.ID, realID)
	}
}

func TestStickyManager_ResolveMiss(t *testing.T) {
	s := testutil.NewTestStore(t)
	sm := NewStickyManager(s)

	_, ok := sm.Resolve("nonexistent")
	if ok {
		t.Error("expected miss for unknown gate token")
	}
}

func TestStickyManager_StartStop(t *testing.T) {
	s := testutil.NewTestStore(t)
	sm := NewStickyManager(s)

	ctx := context.Background()
	sm.Start(ctx)
	sm.Stop()
	// Should not hang or panic.
}

func TestStickyManager_CleanupRemovesExpired(t *testing.T) {
	s := testutil.NewTestStore(t)
	sm := NewStickyManager(s)

	// Add an already-expired entry to cache directly.
	sm.cache.Store("gate-expired", &stickyEntry{
		realTokenID: "real-1",
		expiresAt:   time.Now().Add(-1 * time.Minute),
	})

	sm.cleanup()

	_, ok := sm.cache.Load("gate-expired")
	if ok {
		t.Error("expected cleanup to remove expired entry from cache")
	}
}
