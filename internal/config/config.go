package config

import (
	"fmt"
	"net"
	"net/url"
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
	CreateAPIKey       string
	MaxActiveShares    int
	MaxStreamsPerShare int
	MaxTotalStreams    int
	SweepInterval      time.Duration
	DefaultTTL         time.Duration
	HandshakeTimeout   time.Duration
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
		SweepInterval:      30 * time.Second,
		DefaultTTL:         24 * time.Hour,
		HandshakeTimeout:   10 * time.Second,
	}
}

// ClientConfig holds the configuration for texlite-tunnel-client.
type ClientConfig struct {
	ServerURL        string
	ShareID          string
	Token            string
	LocalAddr        string
	LocalDialTimeout time.Duration
	MaxRetryDelay    time.Duration
}

// DefaultClientConfig returns standard client defaults.
func DefaultClientConfig() *ClientConfig {
	return &ClientConfig{
		LocalAddr:        "127.0.0.1:3000",
		LocalDialTimeout: 5 * time.Second,
		MaxRetryDelay:    30 * time.Second,
	}
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
