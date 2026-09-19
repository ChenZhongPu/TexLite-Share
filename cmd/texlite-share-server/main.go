package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"texlite-share/internal/api"
	"texlite-share/internal/config"
	"texlite-share/internal/database"
	"texlite-share/internal/logging"
	"texlite-share/internal/registry"
	"texlite-share/internal/version"
)

func main() {
	cfg := config.DefaultServerConfig()
	config.LoadServerConfigFromEnv(cfg)

	showVersion := flag.Bool("version", false, "Show version information and exit")
	flag.BoolVar(showVersion, "v", false, "Show version information and exit (shorthand)")
	flag.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "Address for the public server to listen on (for tunnels and visitors)")
	flag.StringVar(&cfg.AdminListenAddr, "admin-listen", cfg.AdminListenAddr, "Address for dedicated admin management port (default: 127.0.0.1:9001, empty to disable)")
	flag.StringVar(&cfg.BaseDomain, "base-domain", cfg.BaseDomain, "Base domain for public shares (e.g., share.local or share.example.com)")
	flag.StringVar(&cfg.DBPath, "db", cfg.DBPath, "Path to SQLite database file")
	flag.StringVar(&cfg.AdminAPIKey, "admin-api-key", cfg.AdminAPIKey, "Secret key to protect admin dashboard and management APIs (recommended for production)")
	flag.StringVar(&cfg.CreateAPIKey, "create-api-key", cfg.CreateAPIKey, "Secret key to restrict public share creation (optional, leave empty for public community service)")
	flag.IntVar(&cfg.MaxActiveShares, "max-shares", cfg.MaxActiveShares, "Maximum active non-expired shares allowed")
	flag.IntVar(&cfg.MaxStreamsPerShare, "max-streams-per-share", cfg.MaxStreamsPerShare, "Maximum concurrent streams allowed per share")
	flag.IntVar(&cfg.MaxTotalStreams, "max-total-streams", cfg.MaxTotalStreams, "Maximum total concurrent streams across all shares")
	flag.IntVar(&cfg.RateLimitPerDay, "rate-limit-per-day", cfg.RateLimitPerDay, "Maximum share creations allowed per day per client IP (0 to disable)")
	flag.IntVar(&cfg.MaxSharesPerIP, "max-shares-per-ip", cfg.MaxSharesPerIP, "Maximum active non-expired shares allowed per client IP")
	flag.DurationVar(&cfg.SweepInterval, "sweep-interval", cfg.SweepInterval, "Interval for background expiration sweep")
	flag.DurationVar(&cfg.DefaultTTL, "default-ttl", cfg.DefaultTTL, "Default time-to-live for created shares (e.g. 1h, 2h, 24h)")
	flag.StringVar(&cfg.AssetCacheDir, "asset-cache-dir", cfg.AssetCacheDir, "Directory to store cached frontend static assets (empty to disable)")
	flag.IntVar(&cfg.AssetCacheMaxMB, "asset-cache-max-mb", cfg.AssetCacheMaxMB, "Maximum disk usage in megabytes for asset cache (default: 256, 0 to disable)")
	debug := flag.Bool("debug", false, "Enable debug logging")

	flag.Parse()

	if *showVersion {
		fmt.Println(version.Info("texlite-share-server"))
		os.Exit(0)
	}

	logger := logging.InitLogger(*debug)
	slog.SetDefault(logger)

	slog.Info("starting texlite-share-server",
		"version", version.Version,
		"commit", version.GitCommit,
		"listen", cfg.ListenAddr,
		"admin_listen", cfg.AdminListenAddr,
		"base_domain", cfg.BaseDomain,
		"db", cfg.DBPath,
		"max_shares", cfg.MaxActiveShares,
		"asset_cache_dir", cfg.AssetCacheDir,
		"asset_cache_max_mb", cfg.AssetCacheMaxMB,
	)

	// 1. Open SQLite database
	db, err := database.Open(cfg.DBPath)
	if err != nil {
		slog.Error("failed to open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	// 2. Initialize in-memory tunnel registry
	reg := registry.NewRegistry()

	// 3. Start background expiration worker
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api.StartExpirationWorker(ctx, db, reg, cfg.SweepInterval)

	// 4. Configure Public HTTP Server
	var publicHandler http.Handler
	if cfg.AdminListenAddr != "" {
		publicHandler = api.NewPublicRouter(cfg, db, reg)
	} else {
		publicHandler = api.NewRouter(cfg, db, reg)
	}

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           publicHandler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	serverErrCh := make(chan error, 2)
	go func() {
		slog.Info(fmt.Sprintf("public server listening on http://%s (tunnels & visitors)", cfg.ListenAddr))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrCh <- fmt.Errorf("public server error: %w", err)
		}
	}()

	// 5. Configure Dedicated Admin Server (if enabled)
	var adminServer *http.Server
	if cfg.AdminListenAddr != "" {
		adminHandler := api.NewAdminHandler(cfg, db, reg)
		adminServer = &http.Server{
			Addr:              cfg.AdminListenAddr,
			Handler:           adminHandler,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
		}

		go func() {
			slog.Info(fmt.Sprintf("admin management port listening on http://%s (localhost only, Web Dashboard available)", cfg.AdminListenAddr))
			if err := adminServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serverErrCh <- fmt.Errorf("admin server error: %w", err)
			}
		}()
	}

	// 6. Channel for interrupt signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Wait for shutdown signal or fatal server error
	select {
	case err := <-serverErrCh:
		slog.Error("server encountered fatal error", "error", err)
		os.Exit(1)
	case sig := <-sigCh:
		slog.Info("received shutdown signal", "signal", sig.String())
	}

	// Graceful shutdown sequence
	slog.Info("shutting down servers...")
	cancel() // stop expiration worker

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("public server shutdown error", "error", err)
	}

	if adminServer != nil {
		if err := adminServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("admin server shutdown error", "error", err)
		}
	}

	// Close all active tunnel sessions
	slog.Info("closing active tunnel sessions...")
	reg.CloseAll()

	slog.Info("texlite-share-server stopped cleanly")
}

