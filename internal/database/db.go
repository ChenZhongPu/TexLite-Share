package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"texlite-share/internal/protocol"
)

// Share status constants.
const (
	StatusActive  = "ACTIVE"
	StatusExpired = "EXPIRED"
	StatusRevoked = "REVOKED"
)

// Share represents the persisted share entity in the database.
type Share struct {
	ID        string
	TokenHash []byte
	Status    string
	CreatedAt time.Time
	ExpiresAt time.Time
	RevokedAt *time.Time
	ClientIP      string
	ClientCountry string
}

// DB wraps the SQL database handle and provides share-specific persistence methods.
type DB struct {
	db *sql.DB
}

// Open opens a SQLite database at dbPath and runs initial table migrations.
func Open(dbPath string) (*DB, error) {
	// Enable WAL and busy timeout for concurrent safety
	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", dbPath)
	if dbPath == ":memory:" {
		dsn = ":memory:?_pragma=foreign_keys(1)"
	}

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite db: %w", err)
	}

	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("failed to ping sqlite db: %w", err)
	}

	d := &DB{db: sqlDB}
	if err := d.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("failed to migrate db schema: %w", err)
	}

	return d, nil
}

func (d *DB) migrate() error {
	baseSchema := `
	CREATE TABLE IF NOT EXISTS shares (
		id TEXT PRIMARY KEY,
		token_hash BLOB NOT NULL,
		status TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		revoked_at INTEGER,
		client_ip TEXT,
		client_country TEXT
	);

	CREATE TABLE IF NOT EXISTS revoked_stats (
		country TEXT PRIMARY KEY,
		count INTEGER NOT NULL DEFAULT 0
	);
	`
	if _, err := d.db.Exec(baseSchema); err != nil {
		return err
	}

	// For databases upgraded from older versions:
	// v0.1.0 - v0.1.3: shares lacked client_ip and client_country
	// v0.1.4: shares lacked client_country
	_, _ = d.db.Exec(`ALTER TABLE shares ADD COLUMN client_ip TEXT;`)
	_, _ = d.db.Exec(`ALTER TABLE shares ADD COLUMN client_country TEXT;`)

	indexSchema := `
	CREATE INDEX IF NOT EXISTS idx_shares_status
	ON shares(status);

	CREATE INDEX IF NOT EXISTS idx_shares_expires_at
	ON shares(expires_at);

	CREATE INDEX IF NOT EXISTS idx_shares_client_ip
	ON shares(client_ip);
	`
	if _, err := d.db.Exec(indexSchema); err != nil {
		return err
	}

	// Migrate legacy REVOKED shares into revoked_stats if any exist, then purge them
	_, _ = d.db.Exec(`
	INSERT INTO revoked_stats (country, count)
	SELECT COALESCE(NULLIF(client_country, ''), 'Unknown'), COUNT(*)
	FROM shares WHERE status = 'REVOKED'
	GROUP BY COALESCE(NULLIF(client_country, ''), 'Unknown')
	ON CONFLICT(country) DO UPDATE SET count = count + excluded.count;
	`)
	_, _ = d.db.Exec(`DELETE FROM shares WHERE status = 'REVOKED';`)
	return nil
}

// Close closes the underlying SQLite database connection.
func (d *DB) Close() error {
	return d.db.Close()
}

// CreateShare stores a new share record in the database.
func (d *DB) CreateShare(ctx context.Context, share *Share) error {
	query := `
	INSERT INTO shares (id, token_hash, status, created_at, expires_at, revoked_at, client_ip, client_country)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		token_hash = excluded.token_hash,
		status = excluded.status,
		created_at = excluded.created_at,
		expires_at = excluded.expires_at,
		revoked_at = excluded.revoked_at,
		client_ip = excluded.client_ip,
		client_country = excluded.client_country;
	`
	var revokedAtUnix sql.NullInt64
	if share.RevokedAt != nil {
		revokedAtUnix = sql.NullInt64{Int64: share.RevokedAt.Unix(), Valid: true}
	}

	_, err := d.db.ExecContext(ctx, query,
		share.ID,
		share.TokenHash,
		share.Status,
		share.CreatedAt.Unix(),
		share.ExpiresAt.Unix(),
		revokedAtUnix,
		share.ClientIP,
		share.ClientCountry,
	)
	if err != nil {
		return fmt.Errorf("failed to insert share: %w", err)
	}
	return nil
}

// GetShare retrieves a share by its ID.
func (d *DB) GetShare(ctx context.Context, id string) (*Share, error) {
	query := `
	SELECT id, token_hash, status, created_at, expires_at, revoked_at, COALESCE(client_ip, ''), COALESCE(client_country, '')
	FROM shares
	WHERE id = ?;
	`
	var (
		s             Share
		createdUnix   int64
		expiresUnix   int64
		revokedAtUnix sql.NullInt64
	)

	err := d.db.QueryRowContext(ctx, query, id).Scan(
		&s.ID,
		&s.TokenHash,
		&s.Status,
		&createdUnix,
		&expiresUnix,
		&revokedAtUnix,
		&s.ClientIP,
		&s.ClientCountry,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, protocol.ErrShareNotFound
		}
		return nil, fmt.Errorf("failed to query share: %w", err)
	}

	s.CreatedAt = time.Unix(createdUnix, 0).UTC()
	s.ExpiresAt = time.Unix(expiresUnix, 0).UTC()
	if revokedAtUnix.Valid {
		t := time.Unix(revokedAtUnix.Int64, 0).UTC()
		s.RevokedAt = &t
	}
	return &s, nil
}

// CountActiveShares counts the number of shares with status 'ACTIVE' that have not expired.
func (d *DB) CountActiveShares(ctx context.Context, now time.Time) (int, error) {
	query := `
	SELECT COUNT(*)
	FROM shares
	WHERE status = ? AND expires_at > ?;
	`
	var count int
	err := d.db.QueryRowContext(ctx, query, StatusActive, now.Unix()).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count active shares: %w", err)
	}
	return count, nil
}

// CountActiveSharesByIP counts active, non-expired shares created by a specific client IP.
func (d *DB) CountActiveSharesByIP(ctx context.Context, clientIP string, now time.Time) (int, error) {
	if clientIP == "" {
		return 0, nil
	}
	query := `
	SELECT COUNT(*)
	FROM shares
	WHERE client_ip = ? AND status = ? AND expires_at > ?;
	`
	var count int
	err := d.db.QueryRowContext(ctx, query, clientIP, StatusActive, now.Unix()).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count active shares by IP: %w", err)
	}
	return count, nil
}

// RevokeShare removes the share from the database and increments the revoked counter by country.
func (d *DB) RevokeShare(ctx context.Context, id string, revokedAt time.Time) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin revoke tx: %w", err)
	}
	defer tx.Rollback()

	var country sql.NullString
	err = tx.QueryRowContext(ctx, "SELECT client_country FROM shares WHERE id = ?;", id).Scan(&country)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return protocol.ErrShareNotFound
		}
		return fmt.Errorf("failed to query share for revocation: %w", err)
	}

	countryStr := strings.TrimSpace(country.String)
	if countryStr == "" {
		countryStr = "Unknown"
	}

	statsQuery := `
	INSERT INTO revoked_stats (country, count)
	VALUES (?, 1)
	ON CONFLICT(country) DO UPDATE SET count = count + 1;
	`
	if _, err := tx.ExecContext(ctx, statsQuery, countryStr); err != nil {
		return fmt.Errorf("failed to update revoked_stats: %w", err)
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM shares WHERE id = ?;", id); err != nil {
		return fmt.Errorf("failed to delete revoked share: %w", err)
	}

	return tx.Commit()
}

// GetRevokedStats returns the total revoked share count and a breakdown by country.
func (d *DB) GetRevokedStats(ctx context.Context) (int, map[string]int, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT country, count FROM revoked_stats;")
	if err != nil {
		return 0, nil, fmt.Errorf("failed to query revoked_stats: %w", err)
	}
	defer rows.Close()

	byCountry := make(map[string]int)
	total := 0
	for rows.Next() {
		var country string
		var count int
		if err := rows.Scan(&country, &count); err != nil {
			return 0, nil, fmt.Errorf("failed to scan revoked_stats: %w", err)
		}
		byCountry[country] = count
		total += count
	}
	return total, byCountry, rows.Err()
}

// ExpireShares transitions all active shares whose expires_at <= now to 'EXPIRED'
// and returns the list of expired share IDs.
func (d *DB) ExpireShares(ctx context.Context, now time.Time) ([]string, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Find expired shares
	selectQuery := `
	SELECT id FROM shares
	WHERE status = ? AND expires_at <= ?;
	`
	rows, err := tx.QueryContext(ctx, selectQuery, StatusActive, now.Unix())
	if err != nil {
		return nil, fmt.Errorf("failed to select expired shares: %w", err)
	}
	defer rows.Close()

	var expiredIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan expired share id: %w", err)
		}
		expiredIDs = append(expiredIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	if len(expiredIDs) > 0 {
		updateQuery := `
		UPDATE shares
		SET status = ?
		WHERE status = ? AND expires_at <= ?;
		`
		if _, err := tx.ExecContext(ctx, updateQuery, StatusExpired, StatusActive, now.Unix()); err != nil {
			return nil, fmt.Errorf("failed to update expired shares: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit expiration tx: %w", err)
	}
	return expiredIDs, nil
}

// ListShares retrieves shares that are not revoked, ordered by creation time descending.
func (d *DB) ListShares(ctx context.Context, limit int) ([]*Share, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	query := `
	SELECT id, token_hash, status, created_at, expires_at, revoked_at, COALESCE(client_ip, ''), COALESCE(client_country, '')
	FROM shares
	WHERE status != ?
	ORDER BY created_at DESC
	LIMIT ?;
	`
	rows, err := d.db.QueryContext(ctx, query, StatusRevoked, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query shares: %w", err)
	}
	defer rows.Close()

	var shares []*Share
	for rows.Next() {
		var (
			s             Share
			createdUnix   int64
			expiresUnix   int64
			revokedAtUnix sql.NullInt64
		)
		if err := rows.Scan(&s.ID, &s.TokenHash, &s.Status, &createdUnix, &expiresUnix, &revokedAtUnix, &s.ClientIP, &s.ClientCountry); err != nil {
			return nil, fmt.Errorf("failed to scan share: %w", err)
		}
		s.CreatedAt = time.Unix(createdUnix, 0).UTC()
		s.ExpiresAt = time.Unix(expiresUnix, 0).UTC()
		if revokedAtUnix.Valid {
			t := time.Unix(revokedAtUnix.Int64, 0).UTC()
			s.RevokedAt = &t
		}
		shares = append(shares, &s)
	}
	return shares, rows.Err()
}

// UpdateShareExpiration updates the expires_at timestamp of a share and reactivates it if newExpiresAt is in the future.
// It accepts now time.Time for deterministic time handling and runs within a transaction.
func (d *DB) UpdateShareExpiration(ctx context.Context, id string, newExpiresAt time.Time, now time.Time) (*Share, error) {
	newStatus := StatusActive
	if now.After(newExpiresAt) {
		newStatus = StatusExpired
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	query := `
	UPDATE shares
	SET expires_at = ?, status = ?
	WHERE id = ? AND status != ?;
	`
	res, err := tx.ExecContext(ctx, query, newExpiresAt.Unix(), newStatus, id, StatusRevoked)
	if err != nil {
		return nil, fmt.Errorf("failed to update share expiration: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, protocol.ErrShareNotFound
	}

	s, err := d.getShareTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit update expiration tx: %w", err)
	}
	return s, nil
}

func (d *DB) getShareTx(ctx context.Context, tx *sql.Tx, id string) (*Share, error) {
	query := `
	SELECT id, token_hash, status, created_at, expires_at, revoked_at, COALESCE(client_ip, ''), COALESCE(client_country, '')
	FROM shares
	WHERE id = ?;
	`
	var (
		s             Share
		createdUnix   int64
		expiresUnix   int64
		revokedAtUnix sql.NullInt64
	)
	err := tx.QueryRowContext(ctx, query, id).Scan(
		&s.ID,
		&s.TokenHash,
		&s.Status,
		&createdUnix,
		&expiresUnix,
		&revokedAtUnix,
		&s.ClientIP,
		&s.ClientCountry,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, protocol.ErrShareNotFound
		}
		return nil, fmt.Errorf("failed to query share in tx: %w", err)
	}
	s.CreatedAt = time.Unix(createdUnix, 0).UTC()
	s.ExpiresAt = time.Unix(expiresUnix, 0).UTC()
	if revokedAtUnix.Valid {
		t := time.Unix(revokedAtUnix.Int64, 0).UTC()
		s.RevokedAt = &t
	}
	return &s, nil
}
