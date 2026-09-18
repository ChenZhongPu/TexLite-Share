package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"texlite-share/internal/config"
)

// TexLiteServiceInfo contains verified metadata about a running TexLite instance.
type TexLiteServiceInfo struct {
	Address string `json:"address"`
	PID     int    `json:"pid"`
	Latexmk string `json:"latexmk"`
}

// TexLiteStatusResponse mirrors the JSON output of `texlite status --json`.
type TexLiteStatusResponse struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	PM2Status  string `json:"pm2Status"`
	Healthy    bool   `json:"healthy"`
	PID        int    `json:"pid"`
	Address    string `json:"address"`
	ConfigPath string `json:"configPath"`
}

// TexLiteConfigFile represents the minimal structure of texlite.config.json.
type TexLiteConfigFile struct {
	Server struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		BasePath string `json:"basePath"`
	} `json:"server"`
}

// TexLiteHealthResponse represents the JSON payload returned by /api/health.
type TexLiteHealthResponse struct {
	Ok      bool   `json:"ok"`
	PID     int    `json:"pid"`
	Latexmk string `json:"latexmk"`
}

// DiscoverTexLiteAddr attempts to discover the host and port where TexLite is running.
// If an explicitAddr is provided (non-empty), it is validated and returned directly.
func DiscoverTexLiteAddr(ctx context.Context, explicitAddr string) (string, error) {
	if explicitAddr != "" {
		if err := config.ValidateLocalAddr(explicitAddr); err != nil {
			return "", err
		}
		return explicitAddr, nil
	}

	// 1. Try `texlite status --json` CLI
	if addr, err := discoverFromTexLiteCLI(ctx); err == nil && addr != "" {
		slog.Info("discovered TexLite address from texlite status CLI", "address", addr)
		return addr, nil
	}

	// 2. Try configuration files
	if addr, err := discoverFromConfigFile(); err == nil && addr != "" {
		slog.Info("discovered TexLite address from configuration file", "address", addr)
		return addr, nil
	}

	// 3. Try probing common candidate ports
	candidates := []string{"127.0.0.1:3000", "127.0.0.1:3040"}
	for _, cand := range candidates {
		if info, err := VerifyTexLiteService(cand, 500*time.Millisecond); err == nil && info != nil {
			slog.Info("discovered active TexLite service by probing candidate port", "address", cand, "pid", info.PID)
			return cand, nil
		}
	}

	// 4. Default fallback
	return "127.0.0.1:3000", nil
}

func discoverFromTexLiteCLI(ctx context.Context) (string, error) {
	cliPath, err := exec.LookPath("texlite")
	if err != nil {
		return "", err
	}

	execCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(execCtx, cliPath, "status", "--json")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}

	var status TexLiteStatusResponse
	if err := json.Unmarshal(out, &status); err != nil {
		return "", err
	}

	if status.Status != "online" && status.PM2Status != "online" && !status.Healthy {
		return "", fmt.Errorf("texlite is not reported online (status: %s, pm2: %s)", status.Status, status.PM2Status)
	}

	if status.Address == "" {
		return "", errors.New("texlite status did not include address")
	}

	u, err := url.Parse(status.Address)
	if err != nil {
		return "", err
	}

	host := u.Hostname()
	if host == "" {
		host = "127.0.0.1"
	}
	port := u.Port()
	if port == "" {
		port = "3000"
	}

	addr := net.JoinHostPort(host, port)
	if err := config.ValidateLocalAddr(addr); err != nil {
		return "", err
	}
	return addr, nil
}

func discoverFromConfigFile() (string, error) {
	candidates := []string{}

	if envConfig := os.Getenv("TEXLITE_CONFIG"); envConfig != "" {
		candidates = append(candidates, envConfig)
	}

	if xdgConfig := os.Getenv("XDG_CONFIG_HOME"); xdgConfig != "" {
		candidates = append(candidates, filepath.Join(xdgConfig, "texlite", "texlite.config.json"))
	}

	homeDir, err := os.UserHomeDir()
	if err == nil {
		candidates = append(candidates, filepath.Join(homeDir, ".config", "texlite", "texlite.config.json"))
	}

	candidates = append(candidates, "texlite.config.json")

	for _, path := range candidates {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var cfg TexLiteConfigFile
		if err := json.Unmarshal(data, &cfg); err != nil {
			continue
		}

		host := strings.TrimSpace(cfg.Server.Host)
		if host == "" {
			host = "127.0.0.1"
		}
		port := cfg.Server.Port
		if port <= 0 || port > 65535 {
			port = 3000
		}

		addr := fmt.Sprintf("%s:%d", host, port)
		if err := config.ValidateLocalAddr(addr); err == nil {
			return addr, nil
		}
	}

	return "", errors.New("no valid configuration file found")
}

// VerifyTexLiteService verifies that the service at localAddr is running and genuinely is TexLite.
// It checks TCP reachability and verifies the /api/health endpoint.
func VerifyTexLiteService(localAddr string, timeout time.Duration) (*TexLiteServiceInfo, error) {
	if err := config.ValidateLocalAddr(localAddr); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}

	// 1. TCP connectivity check
	conn, err := net.DialTimeout("tcp", localAddr, timeout)
	if err != nil {
		return nil, fmt.Errorf("TexLite service is not running at %s (connection refused: %w).\n   Please start TexLite first (e.g. run 'texlite start', 'texlite serve', or 'docker compose up')", localAddr, err)
	}
	_ = conn.Close()

	// 2. HTTP Health Probe (/api/health)
	client := &http.Client{Timeout: timeout}
	healthURL := fmt.Sprintf("http://%s/api/health", localAddr)
	req, err := http.NewRequest(http.MethodGet, healthURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to construct health check request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			var healthResp TexLiteHealthResponse
			if jsonErr := json.Unmarshal(body, &healthResp); jsonErr == nil && healthResp.Ok {
				return &TexLiteServiceInfo{
					Address: localAddr,
					PID:     healthResp.PID,
					Latexmk: healthResp.Latexmk,
				}, nil
			}
		}
	}

	// 3. Fallback: check /api/config or root HTML
	configURL := fmt.Sprintf("http://%s/api/config", localAddr)
	if cfgReq, err := http.NewRequest(http.MethodGet, configURL, nil); err == nil {
		cfgReq.Header.Set("Accept", "application/json")
		if cfgResp, err := client.Do(cfgReq); err == nil {
			defer cfgResp.Body.Close()
			if cfgResp.StatusCode == http.StatusOK {
				body, _ := io.ReadAll(io.LimitReader(cfgResp.Body, 2048))
				var raw map[string]any
				if jsonErr := json.Unmarshal(body, &raw); jsonErr == nil {
					if _, hasSite := raw["siteName"]; hasSite {
						return &TexLiteServiceInfo{
							Address: localAddr,
						}, nil
					}
				}
			}
		}
	}

	// 4. Fallback: check root HTML title / meta tags
	rootURL := fmt.Sprintf("http://%s/", localAddr)
	if rootReq, err := http.NewRequest(http.MethodGet, rootURL, nil); err == nil {
		if rootResp, err := client.Do(rootReq); err == nil {
			defer rootResp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(rootResp.Body, 4096))
			content := string(body)
			if strings.Contains(content, "<title>TexLite</title>") || strings.Contains(content, "texlite-base-path") {
				return &TexLiteServiceInfo{
					Address: localAddr,
				}, nil
			}
		}
	}

	return nil, fmt.Errorf("service at %s is reachable, but does not appear to be TexLite (failed TexLite identity verification at /api/health).\n   Please make sure TexLite is running on this port, or specify the correct address with --local-addr", localAddr)
}
