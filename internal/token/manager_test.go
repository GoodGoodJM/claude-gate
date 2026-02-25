package token

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ggmolly/claude-gate/internal/logging"
	"github.com/ggmolly/claude-gate/testutil"
)

func TestManager_ResolveToken_RoundRobin(t *testing.T) {
	s := testutil.NewTestStore(t)
	ctx := context.Background()

	_, _ = s.CreateRealToken(ctx, "tok1", "acc1", "ref1")
	_, _ = s.CreateRealToken(ctx, "tok2", "acc2", "ref2")

	m := NewManager(s, 5, 10*time.Minute, logging.Discard())
	if err := m.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop()

	// Create distinct gate tokens for round-robin distribution.
	seen := make(map[string]bool)
	for i := range 4 {
		gt, _ := s.CreateGateToken(ctx, fmt.Sprintf("gate%d", i), "")
		tok, err := m.ResolveToken(ctx, gt.ID)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		seen[tok.Name] = true
	}

	if len(seen) < 2 {
		t.Errorf("expected round-robin across tokens, only saw %d unique", len(seen))
	}
}

func TestManager_ResolveToken_StickySession(t *testing.T) {
	s := testutil.NewTestStore(t)
	ctx := context.Background()

	_, _ = s.CreateRealToken(ctx, "tok1", "acc1", "ref1")
	_, _ = s.CreateRealToken(ctx, "tok2", "acc2", "ref2")

	m := NewManager(s, 5, 10*time.Minute, logging.Discard())
	if err := m.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop()

	gt, _ := s.CreateGateToken(ctx, "gate-sticky", "")

	// First resolve creates a sticky binding.
	first, err := m.ResolveToken(ctx, gt.ID)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}

	// Subsequent resolves should return the same token.
	for i := range 5 {
		tok, err := m.ResolveToken(ctx, gt.ID)
		if err != nil {
			t.Fatalf("resolve %d: %v", i, err)
		}
		if tok.ID != first.ID {
			t.Errorf("expected sticky token %s, got %s", first.ID, tok.ID)
		}
	}
}

func TestManager_ResolveToken_StickyExpiry(t *testing.T) {
	s := testutil.NewTestStore(t)
	ctx := context.Background()

	_, _ = s.CreateRealToken(ctx, "tok1", "acc1", "ref1")

	m := NewManager(s, 5, 1*time.Millisecond, logging.Discard()) // Very short TTL.
	if err := m.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop()

	gt, _ := s.CreateGateToken(ctx, "gate-expiry", "")

	_, err := m.ResolveToken(ctx, gt.ID)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}

	time.Sleep(5 * time.Millisecond)

	// After expiry, should still resolve (just re-selects via round-robin).
	tok, err := m.ResolveToken(ctx, gt.ID)
	if err != nil {
		t.Fatalf("resolve after expiry: %v", err)
	}
	if tok == nil {
		t.Fatal("expected non-nil token")
	}
}

func TestManager_ResolveToken_NoTokens(t *testing.T) {
	s := testutil.NewTestStore(t)
	ctx := context.Background()

	m := NewManager(s, 5, 10*time.Minute, logging.Discard())
	if err := m.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop()

	gt, _ := s.CreateGateToken(ctx, "gate1", "")
	_, err := m.ResolveToken(ctx, gt.ID)
	if err != ErrNoAvailableTokens {
		t.Fatalf("expected ErrNoAvailableTokens, got %v", err)
	}
}

func TestManager_RecordFailure(t *testing.T) {
	s := testutil.NewTestStore(t)
	ctx := context.Background()

	tok, _ := s.CreateRealToken(ctx, "tok1", "acc1", "ref1")

	m := NewManager(s, 3, 10*time.Minute, logging.Discard())
	if err := m.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop()

	// Record failures up to but not exceeding the threshold.
	for i := range 2 {
		if err := m.RecordFailure(ctx, tok.ID); err != nil {
			t.Fatalf("record failure %d: %v", i, err)
		}
	}

	time.Sleep(200 * time.Millisecond) // wait for debounced refresh

	// Token should still be in the pool.
	if m.pool.Len() != 1 {
		t.Fatalf("expected 1 token in pool, got %d", m.pool.Len())
	}

	// One more failure should deactivate.
	if err := m.RecordFailure(ctx, tok.ID); err != nil {
		t.Fatalf("record failure final: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // wait for debounced refresh

	if m.pool.Len() != 0 {
		t.Fatalf("expected 0 tokens in pool after deactivation, got %d", m.pool.Len())
	}
}

func TestManager_RecordFailure_AllDeactivated(t *testing.T) {
	s := testutil.NewTestStore(t)
	ctx := context.Background()

	tok, _ := s.CreateRealToken(ctx, "tok1", "acc1", "ref1")

	m := NewManager(s, 1, 10*time.Minute, logging.Discard())
	if err := m.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop()

	if err := m.RecordFailure(ctx, tok.ID); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // wait for debounced refresh

	gt, _ := s.CreateGateToken(ctx, "gate1", "")
	_, err := m.ResolveToken(ctx, gt.ID)
	if err != ErrNoAvailableTokens {
		t.Fatalf("expected ErrNoAvailableTokens, got %v", err)
	}
}

func TestManager_RefreshPool(t *testing.T) {
	s := testutil.NewTestStore(t)
	ctx := context.Background()

	m := NewManager(s, 5, 10*time.Minute, logging.Discard())
	if err := m.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop()

	if m.pool.Len() != 0 {
		t.Fatalf("expected 0 tokens initially, got %d", m.pool.Len())
	}

	_, _ = s.CreateRealToken(ctx, "tok1", "acc1", "ref1")
	m.RefreshPool()
	time.Sleep(200 * time.Millisecond) // wait for debounced refresh

	if m.pool.Len() != 1 {
		t.Fatalf("expected 1 token after refresh, got %d", m.pool.Len())
	}
}

func TestManager_StickyFallsThrough_WhenTokenDeactivated(t *testing.T) {
	s := testutil.NewTestStore(t)
	ctx := context.Background()

	_, _ = s.CreateRealToken(ctx, "tok1", "acc1", "ref1")
	_, _ = s.CreateRealToken(ctx, "tok2", "acc2", "ref2")

	m := NewManager(s, 1, 10*time.Minute, logging.Discard())
	if err := m.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop()

	gt, _ := s.CreateGateToken(ctx, "gate-test", "")

	// First resolve creates a sticky binding.
	first, err := m.ResolveToken(ctx, gt.ID)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}

	// Deactivate whichever token was selected.
	if err := m.RecordFailure(ctx, first.ID); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // wait for debounced refresh

	// Next resolve should fall through sticky and pick the other token.
	second, err := m.ResolveToken(ctx, gt.ID)
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}

	if second.ID == first.ID {
		t.Error("expected fallthrough to a different token after deactivation")
	}
}
