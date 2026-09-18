package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"texlite-share/internal/auth"
	"texlite-share/internal/config"
	"texlite-share/internal/database"
	"texlite-share/internal/registry"
)

// AdminHandler handles management APIs and serves the internal Web Dashboard.
type AdminHandler struct {
	cfg      *config.ServerConfig
	db       *database.DB
	registry *registry.Registry
	api      *ServerAPI
}

// NewAdminHandler initializes a new AdminHandler.
func NewAdminHandler(cfg *config.ServerConfig, db *database.DB, reg *registry.Registry) *AdminHandler {
	return &AdminHandler{
		cfg:      cfg,
		db:       db,
		registry: reg,
		api:      NewServerAPI(cfg, db, reg),
	}
}

// ShareView is the representation of a share for admin dashboard inspection.
type ShareView struct {
	ID            string     `json:"id"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"createdAt"`
	ExpiresAt     time.Time  `json:"expiresAt"`
	RevokedAt     *time.Time `json:"revokedAt,omitempty"`
	PublicURL     string     `json:"publicUrl"`
	Online        bool       `json:"online"`
	ActiveStreams int32      `json:"activeStreams"`
}

// StatsView contains high-level metrics for the server.
type StatsView struct {
	ActiveShares       int    `json:"activeShares"`
	MaxActiveShares    int    `json:"maxActiveShares"`
	OnlineTunnels      int    `json:"onlineTunnels"`
	TotalStreams       int32  `json:"totalStreams"`
	MaxTotalStreams    int    `json:"maxTotalStreams"`
	MaxStreamsPerShare int    `json:"maxStreamsPerShare"`
	BaseDomain         string `json:"baseDomain"`
}

func (h *AdminHandler) checkAuth(r *http.Request) bool {
	requiredKey := h.cfg.AdminAPIKey
	if requiredKey == "" {
		requiredKey = h.cfg.CreateAPIKey
	}
	if requiredKey == "" {
		return true // No API key required
	}

	// 1. Check Bearer header
	if token, err := auth.ExtractBearerToken(r); err == nil && token == requiredKey {
		return true
	}

	// 2. Check query parameter ?key=... (convenient for browser SSH port forwarding)
	if key := r.URL.Query().Get("key"); key == requiredKey {
		return true
	}

	// 3. Check Cookie (if set by browser dashboard)
	if cookie, err := r.Cookie("texlite_admin_key"); err == nil && cookie.Value == requiredKey {
		return true
	}

	return false
}

// ServeHTTP handles requests arriving at the dedicated admin port.
func (h *AdminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// 1. Always serve the dashboard HTML on root/admin routes
	if path == "/" || path == "/admin" {
		if key := r.URL.Query().Get("key"); key != "" && key == h.cfg.CreateAPIKey {
			http.SetCookie(w, &http.Cookie{
				Name:     "texlite_admin_key",
				Value:    key,
				Path:     "/",
				HttpOnly: false,
				SameSite: http.SameSiteLaxMode,
			})
		}
		h.handleDashboard(w, r)
		return
	}

	// 2. Protect all API endpoints with admin authentication
	if !h.checkAuth(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="TexLite Share Admin"`)
		http.Error(w, "Unauthorized: invalid or missing admin API key", http.StatusUnauthorized)
		return
	}

	switch {
	case path == "/api/v1/stats" && r.Method == http.MethodGet:
		h.handleStats(w, r)
	case path == "/api/v1/shares" && r.Method == http.MethodGet:
		h.handleListShares(w, r)
	case path == "/api/v1/shares" && r.Method == http.MethodPost:
		h.api.HandleCreateShare(w, r)
	case strings.HasPrefix(path, "/api/v1/shares/") && r.Method == http.MethodDelete:
		shareID := strings.TrimPrefix(path, "/api/v1/shares/")
		h.api.HandleRevokeShare(w, r, shareID)
	case path == "/healthz" || path == "/api/v1/health":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.NotFound(w, r)
	}
}

func (h *AdminHandler) handleStats(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	activeCount, err := h.db.CountActiveShares(r.Context(), now)
	if err != nil {
		slog.Error("failed to count active shares", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	stats := StatsView{
		ActiveShares:       activeCount,
		MaxActiveShares:    h.cfg.MaxActiveShares,
		OnlineTunnels:      h.registry.Count(),
		TotalStreams:       h.registry.TotalStreams(),
		MaxTotalStreams:    h.cfg.MaxTotalStreams,
		MaxStreamsPerShare: h.cfg.MaxStreamsPerShare,
		BaseDomain:         h.cfg.BaseDomain,
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(stats)
}

func (h *AdminHandler) handleListShares(w http.ResponseWriter, r *http.Request) {
	shares, err := h.db.ListShares(r.Context(), 50)
	if err != nil {
		slog.Error("failed to list shares", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	views := make([]ShareView, 0, len(shares))
	for _, s := range shares {
		sess, online := h.registry.Get(s.ID)
		var activeStreams int32
		if online && sess != nil {
			activeStreams = sess.ActiveStreams.Load()
		}

		publicURL := fmt.Sprintf("http://%s.%s", s.ID, h.cfg.BaseDomain)
		views = append(views, ShareView{
			ID:            s.ID,
			Status:        s.Status,
			CreatedAt:     s.CreatedAt,
			ExpiresAt:     s.ExpiresAt,
			RevokedAt:     s.RevokedAt,
			PublicURL:     publicURL,
			Online:        online,
			ActiveStreams: activeStreams,
		})
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(views)
}

func (h *AdminHandler) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(dashboardHTML))
}

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>TexLite Share Admin Dashboard</title>
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <style>
    :root {
      --bg: #0f172a;
      --card: #1e293b;
      --text: #f8fafc;
      --text-muted: #94a3b8;
      --border: #334155;
      --primary: #38bdf8;
      --success: #4ade80;
      --warning: #fbbf24;
      --danger: #f87171;
    }
    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
      background: var(--bg);
      color: var(--text);
      margin: 0;
      padding: 24px;
    }
    .container { max-width: 1100px; margin: 0 auto; }
    header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 24px; border-bottom: 1px solid var(--border); padding-bottom: 16px; }
    h1 { font-size: 24px; margin: 0; display: flex; align-items: center; gap: 8px; }
    .badge-local { background: #0369a1; color: #e0f2fe; padding: 2px 8px; border-radius: 4px; font-size: 12px; }
    .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 16px; margin-bottom: 24px; }
    .card { background: var(--card); border: 1px solid var(--border); border-radius: 8px; padding: 16px; }
    .card h3 { margin: 0 0 8px; font-size: 13px; color: var(--text-muted); text-transform: uppercase; }
    .card .value { font-size: 28px; font-weight: bold; color: var(--primary); }
    .actions-panel { background: var(--card); border: 1px solid var(--border); border-radius: 8px; padding: 16px; margin-bottom: 24px; display: flex; gap: 12px; align-items: center; flex-wrap: wrap; }
    button { background: var(--primary); color: #0f172a; border: none; padding: 8px 16px; border-radius: 6px; font-weight: 600; cursor: pointer; }
    button:hover { opacity: 0.9; }
    button.danger { background: var(--danger); color: #fff; padding: 4px 8px; font-size: 12px; }
    select { background: #0f172a; color: var(--text); border: 1px solid var(--border); padding: 8px; border-radius: 6px; }
    table { width: 100%; border-collapse: collapse; margin-top: 8px; }
    th, td { text-align: left; padding: 12px; border-bottom: 1px solid var(--border); font-size: 14px; }
    th { color: var(--text-muted); font-size: 12px; text-transform: uppercase; }
    .status-active { color: var(--success); }
    .status-expired { color: var(--text-muted); }
    .status-revoked { color: var(--danger); }
    .online-indicator { display: inline-block; width: 8px; height: 8px; border-radius: 50%; background: var(--success); margin-right: 6px; }
    .offline-indicator { display: inline-block; width: 8px; height: 8px; border-radius: 50%; background: var(--text-muted); margin-right: 6px; }
    .mono { font-family: monospace; }
    #modal { display: none; position: fixed; inset: 0; background: rgba(0,0,0,0.7); justify-content: center; align-items: center; }
    #modal-box { background: var(--card); border: 1px solid var(--border); padding: 24px; border-radius: 8px; max-width: 600px; width: 90%; }
    pre { background: #0f172a; padding: 12px; border-radius: 6px; overflow-x: auto; color: #38bdf8; font-size: 13px; }
  </style>
</head>
<body>
  <div class="container">
    <header>
      <h1>TexLite Share <span class="badge-local">Admin Port (Localhost)</span></h1>
      <button onclick="refreshData()">↻ Refresh</button>
    </header>

    <div class="grid">
      <div class="card">
        <h3>Active Shares / Quota</h3>
        <div class="value" id="quota-val">- / -</div>
      </div>
      <div class="card">
        <h3>Connected Tunnels</h3>
        <div class="value" id="tunnels-val">-</div>
      </div>
      <div class="card">
        <h3>Active Streams</h3>
        <div class="value" id="streams-val">-</div>
      </div>
      <div class="card">
        <h3>Base Domain</h3>
        <div class="value" style="font-size: 20px;" id="domain-val">-</div>
      </div>
    </div>

    <div class="actions-panel">
      <label>TTL Duration:</label>
      <select id="ttl-select">
        <option value="1h">1 Hour</option>
        <option value="2h">2 Hours</option>
        <option value="24h" selected>24 Hours (Default)</option>
        <option value="72h">3 Days</option>
        <option value="168h">7 Days</option>
      </select>
      <button onclick="createShare()">+ Create New Share</button>
    </div>

    <div class="card">
      <h3 style="margin-bottom: 12px;">Active &amp; Recent Shares</h3>
      <table>
        <thead>
          <tr>
            <th>Share ID</th>
            <th>Tunnel State</th>
            <th>Status</th>
            <th>Streams</th>
            <th>Expires At</th>
            <th>Public URL</th>
            <th>Action</th>
          </tr>
        </thead>
        <tbody id="shares-tbody">
          <tr><td colspan="7" style="text-align:center; color: var(--text-muted);">Loading shares...</td></tr>
        </tbody>
      </table>
    </div>
  </div>

  <div id="modal">
    <div id="modal-box">
      <h2 style="margin-top:0; color:var(--success);">Share Created!</h2>
      <p>Save this token now — it is returned only once.</p>
      <div><strong>Share ID:</strong> <span id="m-id" class="mono"></span></div>
      <div><strong>Token:</strong> <span id="m-token" class="mono"></span></div>
      <div><strong>Public URL:</strong> <a id="m-url" target="_blank" style="color:var(--primary);"></a></div>
      <p style="margin-top:16px;"><strong>Run Client:</strong></p>
      <pre id="m-cmd"></pre>
      <button onclick="document.getElementById('modal').style.display='none'">Done</button>
    </div>
  </div>

  <script>
    function getAdminKey() {
      const urlKey = new URLSearchParams(window.location.search).get('key');
      if (urlKey) {
        localStorage.setItem('texlite_admin_key', urlKey);
        return urlKey;
      }
      return localStorage.getItem('texlite_admin_key') || '';
    }

    async function apiFetch(url, options) {
      options = options || {};
      options.headers = options.headers || {};
      const key = getAdminKey();
      if (key && !options.headers['Authorization']) {
        options.headers['Authorization'] = 'Bearer ' + key;
      }
      const res = await fetch(url, options);
      if (res.status === 401) {
        const inputKey = prompt('Please enter your admin API key:');
        if (inputKey) {
          localStorage.setItem('texlite_admin_key', inputKey);
          document.cookie = 'texlite_admin_key=' + inputKey + '; path=/; SameSite=Lax';
          options.headers['Authorization'] = 'Bearer ' + inputKey;
          return await fetch(url, options);
        }
      }
      return res;
    }

    async function refreshData() {
      try {
        const statsRes = await apiFetch('/api/v1/stats');
        if (!statsRes.ok) return;
        const stats = await statsRes.json();
        document.getElementById('quota-val').textContent = stats.activeShares + ' / ' + stats.maxActiveShares;
        document.getElementById('tunnels-val').textContent = stats.onlineTunnels;
        document.getElementById('streams-val').textContent = stats.totalStreams;
        document.getElementById('domain-val').textContent = stats.baseDomain;

        const sharesRes = await apiFetch('/api/v1/shares');
        if (!sharesRes.ok) return;
        const shares = await sharesRes.json();
        const tbody = document.getElementById('shares-tbody');
        tbody.innerHTML = '';

        if (!shares || shares.length === 0) {
          tbody.innerHTML = '<tr><td colspan="7" style="text-align:center; color: var(--text-muted);">No shares found.</td></tr>';
          return;
        }

        shares.forEach(s => {
          const tr = document.createElement('tr');
          const isOnline = s.online;
          const statusClass = 'status-' + s.status.toLowerCase();
          const expires = new Date(s.expiresAt).toLocaleString();

          let actionBtn = '-';
          if (s.status === 'ACTIVE') {
            actionBtn = '<button class="danger" onclick="revokeShare(\'' + s.id + '\')">Revoke</button>';
          }
          let stateTag = isOnline ? '<span class="online-indicator"></span>Online' : '<span class="offline-indicator"></span>Offline';
          tr.innerHTML = '<td class="mono"><strong>' + s.id + '</strong></td>' +
            '<td>' + stateTag + '</td>' +
            '<td><span class="' + statusClass + '">' + s.status + '</span></td>' +
            '<td>' + (s.activeStreams || 0) + '</td>' +
            '<td>' + expires + '</td>' +
            '<td><a href="' + s.publicUrl + '" target="_blank" style="color:var(--primary);">' + s.publicUrl + '</a></td>' +
            '<td>' + actionBtn + '</td>';
          tbody.appendChild(tr);
        });
      } catch (e) {
        console.error('Failed to refresh data', e);
      }
    }

    async function createShare() {
      const ttl = document.getElementById('ttl-select').value;
      try {
        const res = await apiFetch('/api/v1/shares', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ ttl: ttl })
        });
        if (!res.ok) {
          const txt = await res.text();
          alert('Error: ' + txt);
          return;
        }
        const data = await res.json();
        document.getElementById('m-id').textContent = data.id;
        document.getElementById('m-token').textContent = data.token;
        document.getElementById('m-url').textContent = data.publicUrl;
        document.getElementById('m-url').href = data.publicUrl;
        document.getElementById('m-cmd').textContent = 
          'go run ./cmd/texlite-tunnel-client \\\n  --server-url ' + window.location.origin.replace(/:\d+$/, ':9000') + ' \\\n  --share-id ' + data.id + ' \\\n  --token ' + data.token + ' \\\n  --local-addr 127.0.0.1:3000';
        document.getElementById('modal').style.display = 'flex';
        refreshData();
      } catch (e) {
        alert('Failed to create share: ' + e);
      }
    }

    async function revokeShare(id) {
      if (!confirm('Are you sure you want to revoke share ' + id + '? This will immediately terminate the tunnel.')) return;
      try {
        const res = await apiFetch('/api/v1/shares/' + id, { method: 'DELETE' });
        if (!res.ok) alert('Failed to revoke: ' + (await res.text()));
        refreshData();
      } catch (e) {
        alert('Revoke error: ' + e);
      }
    }

    refreshData();
    setInterval(refreshData, 5000);
  </script>

</body>
</html>`
