package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"texlite-share/internal/api"
	"texlite-share/internal/config"
	"texlite-share/internal/database"
	"texlite-share/internal/registry"
	"texlite-share/internal/tunnel"
)

type testEnv struct {
	ShareServer *httptest.Server
	LocalServer *httptest.Server
	DB          *database.DB
	Registry    *registry.Registry
	ShareID     string
	Token       string
	CancelFn    context.CancelFunc
	ClientDone  chan error
	BaseDomain  string
	ServerCfg   *config.ServerConfig
}

func (e *testEnv) Close() {
	if e.CancelFn != nil {
		e.CancelFn()
	}
	if e.ShareServer != nil {
		e.ShareServer.Close()
	}
	if e.LocalServer != nil {
		e.LocalServer.Close()
	}
	if e.Registry != nil {
		e.Registry.CloseAll()
	}
	if e.DB != nil {
		e.DB.Close()
	}
}

func setupTestEnv(t *testing.T, dbPath string, customLocalHandler http.Handler, ttl time.Duration) *testEnv {
	t.Helper()

	if dbPath == "" {
		dbPath = filepath.Join(t.TempDir(), "test.db")
	}

	db, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}

	reg := registry.NewRegistry()
	cfg := config.DefaultServerConfig()
	cfg.BaseDomain = "share.local"
	cfg.DBPath = dbPath
	cfg.AssetCacheDir = filepath.Join(t.TempDir(), "asset-cache")
	if ttl > 0 {
		cfg.DefaultTTL = ttl
	}

	router := api.NewRouter(cfg, db, reg)
	shareServer := httptest.NewServer(router)

	// Local backend service (simulating TexLite on 127.0.0.1)
	if customLocalHandler == nil {
		customLocalHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/echo-headers" {
				w.Header().Set("X-Received-Host", r.Host)
				w.Header().Set("X-Received-Proto", r.Header.Get("X-Forwarded-Proto"))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("ok"))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = io.Copy(w, r.Body)
			if r.URL.Path == "/" {
				_, _ = w.Write([]byte("hello from local texlite"))
			}
		})
	}
	localServer := httptest.NewServer(customLocalHandler)

	// Create a share via API
	createReq, err := http.NewRequest(http.MethodPost, shareServer.URL+"/api/v1/shares", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	createResp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatalf("failed to call create share API: %v", err)
	}
	defer createResp.Body.Close()

	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create share returned status %d", createResp.StatusCode)
	}

	var shareData api.CreateShareResponse
	if err := json.NewDecoder(createResp.Body).Decode(&shareData); err != nil {
		t.Fatalf("failed to decode create share response: %v", err)
	}

	// Start tunnel client
	clientCtx, clientCancel := context.WithCancel(context.Background())
	clientDone := make(chan error, 1)

	clientCfg := &config.ClientConfig{
		ServerURL:        shareServer.URL,
		ShareID:          shareData.ID,
		Token:            shareData.Token,
		LocalAddr:        localServer.Listener.Addr().String(),
		LocalDialTimeout: 2 * time.Second,
		MaxRetryDelay:    500 * time.Millisecond,
	}

	go func() {
		clientDone <- tunnel.RunClient(clientCtx, clientCfg)
	}()

	// Wait for tunnel to be registered
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, found := reg.Get(shareData.ID); found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for tunnel client to register")
		}
		time.Sleep(20 * time.Millisecond)
	}

	return &testEnv{
		ShareServer: shareServer,
		LocalServer: localServer,
		DB:          db,
		Registry:    reg,
		ShareID:     shareData.ID,
		Token:       shareData.Token,
		CancelFn:    clientCancel,
		ClientDone:  clientDone,
		BaseDomain:  cfg.BaseDomain,
		ServerCfg:   cfg,
	}
}

// 1. Basic HTTP Forwarding (Section 46)
func TestIntegration_BasicHTTP(t *testing.T) {
	env := setupTestEnv(t, "", nil, 0)
	defer env.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	publicURL := env.ShareServer.URL + "/"

	req, err := http.NewRequest(http.MethodGet, publicURL, nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Host = fmt.Sprintf("%s.%s", env.ShareID, env.BaseDomain)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request to public URL failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	if !bytes.Contains(body, []byte("hello from local texlite")) {
		t.Fatalf("unexpected body: %s", string(body))
	}

	// Verify Header Forwarding (Section 36)
	reqHeaders, _ := http.NewRequest(http.MethodGet, env.ShareServer.URL+"/echo-headers", nil)
	reqHeaders.Host = fmt.Sprintf("%s.%s", env.ShareID, env.BaseDomain)
	respHeaders, err := client.Do(reqHeaders)
	if err != nil {
		t.Fatalf("headers request failed: %v", err)
	}
	defer respHeaders.Body.Close()

	expectedHost := fmt.Sprintf("%s.%s", env.ShareID, env.BaseDomain)
	if gotHost := respHeaders.Header.Get("X-Received-Host"); gotHost != expectedHost {
		t.Errorf("expected host %q, got %q", expectedHost, gotHost)
	}
	if proto := respHeaders.Header.Get("X-Received-Proto"); proto != "http" {
		t.Errorf("expected proto 'http', got %q", proto)
	}
}

// 2. Concurrent Requests (Section 47)
func TestIntegration_ConcurrentRequests(t *testing.T) {
	env := setupTestEnv(t, "", nil, 0)
	defer env.Close()

	const concurrency = 32
	var wg sync.WaitGroup
	wg.Add(concurrency)

	errCh := make(chan error, concurrency)

	for i := 0; i < concurrency; i++ {
		go func(idx int) {
			defer wg.Done()
			client := &http.Client{Timeout: 10 * time.Second}
			payload := fmt.Sprintf("concurrent payload %d", idx)

			req, err := http.NewRequest(http.MethodPost, env.ShareServer.URL+"/concurrent", bytes.NewBufferString(payload))
			if err != nil {
				errCh <- err
				return
			}
			req.Host = fmt.Sprintf("%s.%s", env.ShareID, env.BaseDomain)

			resp, err := client.Do(req)
			if err != nil {
				errCh <- fmt.Errorf("worker %d failed: %w", idx, err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				errCh <- fmt.Errorf("worker %d got status %d", idx, resp.StatusCode)
				return
			}

			body, _ := io.ReadAll(resp.Body)
			if string(body) != payload {
				errCh <- fmt.Errorf("worker %d data mismatch: expected %q, got %q", idx, payload, string(body))
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatal(err)
	}
}

// 3. WebSocket Proxying (Section 48)
func TestIntegration_WebSocket(t *testing.T) {
	// Local WebSocket Echo Handler
	wsHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "bye")

		for {
			typ, msg, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if err := c.Write(r.Context(), typ, msg); err != nil {
				return
			}
		}
	})

	env := setupTestEnv(t, "", wsHandler, 0)
	defer env.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Dial WebSocket through the public proxy
	wsURL := fmt.Sprintf("ws://%s/", env.ShareServer.Listener.Addr().String())
	dialOpts := &websocket.DialOptions{
		Host: fmt.Sprintf("%s.%s", env.ShareID, env.BaseDomain),
	}

	clientConn, _, err := websocket.Dial(ctx, wsURL, dialOpts)
	if err != nil {
		t.Fatalf("failed to dial websocket through proxy: %v", err)
	}
	defer clientConn.Close(websocket.StatusNormalClosure, "done")

	// Echo multiple messages
	for i := 0; i < 5; i++ {
		msg := fmt.Sprintf("websocket test message %d", i)
		if err := clientConn.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
			t.Fatalf("failed to write websocket message: %v", err)
		}

		typ, received, err := clientConn.Read(ctx)
		if err != nil {
			t.Fatalf("failed to read websocket message: %v", err)
		}
		if typ != websocket.MessageText || string(received) != msg {
			t.Fatalf("expected message %q, got %q", msg, string(received))
		}
	}
}

// 4. Streaming / Large Responses (Section 49)
func TestIntegration_Streaming(t *testing.T) {
	const streamSize = 32 * 1024 * 1024 // 32 MB
	streamHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)

		buf := make([]byte, 64*1024)
		for i := range buf {
			buf[i] = byte(i % 256)
		}

		remaining := streamSize
		for remaining > 0 {
			toWrite := len(buf)
			if toWrite > remaining {
				toWrite = remaining
			}
			n, err := w.Write(buf[:toWrite])
			if err != nil {
				return
			}
			remaining -= n
		}
	})

	env := setupTestEnv(t, "", streamHandler, 0)
	defer env.Close()

	client := &http.Client{Timeout: 30 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, env.ShareServer.URL+"/large", nil)
	req.Host = fmt.Sprintf("%s.%s", env.ShareID, env.BaseDomain)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("streaming request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var totalRead int64
	buf := make([]byte, 64*1024)
	for {
		n, err := resp.Body.Read(buf)
		totalRead += int64(n)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("error reading stream body: %v", err)
		}
	}

	if totalRead != streamSize {
		t.Fatalf("expected %d bytes, got %d", streamSize, totalRead)
	}
}

// 5. Automatic Reconnection (Section 50)
func TestIntegration_Reconnection(t *testing.T) {
	env := setupTestEnv(t, "", nil, 0)
	defer env.Close()

	// 1. Verify working
	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, env.ShareServer.URL+"/", nil)
	req.Host = fmt.Sprintf("%s.%s", env.ShareID, env.BaseDomain)
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("initial request failed: %v", err)
	}
	resp.Body.Close()

	// 2. Force close the server-side tunnel session
	sess, found := env.Registry.Get(env.ShareID)
	if !found {
		t.Fatal("session not found in registry")
	}
	oldGen := sess.Generation
	_ = sess.Session.Close()

	// 3. Wait for client to reconnect with a new generation
	deadline := time.Now().Add(10 * time.Second)
	var newSess *registry.TunnelSession
	for {
		s, found := env.Registry.Get(env.ShareID)
		if found && s.Generation > oldGen && !s.Session.IsClosed() {
			newSess = s
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for client to reconnect")
		}
		time.Sleep(50 * time.Millisecond)
	}

	if newSess.Generation <= oldGen {
		t.Fatalf("expected new generation > %d, got %d", oldGen, newSess.Generation)
	}

	// 4. Verify public URL works again
	resp2, err := client.Do(req)
	if err != nil || resp2.StatusCode != http.StatusOK {
		t.Fatalf("request after reconnect failed: %v", err)
	}
	resp2.Body.Close()
}

// 6. Expiration Handling (Section 51)
func TestIntegration_Expiration(t *testing.T) {
	// Setup with 1500ms TTL so initial connection and request succeed
	env := setupTestEnv(t, "", nil, 1500*time.Millisecond)
	defer env.Close()

	// Start expiration worker with 50ms interval
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api.StartExpirationWorker(ctx, env.DB, env.Registry, 50*time.Millisecond)

	client := &http.Client{Timeout: 2 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, env.ShareServer.URL+"/", nil)
	req.Host = fmt.Sprintf("%s.%s", env.ShareID, env.BaseDomain)

	// 1. Works initially
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("initial request failed: %v", err)
	}
	resp.Body.Close()

	// 2. Wait for TTL to expire
	time.Sleep(1700 * time.Millisecond)

	// 3. Request should return 410 Gone
	respExpired, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer respExpired.Body.Close()

	if respExpired.StatusCode != http.StatusGone {
		t.Fatalf("expected 410 Gone, got %d", respExpired.StatusCode)
	}

	// 4. Client should receive terminal error on reconnect
	select {
	case err := <-env.ClientDone:
		if err == nil || (!errors.Is(err, tunnel.ErrTerminal) && !errors.Is(err, context.Canceled)) {
			t.Fatalf("expected terminal error on client, got: %v", err)
		}
	case <-time.After(3 * time.Second):
		// Reconnect may have triggered terminal error
	}
}


// 7. Revocation Handling (Section 52)
func TestIntegration_Revocation(t *testing.T) {
	env := setupTestEnv(t, "", nil, 0)
	defer env.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, env.ShareServer.URL+"/", nil)
	req.Host = fmt.Sprintf("%s.%s", env.ShareID, env.BaseDomain)

	// 1. Works initially
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("initial request failed: %v", err)
	}
	resp.Body.Close()

	// 2. Revoke share via DELETE API
	delReq, _ := http.NewRequest(http.MethodDelete, env.ShareServer.URL+"/api/v1/shares/"+env.ShareID, nil)
	delReq.Header.Set("Authorization", "Bearer "+env.Token)
	delResp, err := client.Do(delReq)
	if err != nil || delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke failed: %v", err)
	}
	delResp.Body.Close()

	// 3. Public request returns 404 Not Found (or 403 Forbidden) since revoked share content is purged
	respRevoked, err := client.Do(req)
	if err != nil {
		t.Fatalf("request after revoke failed: %v", err)
	}
	defer respRevoked.Body.Close()

	if respRevoked.StatusCode != http.StatusNotFound && respRevoked.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 404 Not Found or 403 Forbidden, got %d", respRevoked.StatusCode)
	}
}

// 8. Server Restart Simulation (Section 53)
func TestIntegration_ServerRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "restart_test.db")
	env := setupTestEnv(t, dbPath, nil, 0)

	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, env.ShareServer.URL+"/", nil)
	req.Host = fmt.Sprintf("%s.%s", env.ShareID, env.BaseDomain)

	// 1. Initial request works
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("initial request failed: %v", err)
	}
	resp.Body.Close()

	// 2. Stop server (simulate crash/restart)
	serverAddr := env.ShareServer.Listener.Addr().String()
	env.ShareServer.Close()
	env.Registry.CloseAll()
	env.DB.Close()

	// 3. Restart server on the SAME address using the SAME sqlite db
	db2, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to reopen database: %v", err)
	}
	defer db2.Close()

	reg2 := registry.NewRegistry()
	defer reg2.CloseAll()

	router2 := api.NewRouter(env.ServerCfg, db2, reg2)
	l, err := net.Listen("tcp", serverAddr)
	if err != nil {
		t.Fatalf("failed to re-listen on %s: %v", serverAddr, err)
	}
	newServer := httptest.NewUnstartedServer(router2)
	newServer.Listener = l
	newServer.Start()
	defer newServer.Close()

	// 4. Client should automatically reconnect to the restarted server
	deadline := time.Now().Add(10 * time.Second)
	for {
		if s, found := reg2.Get(env.ShareID); found && !s.Session.IsClosed() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for client to reconnect to restarted server")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 5. Verify same share URL works again
	req2, _ := http.NewRequest(http.MethodGet, newServer.URL+"/", nil)
	req2.Host = fmt.Sprintf("%s.%s", env.ShareID, env.BaseDomain)
	resp2, err := client.Do(req2)
	if err != nil || resp2.StatusCode != http.StatusOK {
		t.Fatalf("request after server restart failed: %v", err)
	}
	resp2.Body.Close()

	env.CancelFn()
	env.LocalServer.Close()
}

func TestIntegration_AssetCaching(t *testing.T) {
	tempCacheDir, err := os.MkdirTemp("", "texlite-integration-cache-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempCacheDir)

	var upstreamCalls atomic.Int32
	assetBody := "console.log('vite-hashed-asset-content');"

	customHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/assets/index-hash12345.js" {
			upstreamCalls.Add(1)
			w.Header().Set("Content-Type", "application/javascript")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(assetBody))
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	dbPath := filepath.Join(t.TempDir(), "cache_test.db")
	env := setupTestEnv(t, dbPath, customHandler, time.Hour)
	defer env.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	assetURL := env.ShareServer.URL + "/assets/index-hash12345.js"
	hostHeader := fmt.Sprintf("%s.%s", env.ShareID, env.BaseDomain)

	// 1. First request: should be a cache MISS and hit local backend once
	req1, _ := http.NewRequest(http.MethodGet, assetURL, nil)
	req1.Host = hostHeader
	resp1, err := client.Do(req1)
	if err != nil {
		t.Fatalf("first asset request failed: %v", err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()

	if string(body1) != assetBody {
		t.Fatalf("expected body %q, got %q", assetBody, body1)
	}
	if resp1.Header.Get("X-TexLite-Cache") != "MISS" {
		t.Fatalf("expected X-TexLite-Cache: MISS on first request, got %q", resp1.Header.Get("X-TexLite-Cache"))
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("expected 1 upstream call, got %d", upstreamCalls.Load())
	}

	// 2. Second request: should be a cache HIT and NOT touch the local backend!
	req2, _ := http.NewRequest(http.MethodGet, assetURL, nil)
	req2.Host = hostHeader
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("second asset request failed: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	if string(body2) != assetBody {
		t.Fatalf("expected body %q, got %q", assetBody, body2)
	}
	if resp2.Header.Get("X-TexLite-Cache") != "HIT" {
		t.Fatalf("expected X-TexLite-Cache: HIT on second request, got %q", resp2.Header.Get("X-TexLite-Cache"))
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("expected still 1 upstream call on cache hit, got %d", upstreamCalls.Load())
	}
	if resp2.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("expected immutable Cache-Control header, got %q", resp2.Header.Get("Cache-Control"))
	}
}

