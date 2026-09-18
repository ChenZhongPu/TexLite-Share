package registry_test

import (
	"testing"
	"time"

	"texlite-share/internal/registry"
)

func TestRegistry_RegisterAndGet(t *testing.T) {
	reg := registry.NewRegistry()
	shareID := "abc12345"
	expiresAt := time.Now().Add(time.Hour)

	gen1, old1 := reg.Register(shareID, nil, expiresAt)
	if old1 != nil {
		t.Fatalf("expected nil oldSession, got %v", old1)
	}
	if gen1 == 0 {
		t.Fatal("expected non-zero generation")
	}

	sess, found := reg.Get(shareID)
	if !found {
		t.Fatal("expected session to be found")
	}
	if sess.Generation != gen1 {
		t.Fatalf("expected generation %d, got %d", gen1, sess.Generation)
	}
	if reg.Count() != 1 {
		t.Fatalf("expected count 1, got %d", reg.Count())
	}
}

func TestRegistry_SessionReplacement(t *testing.T) {
	reg := registry.NewRegistry()
	shareID := "abc12345"

	gen1, _ := reg.Register(shareID, nil, time.Now().Add(time.Hour))
	gen2, _ := reg.Register(shareID, nil, time.Now().Add(2*time.Hour))

	if gen2 <= gen1 {
		t.Fatalf("expected gen2 > gen1, got gen1=%d, gen2=%d", gen1, gen2)
	}

	// Try unregister with old gen1; should NOT remove session
	unregistered := reg.Unregister(shareID, gen1)
	if unregistered {
		t.Fatal("unregister with old generation should have failed")
	}

	sess, found := reg.Get(shareID)
	if !found || sess.Generation != gen2 {
		t.Fatal("expected gen2 session to still be present")
	}

	// Unregister with gen2; should succeed
	unregistered = reg.Unregister(shareID, gen2)
	if !unregistered {
		t.Fatal("unregister with matching generation should have succeeded")
	}

	_, found = reg.Get(shareID)
	if found {
		t.Fatal("expected session to be removed")
	}
}

func TestRegistry_CloseAll(t *testing.T) {
	reg := registry.NewRegistry()
	reg.Register("s1", nil, time.Now().Add(time.Hour))
	reg.Register("s2", nil, time.Now().Add(time.Hour))

	if reg.Count() != 2 {
		t.Fatalf("expected 2 sessions, got %d", reg.Count())
	}

	reg.CloseAll()

	if reg.Count() != 0 {
		t.Fatalf("expected 0 sessions after CloseAll, got %d", reg.Count())
	}
}

func TestRegistry_StreamCounters(t *testing.T) {
	reg := registry.NewRegistry()
	reg.Register("s1", nil, time.Now().Add(time.Hour))
	sess, _ := reg.Get("s1")

	reg.IncrStreams(sess)
	reg.IncrStreams(sess)

	if sess.ActiveStreams.Load() != 2 {
		t.Fatalf("expected 2 active streams on session, got %d", sess.ActiveStreams.Load())
	}
	if reg.TotalStreams() != 2 {
		t.Fatalf("expected 2 total streams on registry, got %d", reg.TotalStreams())
	}

	reg.DecrStreams(sess)
	if sess.ActiveStreams.Load() != 1 {
		t.Fatalf("expected 1 active stream on session, got %d", sess.ActiveStreams.Load())
	}
	if reg.TotalStreams() != 1 {
		t.Fatalf("expected 1 total stream on registry, got %d", reg.TotalStreams())
	}
}
