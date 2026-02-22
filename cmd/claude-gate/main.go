package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ggmolly/claude-gate/internal/admin"
	"github.com/ggmolly/claude-gate/internal/config"
	"github.com/ggmolly/claude-gate/internal/logging"
	"github.com/ggmolly/claude-gate/internal/proxy"
	"github.com/ggmolly/claude-gate/internal/store"
	"github.com/ggmolly/claude-gate/internal/token"
	"github.com/ggmolly/claude-gate/web"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	logger := logging.SetupLogger(cfg.LogLevel)
	cfg.Validate(logger)

	db, err := store.New(cfg.DBPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	// Token manager
	tokenMgr := token.NewManager(db, cfg.MaxFailures, cfg.StickyTTL, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := tokenMgr.Start(ctx); err != nil {
		log.Fatalf("start token manager: %v", err)
	}
	defer tokenMgr.Stop()

	// Proxy handler + usage writer
	proxyHandler, cancelUsageWriter, err := proxy.Setup(ctx, db, tokenMgr, cfg.UpstreamURL, logger)
	if err != nil {
		log.Fatalf("setup proxy: %v", err)
	}
	defer cancelUsageWriter()

	mux := http.NewServeMux()

	// Admin API (each route has auth middleware applied)
	adminHandler := admin.NewAdminHandler(db, func() {
		if err := tokenMgr.RefreshPool(); err != nil {
			logger.Error("failed to refresh token pool", "error", err)
		}
	}, logger)
	adminHandler.Register(mux, cfg.AdminSecret)

	// Web UI
	webHandler, err := web.NewHandler(db, cfg.AdminSecret, func() {
		if err := tokenMgr.RefreshPool(); err != nil {
			logger.Error("failed to refresh token pool", "error", err)
		}
	}, logger)
	if err != nil {
		log.Fatalf("init web handler: %v", err)
	}
	webHandler.RegisterRoutes(mux)

	// Proxy catch-all (must be last)
	mux.Handle("/", proxyHandler)

	srv := &http.Server{
		Addr:         cfg.Addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // no timeout for SSE streaming
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		logger.Info("claude-gate starting", "addr", cfg.Addr, "upstream", cfg.UpstreamURL)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("shutdown error: %v", err)
	}
	logger.Info("server stopped")
}
