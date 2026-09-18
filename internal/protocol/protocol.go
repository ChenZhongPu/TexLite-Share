package protocol

import (
	"errors"
	"net/http"
)

// SubprotocolName is the WebSocket subprotocol defined for TexLite Share Tunnel v1.
const SubprotocolName = "texlite-tunnel-v1"

// Predefined typed errors across the system.
var (
	ErrInvalidShare     = errors.New("invalid share ID")
	ErrShareNotFound    = errors.New("share not found")
	ErrInvalidToken     = errors.New("invalid or missing tunnel token")
	ErrExpired          = errors.New("share has expired")
	ErrRevoked          = errors.New("share has been revoked")
	ErrTunnelOffline    = errors.New("tunnel is currently offline")
	ErrCapacityExceeded = errors.New("server share capacity exceeded")
	ErrProtocolMismatch = errors.New("unsupported protocol version")
	ErrTooManyStreams   = errors.New("too many active streams")
	ErrInvalidLocalAddr = errors.New("local address must be a loopback address")
)

// ErrorToHTTPStatus maps typed protocol errors to HTTP status codes.
func ErrorToHTTPStatus(err error) int {
	switch {
	case errors.Is(err, ErrInvalidShare), errors.Is(err, ErrShareNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrInvalidToken):
		return http.StatusUnauthorized
	case errors.Is(err, ErrRevoked):
		return http.StatusForbidden
	case errors.Is(err, ErrExpired):
		return http.StatusGone
	case errors.Is(err, ErrCapacityExceeded):
		return http.StatusServiceUnavailable
	case errors.Is(err, ErrTunnelOffline):
		return http.StatusServiceUnavailable
	case errors.Is(err, ErrTooManyStreams):
		return http.StatusTooManyRequests
	case errors.Is(err, ErrProtocolMismatch), errors.Is(err, ErrInvalidLocalAddr):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
