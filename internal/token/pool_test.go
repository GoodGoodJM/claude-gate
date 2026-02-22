package token

import (
	"testing"

	"github.com/ggmolly/claude-gate/testutil"
)

func TestTokenPool_RoundRobin(t *testing.T) {
	s := testutil.NewTestStore(t)

	_, _ = s.CreateRealToken("tok1", "acc1", "ref1")
	_, _ = s.CreateRealToken("tok2", "acc2", "ref2")
	_, _ = s.CreateRealToken("tok3", "acc3", "ref3")

	pool := NewTokenPool(s, 5)
	if err := pool.Refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if pool.Len() != 3 {
		t.Fatalf("expected 3 tokens, got %d", pool.Len())
	}

	// Collect 6 selections; should cycle through all 3 tokens twice.
	seen := make(map[string]int)
	for i := 0; i < 6; i++ {
		tok, err := pool.Select(nil)
		if err != nil {
			t.Fatalf("select %d: %v", i, err)
		}
		seen[tok.Name]++
	}

	for _, name := range []string{"tok1", "tok2", "tok3"} {
		if seen[name] != 2 {
			t.Errorf("expected %s selected 2 times, got %d", name, seen[name])
		}
	}
}

func TestTokenPool_ExcludeIDs(t *testing.T) {
	s := testutil.NewTestStore(t)

	t1, _ := s.CreateRealToken("tok1", "acc1", "ref1")
	t2, _ := s.CreateRealToken("tok2", "acc2", "ref2")

	pool := NewTokenPool(s, 5)
	if err := pool.Refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	// Exclude t1; should always get t2.
	exclude := map[string]bool{t1.ID: true}
	for i := 0; i < 5; i++ {
		tok, err := pool.Select(exclude)
		if err != nil {
			t.Fatalf("select: %v", err)
		}
		if tok.ID != t2.ID {
			t.Errorf("expected %s, got %s", t2.ID, tok.ID)
		}
	}
}

func TestTokenPool_AllExcluded(t *testing.T) {
	s := testutil.NewTestStore(t)

	t1, _ := s.CreateRealToken("tok1", "acc1", "ref1")

	pool := NewTokenPool(s, 5)
	if err := pool.Refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	exclude := map[string]bool{t1.ID: true}
	_, err := pool.Select(exclude)
	if err != ErrNoAvailableTokens {
		t.Fatalf("expected ErrNoAvailableTokens, got %v", err)
	}
}

func TestTokenPool_EmptyPool(t *testing.T) {
	s := testutil.NewTestStore(t)

	pool := NewTokenPool(s, 5)
	if err := pool.Refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	_, err := pool.Select(nil)
	if err != ErrNoAvailableTokens {
		t.Fatalf("expected ErrNoAvailableTokens, got %v", err)
	}
}

func TestTokenPool_FiltersInactiveTokens(t *testing.T) {
	s := testutil.NewTestStore(t)

	t1, _ := s.CreateRealToken("active", "acc1", "ref1")
	t2, _ := s.CreateRealToken("inactive", "acc2", "ref2")
	_ = s.SetRealTokenActive(t2.ID, false)

	pool := NewTokenPool(s, 5)
	if err := pool.Refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if pool.Len() != 1 {
		t.Fatalf("expected 1 token, got %d", pool.Len())
	}

	tok, err := pool.Select(nil)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if tok.ID != t1.ID {
		t.Errorf("expected active token %s, got %s", t1.ID, tok.ID)
	}
}

func TestTokenPool_FiltersHighFailureTokens(t *testing.T) {
	s := testutil.NewTestStore(t)

	t1, _ := s.CreateRealToken("healthy", "acc1", "ref1")
	t2, _ := s.CreateRealToken("failing", "acc2", "ref2")

	// Push t2 to 3 failures with maxFailures=3.
	for i := 0; i < 3; i++ {
		_ = s.IncrementRealTokenFailure(t2.ID)
	}

	pool := NewTokenPool(s, 3)
	if err := pool.Refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if pool.Len() != 1 {
		t.Fatalf("expected 1 token, got %d", pool.Len())
	}

	tok, err := pool.Select(nil)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if tok.ID != t1.ID {
		t.Errorf("expected healthy token %s, got %s", t1.ID, tok.ID)
	}
}

func TestTokenPool_GetByID(t *testing.T) {
	s := testutil.NewTestStore(t)

	created, _ := s.CreateRealToken("tok1", "acc1", "ref1")

	pool := NewTokenPool(s, 5)
	_ = pool.Refresh()

	got := pool.GetByID(created.ID)
	if got == nil {
		t.Fatal("expected to find token by ID")
	}
	if got.Name != "tok1" {
		t.Errorf("expected name tok1, got %s", got.Name)
	}

	if pool.GetByID("nonexistent") != nil {
		t.Error("expected nil for nonexistent ID")
	}
}
