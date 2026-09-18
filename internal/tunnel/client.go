package tunnel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/xtaci/smux"

	"texlite-share/internal/config"
	"texlite-share/internal/logging"
	"texlite-share/internal/protocol"
)

// ErrTerminal indicates that the tunnel client received a non-retryable error (e.g., 401, 403, 410).
var ErrTerminal = errors.New("terminal tunnel error")

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
				return fmt.Errorf("%w: invalid token (HTTP 401)", ErrTerminal)
			case http.StatusForbidden:
				slog.Error("tunnel rejected (share revoked)", "share_id", cfg.ShareID, "status", resp.StatusCode)
				return fmt.Errorf("%w: share revoked (HTTP 403)", ErrTerminal)
			case http.StatusGone:
				slog.Error("tunnel rejected (share expired)", "share_id", cfg.ShareID, "status", resp.StatusCode)
				return fmt.Errorf("%w: share expired (HTTP 410)", ErrTerminal)
			}
		}
		return fmt.Errorf("websocket dial failed: %w", err)
	}
	defer wsConn.Close(websocket.StatusNormalClosure, "client disconnecting")

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
