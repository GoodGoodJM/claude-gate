package token

import (
	"context"
	"testing"
	"time"

	"github.com/ggmolly/claude-gate/internal/logging"
	"github.com/ggmolly/claude-gate/testutil"
)

func TestStickyManager_BindAndResolve(t *testing.T) {
	s := testutil.NewTestStore(t)
	ctx := context.Background()
	sm := NewStickyManager(s, logging.Discard())

	gt, _ := s.CreateGateToken(ctx, "gate1", "")
	rt, _ := s.CreateRealToken(ctx, "real1", "acc1", "ref1")

	err := sm.Bind(ctx, gt.ID, rt.ID, 10*time.Minute)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}

	realID, ok := sm.Resolve(ctx, gt.ID)
	if !ok {
		t.Fatal("expected resolve to succeed")
	}
	if realID != rt.ID {
		t.Errorf("expected %s, got %s", rt.ID, realID)
	}
}

func TestStickyManager_Expiry(t *testing.T) {
	s := testutil.NewTestStore(t)
	ctx := context.Background()
	sm := NewStickyManager(s, logging.Discard())

	gt, _ := s.CreateGateToken(ctx, "gate1", "")
	rt, _ := s.CreateRealToken(ctx, "real1", "acc1", "ref1")

	// Bind with a very short TTL.
	err := sm.Bind(ctx, gt.ID, rt.ID, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}

	// Should resolve immediately.
	if _, ok := sm.Resolve(ctx, gt.ID); !ok {
		t.Fatal("expected resolve to succeed before expiry")
	}

	// Wait for expiry.
	time.Sleep(100 * time.Millisecond)

	// Remove DB entry so the DB fallback also returns nothing.
	_ = s.DeleteStickySession(ctx, gt.ID)

	_, ok := sm.Resolve(ctx, gt.ID)
	if ok {
		t.Error("expected expired session to not resolve")
	}
}

func TestStickyManager_DBFallback(t *testing.T) {
	s := testutil.NewTestStore(t)
	ctx := context.Background()
	sm := NewStickyManager(s, logging.Discard())

	gt, _ := s.CreateGateToken(ctx, "gate1", "")
	rt, _ := s.CreateRealToken(ctx, "real1", "acc1", "ref1")

	// Write to DB directly (use UTC to match SQLite datetime('now')).
	err := s.UpsertStickySession(ctx, gt.ID, rt.ID, time.Now().UTC().Add(10*time.Minute))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// sm's cache is empty, so it should fall back to DB.
	realID, ok := sm.Resolve(ctx, gt.ID)
	if !ok {
		t.Fatal("expected DB fallback to succeed")
	}
	if realID != rt.ID {
		t.Errorf("expected %s, got %s", rt.ID, realID)
	}

	// Second call should hit cache.
	realID2, ok2 := sm.Resolve(ctx, gt.ID)
	if !ok2 || realID2 != rt.ID {
		t.Error("expected cached result")
	}
}

func TestStickyManager_Rebind(t *testing.T) {
	s := testutil.NewTestStore(t)
	ctx := context.Background()
	sm := NewStickyManager(s, logging.Discard())

	gt, _ := s.CreateGateToken(ctx, "gate1", "")
	rt1, _ := s.CreateRealToken(ctx, "real1", "acc1", "ref1")
	rt2, _ := s.CreateRealToken(ctx, "real2", "acc2", "ref2")

	_ = sm.Bind(ctx, gt.ID, rt1.ID, 10*time.Minute)
	_ = sm.Bind(ctx, gt.ID, rt2.ID, 10*time.Minute)

	realID, ok := sm.Resolve(ctx, gt.ID)
	if !ok {
		t.Fatal("expected resolve to succeed after rebind")
	}
	if realID != rt2.ID {
		t.Errorf("expected %s after rebind, got %s", rt2.ID, realID)
	}
}

func TestStickyManager_ResolveMiss(t *testing.T) {
	s := testutil.NewTestStore(t)
	ctx := context.Background()
	sm := NewStickyManager(s, logging.Discard())

	_, ok := sm.Resolve(ctx, "nonexistent")
	if ok {
		t.Error("expected miss for unknown gate token")
	}
}

func TestStickyManager_StartStop(t *testing.T) {
	s := testutil.NewTestStore(t)
	sm := NewStickyManager(s, logging.Discard())

	ctx := context.Background()
	sm.Start(ctx)
	sm.Stop()
	// Should not hang or panic.
}

func TestStickyManager_ExpiredNotResolved(t *testing.T) {
	s := testutil.NewTestStore(t)
	ctx := context.Background()
	sm := NewStickyManager(s, logging.Discard())

	gt, _ := s.CreateGateToken(ctx, "gate1", "")
	rt, _ := s.CreateRealToken(ctx, "real1", "acc1", "ref1")

	// Bind with a very short TTL and wait for it to expire.
	err := sm.Bind(ctx, gt.ID, rt.ID, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// DB entry is also expired (50ms TTL means expiresAt is in the past).
	// Resolve should return false for both cache and DB.
	_, ok := sm.Resolve(ctx, gt.ID)
	if ok {
		// DB may still have the row but with a past expiresAt. The DB query
		// filters by expires_at > datetime('now'), so it should not return it.
		t.Error("expected expired entry to not resolve")
	}
}
