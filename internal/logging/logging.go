package logging

import (
	"log/slog"
	"os"
)

// Standard event names defined in the specification.
const (
	EventShareCreated        = "share.created"
	EventShareExpired        = "share.expired"
	EventShareRevoked        = "share.revoked"
	EventTunnelConnected     = "tunnel.connected"
	EventTunnelReplaced      = "tunnel.replaced"
	EventTunnelDisconnected  = "tunnel.disconnected"
	EventTunnelAuthFailed    = "tunnel.auth_failed"
	EventStreamOpened        = "stream.opened"
	EventStreamClosed        = "stream.closed"
	EventStreamDialFailed    = "stream.local_dial_failed"
	EventProxyRequestFailed  = "proxy.request_failed"
)

// InitLogger configures the default slog logger.
func InitLogger(debug bool) *slog.Logger {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, opts))
	slog.SetDefault(logger)
	return logger
}
