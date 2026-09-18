package registry

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtaci/smux"
)

// TunnelSession holds an active tunnel session with its metadata and generation counter.
type TunnelSession struct {
	ShareID       string
	Session       *smux.Session
	Connected     time.Time
	ExpiresAt     time.Time
	Generation    uint64
	ActiveStreams atomic.Int32
}

// Registry manages in-memory active tunnel sessions.
type Registry struct {
	mu          sync.RWMutex
	sessions    map[string]*TunnelSession
	nextGen     uint64
	totalStreams atomic.Int32
}

// NewRegistry creates a new active tunnel registry.
func NewRegistry() *Registry {
	return &Registry{
		sessions: make(map[string]*TunnelSession),
	}
}

// Register adds or replaces a session for the specified shareID.
// If an existing session is replaced, the old session is returned so the caller can close it asynchronously.
func (r *Registry) Register(shareID string, session *smux.Session, expiresAt time.Time) (uint64, *smux.Session) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextGen++
	gen := r.nextGen

	var oldSession *smux.Session
	if existing, found := r.sessions[shareID]; found {
		oldSession = existing.Session
	}

	r.sessions[shareID] = &TunnelSession{
		ShareID:    shareID,
		Session:    session,
		Connected:  time.Now().UTC(),
		ExpiresAt:  expiresAt,
		Generation: gen,
	}

	return gen, oldSession
}

// Unregister removes the session for shareID if its generation matches the given gen.
// This prevents older disconnected sessions from inadvertently removing newer replacement sessions.
func (r *Registry) Unregister(shareID string, gen uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, found := r.sessions[shareID]
	if !found {
		return false
	}

	if existing.Generation == gen {
		delete(r.sessions, shareID)
		return true
	}
	return false
}

// Get retrieves the active TunnelSession for a shareID.
func (r *Registry) Get(shareID string) (*TunnelSession, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sess, found := r.sessions[shareID]
	return sess, found
}

// Close closes and removes an active tunnel session for a shareID immediately.
func (r *Registry) Close(shareID string) bool {
	r.mu.Lock()
	existing, found := r.sessions[shareID]
	if found {
		delete(r.sessions, shareID)
	}
	r.mu.Unlock()

	if found && existing.Session != nil {
		existing.Session.Close()
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
		if s.Session != nil {
			s.Session.Close()
		}
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

// DecrStreams decrements active stream counters.
func (r *Registry) DecrStreams(sess *TunnelSession) {
	sess.ActiveStreams.Add(-1)
	r.totalStreams.Add(-1)
}
