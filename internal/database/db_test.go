package database_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	s.ClientCountry = "China (CN)"
	if err := db.CreateShare(ctx, s); err != nil {
		t.Fatalf("CreateShare failed: %v", err)
	}

	if err := db.RevokeShare(ctx, s.ID, revokeTime); err != nil {
		t.Fatalf("RevokeShare failed: %v", err)
	}

	// Concrete share content must be purged upon revoke
	_, err := db.GetShare(ctx, s.ID)
	if err == nil || !errors.Is(err, protocol.ErrShareNotFound) {
		t.Fatalf("expected share not found after revoke, got err: %v", err)
	}

	// Revoked count and stats by country must be updated
	total, byCountry, err := db.GetRevokedStats(ctx)
	if err != nil {
		t.Fatalf("GetRevokedStats failed: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected total revoked 1, got %d", total)
	}
	if byCountry["China (CN)"] != 1 {
		t.Fatalf("expected country China (CN) count 1, got %+v", byCountry)
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

func TestListShares(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for i := 0; i < 5; i++ {
		s := &database.Share{
			ID:        fmt.Sprintf("share_%d_test123", i),
			TokenHash: []byte("01234567890123456789012345678901"),
			Status:    database.StatusActive,
			CreatedAt: now.Add(time.Duration(i) * time.Minute),
			ExpiresAt: now.Add(time.Hour),
		}
		if err := db.CreateShare(ctx, s); err != nil {
			t.Fatalf("CreateShare %d failed: %v", i, err)
		}
	}

	list, err := db.ListShares(ctx, 3)
	if err != nil {
		t.Fatalf("ListShares failed: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 shares, got %d", len(list))
	}
	// Should be descending by created_at
	if list[0].ID != "share_4_test123" {
		t.Fatalf("expected first item to be share_4_test123, got %q", list[0].ID)
	}
}

func TestUpdateShareExpiration(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	s := &database.Share{
		ID:        "upd_exp_12345678",
		TokenHash: []byte("01234567890123456789012345678901"),
		Status:    database.StatusActive,
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	if err := db.CreateShare(ctx, s); err != nil {
		t.Fatalf("CreateShare failed: %v", err)
	}

	// 1. Extend expiration into the future
	newExp := now.Add(24 * time.Hour)
	updated, err := db.UpdateShareExpiration(ctx, s.ID, newExp, now)
	if err != nil {
		t.Fatalf("UpdateShareExpiration failed: %v", err)
	}
	if updated.ExpiresAt.Unix() != newExp.Unix() {
		t.Fatalf("expected expiresAt %v, got %v", newExp, updated.ExpiresAt)
	}
	if updated.Status != database.StatusActive {
		t.Fatalf("expected status ACTIVE, got %v", updated.Status)
	}

	// 2. Set expiration into the past -> becomes EXPIRED
	pastExp := now.Add(-time.Hour)
	expired, err := db.UpdateShareExpiration(ctx, s.ID, pastExp, now)
	if err != nil {
		t.Fatalf("UpdateShareExpiration to past failed: %v", err)
	}
	if expired.Status != database.StatusExpired {
		t.Fatalf("expected status EXPIRED, got %v", expired.Status)
	}

	// 3. Reactivate expired share into future -> becomes ACTIVE
	reactivated, err := db.UpdateShareExpiration(ctx, s.ID, newExp, now)
	if err != nil {
		t.Fatalf("reactivate expired share failed: %v", err)
	}
	if reactivated.Status != database.StatusActive {
		t.Fatalf("expected reactivated status ACTIVE, got %v", reactivated.Status)
	}

	// 4. Revoking share removes it; UpdateShareExpiration must return ErrShareNotFound
	if err := db.RevokeShare(ctx, s.ID, now); err != nil {
		t.Fatalf("RevokeShare failed: %v", err)
	}
	_, err = db.UpdateShareExpiration(ctx, s.ID, newExp, now)
	if err == nil || !errors.Is(err, protocol.ErrShareNotFound) {
		t.Fatalf("expected ErrShareNotFound when updating revoked share, got: %v", err)
	}
}

func TestCountActiveSharesByIP(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	ip := "198.51.100.42"
	for i := 0; i < 3; i++ {
		s := &database.Share{
			ID:        fmt.Sprintf("ip_test_share_%d", i),
			TokenHash: []byte("01234567890123456789012345678901"),
			Status:    database.StatusActive,
			CreatedAt: now,
			ExpiresAt: now.Add(time.Hour),
			ClientIP:  ip,
		}
		if err := db.CreateShare(ctx, s); err != nil {
			t.Fatalf("CreateShare %d failed: %v", i, err)
		}
	}

	count, err := db.CountActiveSharesByIP(ctx, ip, now)
	if err != nil {
		t.Fatalf("CountActiveSharesByIP failed: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected 3 active shares for IP, got %d", count)
	}

	// Revoke one share -> count decreases to 2
	if err := db.RevokeShare(ctx, "ip_test_share_0", now); err != nil {
		t.Fatalf("RevokeShare failed: %v", err)
	}
	countAfter, err := db.CountActiveSharesByIP(ctx, ip, now)
	if err != nil {
		t.Fatalf("CountActiveSharesByIP after revoke failed: %v", err)
	}
	if countAfter != 2 {
		t.Fatalf("expected 2 active shares for IP after revoke, got %d", countAfter)
	}
}

func TestUpgradeFromOldDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "legacy_v010.db")

	// 1. Manually create a legacy v0.1.0 database (no client_ip, no client_country, no revoked_stats)
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open raw sqlite db: %v", err)
	}
	v010Schema := `
	CREATE TABLE shares (
		id TEXT PRIMARY KEY,
		token_hash BLOB NOT NULL,
		status TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		revoked_at INTEGER
	);

	CREATE INDEX idx_shares_status ON shares(status);
	CREATE INDEX idx_shares_expires_at ON shares(expires_at);
	`
	if _, err := rawDB.Exec(v010Schema); err != nil {
		t.Fatalf("failed to create v0.1.0 schema: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	// Insert 1 active share, 1 expired share, and 1 revoked share using v0.1.0 schema
	insertQuery := `INSERT INTO shares (id, token_hash, status, created_at, expires_at, revoked_at) VALUES (?, ?, ?, ?, ?, ?);`
	if _, err := rawDB.Exec(insertQuery, "active_old", []byte("tokenhash123"), database.StatusActive, now.Unix(), now.Add(time.Hour).Unix(), nil); err != nil {
		t.Fatalf("insert active_old failed: %v", err)
	}
	if _, err := rawDB.Exec(insertQuery, "expired_old", []byte("tokenhash123"), database.StatusExpired, now.Add(-2*time.Hour).Unix(), now.Add(-time.Hour).Unix(), nil); err != nil {
		t.Fatalf("insert expired_old failed: %v", err)
	}
	if _, err := rawDB.Exec(insertQuery, "revoked_old", []byte("tokenhash123"), database.StatusRevoked, now.Add(-time.Hour).Unix(), now.Add(time.Hour).Unix(), now.Unix()); err != nil {
		t.Fatalf("insert revoked_old failed: %v", err)
	}
	rawDB.Close()

	// 2. Open the legacy database with new binary's database.Open (triggers auto-migration)
	db, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("database.Open on legacy db failed: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// 3. Verify active share was preserved and readable
	active, err := db.GetShare(ctx, "active_old")
	if err != nil {
		t.Fatalf("GetShare active_old failed: %v", err)
	}
	if active.Status != database.StatusActive {
		t.Fatalf("expected status ACTIVE, got %s", active.Status)
	}
	if active.ClientIP != "" || active.ClientCountry != "" {
		t.Fatalf("expected empty client_ip and client_country for legacy share, got ip=%q country=%q", active.ClientIP, active.ClientCountry)
	}

	// 4. Verify expired share was preserved
	expired, err := db.GetShare(ctx, "expired_old")
	if err != nil {
		t.Fatalf("GetShare expired_old failed: %v", err)
	}
	if expired.Status != database.StatusExpired {
		t.Fatalf("expected status EXPIRED, got %s", expired.Status)
	}

	// 5. Verify revoked share content was purged from shares
	_, err = db.GetShare(ctx, "revoked_old")
	if err == nil || !errors.Is(err, protocol.ErrShareNotFound) {
		t.Fatalf("expected revoked_old to be purged, got: %v", err)
	}

	// 6. Verify revoked_old was counted into revoked_stats
	total, byCountry, err := db.GetRevokedStats(ctx)
	if err != nil {
		t.Fatalf("GetRevokedStats failed: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected totalRevoked 1, got %d", total)
	}
	if byCountry["Unknown"] != 1 {
		t.Fatalf("expected Unknown country count 1, got %+v", byCountry)
	}

	// 7. Verify creating a new share with new columns works seamlessly
	newShare := &database.Share{
		ID:            "brand_new_share",
		TokenHash:     []byte("tokenhash123"),
		Status:        database.StatusActive,
		CreatedAt:     now,
		ExpiresAt:     now.Add(time.Hour),
		ClientIP:      "1.2.3.4",
		ClientCountry: "Japan (JP)",
	}
	if err := db.CreateShare(ctx, newShare); err != nil {
		t.Fatalf("CreateShare on upgraded db failed: %v", err)
	}
	gotNew, err := db.GetShare(ctx, "brand_new_share")
	if err != nil {
		t.Fatalf("GetShare brand_new_share failed: %v", err)
	}
	if gotNew.ClientIP != "1.2.3.4" || gotNew.ClientCountry != "Japan (JP)" {
		t.Fatalf("unexpected new share data: %+v", gotNew)
	}
}
