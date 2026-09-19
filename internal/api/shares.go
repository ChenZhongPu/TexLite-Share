package api

import (
	"context"
	"crypto/subtle"
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
	"texlite-share/internal/geoip"
	"texlite-share/internal/logging"
	"texlite-share/internal/protocol"
	"texlite-share/internal/registry"
)

type contextKey string

const AdminAuthContextKey contextKey = "texlite_admin_auth"

func secureCompare(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// WithAdminAuth returns a new context marked as authenticated admin.
func WithAdminAuth(ctx context.Context) context.Context {
	return context.WithValue(ctx, AdminAuthContextKey, true)
}

// IsAdminRequest returns true if the request was made via admin context or with the admin API key.
func IsAdminRequest(r *http.Request, adminKey string) bool {
	if r.Context().Value(AdminAuthContextKey) != nil {
		return true
	}
	if adminKey != "" {
		token, err := auth.ExtractBearerToken(r)
		if err == nil && secureCompare(token, adminKey) {
			return true
		}
	}
	return false
}

// ServerAPI encapsulates all API handlers for share management and tunnel termination.
type ServerAPI struct {
	cfg      *config.ServerConfig
	db       *database.DB
	registry *registry.Registry
	limiter  *IPRateLimiter
}

// NewServerAPI initializes a new ServerAPI.
func NewServerAPI(cfg *config.ServerConfig, db *database.DB, reg *registry.Registry) *ServerAPI {
	rateLimit := cfg.RateLimitPerDay
	return &ServerAPI{
		cfg:      cfg,
		db:       db,
		registry: reg,
		limiter:  NewIPRateLimiter(rateLimit, 24*time.Hour),
	}
}

// CreateShareRequest represents the optional JSON body for creating a share.
type CreateShareRequest struct {
	ID         string `json:"id,omitempty"`         // Optional custom ID for fixed subdomain (Admin only)
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

	clientIP := ExtractClientIP(r)

	// 1. IP rate limiting (daily frequency check)
	if a.limiter != nil && !a.limiter.Allow(clientIP) {
		slog.Warn("client share creation daily rate limit exceeded", "client_ip", clientIP)
		http.Error(w, "Too Many Requests: daily share creation rate limit exceeded for your IP. Please try again tomorrow.", http.StatusTooManyRequests)
		return
	}

	// 2. Verify creation auth (API key)
	if a.cfg.CreateAPIKey != "" {
		token, err := auth.ExtractBearerToken(r)
		if err != nil || !secureCompare(token, a.cfg.CreateAPIKey) {
			slog.Warn("unauthorized share creation attempt", "remote_addr", r.RemoteAddr)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
	}

	// 3. TTL calculation & custom fixed share ID (Admin only)
	// Standard clients cannot customize TTL; the default expiration is strictly 1h (ServerConfig.DefaultTTL).
	// Only server administrators or requests authenticated via the Admin port / Admin API key may customize TTL or set a fixed ID.
	ttl := a.cfg.DefaultTTL
	var customShareID string

	if IsAdminRequest(r, a.cfg.AdminAPIKey) && r.Body != nil {
		var req CreateShareRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			if req.TTL != "" {
				if d, err := time.ParseDuration(req.TTL); err == nil && d > 0 {
					ttl = d
				}
			} else if req.TTLSeconds > 0 {
				ttl = time.Duration(req.TTLSeconds) * time.Second
			}
			if req.ID != "" {
				reqID := strings.TrimSpace(strings.ToLower(req.ID))
				if err := protocol.ValidateShareID(reqID); err != nil {
					http.Error(w, fmt.Sprintf("Invalid Share ID format %q: %v", reqID, err), http.StatusBadRequest)
					return
				}
				// Check if ID is already in use by an active share
				existing, err := a.db.GetShare(r.Context(), reqID)
				if err == nil && existing != nil && existing.Status == database.StatusActive && time.Now().UTC().Before(existing.ExpiresAt) {
					http.Error(w, fmt.Sprintf("Conflict: Share ID %q is already active", reqID), http.StatusConflict)
					return
				}
				customShareID = reqID
			}
		}
	}
	if ttl <= 0 {
		ttl = 1 * time.Hour
	}

	now := time.Now().UTC()

	// 4. Per-IP active share quota check
	if a.cfg.MaxSharesPerIP > 0 {
		ipActiveCount, err := a.db.CountActiveSharesByIP(r.Context(), clientIP, now)
		if err != nil {
			slog.Error("failed to count active shares by IP", "client_ip", clientIP, "error", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		if ipActiveCount >= a.cfg.MaxSharesPerIP {
			slog.Warn("client exceeded maximum active shares per IP", "client_ip", clientIP, "active", ipActiveCount, "max", a.cfg.MaxSharesPerIP)
			http.Error(w, "Too Many Requests: maximum active shares reached for your IP", http.StatusTooManyRequests)
			return
		}
	}

	// 5. Global server capacity check
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

	shareID := customShareID
	if shareID == "" {
		generated, err := protocol.GenerateShareID()
		if err != nil {
			slog.Error("failed to generate share ID", "error", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		shareID = generated
	}

	token, err := auth.GenerateToken()
	if err != nil {
		slog.Error("failed to generate tunnel token", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	tokenHash := auth.HashToken(token)
	expiresAt := now.Add(ttl)
	country := geoip.LookupWithContext(r.Context(), clientIP)

	share := &database.Share{
		ID:            shareID,
		TokenHash:     tokenHash[:],
		Status:        database.StatusActive,
		CreatedAt:     now,
		ExpiresAt:     expiresAt,
		ClientIP:      clientIP,
		ClientCountry: country,
	}

	if err := a.db.CreateShare(r.Context(), share); err != nil {
		slog.Error("failed to store share in db", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	publicURL := fmt.Sprintf("http://%s.%s", shareID, a.cfg.BaseDomain)
	resp := CreateShareResponse{
		ID:        shareID,
		Token:     token,
		ExpiresAt: expiresAt,
		PublicURL: publicURL,
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)

	slog.Info("share created successfully",
		"event", logging.EventShareCreated,
		"share_id", shareID,
		"expires_at", expiresAt.Format(time.RFC3339),
	)
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

	isAdmin := (a.cfg.AdminAPIKey != "" && secureCompare(bearer, a.cfg.AdminAPIKey)) ||
		(a.cfg.CreateAPIKey != "" && secureCompare(bearer, a.cfg.CreateAPIKey))
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
