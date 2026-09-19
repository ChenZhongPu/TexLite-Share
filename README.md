# TexLite Share Tunnel

A specialized, secure reverse-tunnel system engineered exclusively for **TexLite Share** (serving [TexLite](https://github.com/SWUFE-DB-Group/TexLite)).

TexLite Share enables users to instantly expose their local TexLite instance (e.g. running at `127.0.0.1:3000`) to collaborators via a public URL (e.g. `https://k83fx2m7pq4z7abc.share.example.com`), completely eliminating router port forwarding, public IP requirements, manual DNS/HTTPS setups, and third-party tools like frp or rathole.

---

## ⚡ Quick Client Installation (Linux & macOS)

Install or upgrade `texlite-tunnel-client` directly via:

```bash
curl -fsSL https://raw.githubusercontent.com/ChenZhongPu/TexLite-Share/main/install-client.sh | bash
```

> **Note**: Installs to `~/.local/bin/texlite-tunnel-client` by default. To customize install path, prefix with `BIN_DIR`:
> ```bash
> BIN_DIR=~/.local/share/bin curl -fsSL https://raw.githubusercontent.com/ChenZhongPu/TexLite-Share/main/install-client.sh | bash
> ```

---

## 1. High-Level Architecture

```text
                                Internet
                                   │
                                   │ HTTPS :443
                                   ▼
                    k83fx2.share.example.com
                                   │
                              wildcard DNS
                                   │
                                   ▼
                                 Caddy
                            TLS termination
                                   │
                                   │ HTTP / WebSocket
                                   ▼
                       texlite-share-server
                          127.0.0.1:9000
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
- **WebSocket Transport (`github.com/coder/websocket`)**: Normal browser traffic and tunnel-client connections both operate on standard HTTP/HTTPS ports (e.g. 443 via Caddy). No special tunnel TCP ports required.
- **Multiplexing (`github.com/xtaci/smux` v2)**: A single persistent WebSocket connection handles arbitrary concurrent HTTP requests, large file downloads (PDFs), and TexLite WebSockets over lightweight logical streams.
- **CGO-Free Pure Go SQLite (`modernc.org/sqlite`)**: Cross-compiles out of the box for Linux, macOS, and Windows.
- **Loopback Safety**: The client strictly validates local destination addresses and only allows loopback addresses (`127.0.0.1`, `localhost`, `[::1]`). The server cannot command the client to dial arbitrary internal IPs.
- **Cryptographic Security**: Tokens are generated via `crypto/rand` (256-bit), stored only as SHA-256 hashes, and verified in constant time. Share IDs are 80-bit random Base32 identifiers.
- **Session Replacement & Recovery**: Reconnection automatically replaces older sessions using generation counters, seamlessly surviving network interruptions and server restarts.

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
│   ├── api/                      # Control API (create/revoke), Tunnel WS endpoint, Router
│   ├── auth/                     # Token generation, SHA-256 hashing, constant-time compare
│   ├── config/                   # Server/client configuration, loopback validation
│   ├── database/                 # SQLite persistence, WAL mode, queries, migrations
│   ├── protocol/                 # Protocol constants, ShareID generator/validator, typed errors
│   ├── proxy/                    # Subdomain Host routing, ReverseProxy with smux Transport
│   ├── registry/                 # Thread-safe in-memory session registry with generation tracking
│   ├── tunnel/                   # Stream bridge, smux config, client runner & backoff
│   └── logging/                  # Structured logging (slog) with credential safety
│
├── migrations/                   # SQLite schema definitions
├── integration/                  # Automated integration tests (8 end-to-end scenarios)
├── go.mod
└── go.sum
```

---

## 3. Building the Binaries

### Standard Build:
```bash
# Build server
go build -o bin/texlite-share-server ./cmd/texlite-share-server

# Build client
go build -o bin/texlite-tunnel-client ./cmd/texlite-tunnel-client
```

### Cross-Compilation (CGO-Free):
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

## 4. Local Development Walkthrough (No Domain / DNS Required)

### 1. Start Local TexLite (or a test server):
```bash
python3 -m http.server 3000 --bind 127.0.0.1
```

### 2. Start Share Server:
```bash
go run ./cmd/texlite-share-server \
    --listen 127.0.0.1:9000 \
    --admin-listen 127.0.0.1:9001 \
    --base-domain share.local \
    --db /tmp/texlite-share.db \
    --create-api-key dev-secret
```

- Public Port (`:9000`): Only handles visitor subdomain traffic and tunnel WebSocket connections.
- Admin Port (`:9001`): Dedicated localhost port serving the Web Dashboard and management APIs.

### 3. Manage via Web Dashboard (or SSH Tunnel):
Open `http://127.0.0.1:9001/?key=dev-secret` in your browser!
If the server is on a remote VPS, forward the port via SSH:
```bash
ssh -L 9001:127.0.0.1:9001 user@your-server-ip
```
Then visit `http://localhost:9001/?key=dev-secret` on your local laptop to view the graphical dashboard, monitor active shares/tunnels, and create/revoke shares with one click.

### 4. Or Create a Share via curl:
```bash
curl -X POST \
  -H 'Authorization: Bearer dev-secret' \
  http://127.0.0.1:9001/api/v1/shares
```

Example JSON response:
```json
{
  "id": "k83fx2m7pq4z7abc",
  "token": "4vQyW1...",
  "expiresAt": "2026-09-19T10:00:00Z",
  "publicUrl": "http://k83fx2m7pq4z7abc.share.local"
}
```

### 4. Client Startup Modes

The client supports two distinct operating modes:

#### Mode 1: Direct Start / Automatic Temporary Share (Default)
When starting directly without `--share-id` or `--token`:
1. Checks for and **proactively deletes any previous temporary share** from the server.
2. Requests creation of a **new temporary share** (default 1-hour expiration).
3. Automatically connects the reverse tunnel.
4. On exit (Ctrl+C), automatically revokes the temporary share on the server.

```bash
# Run client directly
./bin/texlite-tunnel-client
```

Terminal output:
```text
===================================================================
 ✨ TexLite Share is Live! (Automatic Temporary Mode)
 🔗 Public URL:   https://k83fx2m7pq4z7abc.share.example.com
 🆔 Share ID:     k83fx2m7pq4z7abc
 🎯 Local Target: http://127.0.0.1:3000 (TexLite verified, PID: 3352906)
 ⏳ Expires At:   2026-09-18 16:15:00
===================================================================
Press Ctrl+C to stop sharing.
```

#### Mode 2: Manual Connection Mode (Existing Share)
When connecting to a pre-allocated share or permanent share, supply both `--share-id` and `--token`:
- Connects directly without modifying or deleting temporary shares.
- Does not auto-revoke the share upon exit.

```bash
./bin/texlite-tunnel-client \
    --server-url https://share.example.com \
    --share-id k83fx2m7pq4z7abc \
    --token 4vQyW1... \
    --local-addr 127.0.0.1:3000
```

### 6. Access via Public Host:
```bash
curl -H 'Host: k83fx2m7pq4z7abc.share.local' http://127.0.0.1:9000/
```

For browser testing locally, simply add the hostname to `/etc/hosts`:
```text
127.0.0.1 k83fx2m7pq4z7abc.share.local
```
Then navigate to `http://k83fx2m7pq4z7abc.share.local:9000` in any web browser.


---

## 5. Production Deployment with Caddy

### DNS Configuration:
Point wildcard DNS to your server IP:
```dns
share.example.com      A    SERVER_IP
*.share.example.com    A    SERVER_IP
```

### Caddyfile:
```caddy
share.example.com, *.share.example.com {
    tls {
        dns cloudflare {env.CF_API_TOKEN}
    }

    reverse_proxy 127.0.0.1:9000
}
```

Caddy handles automatic wildcard TLS termination and reverse-proxies all control APIs, tunnel WebSocket connections, and public subdomain traffic directly to `texlite-share-server`. Creating new shares requires zero Caddy reloads, zero DNS changes, and zero cert renewals.

---

## 6. Testing & Quality Assurance

Run all unit tests and integration tests with the Go race detector:

```bash
# Run unit tests
go test -v -race ./internal/...

# Run end-to-end integration tests
go test -v -race ./integration/...

# Run all tests in the repository
go test -v -race ./...
```
