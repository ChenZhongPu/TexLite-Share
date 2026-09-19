package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"texlite-share/internal/protocol"
)

// ServerConfig holds the configuration for texlite-share-server.
type ServerConfig struct {
	ListenAddr         string
	AdminListenAddr    string
	BaseDomain         string
	DBPath             string
	AdminAPIKey        string
	CreateAPIKey       string
	MaxActiveShares    int
	MaxStreamsPerShare int
	MaxTotalStreams    int
	RateLimitPerDay    int
	MaxSharesPerIP     int
	SweepInterval      time.Duration
	DefaultTTL         time.Duration
	HandshakeTimeout   time.Duration
	AssetCacheDir      string
	AssetCacheMaxMB    int
}

// DefaultServerConfig returns standard production/dev defaults.
func DefaultServerConfig() *ServerConfig {
	return &ServerConfig{
		ListenAddr:         "127.0.0.1:9000",
		AdminListenAddr:    "127.0.0.1:9001",
		BaseDomain:         "share.local",
		DBPath:             "texlite-share.db",
		CreateAPIKey:       "",
		MaxActiveShares:    50,
		MaxStreamsPerShare: 64,
		MaxTotalStreams:    1024,
		RateLimitPerDay:    20,
		MaxSharesPerIP:     5,
		SweepInterval:      30 * time.Second,
		DefaultTTL:         1 * time.Hour,
		HandshakeTimeout:   10 * time.Second,
		AssetCacheDir:      "asset-cache",
		AssetCacheMaxMB:    256,
	}
}

// ClientConfig holds the configuration for texlite-tunnel-client.
type ClientConfig struct {
	ServerURL        string
	ShareID          string
	Token            string
	LocalAddr        string
	APIKey           string
	AutoRevokeOnExit bool
	LocalDialTimeout time.Duration
	MaxRetryDelay    time.Duration
}

// DefaultClientConfig returns standard client defaults.
func DefaultClientConfig() *ClientConfig {
	return &ClientConfig{
		ServerURL:        "http://127.0.0.1:9000",
		LocalAddr:        "127.0.0.1:3000",
		AutoRevokeOnExit: true,
		LocalDialTimeout: 5 * time.Second,
		MaxRetryDelay:    30 * time.Second,
	}
}

// LoadServerConfigFromEnv loads server configuration overrides from environment variables.
func LoadServerConfigFromEnv(cfg *ServerConfig) {
	if v := getEnv("TEXLITE_LISTEN_ADDR"); v != "" {
		cfg.ListenAddr = v
	}
	if v := getEnv("TEXLITE_ADMIN_LISTEN_ADDR"); v != "" {
		cfg.AdminListenAddr = v
	}
	if v := getEnv("TEXLITE_BASE_DOMAIN"); v != "" {
		cfg.BaseDomain = v
	}
	if v := getEnv("TEXLITE_DB_PATH"); v != "" {
		cfg.DBPath = v
	}
	if v := getEnv("TEXLITE_ADMIN_API_KEY"); v != "" {
		cfg.AdminAPIKey = v
	}
	if v := getEnv("TEXLITE_CREATE_API_KEY"); v != "" {
		cfg.CreateAPIKey = v
	}
	if v := getEnv("TEXLITE_DEFAULT_TTL", "TEXLITE_SHARE_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.DefaultTTL = d
		}
	}
	if v := getEnv("TEXLITE_MAX_SHARES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxActiveShares = n
		}
	}
	if v := getEnv("TEXLITE_RATE_LIMIT_PER_DAY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.RateLimitPerDay = n
		}
	}
	if v := getEnv("TEXLITE_ASSET_CACHE_DIR"); v != "" {
		cfg.AssetCacheDir = v
	}
}

// LoadClientConfigFromEnv loads client configuration overrides from environment variables.
func LoadClientConfigFromEnv(cfg *ClientConfig) {
	if v := getEnv("TEXLITE_SERVER_URL", "TEXLITE_SHARE_SERVER_URL"); v != "" {
		cfg.ServerURL = v
	}
	if v := getEnv("TEXLITE_LOCAL_ADDR"); v != "" {
		cfg.LocalAddr = v
	}
	if v := getEnv("TEXLITE_SHARE_API_KEY"); v != "" {
		cfg.APIKey = v
	}
	if v := getEnv("TEXLITE_SHARE_ID"); v != "" {
		cfg.ShareID = v
	}
	if v := getEnv("TEXLITE_TUNNEL_TOKEN"); v != "" {
		cfg.Token = v
	}
}

func getEnv(keys ...string) string {
	for _, k := range keys {
		if val := strings.TrimSpace(os.Getenv(k)); val != "" {
			return val
		}
	}
	return ""
}

// ValidateLocalAddr strictly ensures that the local target address is a loopback destination.
// Only 127.0.0.1:<port>, localhost:<port>, and [::1]:<port> are accepted to prevent SSRF.
func ValidateLocalAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%w: invalid host:port format %q: %v", protocol.ErrInvalidLocalAddr, addr, err)
	}

	if port == "" {
		return fmt.Errorf("%w: missing port in %q", protocol.ErrInvalidLocalAddr, addr)
	}

	hostLower := strings.ToLower(host)
	if hostLower == "localhost" {
		return nil
	}

	ip := net.ParseIP(hostLower)
	if ip == nil {
		return fmt.Errorf("%w: %q is not a valid IP or 'localhost'", protocol.ErrInvalidLocalAddr, host)
	}

	if !ip.IsLoopback() {
		return fmt.Errorf("%w: address %q is not a loopback address", protocol.ErrInvalidLocalAddr, host)
	}

	return nil
}

// DeriveWebSocketURL translates an http/https server URL into the tunnel's ws/wss URL for a given shareID.
func DeriveWebSocketURL(serverURL, shareID string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", fmt.Errorf("invalid server URL %q: %w", serverURL, err)
	}

	switch strings.ToLower(u.Scheme) {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
		// Already websocket scheme
	default:
		return "", fmt.Errorf("unsupported URL scheme %q in %q", u.Scheme, serverURL)
	}

	// Endpoint path is /api/v1/tunnel/{shareID}
	u.Path = fmt.Sprintf("/api/v1/tunnel/%s", shareID)
	u.RawQuery = ""
	return u.String(), nil
}
