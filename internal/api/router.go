package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"texlite-share/internal/config"
	"texlite-share/internal/database"
	"texlite-share/internal/logging"
	"texlite-share/internal/proxy"
	"texlite-share/internal/registry"
)

// Router unifies routing between control APIs, tunnel endpoints, and public subdomain reverse proxies.
type Router struct {
	cfg          *config.ServerConfig
	api          *ServerAPI
	proxyHandler *proxy.ProxyHandler
}

// NewRouter creates an unified HTTP Handler for texlite-share-server.
func NewRouter(cfg *config.ServerConfig, db *database.DB, reg *registry.Registry) *Router {
	return &Router{
		cfg:          cfg,
		api:          NewServerAPI(cfg, db, reg),
		proxyHandler: proxy.NewProxyHandler(cfg, db, reg),
	}
}

// ServeHTTP inspects the incoming request URL path and Host header to dispatch appropriately.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// 1. Health check endpoint
	if path == "/healthz" || path == "/api/v1/health" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		return
	}

	// 2. Control APIs & Tunnel WebSocket endpoint
	if path == "/api/v1/shares" {
		rt.api.HandleCreateShare(w, r)
		return
	}

	if strings.HasPrefix(path, "/api/v1/shares/") {
		shareID := strings.TrimPrefix(path, "/api/v1/shares/")
		rt.api.HandleRevokeShare(w, r, shareID)
		return
	}

	if strings.HasPrefix(path, "/api/v1/tunnel/") {
		shareID := strings.TrimPrefix(path, "/api/v1/tunnel/")
		rt.api.HandleTunnelWS(w, r, shareID)
		return
	}

	// 3. Public subdomain proxy routing: check if Host header contains a valid share subdomain
	if _, err := proxy.ExtractShareID(r.Host, rt.cfg.BaseDomain); err == nil {
		rt.proxyHandler.ServeHTTP(w, r)
		return
	}

	// 4. Default fallback: not found
	http.Error(w, "Not Found", http.StatusNotFound)
}

// StartExpirationWorker starts a background ticker to sweep expired shares and close their sessions.
func StartExpirationWorker(ctx context.Context, db *database.DB, reg *registry.Registry, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				expiredIDs, err := db.ExpireShares(ctx, now.UTC())
				if err != nil {
					slog.Error("failed to sweep expired shares", "error", err)
					continue
				}

				for _, id := range expiredIDs {
					slog.Info("share expired, closing tunnel session",
						"event", logging.EventShareExpired,
						"share_id", id,
					)
					reg.Close(id)
				}
			}
		}
	}()
}
