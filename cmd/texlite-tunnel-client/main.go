package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"texlite-share/internal/config"
	"texlite-share/internal/logging"
	"texlite-share/internal/tunnel"
)

func main() {
	cfg := config.DefaultClientConfig()

	// 1. Load overrides from environment variables
	config.LoadClientConfigFromEnv(cfg)

	// 2. Command-line flags override environment variables
	flag.StringVar(&cfg.ServerURL, "server-url", cfg.ServerURL, "Share server URL (default: http://127.0.0.1:9000, or env TEXLITE_SERVER_URL)")
	flag.StringVar(&cfg.ShareID, "share-id", cfg.ShareID, "Assigned Share ID (optional, auto-creates if omitted)")
	flag.StringVar(&cfg.Token, "token", cfg.Token, "Tunnel authentication token (optional, auto-creates if omitted)")
	flag.StringVar(&cfg.LocalAddr, "local-addr", cfg.LocalAddr, "Local destination address (default: 127.0.0.1:3000, or env TEXLITE_LOCAL_ADDR)")
	flag.StringVar(&cfg.APIKey, "api-key", cfg.APIKey, "Server creation API key if required (or env TEXLITE_SHARE_API_KEY)")
	flag.StringVar(&cfg.TTL, "ttl", cfg.TTL, "Requested share duration, e.g. 2h, 24h (or env TEXLITE_SHARE_TTL)")
	flag.BoolVar(&cfg.AutoRevokeOnExit, "auto-revoke", cfg.AutoRevokeOnExit, "Automatically revoke share on server upon exit")
	flag.DurationVar(&cfg.LocalDialTimeout, "dial-timeout", cfg.LocalDialTimeout, "Timeout for dialing local service")
	debug := flag.Bool("debug", false, "Enable debug logging")

	flag.Parse()

	logger := logging.InitLogger(*debug)
	slog.SetDefault(logger)

	// 3. Pre-flight check: ensure local TexLite service is running before proceeding
	if err := tunnel.ProbeLocalService(cfg.LocalAddr, 1500*time.Millisecond); err != nil {
		fmt.Fprintf(os.Stderr, "\n❌ Error: Local TexLite service is not reachable at %s\n   Details: %v\n   Please start TexLite first before running texlite-share.\n\n", cfg.LocalAddr, err)
		os.Exit(1)
	}

	// 4. Auto-create share if share-id or token is not provided
	var isAutoCreated bool
	if cfg.ShareID == "" || cfg.Token == "" {
		slog.Info("no share ID provided, auto-creating a new share on server", "server_url", cfg.ServerURL)
		created, err := tunnel.AutoCreateShare(context.Background(), cfg.ServerURL, cfg.APIKey, cfg.TTL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\n❌ Failed to create share on server (%s): %v\n\n", cfg.ServerURL, err)
			os.Exit(1)
		}

		cfg.ShareID = created.ID
		cfg.Token = created.Token
		isAutoCreated = true

		fmt.Printf("\n" +
			"===================================================================\n" +
			" ✨ TexLite Share is Live!\n" +
			" 🔗 Public URL:   %s\n" +
			" 🎯 Local Target: http://%s\n" +
			" ⏳ Expires At:   %s\n" +
			"===================================================================\n" +
			"Press Ctrl+C to stop sharing.\n\n",
			created.PublicURL, cfg.LocalAddr, created.ExpiresAt.Local().Format("2006-01-02 15:04:05"),
		)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	slog.Info("starting texlite-tunnel-client",
		"share_id", cfg.ShareID,
		"server_url", cfg.ServerURL,
		"local_addr", cfg.LocalAddr,
	)

	err := tunnel.RunClient(ctx, cfg)

	// 5. Cleanup upon exit
	if isAutoCreated && cfg.AutoRevokeOnExit {
		revokeCtx, revokeCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer revokeCancel()
		_ = tunnel.AutoRevokeShare(revokeCtx, cfg.ServerURL, cfg.ShareID, cfg.Token)
		slog.Info("share automatically revoked upon exit", "share_id", cfg.ShareID)
	}

	if err != nil {
		if errors.Is(err, context.Canceled) {
			slog.Info("tunnel client stopped cleanly")
			os.Exit(0)
		}
		if errors.Is(err, tunnel.ErrTerminal) {
			slog.Error("tunnel client terminated due to non-retryable server response", "error", err)
			os.Exit(1)
		}
		slog.Error("tunnel client exited with error", "error", err)
		os.Exit(1)
	}

	slog.Info("texlite-tunnel-client stopped")
}
