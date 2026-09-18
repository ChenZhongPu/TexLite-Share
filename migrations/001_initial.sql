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
