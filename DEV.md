# TexLite Share — Developer Guide

This document covers internal architecture, building from source, local development workflows, testing, and protocol specifications for developers contributing to `texlite-share`.

---

## 1. High-Level Architecture

TexLite Share consists of two primary binaries:
- **`texlite-share-server`**: High-performance reverse-tunnel server, HTTP reverse-proxy, and administrative control center.
- **`texlite-tunnel-client`**: Lightweight, zero-dependency client running alongside a local [TexLite](https://github.com/SWUFE-DB-Group/TexLite) instance.

```text
                                Internet
                                   │
                                   │ HTTPS :443
                                   ▼
                     *.share.example.com / share.example.com
                                   │
                               wildcard DNS
                                   │
                                   ▼
                                 Caddy
                            TLS termination
                                   │
                    ┌──────────────┴──────────────┐
                    │ HTTP (/api/*, wss://, *.sub)│ HTTP (static landing page)
                    ▼                             ▼
          texlite-share-server            /var/www/texlite-share
             127.0.0.1:9000                    (index.html)
              /            \
             /              \
    browser requests       tunnel connection
           │                      ▲
           │                      │
           │               WebSocket + smux
           │                      │
           ▼                      │
      active session ─────────────┘
                                  │
                                  ▼
                      texlite-tunnel-client
                                  │
                                  ▼
                         127.0.0.1:3000
                                  │
                                  ▼
                               TexLite
```

### Key Technical Properties:

- **WebSocket Transport (`github.com/coder/websocket`)**: Both public browser traffic and tunnel-client connections multiplex over standard HTTP/HTTPS ports (e.g. 443 via Caddy). No special tunnel TCP ports are required.
- **Stream Multiplexing (`github.com/xtaci/smux` v2)**: A single persistent WebSocket connection handles arbitrary concurrent HTTP requests, large file downloads (PDFs, images), and TexLite internal WebSockets over lightweight logical streams with millisecond keep-alive.
- **CGO-Free Pure Go SQLite (`modernc.org/sqlite`)**: Cross-compiles out of the box for Linux, macOS, and Windows without requiring GCC or external C libraries. Uses WAL (Write-Ahead Logging) mode for high-concurrency read/write operations.
- **Loopback Safety & SSRF Protection**: The client strictly validates local destination addresses and only allows loopback addresses (`127.0.0.1`, `localhost`, `[::1]`). The server cannot command the client to dial arbitrary internal IPs.
- **Cryptographic Security**: Tokens are generated via `crypto/rand` (256-bit), stored only as SHA-256 hashes in the database, and verified using constant-time comparison. Share IDs are 80-bit random Base32 identifiers (or validated custom IDs).
- **Session Replacement & Recovery**: Reconnection automatically replaces older sessions using generation counters, seamlessly surviving network interruptions and client/server restarts.

---

## 2. Directory Structure

```text
texlite-share/
├── cmd/
│   ├── texlite-share-server/     # Share server entrypoint
│   │   └── main.go
│   └── texlite-tunnel-client/    # Tunnel client entrypoint
│       └── main.go
│
├── internal/
│   ├── api/                      # Control API (create/revoke), Tunnel WS endpoint, Admin UI, Router
│   ├── auth/                     # Token generation, SHA-256 hashing, constant-time compare
│   ├── cache/                    # Edge static asset caching (LRU, max-age, compression)
│   ├── config/                   # Server/client configuration, loopback validation
│   ├── database/                 # SQLite persistence, WAL mode, migrations, queries
│   ├── geoip/                    # Client IP geolocation lookup (offline database / fallback)
│   ├── logging/                  # Structured logging (log/slog) with credential safety
│   ├── protocol/                 # Protocol constants, ShareID generator/validator, typed errors
│   ├── proxy/                    # Subdomain Host routing, ReverseProxy with smux Transport
│   ├── registry/                 # Thread-safe in-memory session registry with generation tracking
│   ├── tunnel/                   # Stream bridge, smux config, client runner, auto-discovery
│   └── version/                  # Semantic versioning, Git commit SHA, build metadata
│
├── migrations/                   # SQLite schema definitions
├── integration/                  # Automated integration tests (end-to-end scenarios)
├── install-client.sh             # Client one-line installer script
├── Makefile                      # Build automation and cross-compilation targets
├── go.mod
└── go.sum
```

---

## 3. Building from Source

### Prerequisites:
- Go 1.22 or higher

### Standard Build:
```bash
# Build both server and client binaries
make all

# Or build individually
make server
make client
```
Binaries will be placed in `bin/`:
- `bin/texlite-share-server`
- `bin/texlite-tunnel-client`

### Cross-Compilation (CGO-Free):
Because SQLite is backed by `modernc.org/sqlite`, cross-compilation requires zero C toolchains:

```bash
# Linux (amd64 / arm64)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/texlite-tunnel-client-linux-amd64 ./cmd/texlite-tunnel-client
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bin/texlite-tunnel-client-linux-arm64 ./cmd/texlite-tunnel-client

# macOS (Intel / Apple Silicon)
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o bin/texlite-tunnel-client-darwin-amd64 ./cmd/texlite-tunnel-client
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o bin/texlite-tunnel-client-darwin-arm64 ./cmd/texlite-tunnel-client

# Windows (amd64)
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o bin/texlite-tunnel-client-windows-amd64.exe ./cmd/texlite-tunnel-client
```

---

## 4. Local Development Walkthrough

You can test the entire client-server tunnel workflow locally without registering a real domain or configuring public DNS.

### Step 1: Start a Local Mock Web Service (Simulating TexLite)
In a terminal, start a simple HTTP server on port 3000:
```bash
python3 -m http.server 3000 --bind 127.0.0.1
```

### Step 2: Start the Share Server locally
In a second terminal:
```bash
go run ./cmd/texlite-share-server \
    --listen 127.0.0.1:9000 \
    --admin-listen 127.0.0.1:9001 \
    --admin-api-key dev-secret \
    --base-domain share.local \
    --db /tmp/texlite-share-dev.db
```
- **Port `:9000` (Public Gateway)**: Accepts visitor traffic and WebSocket tunnels.
- **Port `:9001` (Admin Dashboard)**: Serves the web management console (`http://127.0.0.1:9001/?key=dev-secret`).

### Step 3: Run the Tunnel Client
In a third terminal:
```bash
go run ./cmd/texlite-tunnel-client \
    --server-url http://127.0.0.1:9000 \
    --local-addr 127.0.0.1:3000
```
Output:
```text
===================================================================
 ✨ TexLite Share is Live! (Automatic Temporary Mode)
 🔗 Public URL:   http://k83fx2m7pq4z7abc.share.local
 🆔 Share ID:     k83fx2m7pq4z7abc
 🎯 Local Target: http://127.0.0.1:3000
 ⏳ Expires At:   2026-11-17 11:15:00
===================================================================
```

### Step 4: Test Access via Host Header or `/etc/hosts`
Test with curl:
```bash
curl -H 'Host: k83fx2m7pq4z7abc.share.local' http://127.0.0.1:9000/
```
Or map the subdomain locally in `/etc/hosts`:
```text
127.0.0.1 k83fx2m7pq4z7abc.share.local
```
Then visit `http://k83fx2m7pq4z7abc.share.local:9000` in any web browser.

---

## 5. Testing & Quality Assurance

Run all unit tests and integration tests with the Go race detector enabled:

```bash
# Run unit tests
go test -v -race ./internal/...

# Run end-to-end integration tests
go test -v -race ./integration/...

# Run all test suites
go test -v -race ./...
```

---

## 6. Internal API & Protocol Specification

### Public Gateway (`--listen`, e.g. `:9000`)

| Endpoint | Method | Description |
| :--- | :--- | :--- |
| `/healthz` or `/api/v1/health` | `GET` | Health check probe (returns `{"status":"ok"}`) |
| `/api/v1/shares` | `POST` | Dynamic share creation (rate-limited, protected by `--create-api-key` if configured) |
| `/api/v1/shares/{id}` | `DELETE` | Client share revocation upon termination (requires bearer token) |
| `/api/v1/tunnel/{shareID}` | `GET` (WebSocket) | Persistent smux multiplexed tunnel connection (requires bearer token) |
| `*.base-domain` | `ALL` | Wildcard subdomain reverse proxying to target tunnel |

### Admin Management (`--admin-listen`, e.g. `:9001`)

| Endpoint | Method | Description |
| :--- | :--- | :--- |
| `/` | `GET` | Embedded HTML/JS Admin Web Dashboard |
| `/api/v1/shares` | `GET` | List active shares with online status, streams, and geolocation |
| `/api/v1/shares` | `POST` | Create share with custom ID and customized TTL |
| `/api/v1/shares/{id}/expires` | `POST` | Extend or modify share lifetime |
| `/api/v1/shares/{id}` | `DELETE` | Force-revoke an active share and terminate tunnel |
| `/api/v1/stats` | `GET` | Server telemetry (active shares, connected tunnels, total revoked) |
| `/api/v1/config` | `GET` | View running server configuration and defaults (secrets masked) |
