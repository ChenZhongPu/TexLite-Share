package tunnel

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestVerifyTexLiteService_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":      true,
				"pid":     4242,
				"latexmk": "latexmk",
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	hostPort := strings.TrimPrefix(server.URL, "http://")
	info, err := VerifyTexLiteService(hostPort, 1*time.Second)
	if err != nil {
		t.Fatalf("expected success, got err: %v", err)
	}
	if info.PID != 4242 {
		t.Errorf("expected PID 4242, got %d", info.PID)
	}
	if info.Latexmk != "latexmk" {
		t.Errorf("expected latexmk, got %s", info.Latexmk)
	}
}

func TestVerifyTexLiteService_NonTexLiteService(t *testing.T) {
	// A generic HTTP server running on local port that is NOT TexLite
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("Welcome to Apache or Nginx"))
	}))
	defer server.Close()

	hostPort := strings.TrimPrefix(server.URL, "http://")
	info, err := VerifyTexLiteService(hostPort, 1*time.Second)
	if err == nil {
		t.Fatalf("expected error for non-TexLite service, got info: %+v", info)
	}
	if !strings.Contains(err.Error(), "does not appear to be TexLite") {
		t.Errorf("expected error message mentioning TexLite identity verification, got: %v", err)
	}
}

func TestVerifyTexLiteService_ClosedPort(t *testing.T) {
	// Get a closed random loopback port
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close() // Close immediately

	info, err := VerifyTexLiteService(addr, 500*time.Millisecond)
	if err == nil {
		t.Fatalf("expected error on closed port, got: %+v", info)
	}
	if !strings.Contains(err.Error(), "TexLite service is not running") {
		t.Errorf("expected error message indicating TexLite is not running, got: %v", err)
	}
}

func TestVerifyTexLiteService_SSRFProtection(t *testing.T) {
	// Non-loopback address should be rejected immediately
	_, err := VerifyTexLiteService("192.168.1.100:3000", 1*time.Second)
	if err == nil {
		t.Fatal("expected error for non-loopback address")
	}
}

func TestDiscoverTexLiteAddr_Explicit(t *testing.T) {
	addr, err := DiscoverTexLiteAddr(context.Background(), "127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	if addr != "127.0.0.1:8080" {
		t.Errorf("expected 127.0.0.1:8080, got %s", addr)
	}
}
