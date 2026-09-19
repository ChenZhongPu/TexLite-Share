package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"texlite-share/internal/auth"
	"texlite-share/internal/config"
	"texlite-share/internal/database"
	"texlite-share/internal/protocol"
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
	ID            string    `json:"id"`
	ClientIP      string    `json:"clientIp"`
	Country       string    `json:"country"`
	CreatedAt     time.Time `json:"createdAt"`
	ExpiresAt     time.Time `json:"expiresAt"`
	PublicURL     string    `json:"publicUrl"`
	Online        bool      `json:"online"`
	ActiveStreams int32     `json:"activeStreams"`
}

// StatsView contains high-level metrics for the server.
type StatsView struct {
	ActiveShares       int            `json:"activeShares"`
	MaxActiveShares    int            `json:"maxActiveShares"`
	OnlineTunnels      int            `json:"onlineTunnels"`
	TotalStreams       int32          `json:"totalStreams"`
	MaxTotalStreams    int            `json:"maxTotalStreams"`
	MaxStreamsPerShare int            `json:"maxStreamsPerShare"`
	BaseDomain         string         `json:"baseDomain"`
	TotalRevoked       int            `json:"totalRevoked"`
	RevokedByCountry   map[string]int `json:"revokedByCountry,omitempty"`
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
	if token, err := auth.ExtractBearerToken(r); err == nil && secureCompare(token, requiredKey) {
		return true
	}

	// 2. Check query parameter ?key=... (convenient for browser SSH port forwarding)
	if key := r.URL.Query().Get("key"); secureCompare(key, requiredKey) {
		return true
	}

	// 3. Check Cookie (if set by browser dashboard)
	if cookie, err := r.Cookie("texlite_admin_key"); err == nil && secureCompare(cookie.Value, requiredKey) {
		return true
	}

	return false
}

// ServeHTTP handles requests arriving at the dedicated admin port.
func (h *AdminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// 1. Always serve the dashboard HTML on root/admin routes
	if path == "/" || path == "/admin" {
		if key := r.URL.Query().Get("key"); key != "" && secureCompare(key, h.cfg.CreateAPIKey) {
			secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
			http.SetCookie(w, &http.Cookie{
				Name:     "texlite_admin_key",
				Value:    key,
				Path:     "/",
				HttpOnly: false,
				Secure:   secure,
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
		h.api.HandleCreateShare(w, r.WithContext(WithAdminAuth(r.Context())))
	case strings.HasPrefix(path, "/api/v1/shares/") && (r.Method == http.MethodPatch || r.Method == http.MethodPut):
		shareID := strings.TrimPrefix(path, "/api/v1/shares/")
		shareID = strings.TrimSuffix(shareID, "/expires")
		h.handleUpdateShareExpiration(w, r, shareID)
	case strings.HasPrefix(path, "/api/v1/shares/") && strings.HasSuffix(path, "/expires") && r.Method == http.MethodPost:
		shareID := strings.TrimPrefix(path, "/api/v1/shares/")
		shareID = strings.TrimSuffix(shareID, "/expires")
		h.handleUpdateShareExpiration(w, r, shareID)
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

	totalRevoked, revokedByCountry, _ := h.db.GetRevokedStats(r.Context())

	stats := StatsView{
		ActiveShares:       activeCount,
		MaxActiveShares:    h.cfg.MaxActiveShares,
		OnlineTunnels:      h.registry.Count(),
		TotalStreams:       h.registry.TotalStreams(),
		MaxTotalStreams:    h.cfg.MaxTotalStreams,
		MaxStreamsPerShare: h.cfg.MaxStreamsPerShare,
		BaseDomain:         h.cfg.BaseDomain,
		TotalRevoked:       totalRevoked,
		RevokedByCountry:   revokedByCountry,
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

		country := s.ClientCountry
		if country == "" {
			country = "Unknown"
		}

		publicURL := fmt.Sprintf("http://%s.%s", s.ID, h.cfg.BaseDomain)
		views = append(views, ShareView{
			ID:            s.ID,
			ClientIP:      s.ClientIP,
			Country:       country,
			CreatedAt:     s.CreatedAt,
			ExpiresAt:     s.ExpiresAt,
			PublicURL:     publicURL,
			Online:        online,
			ActiveStreams: activeStreams,
		})
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(views)
}

// UpdateExpirationRequest represents a request to adjust the expiration of a share.
type UpdateExpirationRequest struct {
	Extend    string `json:"extend,omitempty"`    // e.g. "1 month", "1 year"
	ExpiresAt string `json:"expiresAt,omitempty"` // e.g. "2026-10-18T15:04:05Z" or "2026-10-18T15:04"
}

func (h *AdminHandler) handleUpdateShareExpiration(w http.ResponseWriter, r *http.Request, shareID string) {
	shareID = strings.TrimSpace(strings.ToLower(shareID))
	if shareID == "" {
		http.Error(w, "missing share ID", http.StatusBadRequest)
		return
	}

	var req UpdateExpirationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON payload", http.StatusBadRequest)
		return
	}

	share, err := h.db.GetShare(r.Context(), shareID)
	if err != nil {
		if errors.Is(err, protocol.ErrShareNotFound) {
			http.Error(w, "Share not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	now := time.Now().UTC()
	var newExpiresAt time.Time

	if req.Extend != "" {
		base := share.ExpiresAt
		if base.Before(now) {
			base = now
		}
		switch strings.ToLower(strings.TrimSpace(req.Extend)) {
		case "1 month", "1m", "+1 month", "+1m", "month":
			newExpiresAt = base.AddDate(0, 1, 0)
		case "1 year", "1y", "+1 year", "+1y", "year":
			newExpiresAt = base.AddDate(1, 0, 0)
		default:
			http.Error(w, fmt.Sprintf("unsupported extend option %q (use '1 month' or '1 year')", req.Extend), http.StatusBadRequest)
			return
		}
	} else if req.ExpiresAt != "" {
		parsed, err := parseDateTime(req.ExpiresAt)
		if err != nil {
			http.Error(w, fmt.Sprintf("invalid expiresAt format %q: %v", req.ExpiresAt, err), http.StatusBadRequest)
			return
		}
		newExpiresAt = parsed
	} else {
		http.Error(w, "either 'extend' or 'expiresAt' must be specified", http.StatusBadRequest)
		return
	}

	updated, err := h.db.UpdateShareExpiration(r.Context(), shareID, newExpiresAt, now)
	if err != nil {
		slog.Error("failed to update share expiration", "share_id", shareID, "error", err)
		http.Error(w, "Failed to update expiration", http.StatusInternalServerError)
		return
	}

	h.registry.UpdateExpiration(shareID, updated.ExpiresAt)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":        updated.ID,
		"expiresAt": updated.ExpiresAt,
		"status":    updated.Status,
	})
}

func parseDateTime(s string) (time.Time, error) {
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
	}
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l, s, time.UTC); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("expected ISO 8601 or YYYY-MM-DDTHH:MM")
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
    button.btn-xs { background: #334155; color: #e2e8f0; border: none; padding: 3px 8px; font-size: 11px; border-radius: 4px; font-weight: 500; cursor: pointer; }
    button.btn-xs:hover { background: var(--primary); color: #0f172a; }
    select { background: #0f172a; color: var(--text); border: 1px solid var(--border); padding: 8px; border-radius: 6px; }
    table { width: 100%; border-collapse: collapse; margin-top: 8px; }
    th, td { text-align: left; padding: 12px; border-bottom: 1px solid var(--border); font-size: 14px; }
    th { color: var(--text-muted); font-size: 12px; text-transform: uppercase; }
    .online-indicator { display: inline-block; width: 8px; height: 8px; border-radius: 50%; background: var(--success); margin-right: 6px; }
    .offline-indicator { display: inline-block; width: 8px; height: 8px; border-radius: 50%; background: var(--text-muted); margin-right: 6px; }
    .mono { font-family: monospace; }
    #modal, #edit-modal, #revoked-modal { display: none; position: fixed; inset: 0; background: rgba(0,0,0,0.7); justify-content: center; align-items: center; z-index: 1000; }
    #modal-box, #edit-modal-box, #revoked-modal-box { background: var(--card); border: 1px solid var(--border); padding: 24px; border-radius: 8px; max-width: 600px; width: 90%; }
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
      <div class="card" onclick="openRevokedModal()" style="cursor: pointer;" title="Click to view breakdown by location">
        <div style="display:flex; justify-content:space-between; align-items:center;">
          <h3>Total Revoked</h3>
          <span style="font-size:11px; color:var(--primary); font-weight:600;">📊 Details</span>
        </div>
        <div class="value" id="revoked-val" style="color:var(--danger); margin-top:4px;">-</div>
      </div>
    </div>

    <div class="actions-panel">
      <label>TTL Duration:</label>
      <select id="ttl-select">
        <option value="1h">1 hour</option>
        <option value="168h" selected>7 days</option>
        <option value="720h">30 days</option>
        <option value="8760h">365 days</option>
      </select>
      <button onclick="createShare()">+ Create New Share</button>
    </div>

    <div class="card">
      <h3 style="margin-bottom: 12px;">Active Shares</h3>
      <table>
        <thead>
          <tr>
            <th>Share ID</th>
            <th>Tunnel State</th>
            <th>Streams</th>
            <th>Client IP &amp; Country</th>
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

  <div id="edit-modal">
    <div id="edit-modal-box">
      <h2 style="margin-top:0; color:var(--primary);">Modify Expiration</h2>
      <div style="margin-bottom:10px;"><strong>Share ID:</strong> <span id="em-id" class="mono"></span></div>
      <div style="margin-bottom:12px;"><strong>Current Expires:</strong> <span id="em-curr" style="color:var(--text-muted);"></span></div>
      <div style="margin-bottom:16px;">
        <label style="display:block; margin-bottom:6px; font-size:13px; color:var(--text-muted);">New Expiration Date &amp; Time:</label>
        <input type="datetime-local" id="em-input" style="width:100%; box-sizing:border-box; background:#0f172a; color:var(--text); border:1px solid var(--border); padding:8px; border-radius:6px; font-family:inherit;">
      </div>
      <div style="display:flex; justify-content:flex-end; gap:8px;">
        <button style="background:var(--border); color:var(--text);" onclick="document.getElementById('edit-modal').style.display='none'">Cancel</button>
        <button onclick="submitCustomExpires()">Save</button>
      </div>
    </div>
  </div>

  <div id="revoked-modal">
    <div id="revoked-modal-box" style="max-width: 620px;">
      <div style="display:flex; justify-content:space-between; align-items:center; margin-bottom:16px;">
        <h2 style="margin:0; font-size:18px; color:var(--text);">Revoked Shares by Location</h2>
        <button style="background:transparent; color:var(--text-muted); font-size:18px; padding:2px 8px; border:none; cursor:pointer;" onclick="closeRevokedModal()">✕</button>
      </div>
      <div id="revoked-modal-body">
        <!-- Rendered by JS -->
      </div>
      <div style="display:flex; justify-content:flex-end; margin-top:20px;">
        <button onclick="closeRevokedModal()">Close</button>
      </div>
    </div>
  </div>

  <script>
    function escapeHtml(str) {
      if (!str) return '';
      return String(str)
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;')
        .replace(/'/g, '&#039;');
    }

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
          document.cookie = 'texlite_admin_key=' + encodeURIComponent(inputKey) + '; path=/; SameSite=Lax';
          options.headers['Authorization'] = 'Bearer ' + inputKey;
          return await fetch(url, options);
        }
      }
      return res;
    }

    let cachedStats = null;

    async function refreshData() {
      try {
        const statsRes = await apiFetch('/api/v1/stats');
        if (!statsRes.ok) return;
        const stats = await statsRes.json();
        cachedStats = stats;
        document.getElementById('quota-val').textContent = stats.activeShares + ' / ' + stats.maxActiveShares;
        document.getElementById('tunnels-val').textContent = stats.onlineTunnels;
        document.getElementById('streams-val').textContent = stats.totalStreams;
        document.getElementById('domain-val').textContent = stats.baseDomain;
        document.getElementById('revoked-val').textContent = stats.totalRevoked || 0;

        const sharesRes = await apiFetch('/api/v1/shares');
        if (!sharesRes.ok) return;
        const shares = await sharesRes.json();
        const tbody = document.getElementById('shares-tbody');
        tbody.innerHTML = '';

        if (!shares || shares.length === 0) {
          tbody.innerHTML = '<tr><td colspan="7" style="text-align:center; color: var(--text-muted);">No active shares found.</td></tr>';
          return;
        }

        shares.forEach(s => {
          const tr = document.createElement('tr');
          const isOnline = s.online;
          const expires = new Date(s.expiresAt).toLocaleString();

          const safeId = escapeHtml(s.id);
          const safeClientIp = escapeHtml(s.clientIp || '-');
          const safeCountry = escapeHtml(s.country ? '🌐 ' + s.country : '-');
          const safeUrl = escapeHtml(s.publicUrl);
          const encId = encodeURIComponent(s.id);
          const encExpires = encodeURIComponent(s.expiresAt);

          let actionBtn = '<button class="danger" onclick="revokeShare(\'' + encId + '\')">Revoke</button>';
          let stateTag = isOnline ? '<span class="online-indicator"></span>Online' : '<span class="offline-indicator"></span>Offline';

          tr.innerHTML = '<td class="mono"><strong>' + safeId + '</strong></td>' +
            '<td>' + stateTag + '</td>' +
            '<td>' + (s.activeStreams || 0) + '</td>' +
            '<td><div class="mono">' + safeClientIp + '</div><div style="font-size:12px; color:var(--text-muted); margin-top:2px;">' + safeCountry + '</div></td>' +
            '<td>' +
              '<div>' + escapeHtml(expires) + '</div>' +
              '<div style="margin-top:6px; display:flex; gap:4px; flex-wrap:wrap;">' +
                '<button class="btn-xs" onclick="extendShare(\'' + encId + '\', \'1 month\')" title="Add 1 Month">+1 Month</button>' +
                '<button class="btn-xs" onclick="extendShare(\'' + encId + '\', \'1 year\')" title="Add 1 Year">+1 Year</button>' +
                '<button class="btn-xs" onclick="openEditModal(\'' + encId + '\', \'' + encExpires + '\')" title="Custom Expiration Date">✏️ Edit</button>' +
              '</div>' +
            '</td>' +
            '<td><a href="' + safeUrl + '" target="_blank" style="color:var(--primary);">' + safeUrl + '</a></td>' +
            '<td>' + actionBtn + '</td>';
          tbody.appendChild(tr);
        });
      } catch (e) {
        console.error('Failed to refresh data', e);
      }
    }

    async function extendShare(id, ext) {
      id = decodeURIComponent(id);
      try {
        const res = await apiFetch('/api/v1/shares/' + encodeURIComponent(id) + '/expires', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ extend: ext })
        });
        if (!res.ok) {
          alert('Failed to extend share: ' + (await res.text()));
          return;
        }
        refreshData();
      } catch (e) {
        alert('Extend error: ' + e);
      }
    }

    let currentEditingShareId = '';
    function openEditModal(id, currentIso) {
      id = decodeURIComponent(id);
      currentIso = decodeURIComponent(currentIso);
      currentEditingShareId = id;
      document.getElementById('em-id').textContent = id;
      const d = new Date(currentIso);
      document.getElementById('em-curr').textContent = d.toLocaleString();
      const pad = n => String(n).padStart(2, '0');
      const localIso = d.getFullYear() + '-' + pad(d.getMonth()+1) + '-' + pad(d.getDate()) + 'T' + pad(d.getHours()) + ':' + pad(d.getMinutes());
      document.getElementById('em-input').value = localIso;
      document.getElementById('edit-modal').style.display = 'flex';
    }

    async function submitCustomExpires() {
      const val = document.getElementById('em-input').value;
      if (!val) {
        alert('Please select an expiration date/time');
        return;
      }
      try {
        const res = await apiFetch('/api/v1/shares/' + encodeURIComponent(currentEditingShareId) + '/expires', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ expiresAt: val })
        });
        if (!res.ok) {
          alert('Failed to update expiration: ' + (await res.text()));
          return;
        }
        document.getElementById('edit-modal').style.display = 'none';
        refreshData();
      } catch (e) {
        alert('Update error: ' + e);
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
      id = decodeURIComponent(id);
      if (!confirm('Are you sure you want to revoke share ' + id + '? This will immediately terminate the tunnel.')) return;
      try {
        const res = await apiFetch('/api/v1/shares/' + encodeURIComponent(id), { method: 'DELETE' });
        if (!res.ok) alert('Failed to revoke: ' + (await res.text()));
        refreshData();
      } catch (e) {
        alert('Revoke error: ' + e);
      }
    }

    function openRevokedModal() {
      const modal = document.getElementById('revoked-modal');
      const body = document.getElementById('revoked-modal-body');
      modal.style.display = 'flex';

      const stats = cachedStats || {};
      const total = stats.totalRevoked || 0;
      const byCountry = stats.revokedByCountry || {};
      const countries = Object.keys(byCountry);

      if (total === 0 || countries.length === 0) {
        body.innerHTML = '<div style="text-align:center; padding:36px 0; color:var(--text-muted); font-size:14px;">No revoked shares recorded yet.</div>';
        return;
      }

      // Sort countries descending by count
      const items = countries
        .map(c => ({ country: c, count: byCountry[c] }))
        .sort((a, b) => b.count - a.count);

      const colors = [
        '#38bdf8', '#f43f5e', '#10b981', '#fbbf24', '#a855f7',
        '#f97316', '#06b6d4', '#ec4899', '#84cc16', '#6366f1'
      ];

      body.innerHTML = 
        '<div style="display:flex; flex-wrap:wrap; gap:24px; align-items:center; justify-content:center; padding:8px 0;">' +
          '<div style="display:flex; flex-direction:column; align-items:center;">' +
            '<canvas id="revoked-pie-canvas" width="200" height="200" style="display:block;"></canvas>' +
          '</div>' +
          '<div style="flex:1; min-width:220px; max-height:240px; overflow-y:auto;">' +
            '<table style="width:100%; border-collapse:collapse; font-size:13px;">' +
              '<thead>' +
                '<tr style="border-bottom:1px solid var(--border);">' +
                  '<th style="text-align:left; padding:6px; color:var(--text-muted);">Location</th>' +
                  '<th style="text-align:right; padding:6px; color:var(--text-muted);">Count</th>' +
                  '<th style="text-align:right; padding:6px; color:var(--text-muted);">Ratio</th>' +
                '</tr>' +
              '</thead>' +
              '<tbody id="revoked-legend-tbody"></tbody>' +
            '</table>' +
          '</div>' +
        '</div>';

      const tbody = document.getElementById('revoked-legend-tbody');
      items.forEach((item, idx) => {
        const color = colors[idx % colors.length];
        const pct = ((item.count / total) * 100).toFixed(1) + '%';
        const tr = document.createElement('tr');
        tr.style.borderBottom = '1px solid rgba(51, 65, 85, 0.5)';
        tr.innerHTML = 
          '<td style="padding:6px; display:flex; align-items:center; gap:8px;">' +
            '<span style="display:inline-block; width:10px; height:10px; border-radius:50%; background:' + color + ';"></span>' +
            '<span>' + escapeHtml(item.country) + '</span>' +
          '</td>' +
          '<td style="text-align:right; padding:6px; font-weight:600;">' + item.count + '</td>' +
          '<td style="text-align:right; padding:6px; color:var(--text-muted);">' + pct + '</td>';
        tbody.appendChild(tr);
      });

      // Draw modern Donut Pie Chart on Canvas
      const canvas = document.getElementById('revoked-pie-canvas');
      if (canvas && canvas.getContext) {
        const ctx = canvas.getContext('2d');
        const cx = canvas.width / 2;
        const cy = canvas.height / 2;
        const r = Math.min(cx, cy) - 10;
        let startAngle = -0.5 * Math.PI;

        items.forEach((item, idx) => {
          const slice = (item.count / total) * 2 * Math.PI;
          const color = colors[idx % colors.length];

          ctx.beginPath();
          ctx.moveTo(cx, cy);
          ctx.arc(cx, cy, r, startAngle, startAngle + slice);
          ctx.closePath();
          ctx.fillStyle = color;
          ctx.fill();

          ctx.strokeStyle = '#1e293b';
          ctx.lineWidth = 2;
          ctx.stroke();

          startAngle += slice;
        });

        // Donut hole
        ctx.beginPath();
        ctx.arc(cx, cy, r * 0.6, 0, 2 * Math.PI);
        ctx.fillStyle = '#1e293b';
        ctx.fill();

        // Center text
        ctx.fillStyle = '#f8fafc';
        ctx.font = 'bold 22px sans-serif';
        ctx.textAlign = 'center';
        ctx.textBaseline = 'middle';
        ctx.fillText(total, cx, cy - 6);
        ctx.font = '11px sans-serif';
        ctx.fillStyle = '#94a3b8';
        ctx.fillText('Revoked', cx, cy + 14);
      }
    }

    function closeRevokedModal() {
      document.getElementById('revoked-modal').style.display = 'none';
    }

    window.addEventListener('click', function(e) {
      if (e.target === document.getElementById('revoked-modal')) closeRevokedModal();
      if (e.target === document.getElementById('edit-modal')) document.getElementById('edit-modal').style.display = 'none';
      if (e.target === document.getElementById('modal')) document.getElementById('modal').style.display = 'none';
    });

    refreshData();
    setInterval(refreshData, 5000);
  </script>

</body>
</html>`
