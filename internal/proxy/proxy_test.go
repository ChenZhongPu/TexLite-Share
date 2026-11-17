package proxy_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"texlite-share/internal/config"
	"texlite-share/internal/database"
	"texlite-share/internal/proxy"
	"texlite-share/internal/registry"

	"github.com/xtaci/smux"
)

func TestExtractShareID(t *testing.T) {
	tests := []struct {
		host        string
		baseDomain  string
		expectedID  string
		expectError bool
	}{
		{"abc12345.share.local", "share.local", "abc12345", false},
		{"abc12345.share.local:9000", "share.local", "abc12345", false},
		{"k83fx2m7pq4z7abc.share.example.com", "share.example.com", "k83fx2m7pq4z7abc", false},
		{"k83fx2m7pq4z7abc.share.example.com:443", "share.example.com", "k83fx2m7pq4z7abc", false},
		{"K83FX2M7PQ4Z7ABC.SHARE.LOCAL:9000", "share.local", "k83fx2m7pq4z7abc", false}, // case insensitivity
		{"share.local", "share.local", "", true},
		{"share.local:9000", "share.local", "", true},
		{"foo.bar.share.local", "share.local", "", true},
		{"bad_id_12345.share.local", "share.local", "", true}, // invalid characters
		{"short.share.local", "share.local", "", true},         // too short
		{"randomdomain.com", "share.local", "", true},
		{"127.0.0.1:9000", "share.local", "", true},
		{"", "share.local", "", true},
	}

	for _, tc := range tests {
		id, err := proxy.ExtractShareID(tc.host, tc.baseDomain)
		if tc.expectError {
			if err == nil {
				t.Errorf("expected error for host %q (base %q), got ID %q", tc.host, tc.baseDomain, id)
			}
		} else {
			if err != nil {
				t.Errorf("unexpected error for host %q (base %q): %v", tc.host, tc.baseDomain, err)
			}
			if id != tc.expectedID {
				t.Errorf("expected ID %q, got %q for host %q", tc.expectedID, id, tc.host)
			}
		}
	}
}

func TestReverseProxyEnforcesIdentityAcceptEncoding(t *testing.T) {
	var receivedAcceptEncoding string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAcceptEncoding = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "application/javascript")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("console.log('texlite');"))
	}))
	defer backend.Close()

	dir := t.TempDir()
	db, err := database.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	defer db.Close()

	shareID := "abc123456789"
	err = db.CreateShare(context.Background(), &database.Share{
		ID:        shareID,
		TokenHash: []byte("hash"),
		Status:    database.StatusActive,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("failed to create share: %v", err)
	}

	p1, p2 := net.Pipe()
	defer p1.Close()
	defer p2.Close()
	smuxSess, err := smux.Server(p1, nil)
	if err != nil {
		t.Fatalf("failed to create smux session: %v", err)
	}
	defer smuxSess.Close()

	reg := registry.NewRegistry()
	reg.Register(shareID, smuxSess, time.Now().Add(2*time.Hour))
	sess, ok := reg.Get(shareID)
	if !ok {
		t.Fatalf("failed to get registered session")
	}
	sess.Transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return net.Dial("tcp", backend.Listener.Addr().String())
		},
	}

	cfg := config.DefaultServerConfig()
	cfg.BaseDomain = "share.local"
	cfg.AssetCacheDir = filepath.Join(dir, "asset-cache")
	proxyHandler := proxy.NewProxyHandler(cfg, db, reg)

	req := httptest.NewRequest(http.MethodGet, "http://abc123456789.share.local/assets/index.js", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	rec := httptest.NewRecorder()

	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if receivedAcceptEncoding != "identity" {
		t.Fatalf("expected backend to receive Accept-Encoding 'identity', got %q", receivedAcceptEncoding)
	}

	if rec.Body.String() != "console.log('texlite');" {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}

