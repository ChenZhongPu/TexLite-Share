package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"texlite-share/internal/config"
	"texlite-share/internal/database"
	"texlite-share/internal/logging"
	"texlite-share/internal/protocol"
	"texlite-share/internal/registry"
)

// ExtractShareID parses and strictly validates the share ID from the incoming Host header.
// It matches "<share-id>.<base-domain>" and strips optional port numbers.
func ExtractShareID(hostHeader, baseDomain string) (string, error) {
	host := hostHeader
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		host = h
	}

	host = strings.ToLower(strings.TrimSpace(host))
	baseDomain = strings.ToLower(strings.TrimSpace(baseDomain))

	expectedSuffix := "." + baseDomain
	if !strings.HasSuffix(host, expectedSuffix) {
		return "", fmt.Errorf("%w: host %q does not match base domain %q", protocol.ErrInvalidShare, host, baseDomain)
	}

	subdomain := strings.TrimSuffix(host, expectedSuffix)
	if strings.Contains(subdomain, ".") || subdomain == "" {
		return "", fmt.Errorf("%w: invalid subdomain format %q", protocol.ErrInvalidShare, subdomain)
	}

	if err := protocol.ValidateShareID(subdomain); err != nil {
		return "", err
	}

	return subdomain, nil
}

// ProxyHandler handles public subdomain traffic, routing it through active smux tunnel streams.
type ProxyHandler struct {
	cfg      *config.ServerConfig
	db       *database.DB
	registry *registry.Registry
}

// NewProxyHandler creates a new reverse proxy handler.
func NewProxyHandler(cfg *config.ServerConfig, db *database.DB, reg *registry.Registry) *ProxyHandler {
	return &ProxyHandler{
		cfg:      cfg,
		db:       db,
		registry: reg,
	}
}

// ServeHTTP handles the incoming public HTTP/WebSocket request.
func (p *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. Extract share ID from Host
	shareID, err := ExtractShareID(r.Host, p.cfg.BaseDomain)
	if err != nil {
		slog.Warn("proxy request rejected: invalid host", "host", r.Host, "error", err)
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	// 2. Query share metadata in database
	share, err := p.db.GetShare(r.Context(), shareID)
	if err != nil {
		if errors.Is(err, protocol.ErrShareNotFound) {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		slog.Error("database error fetching share", "share_id", shareID, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// 3. Verify status & expiration
	now := time.Now().UTC()
	if share.Status == database.StatusRevoked {
		http.Error(w, "Forbidden: share has been revoked", http.StatusForbidden)
		return
	}

	if share.Status == database.StatusExpired || now.After(share.ExpiresAt) {
		http.Error(w, "Gone: share has expired", http.StatusGone)
		return
	}

	if share.Status != database.StatusActive {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	// 4. Look up active tunnel session
	session, found := p.registry.Get(shareID)
	if !found || session.Session == nil || session.Session.IsClosed() {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`<!DOCTYPE html>
<html>
<head><title>TexLite Share Offline</title></head>
<body>
  <h1>503 Service Unavailable</h1>
  <p>This TexLite Share is temporarily offline.</p>
</body>
</html>`))
		return
	}

	// 5. Check stream limits
	if p.cfg.MaxStreamsPerShare > 0 && int(session.ActiveStreams.Load()) >= p.cfg.MaxStreamsPerShare {
		slog.Warn("max streams per share reached", "share_id", shareID)
		http.Error(w, "Too Many Requests: share stream limit reached", http.StatusTooManyRequests)
		return
	}

	if p.cfg.MaxTotalStreams > 0 && int(p.registry.TotalStreams()) >= p.cfg.MaxTotalStreams {
		slog.Warn("max total streams reached", "total", p.registry.TotalStreams())
		http.Error(w, "Service Unavailable: server stream capacity reached", http.StatusServiceUnavailable)
		return
	}

	// 6. Reverse-proxy the request through smux
	p.serveReverseProxy(w, r, session)
}

func (p *ProxyHandler) serveReverseProxy(w http.ResponseWriter, r *http.Request, sess *registry.TunnelSession) {
	// Custom Transport where DialContext opens an smux stream
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if sess.Session.IsClosed() {
				return nil, protocol.ErrTunnelOffline
			}

			stream, err := sess.Session.OpenStream()
			if err != nil {
				return nil, fmt.Errorf("failed to open smux stream: %w", err)
			}

			p.registry.IncrStreams(sess)
			return &trackedConn{
				Conn:     stream,
				registry: p.registry,
				session:  sess,
			}, nil
		},
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	targetURL := &url.URL{
		Scheme: "http",
		Host:   r.Host,
	}

	rp := httputil.NewSingleHostReverseProxy(targetURL)
	rp.Transport = transport
	rp.FlushInterval = -1 // Flush immediately for streaming and WebSocket support

	originalDirector := rp.Director
	rp.Director = func(req *http.Request) {
		originalDirector(req)

		// Sanitize proxy headers and set authoritative forwarding headers (Section 36)
		clientIP, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			clientIP = r.RemoteAddr
		}

		proto := "http"
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			proto = "https"
		}

		req.Header.Set("X-Forwarded-For", clientIP)
		req.Header.Set("X-Forwarded-Host", r.Host)
		req.Header.Set("X-Forwarded-Proto", proto)
		req.Header.Del("Forwarded")
	}

	rp.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		slog.Error("reverse proxy request failed",
			"event", logging.EventProxyRequestFailed,
			"share_id", sess.ShareID,
			"error", err,
		)
		if errors.Is(err, protocol.ErrTunnelOffline) || errors.Is(err, net.ErrClosed) {
			rw.WriteHeader(http.StatusServiceUnavailable)
			_, _ = rw.Write([]byte("503 Service Unavailable: tunnel connection dropped"))
			return
		}
		rw.WriteHeader(http.StatusBadGateway)
		_, _ = rw.Write([]byte("502 Bad Gateway: failed to communicate with local TexLite service"))
	}

	rp.ServeHTTP(w, r)
}

// trackedConn wraps net.Conn to decrement stream counters upon close.
type trackedConn struct {
	net.Conn
	registry *registry.Registry
	session  *registry.TunnelSession
	once     sync.Once
}

func (c *trackedConn) Close() error {
	c.once.Do(func() {
		c.registry.DecrStreams(c.session)
	})
	return c.Conn.Close()
}
