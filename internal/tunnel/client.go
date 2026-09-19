package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/xtaci/smux"

	"texlite-share/internal/config"
	"texlite-share/internal/logging"
	"texlite-share/internal/protocol"
)

var (
	// ErrTerminal indicates that the tunnel client received a non-retryable error.
	ErrTerminal      = errors.New("terminal tunnel error")
	ErrShareExpired  = fmt.Errorf("%w: share has expired on server (HTTP 410)", ErrTerminal)
	ErrShareRevoked  = fmt.Errorf("%w: share has been revoked (HTTP 403)", ErrTerminal)
	ErrShareNotFound = fmt.Errorf("%w: share not found on server (HTTP 404)", ErrTerminal)
	ErrAuthFailed    = fmt.Errorf("%w: authentication failed (invalid token, HTTP 401)", ErrTerminal)
)

// ProbeLocalService checks whether the local destination service is alive and accepting connections.
func ProbeLocalService(localAddr string, timeout time.Duration) error {
	if err := config.ValidateLocalAddr(localAddr); err != nil {
		return err
	}
	if timeout <= 0 {
		timeout = 1500 * time.Millisecond
	}

	conn, err := net.DialTimeout("tcp", localAddr, timeout)
	if err != nil {
		return fmt.Errorf("could not connect to local TexLite service at %s: %w", localAddr, err)
	}
	_ = conn.Close()
	return nil
}

// CreatedShare contains the response metadata from auto-creating a share on the server.
type CreatedShare struct {
	ID        string    `json:"id"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
	PublicURL string    `json:"publicUrl"`
}

// AutoCreateShare calls the server's POST /api/v1/shares to provision a share dynamically.
func AutoCreateShare(ctx context.Context, serverURL, apiKey string) (*CreatedShare, error) {
	endpoint := strings.TrimRight(serverURL, "/") + "/api/v1/shares"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to construct create share request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to reach share server at %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := ioReadAll(resp.Body, 512)
		return nil, fmt.Errorf("server returned error %d: %s", resp.StatusCode, string(respBody))
	}

	var share CreatedShare
	if err := json.NewDecoder(resp.Body).Decode(&share); err != nil {
		return nil, fmt.Errorf("failed to decode create share response: %w", err)
	}
	return &share, nil
}

// AutoRevokeShare calls DELETE /api/v1/shares/{id} to clean up the share upon client exit.
func AutoRevokeShare(ctx context.Context, serverURL, shareID, token string) error {
	endpoint := fmt.Sprintf("%s/api/v1/shares/%s", strings.TrimRight(serverURL, "/"), shareID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := ioReadAll(resp.Body, 256)
		return fmt.Errorf("server returned status %d on revoke: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func ioReadAll(r io.Reader, maxBytes int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, maxBytes))
}

// RunClient runs the tunnel client loop, maintaining a persistent multiplexed connection
// to the share server and forwarding streams to the local target address.
func RunClient(ctx context.Context, cfg *config.ClientConfig) error {
	if err := config.ValidateLocalAddr(cfg.LocalAddr); err != nil {
		return err
	}

	wsURL, err := config.DeriveWebSocketURL(cfg.ServerURL, cfg.ShareID)
	if err != nil {
		return err
	}

	backoff := 1 * time.Second
	maxBackoff := cfg.MaxRetryDelay
	if maxBackoff <= 0 {
		maxBackoff = 30 * time.Second
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		slog.Info("connecting to share server tunnel endpoint",
			"share_id", cfg.ShareID,
			"ws_url", wsURL,
		)

		connectedAt := time.Now()
		err := connectAndServe(ctx, wsURL, cfg)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			if errors.Is(err, ErrTerminal) {
				return err
			}

			// If the session lasted longer than 30s, reset backoff
			if time.Since(connectedAt) > 30*time.Second {
				backoff = 1 * time.Second
			}

			// Calculate jittered delay: backoff ± 20%
			jitterFactor := 0.8 + 0.4*rand.Float64()
			delay := time.Duration(float64(backoff) * jitterFactor)

			slog.Warn("tunnel disconnected or failed to connect, scheduling reconnect",
				"share_id", cfg.ShareID,
				"error", err,
				"retry_in", delay.Round(time.Millisecond),
			)

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}

			// Double backoff up to max
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		} else {
			// Clean exit or reset backoff
			backoff = 1 * time.Second
		}
	}
}

func connectAndServe(ctx context.Context, wsURL string, cfg *config.ClientConfig) error {
	dialOpts := &websocket.DialOptions{
		Subprotocols: []string{protocol.SubprotocolName},
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + cfg.Token},
		},
	}

	wsConn, resp, err := websocket.Dial(ctx, wsURL, dialOpts)
	if err != nil {
		if resp != nil {
			switch resp.StatusCode {
			case http.StatusUnauthorized:
				slog.Error("tunnel authentication failed (invalid token)", "share_id", cfg.ShareID, "status", resp.StatusCode)
				return ErrAuthFailed
			case http.StatusForbidden:
				slog.Error("tunnel rejected (share revoked)", "share_id", cfg.ShareID, "status", resp.StatusCode)
				return ErrShareRevoked
			case http.StatusGone:
				slog.Error("tunnel rejected (share expired)", "share_id", cfg.ShareID, "status", resp.StatusCode)
				return ErrShareExpired
			case http.StatusNotFound:
				slog.Error("tunnel rejected (share not found)", "share_id", cfg.ShareID, "status", resp.StatusCode)
				return ErrShareNotFound
			}
		}
		return fmt.Errorf("websocket dial failed: %w", err)
	}
	defer wsConn.Close(websocket.StatusNormalClosure, "client disconnecting")

	// Set large read limit to support large multiplexed frames without throttling
	wsConn.SetReadLimit(16 * 1024 * 1024)

	if sub := wsConn.Subprotocol(); sub != protocol.SubprotocolName {
		return fmt.Errorf("%w: expected %s, got %s", protocol.ErrProtocolMismatch, protocol.SubprotocolName, sub)
	}

	slog.Info("tunnel connection established", "share_id", cfg.ShareID)

	netConn := websocket.NetConn(ctx, wsConn, websocket.MessageBinary)
	defer netConn.Close()

	smuxSession, err := smux.Client(netConn, SmuxConfig())
	if err != nil {
		return fmt.Errorf("failed to initialize smux client: %w", err)
	}
	defer smuxSession.Close()

	// Accept incoming multiplexed streams from the server and forward to local address
	for {
		stream, err := smuxSession.AcceptStream()
		if err != nil {
			if smuxSession.IsClosed() || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept stream error: %w", err)
		}

		go handleStream(stream, cfg.LocalAddr, cfg.LocalDialTimeout)
	}
}

func handleStream(stream *smux.Stream, localAddr string, dialTimeout time.Duration) {
	if dialTimeout <= 0 {
		dialTimeout = 5 * time.Second
	}

	localConn, err := net.DialTimeout("tcp", localAddr, dialTimeout)
	if err != nil {
		slog.Error("failed to connect to local target address",
			"event", logging.EventStreamDialFailed,
			"local_addr", localAddr,
			"error", err,
		)
		_ = stream.Close()
		return
	}

	slog.Debug("bridging stream to local service",
		"event", logging.EventStreamOpened,
		"local_addr", localAddr,
	)

	Bridge(stream, localConn)

	slog.Debug("stream closed",
		"event", logging.EventStreamClosed,
		"local_addr", localAddr,
	)
}
