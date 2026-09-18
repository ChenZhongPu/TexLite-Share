package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"texlite-share/internal/api"
	"texlite-share/internal/config"
	"texlite-share/internal/database"
	"texlite-share/internal/registry"
)

func setupTestRouter(t *testing.T, apiKey string, maxShares int) (*api.Router, *database.DB, *registry.Registry) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() {
		db.Close()
	})

	cfg := config.DefaultServerConfig()
	cfg.CreateAPIKey = apiKey
	if maxShares > 0 {
		cfg.MaxActiveShares = maxShares
	}
	reg := registry.NewRegistry()
	router := api.NewRouter(cfg, db, reg)

	return router, db, reg
}

func TestHealthCheck(t *testing.T) {
	router, _, _ := setupTestRouter(t, "", 50)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("expected status 'ok', got %q", body["status"])
	}
}

func TestCreateAndRevokeShare(t *testing.T) {
	apiKey := "test-secret-key"
	router, db, reg := setupTestRouter(t, apiKey, 50)

	// 1. Unauthorized create
	unauthReq := httptest.NewRequest(http.MethodPost, "/api/v1/shares", nil)
	unauthRec := httptest.NewRecorder()
	router.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", unauthRec.Code)
	}

	// 2. Authorized create
	req := httptest.NewRequest(http.MethodPost, "/api/v1/shares", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp api.CreateShareResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to parse json response: %v", err)
	}
	if resp.ID == "" || resp.Token == "" {
		t.Fatalf("empty ID or Token returned: %+v", resp)
	}

	// Check DB
	share, err := db.GetShare(context.Background(), resp.ID)
	if err != nil {
		t.Fatalf("failed to query share in db: %v", err)
	}
	if share.Status != database.StatusActive {
		t.Fatalf("expected status ACTIVE, got %q", share.Status)
	}

	// 3. Revoke using share token
	delReq := httptest.NewRequest(http.MethodDelete, "/api/v1/shares/"+resp.ID, nil)
	delReq.Header.Set("Authorization", "Bearer "+resp.Token)
	delRec := httptest.NewRecorder()
	router.ServeHTTP(delRec, delReq)

	if delRec.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d", delRec.Code)
	}

	// Check DB updated to REVOKED
	share, err = db.GetShare(context.Background(), resp.ID)
	if err != nil {
		t.Fatalf("failed to query share in db: %v", err)
	}
	if share.Status != database.StatusRevoked {
		t.Fatalf("expected status REVOKED, got %q", share.Status)
	}
	if share.RevokedAt == nil {
		t.Fatal("expected revoked_at to be set")
	}

	_ = reg
}

func TestCapacityExceeded(t *testing.T) {
	router, _, _ := setupTestRouter(t, "", 2) // max 2 shares

	// Create 1
	r1 := httptest.NewRequest(http.MethodPost, "/api/v1/shares", nil)
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, r1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w1.Code)
	}

	// Create 2
	r2 := httptest.NewRequest(http.MethodPost, "/api/v1/shares", nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, r2)
	if w2.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w2.Code)
	}

	// Create 3 -> Capacity exceeded
	r3 := httptest.NewRequest(http.MethodPost, "/api/v1/shares", nil)
	w3 := httptest.NewRecorder()
	router.ServeHTTP(w3, r3)
	if w3.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Capacity Exceeded, got %d", w3.Code)
	}
}

func TestExpirationWorker(t *testing.T) {
	_, db, reg := setupTestRouter(t, "", 50)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	now := time.Now().UTC()
	// Insert an expired active share
	expiredShare := &database.Share{
		ID:        "expwork12345678",
		TokenHash: []byte("01234567890123456789012345678901"),
		Status:    database.StatusActive,
		CreatedAt: now.Add(-time.Hour),
		ExpiresAt: now.Add(-10 * time.Second),
	}
	if err := db.CreateShare(ctx, expiredShare); err != nil {
		t.Fatalf("failed to insert expired share: %v", err)
	}

	// Register in in-memory registry
	reg.Register(expiredShare.ID, nil, expiredShare.ExpiresAt)
	if reg.Count() != 1 {
		t.Fatalf("expected 1 session in registry, got %d", reg.Count())
	}

	// Start worker with fast interval
	api.StartExpirationWorker(ctx, db, reg, 50*time.Millisecond)

	// Wait for worker to sweep
	time.Sleep(200 * time.Millisecond)

	if reg.Count() != 0 {
		t.Fatalf("expected 0 sessions in registry after sweep, got %d", reg.Count())
	}

	s, err := db.GetShare(ctx, expiredShare.ID)
	if err != nil {
		t.Fatalf("failed to get share: %v", err)
	}
	if s.Status != database.StatusExpired {
		t.Fatalf("expected status EXPIRED in DB, got %q", s.Status)
	}
}
