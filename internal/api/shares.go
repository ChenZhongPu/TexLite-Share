package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"texlite-share/internal/auth"
	"texlite-share/internal/config"
	"texlite-share/internal/database"
	"texlite-share/internal/logging"
	"texlite-share/internal/protocol"
	"texlite-share/internal/registry"
)

// ServerAPI encapsulates all API handlers for share management and tunnel termination.
type ServerAPI struct {
	cfg      *config.ServerConfig
	db       *database.DB
	registry *registry.Registry
}

// NewServerAPI initializes a new ServerAPI.
func NewServerAPI(cfg *config.ServerConfig, db *database.DB, reg *registry.Registry) *ServerAPI {
	return &ServerAPI{
		cfg:      cfg,
		db:       db,
		registry: reg,
	}
}

// CreateShareRequest represents the optional JSON body for creating a share.
type CreateShareRequest struct {
	TTL        string `json:"ttl,omitempty"`        // e.g. "2h", "30m"
	TTLSeconds int64  `json:"ttlSeconds,omitempty"` // e.g. 7200
}

// CreateShareResponse represents the response payload for a created share.
type CreateShareResponse struct {
	ID        string    `json:"id"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
	PublicURL string    `json:"publicUrl"`
}

// HandleCreateShare handles POST /api/v1/shares.
func (a *ServerAPI) HandleCreateShare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// Verify creation auth (API key)
	if a.cfg.CreateAPIKey != "" {
		token, err := auth.ExtractBearerToken(r)
		if err != nil || token != a.cfg.CreateAPIKey {
			slog.Warn("unauthorized share creation attempt", "remote_addr", r.RemoteAddr)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
	}

	// Optional request body parsing for custom TTL
	ttl := a.cfg.DefaultTTL
	if r.Body != nil && r.ContentLength > 0 {
		var req CreateShareRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			if req.TTL != "" {
				if d, err := time.ParseDuration(req.TTL); err == nil && d > 0 {
					ttl = d
				}
			} else if req.TTLSeconds > 0 {
				ttl = time.Duration(req.TTLSeconds) * time.Second
			}
		}
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}

	now := time.Now().UTC()

	// Check logical capacity limit
	activeCount, err := a.db.CountActiveShares(r.Context(), now)
	if err != nil {
		slog.Error("failed to count active shares", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if activeCount >= a.cfg.MaxActiveShares {
		slog.Warn("share capacity limit reached", "active_count", activeCount, "max", a.cfg.MaxActiveShares)
		http.Error(w, "Capacity Exceeded: maximum active shares reached", http.StatusServiceUnavailable)
		return
	}

	shareID, err := protocol.GenerateShareID()
	if err != nil {
		slog.Error("failed to generate share ID", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	token, err := auth.GenerateToken()
	if err != nil {
		slog.Error("failed to generate tunnel token", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	tokenHash := auth.HashToken(token)
	expiresAt := now.Add(ttl)

	share := &database.Share{
		ID:        shareID,
		TokenHash: tokenHash[:],
		Status:    database.StatusActive,
		CreatedAt: now,
		ExpiresAt: expiresAt,
	}

	if err := a.db.CreateShare(r.Context(), share); err != nil {
		slog.Error("failed to store share in db", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}

	publicURL := fmt.Sprintf("%s://%s.%s", scheme, shareID, a.cfg.BaseDomain)

	slog.Info("share created successfully",
		"event", logging.EventShareCreated,
		"share_id", shareID,
		"expires_at", expiresAt.Format(time.RFC3339),
	)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(CreateShareResponse{
		ID:        shareID,
		Token:     token,
		ExpiresAt: expiresAt,
		PublicURL: publicURL,
	})
}

// HandleRevokeShare handles DELETE /api/v1/shares/{shareID}.
func (a *ServerAPI) HandleRevokeShare(w http.ResponseWriter, r *http.Request, shareID string) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := protocol.ValidateShareID(shareID); err != nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	share, err := a.db.GetShare(r.Context(), shareID)
	if err != nil {
		if errors.Is(err, protocol.ErrShareNotFound) {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Verify authorization: admin API key OR the share's own tunnel token
	bearer, err := auth.ExtractBearerToken(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	isAdmin := a.cfg.CreateAPIKey != "" && bearer == a.cfg.CreateAPIKey
	isShareToken := auth.VerifyToken(bearer, share.TokenHash)
	if !isAdmin && !isShareToken {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	now := time.Now().UTC()
	if err := a.db.RevokeShare(r.Context(), shareID, now); err != nil {
		slog.Error("failed to revoke share in db", "share_id", shareID, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Immediately terminate active session in registry
	a.registry.Close(shareID)

	slog.Info("share revoked",
		"event", logging.EventShareRevoked,
		"share_id", shareID,
	)

	w.WriteHeader(http.StatusNoContent)
}
