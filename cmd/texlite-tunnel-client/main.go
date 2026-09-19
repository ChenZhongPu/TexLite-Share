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
	"texlite-share/internal/protocol"
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
	flag.StringVar(&cfg.ShareID, "share-id", cfg.ShareID, "Share ID for manual connection mode (requires -token, or env TEXLITE_SHARE_ID)")
	flag.StringVar(&cfg.Token, "token", cfg.Token, "Tunnel authentication token for manual connection mode (requires -share-id, or env TEXLITE_TUNNEL_TOKEN)")
	flag.StringVar(&cfg.LocalAddr, "local-addr", cfg.LocalAddr, "Local destination address (auto-detected if omitted, or env TEXLITE_LOCAL_ADDR)")
	flag.StringVar(&cfg.APIKey, "api-key", cfg.APIKey, "Server creation API key if required (or env TEXLITE_SHARE_API_KEY)")
	flag.BoolVar(&cfg.AutoRevokeOnExit, "auto-revoke", cfg.AutoRevokeOnExit, "Automatically revoke share on server upon exit (applies to automatic mode)")
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

	// 5. Startup Modes:
	// Mode 1: Direct start (omit both --share-id and --token)
	//         -> Request deletion of old temporary share info (if any), create new temporary share, auto-connect
	// Mode 2: Manual connection mode (provide both --share-id and --token)
	//         -> Connect directly to existing share without touching temporary share state
	var isAutoCreated bool

	if cfg.ShareID == "" && cfg.Token == "" {
		// --- Mode 1: Direct start / Automatic Temporary Share ---
		// 1. Proactively delete old temporary share from server (if any exists in local state)
		if statePath != "" {
			if oldState, pErr := tunnel.ProactivelyRevokePreviousShare(context.Background(), statePath, cfg.ServerURL); pErr == nil && oldState != nil && oldState.ShareID != "" {
				slog.Info("cleaned up previous temporary share on server", "share_id", oldState.ShareID)
			}
		}

		// 2. Request creation of a new temporary share
		slog.Info("no share ID provided, auto-creating a new temporary share on server", "server_url", cfg.ServerURL)
		created, err := tunnel.AutoCreateShare(context.Background(), cfg.ServerURL, cfg.APIKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\n❌ Failed to create share on server (%s): %v\n\n", cfg.ServerURL, err)
			os.Exit(1)
		}

		cfg.ShareID = created.ID
		cfg.Token = created.Token
		isAutoCreated = true

		// 3. Persist newly created temporary share state with token
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
			" ✨ TexLite Share is Live! (Automatic Temporary Mode)\n" +
			" 🔗 Public URL:   %s\n" +
			" 🆔 Share ID:     %s\n" +
			" 🎯 Local Target: %s\n" +
			" ⏳ Expires At:   %s\n" +
			"===================================================================\n" +
			"Press Ctrl+C to stop sharing.\n\n",
			created.PublicURL, created.ID, texliteLabel, created.ExpiresAt.Local().Format("2006-01-02 15:04:05"),
		)
	} else if cfg.ShareID != "" && cfg.Token != "" {
		// --- Mode 2: Manual connection mode ---
		// Validate Share ID format
		if err := protocol.ValidateShareID(cfg.ShareID); err != nil {
			fmt.Fprintf(os.Stderr, "\n❌ Invalid Share ID format %q: %v\n\n", cfg.ShareID, err)
			os.Exit(1)
		}

		texliteLabel := fmt.Sprintf("http://%s (TexLite verified", cfg.LocalAddr)
		if texliteInfo.PID > 0 {
			texliteLabel += fmt.Sprintf(", PID: %d", texliteInfo.PID)
		}
		texliteLabel += ")"

		fmt.Printf("\n" +
			"===================================================================\n" +
			" 🚀 TexLite Share Tunnel is Live! (Fixed Subdomain / Manual Mode)\n" +
			" 🆔 Share ID:     %s\n" +
			" 🎯 Local Target: %s\n" +
			" 🌐 Server URL:   %s\n" +
			"===================================================================\n" +
			"Press Ctrl+C to disconnect (fixed share remains active on server).\n\n",
			cfg.ShareID, texliteLabel, cfg.ServerURL,
		)
	} else {
		// Invalid input: one was supplied and one was omitted
		fmt.Fprintf(os.Stderr, "\n❌ Configuration Error: Manual connection mode requires both -share-id and -token.\nTo automatically create a temporary share, omit both flags.\n\n")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	slog.Info("starting texlite-tunnel-client",
		"share_id", cfg.ShareID,
		"server_url", cfg.ServerURL,
		"local_addr", cfg.LocalAddr,
	)

	err = tunnel.RunClient(ctx, cfg)

	// 6. Cleanup upon exit (Only applies to Mode 1 temporary shares)
	if isAutoCreated && cfg.AutoRevokeOnExit {
		revokeCtx, revokeCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer revokeCancel()
		_ = tunnel.AutoRevokeShare(revokeCtx, cfg.ServerURL, cfg.ShareID, cfg.Token)
		slog.Info("temporary share automatically revoked upon exit", "share_id", cfg.ShareID)

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
		if errors.Is(err, tunnel.ErrShareExpired) {
			fmt.Fprintf(os.Stderr, "\n❌ 连接失败：固定子域名共享 %q 在服务端已过期 (HTTP 410)。\n   已过期的共享无法再建立隧道连接。\n   请在服务端或管理后台重新续期/创建该共享后再试。\n\n", cfg.ShareID)
			os.Exit(1)
		}
		if errors.Is(err, tunnel.ErrShareRevoked) {
			fmt.Fprintf(os.Stderr, "\n❌ 连接失败：共享 %q 已在服务端被撤销 (HTTP 403)。\n\n", cfg.ShareID)
			os.Exit(1)
		}
		if errors.Is(err, tunnel.ErrShareNotFound) {
			fmt.Fprintf(os.Stderr, "\n❌ 连接失败：服务端未找到共享 %q (HTTP 404)。\n   请确认 Share ID 是否正确并在服务端已成功创建。\n\n", cfg.ShareID)
			os.Exit(1)
		}
		if errors.Is(err, tunnel.ErrAuthFailed) {
			fmt.Fprintf(os.Stderr, "\n❌ 连接失败：共享 %q 认证失败 (HTTP 401: Token 无效)。\n   请检查指定的 Token 是否正确。\n\n", cfg.ShareID)
			os.Exit(1)
		}
		if errors.Is(err, tunnel.ErrTerminal) {
			fmt.Fprintf(os.Stderr, "\n❌ 连接失败：%v\n\n", err)
			os.Exit(1)
		}
		slog.Error("tunnel client exited with error", "error", err)
		os.Exit(1)
	}

	slog.Info("texlite-tunnel-client stopped")
}
