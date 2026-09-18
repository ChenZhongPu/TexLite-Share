package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"texlite-share/internal/api"
	"texlite-share/internal/config"
	"texlite-share/internal/database"
	"texlite-share/internal/registry"
)

func setupTestAdmin(t *testing.T, apiKey string) (*api.AdminHandler, http.Handler, *database.DB, *registry.Registry) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_admin.db")
	db, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() {
		db.Close()
	})

	cfg := config.DefaultServerConfig()
	cfg.CreateAPIKey = apiKey
	cfg.AdminListenAddr = "127.0.0.1:9001"
	reg := registry.NewRegistry()

	adminHandler := api.NewAdminHandler(cfg, db, reg)
	publicHandler := api.NewPublicRouter(cfg, db, reg)

	return adminHandler, publicHandler, db, reg
}

func TestAdminDashboardAndAPIs(t *testing.T) {
	adminHandler, publicHandler, db, reg := setupTestAdmin(t, "admin-key-123")

	// 1. Unauthenticated request to dashboard -> 401
	unauthReq := httptest.NewRequest(http.MethodGet, "/", nil)
	unauthRec := httptest.NewRecorder()
	adminHandler.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", unauthRec.Code)
	}

	// 2. Authenticated request to dashboard via query parameter ?key=... -> 200 HTML
	dashReq := httptest.NewRequest(http.MethodGet, "/admin?key=admin-key-123", nil)
	dashRec := httptest.NewRecorder()
	adminHandler.ServeHTTP(dashRec, dashReq)
	if dashRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for dashboard, got %d", dashRec.Code)
	}
	if !strings.Contains(dashRec.Body.String(), "TexLite Share") {
		t.Fatal("dashboard HTML content missing expected title")
	}

	// 3. Stats API via Bearer header
	statsReq := httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	statsReq.Header.Set("Authorization", "Bearer admin-key-123")
	statsRec := httptest.NewRecorder()
	adminHandler.ServeHTTP(statsRec, statsReq)
	if statsRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for stats, got %d", statsRec.Code)
	}

	var stats api.StatsView
	if err := json.NewDecoder(statsRec.Body).Decode(&stats); err != nil {
		t.Fatalf("failed to decode stats: %v", err)
	}
	if stats.MaxActiveShares != 50 {
		t.Fatalf("expected MaxActiveShares 50, got %d", stats.MaxActiveShares)
	}

	// 4. Create Share via Admin API
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/shares", bytes.NewBufferString(`{"ttl":"1h"}`))
	createReq.Header.Set("Authorization", "Bearer admin-key-123")
	createRec := httptest.NewRecorder()
	adminHandler.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d", createRec.Code)
	}

	var created api.CreateShareResponse
	_ = json.NewDecoder(createRec.Body).Decode(&created)

	// Simulate tunnel online in registry
	reg.Register(created.ID, nil, created.ExpiresAt)

	// 5. List Shares via Admin API
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/shares", nil)
	listReq.Header.Set("Authorization", "Bearer admin-key-123")
	listRec := httptest.NewRecorder()
	adminHandler.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for list shares, got %d", listRec.Code)
	}

	var list []api.ShareView
	if err := json.NewDecoder(listRec.Body).Decode(&list); err != nil {
		t.Fatalf("failed to decode list shares: %v", err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("expected 1 share with ID %s, got %+v", created.ID, list)
	}
	if !list[0].Online {
		t.Fatal("expected share to show Online=true")
	}

	// 6. Verify that the PUBLIC router rejects /api/v1/shares with 404 (Physical isolation)
	pubReq := httptest.NewRequest(http.MethodPost, "/api/v1/shares", nil)
	pubRec := httptest.NewRecorder()
	publicHandler.ServeHTTP(pubRec, pubReq)
	if pubRec.Code != http.StatusNotFound {
		t.Fatalf("expected public router to return 404 for /api/v1/shares, got %d", pubRec.Code)
	}

	_ = db
}
