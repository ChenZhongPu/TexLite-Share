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

	"texlite-share/internal/config"
	"texlite-share/internal/logging"
	"texlite-share/internal/tunnel"
)

func main() {
	cfg := config.DefaultClientConfig()

	flag.StringVar(&cfg.ServerURL, "server-url", "", "Share server URL (e.g. http://127.0.0.1:9000 or https://share.example.com)")
	flag.StringVar(&cfg.ShareID, "share-id", "", "Assigned Share ID")
	flag.StringVar(&cfg.Token, "token", "", "Tunnel authentication token (or set TEXLITE_TUNNEL_TOKEN)")
	flag.StringVar(&cfg.LocalAddr, "local-addr", cfg.LocalAddr, "Local destination address (e.g., 127.0.0.1:3000)")
	flag.DurationVar(&cfg.LocalDialTimeout, "dial-timeout", cfg.LocalDialTimeout, "Timeout for dialing local service")
	debug := flag.Bool("debug", false, "Enable debug logging")

	flag.Parse()

	// Fallback to environment variable for token if flag not provided (Section 56)
	if cfg.Token == "" {
		cfg.Token = os.Getenv("TEXLITE_TUNNEL_TOKEN")
	}

	logger := logging.InitLogger(*debug)
	slog.SetDefault(logger)

	if cfg.ServerURL == "" || cfg.ShareID == "" || cfg.Token == "" {
		fmt.Fprintf(os.Stderr, "Error: --server-url, --share-id, and --token are required\n\n")
		flag.Usage()
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	slog.Info("starting texlite-tunnel-client",
		"share_id", cfg.ShareID,
		"server_url", cfg.ServerURL,
		"local_addr", cfg.LocalAddr,
	)

	err := tunnel.RunClient(ctx, cfg)
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
