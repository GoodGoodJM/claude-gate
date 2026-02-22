package token

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ggmolly/claude-gate/testutil"
)

func TestManager_ResolveToken_RoundRobin(t *testing.T) {
	s := testutil.NewTestStore(t)

	s.CreateRealToken("tok1", "acc1", "ref1")
	s.CreateRealToken("tok2", "acc2", "ref2")

	m := NewManager(s, 5, 10*time.Minute)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop()

	// Create distinct gate tokens for round-robin distribution.
	seen := make(map[string]bool)
	for i := 0; i < 4; i++ {
		gt, _ := s.CreateGateToken(fmt.Sprintf("gate%d", i))
		tok, err := m.ResolveToken(gt.ID)
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

	s.CreateRealToken("tok1", "acc1", "ref1")
	s.CreateRealToken("tok2", "acc2", "ref2")

	m := NewManager(s, 5, 10*time.Minute)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop()

	gt, _ := s.CreateGateToken("gate-sticky")

	// First resolve creates a sticky binding.
	first, err := m.ResolveToken(gt.ID)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}

	// Subsequent resolves should return the same token.
	for i := 0; i < 5; i++ {
		tok, err := m.ResolveToken(gt.ID)
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

	s.CreateRealToken("tok1", "acc1", "ref1")

	m := NewManager(s, 5, 1*time.Millisecond) // Very short TTL.
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop()

	gt, _ := s.CreateGateToken("gate-expiry")

	_, err := m.ResolveToken(gt.ID)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}

	time.Sleep(5 * time.Millisecond)

	// After expiry, should still resolve (just re-selects via round-robin).
	tok, err := m.ResolveToken(gt.ID)
	if err != nil {
		t.Fatalf("resolve after expiry: %v", err)
	}
	if tok == nil {
		t.Fatal("expected non-nil token")
	}
}

func TestManager_ResolveToken_NoTokens(t *testing.T) {
	s := testutil.NewTestStore(t)

	m := NewManager(s, 5, 10*time.Minute)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop()

	gt, _ := s.CreateGateToken("gate1")
	_, err := m.ResolveToken(gt.ID)
	if err != ErrNoAvailableTokens {
		t.Fatalf("expected ErrNoAvailableTokens, got %v", err)
	}
}

func TestManager_RecordFailure(t *testing.T) {
	s := testutil.NewTestStore(t)

	tok, _ := s.CreateRealToken("tok1", "acc1", "ref1")

	m := NewManager(s, 3, 10*time.Minute)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop()

	// Record failures up to but not exceeding the threshold.
	for i := 0; i < 2; i++ {
		if err := m.RecordFailure(tok.ID); err != nil {
			t.Fatalf("record failure %d: %v", i, err)
		}
	}

	// Token should still be in the pool.
	if m.pool.Len() != 1 {
		t.Fatalf("expected 1 token in pool, got %d", m.pool.Len())
	}

	// One more failure should deactivate.
	if err := m.RecordFailure(tok.ID); err != nil {
		t.Fatalf("record failure final: %v", err)
	}

	if m.pool.Len() != 0 {
		t.Fatalf("expected 0 tokens in pool after deactivation, got %d", m.pool.Len())
	}
}

func TestManager_RecordFailure_AllDeactivated(t *testing.T) {
	s := testutil.NewTestStore(t)

	tok, _ := s.CreateRealToken("tok1", "acc1", "ref1")

	m := NewManager(s, 1, 10*time.Minute)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop()

	if err := m.RecordFailure(tok.ID); err != nil {
		t.Fatalf("record failure: %v", err)
	}

	gt, _ := s.CreateGateToken("gate1")
	_, err := m.ResolveToken(gt.ID)
	if err != ErrNoAvailableTokens {
		t.Fatalf("expected ErrNoAvailableTokens, got %v", err)
	}
}

func TestManager_RefreshPool(t *testing.T) {
	s := testutil.NewTestStore(t)

	m := NewManager(s, 5, 10*time.Minute)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop()

	if m.pool.Len() != 0 {
		t.Fatalf("expected 0 tokens initially, got %d", m.pool.Len())
	}

	s.CreateRealToken("tok1", "acc1", "ref1")
	if err := m.RefreshPool(); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if m.pool.Len() != 1 {
		t.Fatalf("expected 1 token after refresh, got %d", m.pool.Len())
	}
}

func TestManager_StickyFallsThrough_WhenTokenDeactivated(t *testing.T) {
	s := testutil.NewTestStore(t)

	s.CreateRealToken("tok1", "acc1", "ref1")
	s.CreateRealToken("tok2", "acc2", "ref2")

	m := NewManager(s, 1, 10*time.Minute)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop()

	gt, _ := s.CreateGateToken("gate-test")

	// First resolve creates a sticky binding.
	first, err := m.ResolveToken(gt.ID)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}

	// Deactivate whichever token was selected.
	if err := m.RecordFailure(first.ID); err != nil {
		t.Fatalf("record failure: %v", err)
	}

	// Next resolve should fall through sticky and pick the other token.
	second, err := m.ResolveToken(gt.ID)
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}

	if second.ID == first.ID {
		t.Error("expected fallthrough to a different token after deactivation")
	}
}
