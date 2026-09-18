# TexLite Share Tunnel — Phase 1 Development Specification

## 1. Objective

Implement a small reverse-tunnel system dedicated exclusively to **TexLite Share**.

The system consists of two Go binaries:

```text
texlite-share-server
texlite-tunnel-client
```

The goal is to support the following user experience in the future:

```bash
texlite share
```

which produces:

```text
https://k83fx2.share.example.com
```

A collaborator opens this URL in a normal browser and accesses the TexLite instance running on the user's local machine.

The local TexLite process may be:

```text
127.0.0.1:3000
```

The user must NOT need to:

- configure router port forwarding;
- expose port 3000 publicly;
- own a public IP;
- configure DNS;
- configure HTTPS;
- run frp/rathole manually.

The tunnel client initiates an outbound connection to the TexLite Share server.

---

# 2. Non-Goal

This project is **NOT a generic frp/rathole replacement**.

Do NOT implement:

```text
TCP port forwarding as a generic feature
UDP forwarding
arbitrary remote ports
arbitrary local destinations requested by the server
custom user domains
P2P
STCP / XTCP equivalents
SOCKS proxy
generic reverse proxy configuration
user-defined proxy types
load balancing
multi-region routing
```

The only intended operation is conceptually:

```text
public TexLite Share URL
        ↓
active tunnel
        ↓
local TexLite HTTP server
```

Keep the implementation deliberately specialized.

---

# 3. Language and Repository

Use **Go for both server and client**.

Use one Go module and build two binaries.

Recommended structure:

```text
texlite-share/
├── cmd/
│   ├── texlite-share-server/
│   │   └── main.go
│   └── texlite-tunnel-client/
│       └── main.go
│
├── internal/
│   ├── api/
│   ├── auth/
│   ├── config/
│   ├── database/
│   ├── protocol/
│   ├── proxy/
│   ├── registry/
│   ├── tunnel/
│   └── logging/
│
├── migrations/
├── integration/
├── go.mod
├── go.sum
└── README.md
```

Do not create two independent repositories initially.

The server and client should share:

```text
protocol version
share ID validation
error definitions
constants
tunnel metadata structures
test utilities
```

---

# 4. High-Level Architecture

Production architecture:

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

Caddy only handles:

```text
DNS-facing HTTP/HTTPS
TLS certificates
reverse proxy to Go server
```

The Go server handles:

```text
share lifecycle
authentication
tunnel registry
Host → share mapping
HTTP reverse proxy
```

---

# 5. Why WebSocket for the Tunnel

Phase 1 should use:

```text
HTTPS/WSS
    ↓
WebSocket
    ↓
smux
    ↓
logical streams
```

instead of exposing a special tunnel TCP port.

This means both:

```text
normal browser traffic
```

and:

```text
tunnel-client traffic
```

can use ordinary HTTPS port 443 in production.

Advantages:

```text
works through common NATs
works through most firewalls
works through HTTP reverse proxies
Caddy can proxy it directly
no additional public tunnel port
TLS handled by Caddy
```

Do NOT introduce QUIC in Phase 1.

QUIC may be evaluated later if TCP head-of-line blocking becomes a measurable problem.

---

# 6. Recommended Dependencies

Use the Go standard library wherever possible.

For WebSocket:

```text
github.com/coder/websocket
```

Required capability:

```text
WebSocket connection → net.Conn abstraction
```

For multiplexing:

```text
github.com/xtaci/smux
```

Use smux protocol version 2.

Do NOT implement a custom multiplexing protocol in Phase 1.

Pin dependency versions in `go.mod`.

Do not automatically track master/main branches.

---

# 7. Why Multiplexing Is Needed

One TexLite page can generate many simultaneous connections:

```text
HTML
JavaScript
CSS
PDF
API calls
WebSocket
images
fonts
```

Do NOT open one independent Internet-facing tunnel connection for every request.

Instead:

```text
                 one persistent WebSocket
                          │
                     smux session
                          │
          ┌───────────────┼──────────────┐
          ▼               ▼              ▼
       stream 1         stream 2       stream 3
          │               │              │
          ▼               ▼              ▼
     HTTP conn        HTTP conn      WebSocket conn
```

Each smux stream behaves like an independent `net.Conn`.

---

# 8. Core Tunnel Model

A share has:

```text
share_id
tunnel_token
created_at
expires_at
status
```

Example:

```text
share_id       k83fx2abcdefgh
tunnel_token   <256-bit random value>
status         ACTIVE
expires_at     2026-09-19T10:00:00Z
```

The server database stores:

```text
share_id
SHA-256(tunnel_token)
timestamps
status
```

The plaintext tunnel token must be returned only when the share is created.

Do NOT store plaintext tunnel tokens in the database.

---

# 9. Share ID

Generate the share ID using cryptographically secure randomness.

Recommended minimum:

```text
80 bits random
```

encoded using lowercase Base32/Base32hex or another URL-safe alphabet.

Example:

```text
k83fx2m7pq4z7abc
```

The share ID:

```text
is visible in the URL
is not a secret
must still be difficult to enumerate
```

Do NOT use:

```text
database sequence IDs
timestamps
usernames
incremental counters
ports
```

---

# 10. Tunnel Token

Generate:

```text
32 random bytes
```

using:

```go
crypto/rand
```

Encode as Base64URL or hex.

Never use:

```go
math/rand
```

for credentials.

The tunnel token must be:

```text
per-share
short-lived
revocable
unpredictable
```

There must be no server-wide credential distributed to every client.

This deliberately avoids the shared `auth.token` problem encountered with frp.

---

# 11. Share Database

Use SQLite.

Prefer a pure-Go SQLite driver so the server remains easy to cross-compile and deploy as a single binary.

Suggested schema:

```sql
CREATE TABLE shares (
    id TEXT PRIMARY KEY,
    token_hash BLOB NOT NULL,
    status TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    revoked_at INTEGER
);

CREATE INDEX idx_shares_status
ON shares(status);

CREATE INDEX idx_shares_expires_at
ON shares(expires_at);
```

Valid statuses:

```text
ACTIVE
EXPIRED
REVOKED
```

Do not persist active TCP/WebSocket connection state.

Connection state belongs only in memory.

---

# 12. Active Tunnel Registry

Maintain an in-memory registry:

```go
map[string]*TunnelSession
```

protected by a mutex or equivalent synchronization.

Conceptual structure:

```go
type TunnelSession struct {
    ShareID    string
    Session    *smux.Session
    Connected  time.Time
    ExpiresAt  time.Time
    Generation uint64
}
```

The registry answers:

```text
Is k83fx2 currently online?
```

The database answers:

```text
Is k83fx2 valid and authorized?
```

These are different questions.

---

# 13. Tunnel Endpoint

Expose:

```text
GET /api/v1/tunnel/{shareID}
```

The client sends:

```http
Authorization: Bearer <tunnel-token>
Sec-WebSocket-Protocol: texlite-tunnel-v1
```

The token MUST NOT appear in:

```text
URL path
query string
share hostname
logs
```

Server procedure:

```text
1. Parse share ID.

2. Look up share.

3. Verify status == ACTIVE.

4. Verify current time < expires_at.

5. Hash supplied token.

6. Compare with stored token hash.

7. Verify protocol version.

8. Upgrade to WebSocket.

9. Convert WebSocket to net.Conn.

10. Start smux server session.

11. Register active session.
```

If authentication fails:

```text
401 Unauthorized
```

If expired:

```text
410 Gone
```

If revoked:

```text
403 Forbidden
```

---

# 14. Session Replacement

Only one active tunnel should exist for one share ID.

Policy:

```text
new valid tunnel wins
```

When a new authenticated client connects using the same share:

```text
old session
    ↓
close
    ↓
replace with new session
```

This supports automatic client reconnection after:

```text
Wi-Fi change
sleep/wake
network interruption
temporary server disconnect
```

Use a generation number or equivalent mechanism so cleanup from an old session cannot accidentally remove a newer replacement.

---

# 15. Public Share Routing

Production base domain:

```text
share.example.com
```

Public URLs:

```text
k83fx2m7pq4z7abc.share.example.com
```

For every ordinary incoming request:

```text
Host: k83fx2m7pq4z7abc.share.example.com
```

extract:

```text
share_id = k83fx2m7pq4z7abc
```

Validate strictly.

Only hostnames matching:

```text
<valid-share-id>.share.example.com
```

may enter the tunnel proxy.

Do NOT accept arbitrary Host values.

---

# 16. Public Request States

For:

```text
https://abc.share.example.com
```

the server should distinguish:

### Unknown share

Return:

```text
404 Not Found
```

### Expired share

Return:

```text
410 Gone
```

### Valid share but tunnel offline

Return:

```text
503 Service Unavailable
```

with a simple human-readable page:

```text
This TexLite Share is temporarily offline.
```

### Active share + active tunnel

Proxy the request through the tunnel.

---

# 17. HTTP Reverse Proxy

Do NOT manually parse and serialize HTTP requests through the tunnel.

Use Go's standard HTTP reverse-proxy implementation:

```go
net/http/httputil.ReverseProxy
```

Provide a custom:

```go
http.Transport
```

whose:

```go
DialContext(...)
```

opens a new smux stream.

Conceptually:

```go
DialContext(...)
    ↓
session.OpenStream()
    ↓
return smux stream as net.Conn
```

The Go HTTP stack then handles:

```text
HTTP/1.1
keep-alive
chunked transfer
streaming
WebSocket upgrade
```

The tunnel protocol should remain unaware of HTTP semantics.

---

# 18. Client Stream Handling

The client maintains one persistent smux session.

Pseudo-flow:

```text
for {
    stream = session.AcceptStream()

    concurrently:
        local = net.Dial("tcp", "127.0.0.1:3000")

        stream ←→ local
}
```

For every accepted stream:

```text
smux stream
      │
      ▼
TCP connection
      │
      ▼
127.0.0.1:3000
```

The server must never tell the client what host or port to connect to.

The client already knows:

```text
local target = 127.0.0.1:3000
```

This is a deliberate security property.

---

# 19. No Arbitrary Local Targets

The wire protocol must NOT contain:

```text
targetHost
targetPort
destination
```

Otherwise a compromised server or malicious request could potentially turn the tunnel client into an internal network proxy.

For TexLite:

```text
target = local TexLite
```

is implicit.

During development a CLI option may exist:

```bash
--local-addr 127.0.0.1:3000
```

but this value comes from the local user's configuration, never from the remote server.

Production TexLite integration should default to loopback addresses only.

---

# 20. Bidirectional Copy

Implement one shared function for stream forwarding.

Conceptually:

```go
func bridge(a, b net.Conn)
```

It must:

```text
copy A → B
copy B → A
terminate when either side fails
close both connections
avoid goroutine leaks
```

Use:

```go
io.Copy
```

rather than buffering entire payloads.

Large PDF transfers must remain streaming.

---

# 21. Backpressure

Never read an entire HTTP body or PDF into memory.

The pipeline must behave like:

```text
Browser
   ↓
TCP backpressure
   ↓
Caddy
   ↓
Go ReverseProxy
   ↓
smux flow control
   ↓
client
   ↓
TexLite
```

Memory usage should be bounded independently of file size.

A 500 MB response must not imply 500 MB of application buffering.

---

# 22. smux Configuration

Use smux protocol version 2.

Do not aggressively tune buffer sizes initially.

Start close to library defaults, but explicitly configure and expose:

```text
keep-alive interval
keep-alive timeout
max receive buffer
max stream buffer
```

as internal configuration parameters.

Recommended initial connection health policy:

```text
keepalive interval ≈ 15 seconds
timeout ≈ 45 seconds
```

Exact values may be adjusted after integration testing.

---

# 23. Tunnel Client Reconnection

The tunnel client must reconnect automatically.

Use exponential backoff with jitter.

Example progression:

```text
1s
2s
4s
8s
16s
30s
30s
...
```

Cap the retry delay.

Retry on:

```text
network failure
WebSocket disconnect
server 5xx
unexpected EOF
temporary DNS error
```

Do NOT indefinitely retry on:

```text
401 invalid token
403 revoked
410 expired
```

Those should terminate the client with a clear exit status.

---

# 24. Client CLI

Phase 1 binary:

```bash
texlite-tunnel-client \
    --server-url http://127.0.0.1:9000 \
    --share-id k83fx2m7pq4z7abc \
    --token <token> \
    --local-addr 127.0.0.1:3000
```

For:

```text
http://...
```

use:

```text
ws://...
```

for the tunnel.

For:

```text
https://...
```

use:

```text
wss://...
```

Do not require the user to construct the WebSocket URL manually.

---

# 25. Create Share API

Implement:

```text
POST /api/v1/shares
```

Example request:

```http
POST /api/v1/shares
Authorization: Bearer <create-api-key>
```

Example response:

```json
{
  "id": "k83fx2m7pq4z7abc",
  "token": "SINGLE_USE_RETURNED_SECRET",
  "expiresAt": "2026-09-19T10:00:00Z",
  "publicUrl": "http://k83fx2m7pq4z7abc.share.local"
}
```

The token is returned only once.

Subsequent GET APIs must never reveal it.

---

# 26. Creation Authentication

Phase 1 is an infrastructure experiment.

Do NOT solve the final anonymous-public-service abuse problem yet.

Initially protect share creation with:

```text
SHARE_CREATE_API_KEY
```

or allow unauthenticated creation only when the server explicitly runs in:

```text
development mode
```

Future public versions may add:

```text
account authentication
rate limiting
CAPTCHA
quota
invite codes
```

Keep those outside Phase 1.

---

# 27. Capacity Limit

Retain the original product constraint:

```text
MAX_ACTIVE_SHARES=50
```

But it is now a logical quota, not a port pool.

Before creating a share:

```text
count ACTIVE non-expired shares
```

If:

```text
>= 50
```

return:

```text
503 Service Unavailable
```

or:

```text
429 Too Many Requests
```

with an explicit capacity error.

There are no ports `10000–10050`.

That entire design is removed.

---

# 28. Revoke API

Implement:

```text
DELETE /api/v1/shares/{shareID}
```

Authentication may be either:

```text
admin create key
```

or the share's own tunnel token.

On revoke:

```text
DB status → REVOKED
        ↓
remove active registry entry
        ↓
close smux session
        ↓
all streams terminate
```

Revocation must take effect immediately.

---

# 29. Expiration

Run a background expiration sweep.

Example:

```text
every 30–60 seconds
```

Find:

```text
ACTIVE shares
where expires_at <= now
```

then:

```text
status = EXPIRED
close active tunnel session
remove from registry
```

Authentication and public routing must also independently check expiration.

Do not depend only on the periodic sweep for correctness.

---

# 30. Server Restart

Persistent state:

```text
SQLite
```

Ephemeral state:

```text
active tunnel connections
smux sessions
HTTP connection pools
```

After server restart:

```text
shares remain ACTIVE in DB
clients automatically reconnect
registry is rebuilt naturally
```

The server does NOT need to reconstruct network sessions from disk.

This is substantially simpler than dynamically generating frp/rathole configuration.

---

# 31. WebSocket Support Through the Public Share

TexLite itself may use WebSockets.

There are therefore two unrelated WebSocket layers:

```text
Layer A:
Browser ↔ TexLite
application WebSocket

Layer B:
Tunnel Client ↔ Share Server
transport WebSocket
```

The tunnel must treat Layer A as ordinary bytes.

Do not special-case TexLite's WebSocket protocol.

The path becomes:

```text
Browser WebSocket
       ↓
Caddy
       ↓
Go ReverseProxy
       ↓
smux stream
       ↓
tunnel client
       ↓
local TexLite WebSocket
```

---

# 32. Caddy Production Configuration

Production DNS:

```dns
share.example.com      A    SERVER_IP
*.share.example.com    A    SERVER_IP
```

Caddy conceptually:

```caddy
share.example.com, *.share.example.com {
    tls {
        dns <provider> {env.DNS_API_TOKEN}
    }

    reverse_proxy 127.0.0.1:9000
}
```

Caddy therefore forwards both:

```text
https://share.example.com/api/v1/...
```

and:

```text
https://abc.share.example.com/...
```

to the same Go server.

The Go server decides whether a request is:

```text
control API
tunnel endpoint
public share traffic
```

Caddy configuration must remain static when shares are created/deleted.

---

# 33. Static DNS Design

Creating:

```text
abc.share.example.com
```

must NOT create a DNS record.

Wildcard DNS already handles it:

```text
*.share.example.com → server
```

Therefore:

```text
new share
```

requires no:

```text
DNS update
Caddy update
TLS certificate update
server restart
```

Only SQLite and in-memory state change.

---

# 34. Local Development Without Domain

No real domain is required.

Start server:

```bash
go run ./cmd/texlite-share-server \
    --listen 127.0.0.1:9000 \
    --base-domain share.local \
    --db /tmp/texlite-share.db \
    --create-api-key dev-secret
```

Start a temporary local web server if TexLite is unavailable:

```bash
python3 -m http.server 3000 --bind 127.0.0.1
```

Create share:

```bash
curl \
  -X POST \
  -H 'Authorization: Bearer dev-secret' \
  http://127.0.0.1:9000/api/v1/shares
```

Suppose response contains:

```text
id    = abc123
token = TOKEN
```

Start tunnel client:

```bash
go run ./cmd/texlite-tunnel-client \
  --server-url http://127.0.0.1:9000 \
  --share-id abc123 \
  --token TOKEN \
  --local-addr 127.0.0.1:3000
```

Then test:

```bash
curl \
  -H 'Host: abc123.share.local' \
  http://127.0.0.1:9000/
```

This requires:

```text
no DNS
no Caddy
no HTTPS
no public server
```

---

# 35. Browser Local Testing

For browser testing, add a few explicit entries to:

```text
/etc/hosts
```

Example:

```text
127.0.0.1 abc123.share.local
127.0.0.1 xyz789.share.local
```

Then visit:

```text
http://abc123.share.local:9000
```

Do not try to implement wildcard `/etc/hosts`.

For automated tests, simply override the HTTP Host header.

---

# 36. Request Header Handling

Before forwarding a public request:

sanitize proxy-related headers.

Do not blindly trust user-provided:

```text
X-Forwarded-For
X-Forwarded-Host
X-Forwarded-Proto
Forwarded
```

Set authoritative values based on the actual incoming request.

Preserve the public Host when useful for TexLite:

```text
abc.share.example.com
```

and send appropriate:

```text
X-Forwarded-Host
X-Forwarded-Proto
```

This may matter for:

```text
absolute URLs
WebSocket origin handling
cookie behavior
redirects
```

---

# 37. Security Invariants

The following must always hold.

### Invariant 1

The remote server cannot instruct the client to connect to arbitrary internal hosts.

Only the locally configured TexLite address is reachable.

### Invariant 2

A tunnel token authenticates exactly one share.

### Invariant 3

There is no server-wide secret distributed to all clients.

### Invariant 4

Token values never appear in URLs or logs.

### Invariant 5

Expired/revoked shares cannot establish new tunnels.

### Invariant 6

Expiration/revocation closes existing tunnels.

### Invariant 7

An arbitrary Host header cannot select arbitrary network destinations.

### Invariant 8

Browser data is streamed rather than accumulated in memory.

### Invariant 9

The public Go server is not an open HTTP proxy.

### Invariant 10

Only ACTIVE shares with an authenticated active tunnel can be proxied.

---

# 38. Local Address Validation

By default accept only:

```text
127.0.0.1:<port>
localhost:<port>
[::1]:<port>
```

for the client target.

The production TexLite integration should normally use:

```text
127.0.0.1:3000
```

Do not silently allow:

```text
0.0.0.0
192.168.x.x
10.x.x.x
public IP addresses
Unix-controlled arbitrary destinations
```

unless explicitly added as a future feature.

This reduces the chance that TexLite Tunnel becomes an unintended SSRF/internal-network bridge.

---

# 39. Resource Limits

Implement server-side limits from the beginning.

Configuration:

```text
MAX_ACTIVE_SHARES=50
MAX_STREAMS_PER_SHARE=64
MAX_TOTAL_STREAMS=1024
```

Exact values are configurable.

Reject excess streams rather than allowing unbounded goroutine/memory growth.

Also configure:

```text
HTTP header timeout
idle timeout
read header size limit
tunnel handshake timeout
local dial timeout
```

Do not impose a small whole-request timeout that would break:

```text
large PDFs
long WebSockets
long-running collaborative sessions
```

---

# 40. Logging

Use structured logs.

Recommended fields:

```text
event
share_id
remote_ip
session_generation
stream_count
duration
error
```

Events:

```text
share.created
share.expired
share.revoked

tunnel.connected
tunnel.replaced
tunnel.disconnected
tunnel.auth_failed

stream.opened
stream.closed
stream.local_dial_failed

proxy.request_failed
```

Never log:

```text
tunnel token
Authorization header
full sensitive request bodies
```

---

# 41. Metrics for Phase 1

At minimum track internally:

```text
active shares
connected tunnels
active streams
total tunnel connections
failed authentications
local dial failures
bytes uploaded
bytes downloaded
```

A Prometheus endpoint is optional for Phase 1.

Do not delay the basic tunnel implementation to build an elaborate monitoring system.

---

# 42. Graceful Shutdown

On SIGINT/SIGTERM:

```text
1. Stop accepting new HTTP requests.

2. Stop accepting new tunnels.

3. Close active tunnel sessions.

4. Allow short grace period for handlers.

5. Close SQLite.

6. Exit.
```

The tunnel client should interpret server shutdown as transient and reconnect automatically.

---

# 43. Protocol Versioning

Define:

```text
texlite-tunnel-v1
```

using WebSocket subprotocol negotiation.

Do not silently accept incompatible clients.

Future protocol versions may use:

```text
texlite-tunnel-v2
```

The first version should remain intentionally small.

---

# 44. Error Model

Define typed/shared errors in:

```text
internal/protocol
```

Examples:

```text
ErrInvalidShare
ErrInvalidToken
ErrExpired
ErrRevoked
ErrTunnelOffline
ErrCapacityExceeded
ErrProtocolMismatch
ErrTooManyStreams
```

Map these to appropriate HTTP status codes.

Do not expose internal stack traces to public clients.

---

# 45. Unit Tests

Implement unit tests for:

```text
share ID generation
token generation
token hashing/comparison
host parsing
hostname validation
expiration
capacity limit
registry replacement
registry generation safety
revoke logic
local target validation
```

Run:

```bash
go test ./...
go test -race ./...
```

The race detector must pass.

---

# 46. Integration Test: Basic HTTP

Automated integration test:

```text
start local httptest server
        ↓
start share server
        ↓
create share
        ↓
start tunnel client
        ↓
send HTTP request with share Host
        ↓
receive expected local response
```

This test must not require:

```text
root
DNS
Caddy
Internet access
```

---

# 47. Integration Test: Concurrent Requests

Open multiple browser-side requests simultaneously.

Example:

```text
32 concurrent requests
```

Verify:

```text
multiple smux streams
correct responses
no deadlock
no data mixing
```

---

# 48. Integration Test: WebSocket

Run a local WebSocket echo service behind the tunnel.

Verify:

```text
browser test client
      ↓
share server
      ↓
smux
      ↓
tunnel client
      ↓
local WebSocket service
```

Bidirectional messages must work continuously.

This is mandatory before integration with real TexLite.

---

# 49. Integration Test: Streaming

Generate a large response without loading it entirely into memory.

Verify:

```text
correct response bytes
stable memory usage
backpressure
no artificial body-size limit
```

A 32–100 MB test object is sufficient initially.

---

# 50. Integration Test: Reconnection

Test:

```text
active tunnel
    ↓
force-close WebSocket
    ↓
client reconnects
    ↓
new session replaces old session
    ↓
public URL works again
```

No server restart should be required.

---

# 51. Integration Test: Expiration

Create a share with very short TTL.

Verify:

```text
working
  ↓
TTL reached
  ↓
tunnel closed
  ↓
public request returns 410
  ↓
client receives terminal expiry response on reconnect
```

---

# 52. Integration Test: Revocation

While the tunnel is active:

```text
DELETE share
```

Verify immediately:

```text
existing smux session closes
new tunnel login fails
public requests fail
```

---

# 53. Integration Test: Server Restart

Sequence:

```text
create share
connect client
restart server
client notices disconnect
client retries
server starts
client reconnects automatically
same share URL works again
```

Database state must survive restart.

---

# 54. Client Build Targets

Initially build client binaries for:

```text
linux/amd64
linux/arm64
darwin/amd64
darwin/arm64
windows/amd64
```

Server initially needs:

```text
linux/amd64
linux/arm64
```

Keep the client CGO-free if practical.

This makes later bundling with TexLite considerably easier.

---

# 55. Future TexLite Integration

Do NOT integrate tightly with TexLite until standalone tunnel tests pass.

Eventually:

```text
TexLite
   │
   │ create share API
   ▼
TexLite Share Server
   │
   ├── share_id
   └── token
   │
   ▼
TexLite launches bundled tunnel binary
```

The user sees only:

```bash
texlite share
```

not:

```text
WebSocket
smux
token
tunnel client
Caddy
```

Those are implementation details.

---

# 56. Client Process Integration

A future TexLite launcher may invoke:

```bash
texlite-tunnel-client \
  --server-url https://share.example.com \
  --share-id ... \
  --token ... \
  --local-addr 127.0.0.1:3000
```

Prefer passing the token through:

```text
stdin
environment variable
protected temporary IPC
```

rather than command-line arguments in the final production integration, because command-line arguments may be visible in process listings.

CLI token arguments are acceptable for Phase 1 development only.

---

# 57. Phase 1 Milestones

## Milestone 1 — Repository Skeleton

Implement:

```text
two binaries
shared config
logging
SQLite migration
basic HTTP server
```

Success:

```bash
go test ./...
```

passes.

---

## Milestone 2 — Share Lifecycle

Implement:

```text
POST /api/v1/shares
DELETE /api/v1/shares/:id
expiration
50-share quota
token hashing
```

No tunneling yet.

---

## Milestone 3 — WebSocket Tunnel Authentication

Implement:

```text
client connects
token verified
WebSocket upgraded
active registry updated
reconnect/replacement
```

No HTTP proxy yet.

---

## Milestone 4 — smux

Wrap WebSocket as:

```text
net.Conn
```

then establish:

```text
smux server
smux client
```

Verify a manually opened stream can carry arbitrary bytes.

---

## Milestone 5 — Local Forwarding

For every accepted smux stream:

```text
dial 127.0.0.1:3000
bridge bytes
```

Verify with simple local HTTP server.

---

## Milestone 6 — Public Host Reverse Proxy

Implement:

```text
Host → share ID
share ID → active session
ReverseProxy → smux stream
```

Verify:

```bash
curl -H 'Host: abc.share.local' http://127.0.0.1:9000/
```

---

## Milestone 7 — WebSocket and Streaming

Validate:

```text
TexLite-style WebSocket
large PDF/file response
concurrent requests
```

---

## Milestone 8 — Failure Handling

Implement/test:

```text
network disconnect
reconnection
expiration
revocation
server restart
client local service unavailable
```

---

## Milestone 9 — Caddy + Real Domain

Only after local integration passes:

```text
wildcard DNS
wildcard HTTPS
Caddy
remote client
```

This is the first stage that requires a real domain.

---

# 58. Definition of Done for Phase 1

Phase 1 is complete when the following scenario works:

```text
1. TexLite runs at 127.0.0.1:3000.

2. texlite-share-server runs at 127.0.0.1:9000.

3. POST /api/v1/shares returns:
   share ID
   unique token
   expiry.

4. texlite-tunnel-client authenticates.

5. One persistent WebSocket is established.

6. smux runs over that connection.

7. Browser requests for:
   <share-id>.share.local

   are mapped to the active session.

8. Each backend connection uses an smux stream.

9. Client forwards streams only to:
   127.0.0.1:3000.

10. Ordinary HTTP works.

11. WebSocket works.

12. Streaming/large files work.

13. Multiple concurrent requests work.

14. Client reconnects after network interruption.

15. Expired shares stop immediately.

16. Revoked shares stop immediately.

17. Server restart does not lose share metadata.

18. Client reconnects after server restart.

19. go test -race ./... passes.

20. No rathole/frp dependency exists.
```

---

# 59. Explicit Instructions to the Code Agent

Prioritize correctness and simplicity over features.

Do NOT generalize the tunnel.

Do NOT create abstractions for hypothetical TCP/UDP/custom-domain support.

Do NOT introduce Kubernetes, Redis, message queues, or distributed state.

Do NOT implement QUIC in Phase 1.

Do NOT build a custom multiplexing protocol.

Do NOT buffer complete request/response bodies.

Do NOT put authentication tokens into URLs.

Do NOT allow the server to select arbitrary client-side destinations.

Do NOT begin public-domain/Caddy work until the full local integration test passes.

Implement one milestone at a time and keep tests passing after every milestone.

When a design choice is ambiguous, choose the option that keeps the system specialized for:

```text
one temporary TexLite HTTP service
→ one temporary public TexLite Share URL
```

rather than the option that makes the tunnel more general-purpose.