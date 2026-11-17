# TexLite Share

A lightweight, secure reverse-tunnel service designed for [TexLite](https://github.com/SWUFE-DB-Group/TexLite).

TexLite Share enables you to instantly expose your locally deployed TexLite collaborative editor (e.g. running at `127.0.0.1:3000`) to co-authors and reviewers via a public HTTPS link (e.g. `https://ox32xkn3jnsvl7h6.share.zhongpu.info`). No public IP, router port-forwarding, firewall configuration, or third-party tools like frp or ngrok required.

---

## 🚀 1. Client Installation & Quick Start

### Step 1: Install the Client

#### Linux & macOS (One-Line Installer)
Install the `texlite-tunnel-client` binary directly to `~/.local/bin` (no root privileges required):

```bash
curl -fsSL https://raw.githubusercontent.com/ChenZhongPu/TexLite-Share/main/install-client.sh | bash
```

> **Tip (Custom Install Directory)**: To install elsewhere, set `BIN_DIR`:
> ```bash
> BIN_DIR=/usr/local/bin curl -fsSL https://raw.githubusercontent.com/ChenZhongPu/TexLite-Share/main/install-client.sh | sudo bash
> ```

#### Windows
Download the pre-compiled `texlite-tunnel-client-windows-amd64.zip` from [GitHub Releases](https://github.com/ChenZhongPu/TexLite-Share/releases).

---

### Step 2: Start Sharing

> **Public Free Server**: We provide and maintain a free, ready-to-use public server at **`https://share.zhongpu.info`**. You do not need to register an account or obtain an API key—it works straight out of the box!  
> *(Notice: `share.zhongpu.info` is currently in Public Beta, supporting up to 50 active shares and 1,000 concurrent streams).*

1. **Ensure your local TexLite instance is running** (default: `http://127.0.0.1:3000`).
2. **Launch the tunnel client** pointing to `share.zhongpu.info`:

```bash
cd ~/.local/bin && ./texlite-tunnel-client --server-url https://share.zhongpu.info
```

Or set the environment variable in your shell profile (`~/.bashrc` or `~/.zshrc`) so you can simply type `texlite-tunnel-client`:

```bash
export TEXLITE_SERVER_URL=https://share.zhongpu.info
texlite-tunnel-client
```

Terminal output:
```text
time=2026-11-17T10:00:00 level=INFO msg="starting texlite-tunnel-client"
==> Probing local service at http://127.0.0.1:3000... OK (TexLite detected, PID: 3352906)
==> Requesting share session from https://share.zhongpu.info...
==> Public Share Created Successfully!

===================================================================
 ✨ TexLite Share is Live! (Automatic Temporary Mode)
 🔗 Public URL:   https://ox32xkn3jnsvl7h6.share.zhongpu.info
 🆔 Share ID:     ox32xkn3jnsvl7h6
 🎯 Local Target: http://127.0.0.1:3000 (TexLite verified)
 ⏳ Expires At:   2026-11-17 11:00:00
===================================================================
Press Ctrl+C to stop sharing and revoke the link.
```

- **Share the URL**: Copy the generated `https://*.share.zhongpu.info` link and send it to collaborators.
- **Disconnect & Revoke**: Press <kbd>Ctrl + C</kbd> anytime. The client automatically sends a revocation request to the server, instantly terminating the tunnel and releasing resources.

---

### Useful Client Options

| Flag | Default | Description |
| :--- | :--- | :--- |
| `--server-url <url>` | `http://127.0.0.1:9000` | Share server address (use `https://share.zhongpu.info` or env `TEXLITE_SERVER_URL`) |
| `--local-addr <addr>` | `127.0.0.1:3000` | Target local address. Automatically probes and verifies genuine TexLite instances |
| `--share-id <id>` | *(Auto)* | Existing share ID to reconnect to (used in conjunction with `--token`) |
| `--token <secret>` | *(Auto)* | Tunnel secret token for reconnecting to a dedicated share session |
| `--auto-revoke` | `true` | Automatically revokes the share on the server upon exit (<kbd>Ctrl+C</kbd>) |
| `--version` / `-v` | - | Print client version, Git commit SHA, and build timestamp |

#### Examples:

- **Custom Local Port** (e.g. TexLite running on port 8080 or Docker container):
  ```bash
  texlite-tunnel-client --server-url https://share.zhongpu.info --local-addr 127.0.0.1:8080
  ```

- **Manual Reconnection Mode** (reconnecting to a pre-allocated fixed subdomain):
  ```bash
  texlite-tunnel-client \
    --server-url https://share.zhongpu.info \
    --share-id my-paper-2026 \
    --token <YOUR_TOKEN>
  ```

---

## 🛠️ 2. Self-Hosting: Deploy Your Own Server

If you prefer to host a private tunnel service for your lab, institution, or organization, you can deploy `texlite-share-server` on your own VPS.

### Prerequisites

1. A Linux server (x86_64 or arm64) with a public IP address.
2. A domain name with wildcard DNS pointing to your server IP:
   ```dns
   share.example.com      A    <YOUR_SERVER_IP>
   *.share.example.com    A    <YOUR_SERVER_IP>
   ```

---

### Step 1: Download Server Binary

Download the latest `texlite-share-server` binary from [GitHub Releases](https://github.com/ChenZhongPu/TexLite-Share/releases), or build it from source:

```bash
CGO_ENABLED=0 go build -o texlite-share-server ./cmd/texlite-share-server
```

Place the binary in `/opt/texlite-share/texlite-share-server` and make it executable:
```bash
sudo mkdir -p /opt/texlite-share /var/lib/texlite-share
sudo install -m 755 texlite-share-server /opt/texlite-share/texlite-share-server
```

---

### Step 2: Run the Server

Start `texlite-share-server` with your domain and management configuration:

```bash
/opt/texlite-share/texlite-share-server \
  --base-domain share.example.com \
  --listen 127.0.0.1:9000 \
  --admin-listen 127.0.0.1:9001 \
  --admin-api-key your-admin-secret \
  --db /var/lib/texlite-share/texlite-share.db
```

#### Core Server Flags:

| Flag | Default | Description |
| :--- | :--- | :--- |
| `--base-domain` | `share.local` | Base domain for public subdomains (e.g. `share.example.com`) |
| `--listen` | `127.0.0.1:9000` | Port for tunnel WebSocket connections and visitor traffic (behind reverse proxy) |
| `--admin-listen` | `127.0.0.1:9001` | Dedicated localhost port for the Admin Web Dashboard and management APIs |
| `--admin-api-key` | `""` | Secret key protecting the Admin Dashboard and management APIs |
| `--create-api-key` | `""` | Optional key to restrict share creation. Leave empty for an open community server |
| `--db` | `texlite-share.db`| Path to the SQLite database file |
| `--default-ttl` | `1h` | Default share expiration time (e.g. `1h`, `24h`, `168h`) |
| `--max-shares` | `50` | Maximum concurrent active shares allowed |
| `--max-total-streams` | `1000` | Maximum concurrent multiplexed streams across all shares |

---

### Step 3: Configure Reverse Proxy (Caddy)

[Caddy](https://caddyserver.com/) automatically obtains and renews wildcard TLS certificates via DNS challenge (e.g. Cloudflare) and handles WebSocket multiplexing seamlessly.

Example `/etc/caddy/Caddyfile`:

```caddy
*.share.example.com, share.example.com {
    tls {
        dns cloudflare {env.CF_API_TOKEN}
    }

    encode zstd gzip

    # 1. Forward root domain API and tunnel WebSockets to the Go server
    @rootApi {
        host share.example.com
        path /api/* /healthz
    }
    handle @rootApi {
        reverse_proxy 127.0.0.1:9000
    }

    # 2. Serve static landing page for the root domain (optional)
    @rootStatic host share.example.com
    handle @rootStatic {
        root * /var/www/texlite-share
        file_server
    }

    # 3. Reverse-proxy wildcard subdomains (*.share.example.com) to the tunnel server
    handle {
        @assets path /assets/*
        header @assets Cache-Control "public, max-age=31536000, immutable"

        reverse_proxy 127.0.0.1:9000
    }
}
```

Reload Caddy:
```bash
sudo systemctl reload caddy
```

---

### Step 4: Systemd Service (Production Daemon)

Create `/etc/systemd/system/texlite-share.service`:

```ini
[Unit]
Description=TexLite Share Reverse Tunnel Server
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/texlite-share
ExecStart=/opt/texlite-share/texlite-share-server \
  --base-domain share.example.com \
  --listen 127.0.0.1:9000 \
  --admin-listen 127.0.0.1:9001 \
  --admin-api-key your-admin-secret \
  --db /var/lib/texlite-share/texlite-share.db
Restart=always
RestartSec=3
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

Enable and start the service:
```bash
sudo systemctl daemon-reload
sudo systemctl enable --now texlite-share
```

---

### Step 5: Web Admin Dashboard

The server provides a built-in visual web dashboard on the `--admin-listen` port (`127.0.0.1:9001`).

To access it securely from your local machine, use an SSH tunnel:
```bash
ssh -L 9001:127.0.0.1:9001 user@your-server-ip
```

Then open `http://localhost:9001/?key=your-admin-secret` in your browser. From the dashboard, you can:
- View live server telemetry (active shares, connected tunnels, active multiplexed streams).
- Inspect and monitor connected clients, client IPs, and geolocation.
- **Create shares with custom Share IDs** (or generate random ones with the 🎲 Random button) and custom TTLs.
- Extend expiration times or revoke active shares with one click.
- Inspect running server configurations and defaults.

---

## 📖 3. Development & Architecture

For in-depth technical details, including:
- End-to-end WebSocket + smux stream multiplexing architecture
- Repository source code layout
- CGO-free compilation and cross-platform build commands
- Local development walkthrough (without needing real domains or DNS)
- Running unit and integration test suites

Please refer to the **[Developer Guide (DEV.md)](DEV.md)**.

---

## 📄 License

This project is licensed under the [AGPL-3.0 License](https://www.gnu.org/licenses/agpl-3.0.html).
