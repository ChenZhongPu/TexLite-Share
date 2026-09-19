package proxy

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"texlite-share/internal/cache"
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
	cache    *cache.AssetCache
}

// NewProxyHandler creates a new reverse proxy handler.
func NewProxyHandler(cfg *config.ServerConfig, db *database.DB, reg *registry.Registry) *ProxyHandler {
	var assetCache *cache.AssetCache
	if cfg.AssetCacheDir != "" && cfg.AssetCacheMaxMB > 0 {
		c, err := cache.New(cfg.AssetCacheDir, cfg.AssetCacheMaxMB)
		if err != nil {
			slog.Error("failed to initialize asset cache", "dir", cfg.AssetCacheDir, "error", err)
		} else {
			assetCache = c
		}
	}

	return &ProxyHandler{
		cfg:      cfg,
		db:       db,
		registry: reg,
		cache:    assetCache,
	}
}

// Cache returns the underlying AssetCache, or nil if disabled.
func (p *ProxyHandler) Cache() *cache.AssetCache {
	return p.cache
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

	// 5. Check static asset cache hit (serves directly from server disk without tunnel)
	if p.cache != nil && isCacheableAsset(r.URL.Path) && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		if cachedPath, found := p.cache.Get(r.URL.Path); found {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			w.Header().Set("X-TexLite-Cache", "HIT")
			http.ServeFile(w, r, cachedPath)
			return
		}
	}

	// 6. Check stream limits
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

	// 7. Reverse-proxy the request through smux
	p.serveReverseProxy(w, r, session)
}

func (p *ProxyHandler) serveReverseProxy(w http.ResponseWriter, r *http.Request, sess *registry.TunnelSession) {
	if sess.Transport == nil {
		http.Error(w, "Service Unavailable: tunnel transport offline", http.StatusServiceUnavailable)
		return
	}

	targetURL := &url.URL{
		Scheme: "http",
		Host:   r.Host,
	}

	rp := httputil.NewSingleHostReverseProxy(targetURL)
	rp.Transport = sess.Transport
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

	var capture *responseCapture
	var writer http.ResponseWriter = w

	isAsset := isCacheableAsset(r.URL.Path) && r.Method == http.MethodGet
	if p.cache != nil && isAsset {
		capture = &responseCapture{
			ResponseWriter: w,
			body:           &bytes.Buffer{},
			maxCapture:     15 * 1024 * 1024,
		}
		writer = capture
		w.Header().Set("X-TexLite-Cache", "MISS")
	}

	rp.ServeHTTP(writer, r)

	if capture != nil && capture.statusCode == http.StatusOK && !capture.overflow && capture.body != nil && capture.body.Len() > 0 {
		_ = p.cache.Put(r.URL.Path, capture.body.Bytes())
	}
}

func isCacheableAsset(path string) bool {
	clean := strings.TrimPrefix(path, "/")
	if strings.HasPrefix(clean, "assets/") {
		return true
	}
	switch clean {
	case "logo.svg", "pdf-download.svg", "pdf-file.svg", "tex-file.svg", "favicon.ico":
		return true
	}
	return false
}

type responseCapture struct {
	http.ResponseWriter
	statusCode int
	body       *bytes.Buffer
	maxCapture int
	overflow   bool
}

func (c *responseCapture) WriteHeader(code int) {
	c.statusCode = code
	c.ResponseWriter.WriteHeader(code)
}

func (c *responseCapture) Write(b []byte) (int, error) {
	if c.statusCode == 0 {
		c.statusCode = http.StatusOK
	}
	if !c.overflow && c.body != nil {
		if c.body.Len()+len(b) <= c.maxCapture {
			c.body.Write(b)
		} else {
			c.overflow = true
			c.body = nil
		}
	}
	return c.ResponseWriter.Write(b)
}

func (c *responseCapture) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
