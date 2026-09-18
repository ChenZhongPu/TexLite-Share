package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	ClientIP  string
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
	schema := `
	CREATE TABLE IF NOT EXISTS shares (
		id TEXT PRIMARY KEY,
		token_hash BLOB NOT NULL,
		status TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		revoked_at INTEGER,
		client_ip TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_shares_status
	ON shares(status);

	CREATE INDEX IF NOT EXISTS idx_shares_expires_at
	ON shares(expires_at);

	CREATE INDEX IF NOT EXISTS idx_shares_client_ip
	ON shares(client_ip);
	`
	if _, err := d.db.Exec(schema); err != nil {
		return err
	}
	_, _ = d.db.Exec(`ALTER TABLE shares ADD COLUMN client_ip TEXT;`)
	return nil
}

// Close closes the underlying SQLite database connection.
func (d *DB) Close() error {
	return d.db.Close()
}

// CreateShare stores a new share record in the database.
func (d *DB) CreateShare(ctx context.Context, share *Share) error {
	query := `
	INSERT INTO shares (id, token_hash, status, created_at, expires_at, revoked_at, client_ip)
	VALUES (?, ?, ?, ?, ?, ?, ?);
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
	)
	if err != nil {
		return fmt.Errorf("failed to insert share: %w", err)
	}
	return nil
}

// GetShare retrieves a share by its ID.
func (d *DB) GetShare(ctx context.Context, id string) (*Share, error) {
	query := `
	SELECT id, token_hash, status, created_at, expires_at, revoked_at, COALESCE(client_ip, '')
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

// RevokeShare marks an existing share as REVOKED and sets its revoked_at timestamp.
func (d *DB) RevokeShare(ctx context.Context, id string, revokedAt time.Time) error {
	query := `
	UPDATE shares
	SET status = ?, revoked_at = ?
	WHERE id = ? AND status != ?;
	`
	res, err := d.db.ExecContext(ctx, query, StatusRevoked, revokedAt.Unix(), id, StatusRevoked)
	if err != nil {
		return fmt.Errorf("failed to revoke share: %w", err)
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		// Check if share exists
		if _, err := d.GetShare(ctx, id); err != nil {
			return err
		}
	}
	return nil
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

// ListShares retrieves shares ordered by creation time descending.
func (d *DB) ListShares(ctx context.Context, limit int) ([]*Share, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	query := `
	SELECT id, token_hash, status, created_at, expires_at, revoked_at
	FROM shares
	ORDER BY created_at DESC
	LIMIT ?;
	`
	rows, err := d.db.QueryContext(ctx, query, limit)
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
		if err := rows.Scan(&s.ID, &s.TokenHash, &s.Status, &createdUnix, &expiresUnix, &revokedAtUnix); err != nil {
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
