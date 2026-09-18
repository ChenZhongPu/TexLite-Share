package database_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"texlite-share/internal/database"
	"texlite-share/internal/protocol"
)

func setupTestDB(t *testing.T) *database.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("database.Open failed: %v", err)
	}
	t.Cleanup(func() {
		db.Close()
	})
	return db
}

func TestShareCRUD(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	share := &database.Share{
		ID:        "testshare123456",
		TokenHash: []byte("01234567890123456789012345678901"),
		Status:    database.StatusActive,
		CreatedAt: now,
		ExpiresAt: now.Add(24 * time.Hour),
	}

	if err := db.CreateShare(ctx, share); err != nil {
		t.Fatalf("CreateShare failed: %v", err)
	}

	// Retrieve share
	got, err := db.GetShare(ctx, share.ID)
	if err != nil {
		t.Fatalf("GetShare failed: %v", err)
	}
	if got.ID != share.ID {
		t.Errorf("expected ID %q, got %q", share.ID, got.ID)
	}
	if got.Status != database.StatusActive {
		t.Errorf("expected Status %q, got %q", database.StatusActive, got.Status)
	}
	if string(got.TokenHash) != string(share.TokenHash) {
		t.Errorf("token hash mismatch")
	}
	if got.ExpiresAt.Unix() != share.ExpiresAt.Unix() {
		t.Errorf("expires at mismatch: %v vs %v", got.ExpiresAt, share.ExpiresAt)
	}

	// Non-existent share
	_, err = db.GetShare(ctx, "nonexistent")
	if err == nil || err != protocol.ErrShareNotFound {
		t.Errorf("expected ErrShareNotFound, got %v", err)
	}
}

func TestCountActiveShares(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	count, err := db.CountActiveShares(ctx, now)
	if err != nil {
		t.Fatalf("CountActiveShares failed: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 active shares, got %d", count)
	}

	// Create 3 active shares
	for i := 1; i <= 3; i++ {
		s := &database.Share{
			ID:        string(rune('a'+i)) + "testshare123456",
			TokenHash: []byte("01234567890123456789012345678901"),
			Status:    database.StatusActive,
			CreatedAt: now,
			ExpiresAt: now.Add(time.Hour),
		}
		if err := db.CreateShare(ctx, s); err != nil {
			t.Fatalf("CreateShare %d failed: %v", i, err)
		}
	}

	// Create 1 already expired active share
	expiredShare := &database.Share{
		ID:        "expiredshare1234",
		TokenHash: []byte("01234567890123456789012345678901"),
		Status:    database.StatusActive,
		CreatedAt: now.Add(-2 * time.Hour),
		ExpiresAt: now.Add(-time.Hour),
	}
	if err := db.CreateShare(ctx, expiredShare); err != nil {
		t.Fatalf("CreateShare expired failed: %v", err)
	}

	count, err = db.CountActiveShares(ctx, now)
	if err != nil {
		t.Fatalf("CountActiveShares failed: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected 3 active non-expired shares, got %d", count)
	}
}

func TestRevokeShare(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	s := &database.Share{
		ID:        "share-to-revoke1",
		TokenHash: []byte("01234567890123456789012345678901"),
		Status:    database.StatusActive,
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	if err := db.CreateShare(ctx, s); err != nil {
		t.Fatalf("CreateShare failed: %v", err)
	}

	revokeTime := now.Add(10 * time.Minute)
	if err := db.RevokeShare(ctx, s.ID, revokeTime); err != nil {
		t.Fatalf("RevokeShare failed: %v", err)
	}

	got, err := db.GetShare(ctx, s.ID)
	if err != nil {
		t.Fatalf("GetShare failed: %v", err)
	}
	if got.Status != database.StatusRevoked {
		t.Fatalf("expected status %q, got %q", database.StatusRevoked, got.Status)
	}
	if got.RevokedAt == nil || got.RevokedAt.Unix() != revokeTime.Unix() {
		t.Fatalf("expected revoked_at %v, got %v", revokeTime, got.RevokedAt)
	}
}

func TestExpireShares(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Share 1: Expired
	s1 := &database.Share{
		ID:        "expire1_12345678",
		TokenHash: []byte("01234567890123456789012345678901"),
		Status:    database.StatusActive,
		CreatedAt: now.Add(-2 * time.Hour),
		ExpiresAt: now.Add(-1 * time.Minute),
	}
	// Share 2: Still active
	s2 := &database.Share{
		ID:        "active2_12345678",
		TokenHash: []byte("01234567890123456789012345678901"),
		Status:    database.StatusActive,
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}

	if err := db.CreateShare(ctx, s1); err != nil {
		t.Fatalf("Create s1 failed: %v", err)
	}
	if err := db.CreateShare(ctx, s2); err != nil {
		t.Fatalf("Create s2 failed: %v", err)
	}

	expiredIDs, err := db.ExpireShares(ctx, now)
	if err != nil {
		t.Fatalf("ExpireShares failed: %v", err)
	}
	if len(expiredIDs) != 1 || expiredIDs[0] != s1.ID {
		t.Fatalf("expected [%q] expired, got %v", s1.ID, expiredIDs)
	}

	got1, _ := db.GetShare(ctx, s1.ID)
	if got1.Status != database.StatusExpired {
		t.Fatalf("expected s1 status %q, got %q", database.StatusExpired, got1.Status)
	}

	got2, _ := db.GetShare(ctx, s2.ID)
	if got2.Status != database.StatusActive {
		t.Fatalf("expected s2 status %q, got %q", database.StatusActive, got2.Status)
	}
}
