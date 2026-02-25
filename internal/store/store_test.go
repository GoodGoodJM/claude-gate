package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := New(dbPath)
	if err != nil {
		t.Fatalf("New(%s) failed: %v", dbPath, err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// ---------------------------------------------------------------------------
// Store creation and migration
// ---------------------------------------------------------------------------

func TestNew(t *testing.T) {
	s := newTestStore(t)
	if s.WriteDB() == nil {
		t.Fatal("WriteDB() is nil")
	}
	if s.ReadDB() == nil {
		t.Fatal("ReadDB() is nil")
	}

	// Verify migrations ran by checking that all tables exist
	for _, table := range []string{"real_tokens", "gate_tokens", "usage_logs", "sticky_sessions", "schema_migrations"} {
		var name string
		err := s.ReadDB().QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("table %s not found after migration: %v", table, err)
		}
	}
}

func TestNewIdempotentMigration(t *testing.T) {
	s := newTestStore(t)
	// Running migrate again should be a no-op
	if err := s.migrate(); err != nil {
		t.Fatalf("second migrate failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Real token CRUD
// ---------------------------------------------------------------------------

func TestCreateRealToken(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rt, err := s.CreateRealToken(ctx, "test-key", "acc-123", "ref-456")
	if err != nil {
		t.Fatalf("CreateRealToken: %v", err)
	}
	if rt.ID == "" {
		t.Error("expected non-empty ID")
	}
	if rt.Name != "test-key" {
		t.Errorf("Name = %q, want %q", rt.Name, "test-key")
	}
	if rt.AccessToken != "acc-123" {
		t.Errorf("AccessToken = %q, want %q", rt.AccessToken, "acc-123")
	}
	if rt.RefreshToken != "ref-456" {
		t.Errorf("RefreshToken = %q, want %q", rt.RefreshToken, "ref-456")
	}
	if !rt.IsActive {
		t.Error("expected IsActive = true")
	}
	if rt.FailureCount != 0 {
		t.Errorf("FailureCount = %d, want 0", rt.FailureCount)
	}
	if rt.TotalInputTokens != 0 || rt.TotalOutputTokens != 0 {
		t.Errorf("expected zero token counts, got input=%d output=%d", rt.TotalInputTokens, rt.TotalOutputTokens)
	}
}

func TestGetRealToken(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, _ := s.CreateRealToken(ctx, "k", "a", "r")
	got, err := s.GetRealToken(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetRealToken: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("ID = %q, want %q", got.ID, created.ID)
	}
	if got.Name != "k" {
		t.Errorf("Name = %q, want %q", got.Name, "k")
	}
}

func TestGetRealTokenNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.GetRealToken(ctx, "nonexistent")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestListRealTokens(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, _ = s.CreateRealToken(ctx, "a", "a1", "r1")
	_, _ = s.CreateRealToken(ctx, "b", "a2", "r2")
	_, _ = s.CreateRealToken(ctx, "c", "a3", "r3")

	tokens, err := s.ListRealTokens(ctx)
	if err != nil {
		t.Fatalf("ListRealTokens: %v", err)
	}
	if len(tokens) != 3 {
		t.Fatalf("len = %d, want 3", len(tokens))
	}
}

func TestUpdateRealToken(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rt, _ := s.CreateRealToken(ctx, "old", "a", "r")
	if err := s.UpdateRealToken(ctx, rt.ID, "new-name"); err != nil {
		t.Fatalf("UpdateRealToken: %v", err)
	}

	got, _ := s.GetRealToken(ctx, rt.ID)
	if got.Name != "new-name" {
		t.Errorf("Name = %q, want %q", got.Name, "new-name")
	}
}

func TestUpdateRealTokenNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.UpdateRealToken(ctx, "nonexistent", "x")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestDeleteRealToken(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rt, _ := s.CreateRealToken(ctx, "del", "a", "r")
	if err := s.DeleteRealToken(ctx, rt.ID); err != nil {
		t.Fatalf("DeleteRealToken: %v", err)
	}

	_, err := s.GetRealToken(ctx, rt.ID)
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows after delete, got %v", err)
	}
}

func TestDeleteRealTokenNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.DeleteRealToken(ctx, "nonexistent")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Real token operations
// ---------------------------------------------------------------------------

func TestSetRealTokenActive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rt, _ := s.CreateRealToken(ctx, "act", "a", "r")

	// Deactivate
	if err := s.SetRealTokenActive(ctx, rt.ID, false); err != nil {
		t.Fatalf("SetRealTokenActive(false): %v", err)
	}
	got, _ := s.GetRealToken(ctx, rt.ID)
	if got.IsActive {
		t.Error("expected IsActive = false after deactivation")
	}

	// Reactivate -- should reset failure_count
	_ = s.IncrementRealTokenFailure(ctx, rt.ID)
	_ = s.IncrementRealTokenFailure(ctx, rt.ID)
	got, _ = s.GetRealToken(ctx, rt.ID)
	if got.FailureCount != 2 {
		t.Fatalf("FailureCount = %d, want 2", got.FailureCount)
	}

	if err := s.SetRealTokenActive(ctx, rt.ID, true); err != nil {
		t.Fatalf("SetRealTokenActive(true): %v", err)
	}
	got, _ = s.GetRealToken(ctx, rt.ID)
	if !got.IsActive {
		t.Error("expected IsActive = true after reactivation")
	}
	if got.FailureCount != 0 {
		t.Errorf("expected FailureCount = 0 after reactivation, got %d", got.FailureCount)
	}
}

func TestSetRealTokenActiveNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.SetRealTokenActive(ctx, "nonexistent", true)
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestIncrementRealTokenFailure(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rt, _ := s.CreateRealToken(ctx, "fail", "a", "r")

	for i := 1; i <= 3; i++ {
		if err := s.IncrementRealTokenFailure(ctx, rt.ID); err != nil {
			t.Fatalf("IncrementRealTokenFailure #%d: %v", i, err)
		}
	}

	got, _ := s.GetRealToken(ctx, rt.ID)
	if got.FailureCount != 3 {
		t.Errorf("FailureCount = %d, want 3", got.FailureCount)
	}
	if got.LastFailureAt == nil {
		t.Error("expected LastFailureAt to be set")
	}
}

func TestUpdateRealTokenUsage(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rt, _ := s.CreateRealToken(ctx, "usage", "a", "r")

	if err := s.UpdateRealTokenUsage(ctx, rt.ID, 100, 50); err != nil {
		t.Fatalf("UpdateRealTokenUsage: %v", err)
	}
	if err := s.UpdateRealTokenUsage(ctx, rt.ID, 200, 75); err != nil {
		t.Fatalf("UpdateRealTokenUsage: %v", err)
	}

	got, _ := s.GetRealToken(ctx, rt.ID)
	if got.TotalInputTokens != 300 {
		t.Errorf("TotalInputTokens = %d, want 300", got.TotalInputTokens)
	}
	if got.TotalOutputTokens != 125 {
		t.Errorf("TotalOutputTokens = %d, want 125", got.TotalOutputTokens)
	}
	if got.LastUsedAt == nil {
		t.Error("expected LastUsedAt to be set")
	}
}

func TestGetRealTokenByAccessToken(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, _ := s.CreateRealToken(ctx, "by-acc", "unique-acc", "r")
	got, err := s.GetRealTokenByAccessToken(ctx, "unique-acc")
	if err != nil {
		t.Fatalf("GetRealTokenByAccessToken: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("ID = %q, want %q", got.ID, created.ID)
	}
}

func TestGetRealTokenByAccessTokenNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.GetRealTokenByAccessToken(ctx, "nope")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Active token filtering
// ---------------------------------------------------------------------------

func TestListActiveRealTokens(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rt1, _ := s.CreateRealToken(ctx, "active-ok", "a1", "r1")
	rt2, _ := s.CreateRealToken(ctx, "active-fail", "a2", "r2")
	rt3, _ := s.CreateRealToken(ctx, "inactive", "a3", "r3")

	// rt2: increment failures past threshold
	for range 5 {
		_ = s.IncrementRealTokenFailure(ctx, rt2.ID)
	}

	// rt3: deactivate
	_ = s.SetRealTokenActive(ctx, rt3.ID, false)

	// maxFailures=3 should only include rt1
	tokens, err := s.ListActiveRealTokens(ctx, 3)
	if err != nil {
		t.Fatalf("ListActiveRealTokens: %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("len = %d, want 1", len(tokens))
	}
	if tokens[0].ID != rt1.ID {
		t.Errorf("expected token %s, got %s", rt1.ID, tokens[0].ID)
	}

	// maxFailures=10 should include rt1 and rt2 (both active, rt2 has 5 failures)
	tokens, err = s.ListActiveRealTokens(ctx, 10)
	if err != nil {
		t.Fatalf("ListActiveRealTokens: %v", err)
	}
	if len(tokens) != 2 {
		t.Fatalf("len = %d, want 2", len(tokens))
	}
}

// ---------------------------------------------------------------------------
// Gate token CRUD
// ---------------------------------------------------------------------------

func TestCreateGateToken(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	gt, err := s.CreateGateToken(ctx, "my-gate", "")
	if err != nil {
		t.Fatalf("CreateGateToken: %v", err)
	}
	if gt.ID == "" {
		t.Error("expected non-empty ID")
	}
	if !strings.HasPrefix(gt.Token, "gate-") {
		t.Errorf("Token = %q, expected prefix 'gate-'", gt.Token)
	}
	if gt.Name != "my-gate" {
		t.Errorf("Name = %q, want %q", gt.Name, "my-gate")
	}
	if !gt.IsActive {
		t.Error("expected IsActive = true")
	}
}

func TestGateTokenFormat(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Create multiple tokens and verify they all start with "gate-"
	for i := range 10 {
		gt, err := s.CreateGateToken(ctx, "fmt-test", "")
		if err != nil {
			t.Fatalf("CreateGateToken #%d: %v", i, err)
		}
		if !strings.HasPrefix(gt.Token, "gate-") {
			t.Errorf("token #%d = %q, missing 'gate-' prefix", i, gt.Token)
		}
		// Ensure lowercase (base32 encoded, lowered)
		if gt.Token != strings.ToLower(gt.Token) {
			t.Errorf("token #%d = %q, expected lowercase", i, gt.Token)
		}
	}
}

func TestGetGateToken(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, _ := s.CreateGateToken(ctx, "g", "")
	got, err := s.GetGateToken(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetGateToken: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("ID = %q, want %q", got.ID, created.ID)
	}
	if got.Token != created.Token {
		t.Errorf("Token = %q, want %q", got.Token, created.Token)
	}
}

func TestGetGateTokenNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.GetGateToken(ctx, "nonexistent")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestGetGateTokenByToken(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, _ := s.CreateGateToken(ctx, "by-tok", "")
	got, err := s.GetGateTokenByToken(ctx, created.Token)
	if err != nil {
		t.Fatalf("GetGateTokenByToken: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("ID = %q, want %q", got.ID, created.ID)
	}
}

func TestGetGateTokenByTokenNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.GetGateTokenByToken(ctx, "gate-nonexistent")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestListGateTokens(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, _ = s.CreateGateToken(ctx, "g1", "")
	_, _ = s.CreateGateToken(ctx, "g2", "")

	tokens, err := s.ListGateTokens(ctx)
	if err != nil {
		t.Fatalf("ListGateTokens: %v", err)
	}
	if len(tokens) != 2 {
		t.Fatalf("len = %d, want 2", len(tokens))
	}
}

func TestUpdateGateToken(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	gt, _ := s.CreateGateToken(ctx, "old-gate", "")
	if err := s.UpdateGateToken(ctx, gt.ID, "new-gate"); err != nil {
		t.Fatalf("UpdateGateToken: %v", err)
	}

	got, _ := s.GetGateToken(ctx, gt.ID)
	if got.Name != "new-gate" {
		t.Errorf("Name = %q, want %q", got.Name, "new-gate")
	}
}

func TestUpdateGateTokenNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.UpdateGateToken(ctx, "nonexistent", "x")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Gate token operations
// ---------------------------------------------------------------------------

func TestSetGateTokenActive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	gt, _ := s.CreateGateToken(ctx, "toggle", "")

	// Deactivate
	if err := s.SetGateTokenActive(ctx, gt.ID, false); err != nil {
		t.Fatalf("SetGateTokenActive(false): %v", err)
	}
	got, _ := s.GetGateToken(ctx, gt.ID)
	if got.IsActive {
		t.Error("expected IsActive = false")
	}

	// Reactivate
	if err := s.SetGateTokenActive(ctx, gt.ID, true); err != nil {
		t.Fatalf("SetGateTokenActive(true): %v", err)
	}
	got, _ = s.GetGateToken(ctx, gt.ID)
	if !got.IsActive {
		t.Error("expected IsActive = true")
	}
}

func TestSetGateTokenActiveNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.SetGateTokenActive(ctx, "nonexistent", true)
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestDeleteGateToken(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	gt, _ := s.CreateGateToken(ctx, "del-gate", "")
	if err := s.DeleteGateToken(ctx, gt.ID); err != nil {
		t.Fatalf("DeleteGateToken: %v", err)
	}

	_, err := s.GetGateToken(ctx, gt.ID)
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows after delete, got %v", err)
	}
}

func TestDeleteGateTokenNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.DeleteGateToken(ctx, "nonexistent")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestUpdateGateTokenUsage(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	gt, _ := s.CreateGateToken(ctx, "usage-gate", "")

	if err := s.UpdateGateTokenUsage(ctx, gt.ID, 50, 25); err != nil {
		t.Fatalf("UpdateGateTokenUsage: %v", err)
	}
	if err := s.UpdateGateTokenUsage(ctx, gt.ID, 100, 75); err != nil {
		t.Fatalf("UpdateGateTokenUsage: %v", err)
	}

	got, _ := s.GetGateToken(ctx, gt.ID)
	if got.TotalInputTokens != 150 {
		t.Errorf("TotalInputTokens = %d, want 150", got.TotalInputTokens)
	}
	if got.TotalOutputTokens != 100 {
		t.Errorf("TotalOutputTokens = %d, want 100", got.TotalOutputTokens)
	}
}

// ---------------------------------------------------------------------------
// Usage log insert
// ---------------------------------------------------------------------------

func createTestTokens(t *testing.T, s *Store) (*RealToken, *GateToken) {
	t.Helper()
	ctx := context.Background()
	rt, err := s.CreateRealToken(ctx, "rt", "a", "r")
	if err != nil {
		t.Fatalf("CreateRealToken: %v", err)
	}
	gt, err := s.CreateGateToken(ctx, "gt", "")
	if err != nil {
		t.Fatalf("CreateGateToken: %v", err)
	}
	return rt, gt
}

func TestInsertUsageLog(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rt, gt := createTestTokens(t, s)

	log := &UsageLog{
		GateTokenID:              gt.ID,
		RealTokenID:              rt.ID,
		Model:                    "claude-3-opus",
		InputTokens:              1000,
		OutputTokens:             500,
		CacheCreationInputTokens: 200,
		CacheReadInputTokens:     100,
		RequestPath:              "/v1/messages",
		StatusCode:               200,
	}

	if err := s.InsertUsageLog(ctx, log); err != nil {
		t.Fatalf("InsertUsageLog: %v", err)
	}

	// Verify via ListUsageLogs
	logs, err := s.ListUsageLogs(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListUsageLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("len = %d, want 1", len(logs))
	}
	if logs[0].Model != "claude-3-opus" {
		t.Errorf("Model = %q, want %q", logs[0].Model, "claude-3-opus")
	}
	if logs[0].InputTokens != 1000 {
		t.Errorf("InputTokens = %d, want 1000", logs[0].InputTokens)
	}
	if logs[0].OutputTokens != 500 {
		t.Errorf("OutputTokens = %d, want 500", logs[0].OutputTokens)
	}
	if logs[0].StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", logs[0].StatusCode)
	}
}

func TestInsertUsageLogs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rt, gt := createTestTokens(t, s)

	batch := []UsageLog{
		{GateTokenID: gt.ID, RealTokenID: rt.ID, Model: "m1", InputTokens: 10, OutputTokens: 5, RequestPath: "/a", StatusCode: 200},
		{GateTokenID: gt.ID, RealTokenID: rt.ID, Model: "m2", InputTokens: 20, OutputTokens: 10, RequestPath: "/b", StatusCode: 200},
		{GateTokenID: gt.ID, RealTokenID: rt.ID, Model: "m3", InputTokens: 30, OutputTokens: 15, RequestPath: "/c", StatusCode: 500},
	}

	if err := s.InsertUsageLogs(ctx, batch); err != nil {
		t.Fatalf("InsertUsageLogs: %v", err)
	}

	logs, err := s.ListUsageLogs(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListUsageLogs: %v", err)
	}
	if len(logs) != 3 {
		t.Fatalf("len = %d, want 3", len(logs))
	}
}

func TestInsertUsageLogsEmpty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.InsertUsageLogs(ctx, nil); err != nil {
		t.Fatalf("InsertUsageLogs(nil): %v", err)
	}
}

// ---------------------------------------------------------------------------
// Usage stats aggregation
// ---------------------------------------------------------------------------

func TestGetUsageStats(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rt, gt := createTestTokens(t, s)

	since := time.Now().UTC().Add(-1 * time.Hour)

	batch := []UsageLog{
		{GateTokenID: gt.ID, RealTokenID: rt.ID, Model: "m", InputTokens: 100, OutputTokens: 50, CacheCreationInputTokens: 10, CacheReadInputTokens: 5, RequestPath: "/a", StatusCode: 200},
		{GateTokenID: gt.ID, RealTokenID: rt.ID, Model: "m", InputTokens: 200, OutputTokens: 100, CacheCreationInputTokens: 20, CacheReadInputTokens: 10, RequestPath: "/b", StatusCode: 200},
	}
	_ = s.InsertUsageLogs(ctx, batch)

	stats, err := s.GetUsageStats(ctx, since)
	if err != nil {
		t.Fatalf("GetUsageStats: %v", err)
	}
	if stats.TotalInputTokens != 300 {
		t.Errorf("TotalInputTokens = %d, want 300", stats.TotalInputTokens)
	}
	if stats.TotalOutputTokens != 150 {
		t.Errorf("TotalOutputTokens = %d, want 150", stats.TotalOutputTokens)
	}
	if stats.CacheCreationInputTokens != 30 {
		t.Errorf("CacheCreationInputTokens = %d, want 30", stats.CacheCreationInputTokens)
	}
	if stats.CacheReadInputTokens != 15 {
		t.Errorf("CacheReadInputTokens = %d, want 15", stats.CacheReadInputTokens)
	}
	if stats.RequestCount != 2 {
		t.Errorf("RequestCount = %d, want 2", stats.RequestCount)
	}
}

func TestGetUsageStatsEmpty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	stats, err := s.GetUsageStats(ctx, time.Now().UTC().Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("GetUsageStats: %v", err)
	}
	if stats.RequestCount != 0 {
		t.Errorf("RequestCount = %d, want 0", stats.RequestCount)
	}
}

func TestGetUsageStatsByRealToken(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rt1, _ := s.CreateRealToken(ctx, "rt1", "a1", "r1")
	rt2, _ := s.CreateRealToken(ctx, "rt2", "a2", "r2")
	gt, _ := s.CreateGateToken(ctx, "gt", "")

	since := time.Now().UTC().Add(-1 * time.Hour)

	_ = s.InsertUsageLog(ctx, &UsageLog{GateTokenID: gt.ID, RealTokenID: rt1.ID, Model: "m", InputTokens: 100, OutputTokens: 50, RequestPath: "/a", StatusCode: 200})
	_ = s.InsertUsageLog(ctx, &UsageLog{GateTokenID: gt.ID, RealTokenID: rt1.ID, Model: "m", InputTokens: 200, OutputTokens: 100, RequestPath: "/b", StatusCode: 200})
	_ = s.InsertUsageLog(ctx, &UsageLog{GateTokenID: gt.ID, RealTokenID: rt2.ID, Model: "m", InputTokens: 999, OutputTokens: 888, RequestPath: "/c", StatusCode: 200})

	stats, err := s.GetUsageStatsByRealToken(ctx, rt1.ID, since)
	if err != nil {
		t.Fatalf("GetUsageStatsByRealToken: %v", err)
	}
	if stats.TotalInputTokens != 300 {
		t.Errorf("TotalInputTokens = %d, want 300", stats.TotalInputTokens)
	}
	if stats.TotalOutputTokens != 150 {
		t.Errorf("TotalOutputTokens = %d, want 150", stats.TotalOutputTokens)
	}
	if stats.RequestCount != 2 {
		t.Errorf("RequestCount = %d, want 2", stats.RequestCount)
	}
}

func TestGetUsageStatsByGateToken(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rt, _ := s.CreateRealToken(ctx, "rt", "a", "r")
	gt1, _ := s.CreateGateToken(ctx, "gt1", "")
	gt2, _ := s.CreateGateToken(ctx, "gt2", "")

	since := time.Now().UTC().Add(-1 * time.Hour)

	_ = s.InsertUsageLog(ctx, &UsageLog{GateTokenID: gt1.ID, RealTokenID: rt.ID, Model: "m", InputTokens: 100, OutputTokens: 50, RequestPath: "/a", StatusCode: 200})
	_ = s.InsertUsageLog(ctx, &UsageLog{GateTokenID: gt2.ID, RealTokenID: rt.ID, Model: "m", InputTokens: 999, OutputTokens: 888, RequestPath: "/b", StatusCode: 200})

	stats, err := s.GetUsageStatsByGateToken(ctx, gt1.ID, since)
	if err != nil {
		t.Fatalf("GetUsageStatsByGateToken: %v", err)
	}
	if stats.TotalInputTokens != 100 {
		t.Errorf("TotalInputTokens = %d, want 100", stats.TotalInputTokens)
	}
	if stats.TotalOutputTokens != 50 {
		t.Errorf("TotalOutputTokens = %d, want 50", stats.TotalOutputTokens)
	}
	if stats.RequestCount != 1 {
		t.Errorf("RequestCount = %d, want 1", stats.RequestCount)
	}
}

func TestListUsageLogs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rt, gt := createTestTokens(t, s)

	for i := range 5 {
		_ = s.InsertUsageLog(ctx, &UsageLog{GateTokenID: gt.ID, RealTokenID: rt.ID, Model: "m", InputTokens: int64(i), RequestPath: "/x", StatusCode: 200})
	}

	// Test limit
	logs, err := s.ListUsageLogs(ctx, 3, 0)
	if err != nil {
		t.Fatalf("ListUsageLogs: %v", err)
	}
	if len(logs) != 3 {
		t.Errorf("len = %d, want 3", len(logs))
	}

	// Test offset
	logs, err = s.ListUsageLogs(ctx, 10, 3)
	if err != nil {
		t.Fatalf("ListUsageLogs with offset: %v", err)
	}
	if len(logs) != 2 {
		t.Errorf("len = %d, want 2", len(logs))
	}
}

// ---------------------------------------------------------------------------
// Sticky sessions
// ---------------------------------------------------------------------------

func TestUpsertAndGetStickySession(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rt, gt := createTestTokens(t, s)

	expires := time.Now().UTC().Add(1 * time.Hour)
	if err := s.UpsertStickySession(ctx, gt.ID, rt.ID, expires); err != nil {
		t.Fatalf("UpsertStickySession: %v", err)
	}

	ss, err := s.GetStickySession(ctx, gt.ID)
	if err != nil {
		t.Fatalf("GetStickySession: %v", err)
	}
	if ss == nil {
		t.Fatal("expected non-nil sticky session")
	}
	if ss.GateTokenID != gt.ID {
		t.Errorf("GateTokenID = %q, want %q", ss.GateTokenID, gt.ID)
	}
	if ss.RealTokenID != rt.ID {
		t.Errorf("RealTokenID = %q, want %q", ss.RealTokenID, rt.ID)
	}
}

func TestUpsertStickySessionUpdate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rt1, _ := s.CreateRealToken(ctx, "rt1", "a1", "r1")
	rt2, _ := s.CreateRealToken(ctx, "rt2", "a2", "r2")
	gt, _ := s.CreateGateToken(ctx, "gt", "")

	expires := time.Now().UTC().Add(1 * time.Hour)
	_ = s.UpsertStickySession(ctx, gt.ID, rt1.ID, expires)

	// Upsert with a different real token
	_ = s.UpsertStickySession(ctx, gt.ID, rt2.ID, expires)

	ss, _ := s.GetStickySession(ctx, gt.ID)
	if ss.RealTokenID != rt2.ID {
		t.Errorf("RealTokenID = %q, want %q after upsert", ss.RealTokenID, rt2.ID)
	}
}

func TestGetStickySessionNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	ss, err := s.GetStickySession(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetStickySession: %v", err)
	}
	if ss != nil {
		t.Errorf("expected nil for nonexistent sticky session, got %+v", ss)
	}
}

func TestGetStickySessionExpired(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rt, gt := createTestTokens(t, s)

	// Insert with past expiry
	past := time.Now().UTC().Add(-1 * time.Hour)
	_ = s.UpsertStickySession(ctx, gt.ID, rt.ID, past)

	ss, err := s.GetStickySession(ctx, gt.ID)
	if err != nil {
		t.Fatalf("GetStickySession: %v", err)
	}
	if ss != nil {
		t.Error("expected nil for expired sticky session")
	}
}

func TestDeleteStickySession(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rt, gt := createTestTokens(t, s)

	expires := time.Now().UTC().Add(1 * time.Hour)
	_ = s.UpsertStickySession(ctx, gt.ID, rt.ID, expires)

	if err := s.DeleteStickySession(ctx, gt.ID); err != nil {
		t.Fatalf("DeleteStickySession: %v", err)
	}

	ss, _ := s.GetStickySession(ctx, gt.ID)
	if ss != nil {
		t.Error("expected nil after delete")
	}
}

func TestDeleteExpiredStickySessions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rt, _ := createTestTokens(t, s)

	gt1, _ := s.CreateGateToken(ctx, "g1", "")
	gt2, _ := s.CreateGateToken(ctx, "g2", "")
	gt3, _ := s.CreateGateToken(ctx, "g3", "")

	past := time.Now().UTC().Add(-1 * time.Hour)
	future := time.Now().UTC().Add(1 * time.Hour)

	_ = s.UpsertStickySession(ctx, gt1.ID, rt.ID, past)
	_ = s.UpsertStickySession(ctx, gt2.ID, rt.ID, past)
	_ = s.UpsertStickySession(ctx, gt3.ID, rt.ID, future)

	n, err := s.DeleteExpiredStickySessions(ctx)
	if err != nil {
		t.Fatalf("DeleteExpiredStickySessions: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted = %d, want 2", n)
	}

	// gt3 should still exist
	ss, _ := s.GetStickySession(ctx, gt3.ID)
	if ss == nil {
		t.Error("expected gt3 sticky session to survive cleanup")
	}
}

// ---------------------------------------------------------------------------
// Cascade deletes
// ---------------------------------------------------------------------------

func TestDeleteRealTokenCascade(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rt, gt := createTestTokens(t, s)

	// Create usage logs referencing the real token
	_ = s.InsertUsageLog(ctx, &UsageLog{GateTokenID: gt.ID, RealTokenID: rt.ID, Model: "m", InputTokens: 100, OutputTokens: 50, RequestPath: "/a", StatusCode: 200})
	_ = s.InsertUsageLog(ctx, &UsageLog{GateTokenID: gt.ID, RealTokenID: rt.ID, Model: "m", InputTokens: 200, OutputTokens: 75, RequestPath: "/b", StatusCode: 200})

	// Create sticky session referencing the real token
	expires := time.Now().UTC().Add(1 * time.Hour)
	_ = s.UpsertStickySession(ctx, gt.ID, rt.ID, expires)

	// Verify data exists before delete
	logs, err := s.ListUsageLogs(ctx, 100, 0)
	if err != nil {
		t.Fatalf("ListUsageLogs: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected 2 usage logs before delete, got %d", len(logs))
	}

	ss, err := s.GetStickySession(ctx, gt.ID)
	if err != nil {
		t.Fatalf("GetStickySession: %v", err)
	}
	if ss == nil {
		t.Fatal("expected sticky session before delete")
	}

	// Delete real token -- should cascade to usage_logs and sticky_sessions
	if err := s.DeleteRealToken(ctx, rt.ID); err != nil {
		t.Fatalf("DeleteRealToken: %v", err)
	}

	// Verify real token is gone
	_, err = s.GetRealToken(ctx, rt.ID)
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows for real token, got %v", err)
	}

	// Verify usage logs are gone
	logs, err = s.ListUsageLogs(ctx, 100, 0)
	if err != nil {
		t.Fatalf("ListUsageLogs after delete: %v", err)
	}
	if len(logs) != 0 {
		t.Errorf("expected 0 usage logs after cascade delete, got %d", len(logs))
	}

	// Verify sticky session is gone
	ss, err = s.GetStickySession(ctx, gt.ID)
	if err != nil {
		t.Fatalf("GetStickySession after delete: %v", err)
	}
	if ss != nil {
		t.Error("expected nil sticky session after cascade delete")
	}
}

func TestDeleteGateTokenCascade(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rt, gt := createTestTokens(t, s)

	// Create usage logs referencing the gate token
	_ = s.InsertUsageLog(ctx, &UsageLog{GateTokenID: gt.ID, RealTokenID: rt.ID, Model: "m", InputTokens: 100, OutputTokens: 50, RequestPath: "/a", StatusCode: 200})
	_ = s.InsertUsageLog(ctx, &UsageLog{GateTokenID: gt.ID, RealTokenID: rt.ID, Model: "m", InputTokens: 200, OutputTokens: 75, RequestPath: "/b", StatusCode: 200})

	// Create sticky session referencing the gate token
	expires := time.Now().UTC().Add(1 * time.Hour)
	_ = s.UpsertStickySession(ctx, gt.ID, rt.ID, expires)

	// Verify data exists before delete
	logs, err := s.ListUsageLogs(ctx, 100, 0)
	if err != nil {
		t.Fatalf("ListUsageLogs: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected 2 usage logs before delete, got %d", len(logs))
	}

	ss, err := s.GetStickySession(ctx, gt.ID)
	if err != nil {
		t.Fatalf("GetStickySession: %v", err)
	}
	if ss == nil {
		t.Fatal("expected sticky session before delete")
	}

	// Delete gate token -- should cascade to usage_logs and sticky_sessions
	if err := s.DeleteGateToken(ctx, gt.ID); err != nil {
		t.Fatalf("DeleteGateToken: %v", err)
	}

	// Verify gate token is gone
	_, err = s.GetGateToken(ctx, gt.ID)
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows for gate token, got %v", err)
	}

	// Verify usage logs are gone
	logs, err = s.ListUsageLogs(ctx, 100, 0)
	if err != nil {
		t.Fatalf("ListUsageLogs after delete: %v", err)
	}
	if len(logs) != 0 {
		t.Errorf("expected 0 usage logs after cascade delete, got %d", len(logs))
	}

	// Verify sticky session is gone
	ss, err = s.GetStickySession(ctx, gt.ID)
	if err != nil {
		t.Fatalf("GetStickySession after delete: %v", err)
	}
	if ss != nil {
		t.Error("expected nil sticky session after cascade delete")
	}
}

// ---------------------------------------------------------------------------
// Store Close
// ---------------------------------------------------------------------------

func TestClose(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "close-test.db")
	s, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Verify files were created
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("expected database file to exist")
	}
}
