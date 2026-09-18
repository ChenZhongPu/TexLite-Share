package tunnel_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"texlite-share/internal/tunnel"
)

func TestProbeLocalService(t *testing.T) {
	// 1. Port with nothing listening
	err := tunnel.ProbeLocalService("127.0.0.1:49999", 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected error for closed port, got nil")
	}

	// 2. Local service running
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()

	err = tunnel.ProbeLocalService(listener.Addr().String(), 500*time.Millisecond)
	if err != nil {
		t.Fatalf("expected success for open port, got error: %v", err)
	}
}

func TestAutoCreateAndRevokeShare(t *testing.T) {
	var revokedID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/shares" && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(tunnel.CreatedShare{
				ID:        "auto12345678",
				Token:     "test-token",
				ExpiresAt: time.Now().Add(time.Hour),
				PublicURL: "http://auto12345678.share.local",
			})
			return
		}
		if r.URL.Path == "/api/v1/shares/auto12345678" && r.Method == http.MethodDelete {
			revokedID = "auto12345678"
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	// 1. Auto create
	share, err := tunnel.AutoCreateShare(context.Background(), server.URL, "", "2h")
	if err != nil {
		t.Fatalf("AutoCreateShare failed: %v", err)
	}
	if share.ID != "auto12345678" || share.Token != "test-token" {
		t.Fatalf("unexpected share returned: %+v", share)
	}

	// 2. Auto revoke
	err = tunnel.AutoRevokeShare(context.Background(), server.URL, share.ID, share.Token)
	if err != nil {
		t.Fatalf("AutoRevokeShare failed: %v", err)
	}
	if revokedID != share.ID {
		t.Fatalf("expected revokedID %q, got %q", share.ID, revokedID)
	}
}
