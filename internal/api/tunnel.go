package api

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/xtaci/smux"

	"texlite-share/internal/auth"
	"texlite-share/internal/database"
	"texlite-share/internal/logging"
	"texlite-share/internal/protocol"
	"texlite-share/internal/tunnel"
)

// HandleTunnelWS handles GET /api/v1/tunnel/{shareID}, performing authentication and upgrading to smux multiplexer.
func (a *ServerAPI) HandleTunnelWS(w http.ResponseWriter, r *http.Request, shareID string) {
	// 1. Validate share ID
	if err := protocol.ValidateShareID(shareID); err != nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	// 2. Extract bearer token
	token, err := auth.ExtractBearerToken(r)
	if err != nil {
		slog.Warn("tunnel request missing bearer token",
			"event", logging.EventTunnelAuthFailed,
			"share_id", shareID,
			"remote_ip", r.RemoteAddr,
		)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// 3. Database lookup
	share, err := a.db.GetShare(r.Context(), shareID)
	if err != nil {
		if errors.Is(err, protocol.ErrShareNotFound) {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		slog.Error("database query error during tunnel handshake", "share_id", shareID, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// 4. Verify share status & expiration
	now := time.Now().UTC()
	if share.Status == database.StatusRevoked {
		slog.Warn("attempt to connect to revoked share",
			"event", logging.EventTunnelAuthFailed,
			"share_id", shareID,
		)
		http.Error(w, "Forbidden: share revoked", http.StatusForbidden)
		return
	}

	if share.Status == database.StatusExpired || now.After(share.ExpiresAt) {
		slog.Warn("attempt to connect to expired share",
			"event", logging.EventTunnelAuthFailed,
			"share_id", shareID,
		)
		http.Error(w, "Gone: share expired", http.StatusGone)
		return
	}

	if share.Status != database.StatusActive {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	// 5. Verify token against stored SHA-256 hash
	if !auth.VerifyToken(token, share.TokenHash) {
		slog.Warn("invalid tunnel token provided",
			"event", logging.EventTunnelAuthFailed,
			"share_id", shareID,
			"remote_ip", r.RemoteAddr,
		)
		http.Error(w, "Unauthorized: invalid token", http.StatusUnauthorized)
		return
	}

	// 6. Upgrade HTTP request to WebSocket with subprotocol negotiation
	wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{protocol.SubprotocolName},
	})
	if err != nil {
		slog.Error("websocket upgrade failed", "share_id", shareID, "error", err)
		return
	}
	defer wsConn.Close(websocket.StatusNormalClosure, "server closing tunnel")

	// 7. Wrap WebSocket as net.Conn
	netConn := websocket.NetConn(r.Context(), wsConn, websocket.MessageBinary)
	defer netConn.Close()

	// 8. Initialize smux server session
	smuxSession, err := smux.Server(netConn, tunnel.SmuxConfig())
	if err != nil {
		slog.Error("failed to start smux server session", "share_id", shareID, "error", err)
		return
	}
	defer smuxSession.Close()

	// 9. Register session in in-memory registry (new valid tunnel wins)
	gen, oldSession := a.registry.Register(shareID, smuxSession, share.ExpiresAt)
	if oldSession != nil {
		slog.Info("replacing existing tunnel session",
			"event", logging.EventTunnelReplaced,
			"share_id", shareID,
			"new_generation", gen,
		)
		go oldSession.Close()
	}

	slog.Info("tunnel connected successfully",
		"event", logging.EventTunnelConnected,
		"share_id", shareID,
		"generation", gen,
		"remote_ip", r.RemoteAddr,
	)

	// Keep connection alive until closed by either peer or context
	select {
	case <-r.Context().Done():
	case <-smuxSession.CloseChan():
	}

	a.registry.Unregister(shareID, gen)
	slog.Info("tunnel session terminated",
		"event", logging.EventTunnelDisconnected,
		"share_id", shareID,
		"generation", gen,
	)
}

