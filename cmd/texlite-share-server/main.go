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
)

func main() {
	cfg := config.DefaultServerConfig()

	flag.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "Address for the server to listen on")
	flag.StringVar(&cfg.BaseDomain, "base-domain", cfg.BaseDomain, "Base domain for public shares (e.g., share.local or share.example.com)")
	flag.StringVar(&cfg.DBPath, "db", cfg.DBPath, "Path to SQLite database file")
	flag.StringVar(&cfg.CreateAPIKey, "create-api-key", cfg.CreateAPIKey, "Secret key to protect share creation (optional in dev mode)")
	flag.IntVar(&cfg.MaxActiveShares, "max-shares", cfg.MaxActiveShares, "Maximum active non-expired shares allowed")
	flag.IntVar(&cfg.MaxStreamsPerShare, "max-streams-per-share", cfg.MaxStreamsPerShare, "Maximum concurrent streams allowed per share")
	flag.IntVar(&cfg.MaxTotalStreams, "max-total-streams", cfg.MaxTotalStreams, "Maximum total concurrent streams across all shares")
	flag.DurationVar(&cfg.SweepInterval, "sweep-interval", cfg.SweepInterval, "Interval for background expiration sweep")
	flag.DurationVar(&cfg.DefaultTTL, "default-ttl", cfg.DefaultTTL, "Default time-to-live for created shares (e.g. 24h, 2h)")
	debug := flag.Bool("debug", false, "Enable debug logging")

	flag.Parse()

	logger := logging.InitLogger(*debug)
	slog.SetDefault(logger)

	slog.Info("starting texlite-share-server",
		"listen", cfg.ListenAddr,
		"base_domain", cfg.BaseDomain,
		"db", cfg.DBPath,
		"max_shares", cfg.MaxActiveShares,
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

	// 3. Initialize HTTP router
	router := api.NewRouter(cfg, db, reg)

	// 4. Start background expiration worker
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api.StartExpirationWorker(ctx, db, reg, cfg.SweepInterval)

	// 5. Configure HTTP server
	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// 6. Channel for interrupt signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	serverErrCh := make(chan error, 1)
	go func() {
		slog.Info(fmt.Sprintf("server listening on http://%s", cfg.ListenAddr))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrCh <- err
		}
	}()

	// Wait for shutdown signal or fatal server error
	select {
	case err := <-serverErrCh:
		slog.Error("server encountered fatal error", "error", err)
		os.Exit(1)
	case sig := <-sigCh:
		slog.Info("received shutdown signal", "signal", sig.String())
	}

	// Graceful shutdown sequence (Section 42)
	slog.Info("shutting down server...")
	cancel() // stop expiration worker

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("server shutdown error", "error", err)
	}

	// Close all active tunnel sessions
	slog.Info("closing active tunnel sessions...")
	reg.CloseAll()

	slog.Info("texlite-share-server stopped cleanly")
}
