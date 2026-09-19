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

	// 1. Dashboard HTML loads on root route -> 200
	dashReq := httptest.NewRequest(http.MethodGet, "/", nil)
	dashRec := httptest.NewRecorder()
	adminHandler.ServeHTTP(dashRec, dashReq)
	if dashRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for dashboard HTML, got %d", dashRec.Code)
	}
	if !strings.Contains(dashRec.Body.String(), "TexLite Share") {
		t.Fatal("dashboard HTML content missing expected title")
	}
	if !strings.Contains(dashRec.Body.String(), "footer-version") {
		t.Fatal("dashboard HTML content missing footer-version element")
	}
	if !strings.Contains(dashRec.Body.String(), "Active Streams / Limit") {
		t.Fatal("dashboard HTML content missing Active Streams / Limit")
	}

	// 2. Unauthenticated API request -> 401 Unauthorized
	unauthReq := httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	unauthRec := httptest.NewRecorder()
	adminHandler.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for API without key, got %d", unauthRec.Code)
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
	if stats.ServerVersion == "" {
		t.Fatalf("expected non-empty ServerVersion in stats")
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
	if list[0].ClientIP == "" {
		t.Fatal("expected share to have ClientIP")
	}
	if list[0].Country == "" {
		t.Fatal("expected share to have Country populated")
	}

	// 6. Extend Share expiration by 1 month (+1 month)
	extReq := httptest.NewRequest(http.MethodPost, "/api/v1/shares/"+created.ID+"/expires", bytes.NewBufferString(`{"extend":"1 month"}`))
	extReq.Header.Set("Authorization", "Bearer admin-key-123")
	extRec := httptest.NewRecorder()
	adminHandler.ServeHTTP(extRec, extReq)
	if extRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for extend share, got %d: %s", extRec.Code, extRec.Body.String())
	}
	var extResp struct {
		ID        string `json:"id"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := json.NewDecoder(extRec.Body).Decode(&extResp); err != nil {
		t.Fatalf("failed to decode extend resp: %v", err)
	}

	// 7. Extend Share expiration by 1 year (+1 year)
	extYearReq := httptest.NewRequest(http.MethodPost, "/api/v1/shares/"+created.ID+"/expires", bytes.NewBufferString(`{"extend":"1 year"}`))
	extYearReq.Header.Set("Authorization", "Bearer admin-key-123")
	extYearRec := httptest.NewRecorder()
	adminHandler.ServeHTTP(extYearRec, extYearReq)
	if extYearRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for extend 1 year, got %d", extYearRec.Code)
	}

	// 8. Custom manual expiration update via PATCH
	patchReq := httptest.NewRequest(http.MethodPatch, "/api/v1/shares/"+created.ID, bytes.NewBufferString(`{"expiresAt":"2030-01-01T12:00:00Z"}`))
	patchReq.Header.Set("Authorization", "Bearer admin-key-123")
	patchRec := httptest.NewRecorder()
	adminHandler.ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for patch expiresAt, got %d: %s", patchRec.Code, patchRec.Body.String())
	}

	// 9. Revoke Share and verify it is EXCLUDED from listing (REVOKED data not displayed)
	delReq := httptest.NewRequest(http.MethodDelete, "/api/v1/shares/"+created.ID, nil)
	delReq.Header.Set("Authorization", "Bearer admin-key-123")
	delRec := httptest.NewRecorder()
	adminHandler.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for delete share, got %d", delRec.Code)
	}

	listAfterRevokeReq := httptest.NewRequest(http.MethodGet, "/api/v1/shares", nil)
	listAfterRevokeReq.Header.Set("Authorization", "Bearer admin-key-123")
	listAfterRevokeRec := httptest.NewRecorder()
	adminHandler.ServeHTTP(listAfterRevokeRec, listAfterRevokeReq)
	if listAfterRevokeRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for list after revoke, got %d", listAfterRevokeRec.Code)
	}
	var listAfter []api.ShareView
	_ = json.NewDecoder(listAfterRevokeRec.Body).Decode(&listAfter)
	if len(listAfter) != 0 {
		t.Fatalf("expected 0 shares after revoking (REVOKED must be excluded), got %d shares", len(listAfter))
	}

	// 10. Verify that the PUBLIC router rejects admin-only APIs with 404 (Physical isolation)
	// Admin listing of all shares should be rejected on public router
	pubListReq := httptest.NewRequest(http.MethodGet, "/api/v1/shares", nil)
	pubListRec := httptest.NewRecorder()
	publicHandler.ServeHTTP(pubListRec, pubListReq)
	if pubListRec.Code != http.StatusNotFound {
		t.Fatalf("expected public router to return 404 for GET /api/v1/shares, got %d", pubListRec.Code)
	}

	// Admin stats should be rejected on public router
	pubStatsReq := httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	pubStatsRec := httptest.NewRecorder()
	publicHandler.ServeHTTP(pubStatsRec, pubStatsReq)
	if pubStatsRec.Code != http.StatusNotFound {
		t.Fatalf("expected public router to return 404 for /api/v1/stats, got %d", pubStatsRec.Code)
	}

	// 11. Verify stats has totalRevoked = 1 and country breakdown
	statsReq2 := httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	statsReq2.Header.Set("Authorization", "Bearer admin-key-123")
	statsRec2 := httptest.NewRecorder()
	adminHandler.ServeHTTP(statsRec2, statsReq2)
	if statsRec2.Code != http.StatusOK {
		t.Fatalf("expected 200 for stats after revoke, got %d", statsRec2.Code)
	}
	var statsAfter api.StatsView
	_ = json.NewDecoder(statsRec2.Body).Decode(&statsAfter)
	if statsAfter.TotalRevoked != 1 {
		t.Fatalf("expected totalRevoked 1, got %d", statsAfter.TotalRevoked)
	}

	// 12. Attempt to extend expiration of revoked share must fail
	extRevokedReq := httptest.NewRequest(http.MethodPost, "/api/v1/shares/"+created.ID+"/expires", strings.NewReader(`{"extend":"1 month"}`))
	extRevokedReq.Header.Set("Authorization", "Bearer admin-key-123")
	extRevokedRec := httptest.NewRecorder()
	adminHandler.ServeHTTP(extRevokedRec, extRevokedReq)
	if extRevokedRec.Code != http.StatusInternalServerError && extRevokedRec.Code != http.StatusNotFound {
		t.Fatalf("expected error when extending revoked share, got %d", extRevokedRec.Code)
	}

	// 13. Verify GET /api/v1/config returns all parameters with defaults and actuals
	cfgReq := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
	cfgReq.Header.Set("Authorization", "Bearer admin-key-123")
	cfgRec := httptest.NewRecorder()
	adminHandler.ServeHTTP(cfgRec, cfgReq)
	if cfgRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for GET /api/v1/config, got %d", cfgRec.Code)
	}
	var configItems []api.ConfigItemView
	if err := json.NewDecoder(cfgRec.Body).Decode(&configItems); err != nil {
		t.Fatalf("failed to decode config items: %v", err)
	}
	if len(configItems) < 10 {
		t.Fatalf("expected at least 10 config items, got %d", len(configItems))
	}
	// Verify that secret keys are masked
	for _, it := range configItems {
		if it.Key == "--admin-api-key" || it.Key == "--create-api-key" {
			if strings.Contains(it.ActualValue, "admin-key-123") {
				t.Fatalf("secret key leaked in plain text: %s", it.ActualValue)
			}
		}
	}

	_ = db
}

func TestAdminCreateShareWithCustomID(t *testing.T) {
	adminHandler, _, _, _ := setupTestAdmin(t, "admin-key-123")

	// 1. Create share with custom ID containing hyphens
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/shares", bytes.NewBufferString(`{"id":"paper-2026-demo","ttl":"2h"}`))
	createReq.Header.Set("Authorization", "Bearer admin-key-123")
	createRec := httptest.NewRecorder()
	adminHandler.ServeHTTP(createRec, createReq)

	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created for custom ID, got %d: %s", createRec.Code, createRec.Body.String())
	}

	var created api.CreateShareResponse
	if err := json.NewDecoder(createRec.Body).Decode(&created); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if created.ID != "paper-2026-demo" {
		t.Fatalf("expected custom ID 'paper-2026-demo', got %q", created.ID)
	}
	if created.PublicURL != "http://paper-2026-demo.share.local" {
		t.Fatalf("expected public URL 'http://paper-2026-demo.share.local', got %q", created.PublicURL)
	}

	// 2. Attempting to create duplicate active custom ID should fail with 409 Conflict
	dupReq := httptest.NewRequest(http.MethodPost, "/api/v1/shares", bytes.NewBufferString(`{"id":"paper-2026-demo","ttl":"2h"}`))
	dupReq.Header.Set("Authorization", "Bearer admin-key-123")
	dupRec := httptest.NewRecorder()
	adminHandler.ServeHTTP(dupRec, dupReq)

	if dupRec.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict for duplicate active ID, got %d: %s", dupRec.Code, dupRec.Body.String())
	}
}

