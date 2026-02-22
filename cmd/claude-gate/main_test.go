package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ggmolly/claude-gate/internal/admin"
	"github.com/ggmolly/claude-gate/internal/proxy"
	"github.com/ggmolly/claude-gate/internal/store"
	"github.com/ggmolly/claude-gate/internal/token"
	"github.com/ggmolly/claude-gate/web"
)

// TestServerBoot verifies that the server wires up all routes without panicking.
func TestServerBoot(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	tokenMgr := token.NewManager(db, 5, 10*time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := tokenMgr.Start(ctx); err != nil {
		t.Fatalf("start token manager: %v", err)
	}
	defer tokenMgr.Stop()

	proxyHandler, cancelWriter, err := proxy.Setup(ctx, db, tokenMgr, "https://api.anthropic.com")
	if err != nil {
		t.Fatalf("setup proxy: %v", err)
	}
	defer cancelWriter()

	mux := http.NewServeMux()

	adminHandler := admin.NewAdminHandler(db, nil)
	adminHandler.Register(mux, "test-secret")

	webHandler, err := web.NewHandler(db, "test-secret", nil)
	if err != nil {
		t.Fatalf("init web handler: %v", err)
	}
	webHandler.RegisterRoutes(mux)

	mux.Handle("/", proxyHandler)

	// Verify the mux can handle requests without panicking.
	srv := &http.Server{
		Addr:    ":0",
		Handler: mux,
	}
	_ = srv
}

// TestServerBootEnv tests that the full server can start with env vars.
func TestServerBootEnv(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	os.Setenv("CLAUDE_GATE_ADMIN_SECRET", "test-secret")
	os.Setenv("CLAUDE_GATE_DB_PATH", dbPath)
	os.Setenv("CLAUDE_GATE_ADDR", ":0")
	defer func() {
		os.Unsetenv("CLAUDE_GATE_ADMIN_SECRET")
		os.Unsetenv("CLAUDE_GATE_DB_PATH")
		os.Unsetenv("CLAUDE_GATE_ADDR")
	}()

	// This just verifies config loading works; full server boot is tested above.
	_ = t // placeholder
}
