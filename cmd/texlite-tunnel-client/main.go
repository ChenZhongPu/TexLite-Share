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
	"texlite-share/internal/version"
)

func main() {
	cfg := config.DefaultClientConfig()

	// 1. Load overrides from environment variables
	config.LoadClientConfigFromEnv(cfg)
	localAddrExplicit := os.Getenv("TEXLITE_LOCAL_ADDR") != ""

	// 2. Command-line flags override environment variables
	showVersion := flag.Bool("version", false, "Show version information and exit")
	flag.BoolVar(showVersion, "v", false, "Show version information and exit (shorthand)")
	flag.StringVar(&cfg.ServerURL, "server-url", cfg.ServerURL, "Share server URL (default: http://127.0.0.1:9000, or env TEXLITE_SERVER_URL)")
	flag.StringVar(&cfg.ShareID, "share-id", cfg.ShareID, "Assigned Share ID (optional, auto-creates if omitted)")
	flag.StringVar(&cfg.Token, "token", cfg.Token, "Tunnel authentication token (optional, auto-creates if omitted)")
	flag.StringVar(&cfg.LocalAddr, "local-addr", cfg.LocalAddr, "Local destination address (auto-detected if omitted, or env TEXLITE_LOCAL_ADDR)")
	flag.StringVar(&cfg.APIKey, "api-key", cfg.APIKey, "Server creation API key if required (or env TEXLITE_SHARE_API_KEY)")
	flag.StringVar(&cfg.TTL, "ttl", cfg.TTL, "Requested share duration, e.g. 2h, 24h (or env TEXLITE_SHARE_TTL)")
	flag.BoolVar(&cfg.AutoRevokeOnExit, "auto-revoke", cfg.AutoRevokeOnExit, "Automatically revoke share on server upon exit")
	flag.DurationVar(&cfg.LocalDialTimeout, "dial-timeout", cfg.LocalDialTimeout, "Timeout for dialing local service")
	debug := flag.Bool("debug", false, "Enable debug logging")

	flag.Parse()

	if *showVersion {
		fmt.Println(version.Info("texlite-tunnel-client"))
		os.Exit(0)
	}

	flag.Visit(func(f *flag.Flag) {
		if f.Name == "local-addr" {
			localAddrExplicit = true
		}
	})

	logger := logging.InitLogger(*debug)
	slog.SetDefault(logger)

	// 3. Dynamic discovery: if user did not explicitly configure local-addr, auto-discover actual port
	if !localAddrExplicit {
		discovered, err := tunnel.DiscoverTexLiteAddr(context.Background(), "")
		if err == nil && discovered != "" {
			cfg.LocalAddr = discovered
		}
	}

	// 4. Pre-flight verification: strictly verify TexLite is started and healthy
	texliteInfo, err := tunnel.VerifyTexLiteService(cfg.LocalAddr, 2*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n❌ Error: %v\n\n", err)
		os.Exit(1)
	}

	slog.Info("verified running TexLite instance",
		"address", cfg.LocalAddr,
		"pid", texliteInfo.PID,
		"latexmk", texliteInfo.Latexmk,
	)

	// State file path for persistent share credentials
	statePath, _ := tunnel.DefaultStateFilePath()

	// 5. Auto-create share if share-id or token is not provided
	var isAutoCreated bool
	if cfg.ShareID == "" || cfg.Token == "" {
		// Proactively revoke leftover share from previous run carrying its token
		if statePath != "" {
			if oldState, pErr := tunnel.ProactivelyRevokePreviousShare(context.Background(), statePath, cfg.ServerURL); pErr == nil && oldState != nil && oldState.ShareID != "" {
				slog.Info("proactively revoked previous unrevoked share", "share_id", oldState.ShareID)
			}
		}

		slog.Info("no share ID provided, auto-creating a new share on server", "server_url", cfg.ServerURL)
		created, err := tunnel.AutoCreateShare(context.Background(), cfg.ServerURL, cfg.APIKey, cfg.TTL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\n❌ Failed to create share on server (%s): %v\n\n", cfg.ServerURL, err)
			os.Exit(1)
		}

		cfg.ShareID = created.ID
		cfg.Token = created.Token
		isAutoCreated = true

		// Persist newly created share state with token
		if statePath != "" {
			_ = tunnel.SaveLastShareState(statePath, &tunnel.SavedShareState{
				ServerURL: cfg.ServerURL,
				ShareID:   created.ID,
				Token:     created.Token,
				PublicURL: created.PublicURL,
				LocalAddr: cfg.LocalAddr,
				ExpiresAt: created.ExpiresAt,
				CreatedAt: time.Now(),
				Revoked:   false,
			})
		}

		texliteLabel := fmt.Sprintf("http://%s (TexLite verified", cfg.LocalAddr)
		if texliteInfo.PID > 0 {
			texliteLabel += fmt.Sprintf(", PID: %d", texliteInfo.PID)
		}
		texliteLabel += ")"

		fmt.Printf("\n" +
			"===================================================================\n" +
			" ✨ TexLite Share is Live!\n" +
			" 🔗 Public URL:   %s\n" +
			" 🎯 Local Target: %s\n" +
			" ⏳ Expires At:   %s\n" +
			"===================================================================\n" +
			"Press Ctrl+C to stop sharing.\n\n",
			created.PublicURL, texliteLabel, created.ExpiresAt.Local().Format("2006-01-02 15:04:05"),
		)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	slog.Info("starting texlite-tunnel-client",
		"share_id", cfg.ShareID,
		"server_url", cfg.ServerURL,
		"local_addr", cfg.LocalAddr,
	)

	err = tunnel.RunClient(ctx, cfg)

	// 6. Cleanup upon exit
	if isAutoCreated && cfg.AutoRevokeOnExit {
		revokeCtx, revokeCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer revokeCancel()
		_ = tunnel.AutoRevokeShare(revokeCtx, cfg.ServerURL, cfg.ShareID, cfg.Token)
		slog.Info("share automatically revoked upon exit", "share_id", cfg.ShareID)

		if statePath != "" {
			if st, lErr := tunnel.LoadLastShareState(statePath); lErr == nil && st != nil && st.ShareID == cfg.ShareID {
				st.Revoked = true
				_ = tunnel.SaveLastShareState(statePath, st)
			}
		}
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
