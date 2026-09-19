package registry

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtaci/smux"

	"texlite-share/internal/protocol"
)

// TunnelSession holds an active tunnel session with its metadata, generation counter, and pooled transport.
type TunnelSession struct {
	ShareID       string
	Session       *smux.Session
	Connected     time.Time
	ExpiresAt     time.Time
	Generation    uint64
	ActiveStreams atomic.Int32
	Transport     *http.Transport
}

// Close closes the underlying smux session and frees all pooled idle HTTP connections.
func (s *TunnelSession) Close() {
	if s.Transport != nil {
		s.Transport.CloseIdleConnections()
	}
	if s.Session != nil {
		_ = s.Session.Close()
	}
}

// trackedConn wraps net.Conn to decrement stream counters upon close.
type trackedConn struct {
	net.Conn
	registry *Registry
	session  *TunnelSession
	once     sync.Once
}

func (c *trackedConn) Close() error {
	c.once.Do(func() {
		c.registry.DecrStreams(c.session)
	})
	return c.Conn.Close()
}

// Registry manages in-memory active tunnel sessions.
type Registry struct {
	mu           sync.RWMutex
	sessions     map[string]*TunnelSession
	nextGen      uint64
	totalStreams atomic.Int32
}

// NewRegistry creates a new active tunnel registry.
func NewRegistry() *Registry {
	return &Registry{
		sessions: make(map[string]*TunnelSession),
	}
}

// Register adds or replaces a session for the specified shareID.
// It initializes a reusable http.Transport for connection pooling across HTTP requests.
// If an existing session is replaced, the old session is returned so the caller can close it asynchronously.
func (r *Registry) Register(shareID string, session *smux.Session, expiresAt time.Time) (uint64, *TunnelSession) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextGen++
	gen := r.nextGen

	var oldSession *TunnelSession
	if existing, found := r.sessions[shareID]; found {
		oldSession = existing
	}

	sess := &TunnelSession{
		ShareID:    shareID,
		Session:    session,
		Connected:  time.Now().UTC(),
		ExpiresAt:  expiresAt,
		Generation: gen,
	}

	if session != nil {
		sess.Transport = &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if sess.Session == nil || sess.Session.IsClosed() {
					return nil, protocol.ErrTunnelOffline
				}

				stream, err := sess.Session.OpenStream()
				if err != nil {
					return nil, fmt.Errorf("failed to open smux stream: %w", err)
				}

				r.IncrStreams(sess)
				return &trackedConn{
					Conn:     stream,
					registry: r,
					session:  sess,
				}, nil
			},
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   50,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
	}

	r.sessions[shareID] = sess
	return gen, oldSession
}

// Unregister removes the session for shareID if its generation matches the given gen.
// This prevents older disconnected sessions from inadvertently removing newer replacement sessions.
func (r *Registry) Unregister(shareID string, gen uint64) bool {
	r.mu.Lock()
	existing, found := r.sessions[shareID]
	if !found {
		r.mu.Unlock()
		return false
	}

	if existing.Generation == gen {
		delete(r.sessions, shareID)
		r.mu.Unlock()
		existing.Close()
		return true
	}
	r.mu.Unlock()
	return false
}

// Get retrieves the active TunnelSession for a shareID.
func (r *Registry) Get(shareID string) (*TunnelSession, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sess, found := r.sessions[shareID]
	return sess, found
}

// UpdateExpiration updates the expiration time of an active session in the registry.
func (r *Registry) UpdateExpiration(shareID string, expiresAt time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if sess, found := r.sessions[shareID]; found {
		sess.ExpiresAt = expiresAt
	}
}

// Close closes and removes an active tunnel session for a shareID immediately.
func (r *Registry) Close(shareID string) bool {
	r.mu.Lock()
	existing, found := r.sessions[shareID]
	if found {
		delete(r.sessions, shareID)
	}
	r.mu.Unlock()

	if found {
		existing.Close()
		return true
	}
	return false
}

// CloseAll closes all active tunnel sessions in the registry.
func (r *Registry) CloseAll() {
	r.mu.Lock()
	all := make([]*TunnelSession, 0, len(r.sessions))
	for _, s := range r.sessions {
		all = append(all, s)
	}
	r.sessions = make(map[string]*TunnelSession)
	r.mu.Unlock()

	for _, s := range all {
		s.Close()
	}
}

// Count returns the number of active sessions in the registry.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.sessions)
}

// TotalStreams returns the total number of currently active streams across all sessions.
func (r *Registry) TotalStreams() int32 {
	return r.totalStreams.Load()
}

// IncrStreams increments active stream counters for both the session and the global registry.
func (r *Registry) IncrStreams(sess *TunnelSession) {
	sess.ActiveStreams.Add(1)
	r.totalStreams.Add(1)
}

// DecrStreams decrements active stream counters, clamping at 0.
func (r *Registry) DecrStreams(sess *TunnelSession) {
	for {
		curr := sess.ActiveStreams.Load()
		if curr <= 0 {
			break
		}
		if sess.ActiveStreams.CompareAndSwap(curr, curr-1) {
			break
		}
	}
	for {
		curr := r.totalStreams.Load()
		if curr <= 0 {
			break
		}
		if r.totalStreams.CompareAndSwap(curr, curr-1) {
			break
		}
	}
}
