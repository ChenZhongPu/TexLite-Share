package geoip

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type cacheEntry struct {
	result    string
	expiresAt time.Time
}

const (
	maxCacheEntries = 1024
	successTTL      = 24 * time.Hour
	failureTTL      = 5 * time.Minute
)

var (
	cacheMu    sync.RWMutex
	cacheMap   = make(map[string]cacheEntry, maxCacheEntries)
	httpClient = &http.Client{Timeout: 2 * time.Second}
)

func getCached(ipStr string, now time.Time) (string, bool) {
	cacheMu.RLock()
	entry, ok := cacheMap[ipStr]
	cacheMu.RUnlock()
	if ok && now.Before(entry.expiresAt) {
		return entry.result, true
	}
	return "", false
}

func setCached(ipStr string, result string, ttl time.Duration, now time.Time) {
	cacheMu.Lock()
	defer cacheMu.Unlock()

	if len(cacheMap) >= maxCacheEntries {
		for k, v := range cacheMap {
			if now.After(v.expiresAt) {
				delete(cacheMap, k)
			}
		}
	}
	if len(cacheMap) >= maxCacheEntries {
		for k := range cacheMap {
			delete(cacheMap, k)
			break
		}
	}

	cacheMap[ipStr] = cacheEntry{
		result:    result,
		expiresAt: now.Add(ttl),
	}
}

type ipwhoisResponse struct {
	Success     bool   `json:"success"`
	Country     string `json:"country"`
	CountryCode string `json:"country_code"`
}

type ipapiResponse struct {
	Status      string `json:"status"`
	Country     string `json:"country"`
	CountryCode string `json:"countryCode"`
}

// Lookup resolves an IP address string to a country name (e.g. "China (CN)" or "Localhost").
func Lookup(ipStr string) string {
	return LookupWithContext(context.Background(), ipStr)
}

// LookupWithContext resolves an IP address string to a country name with context.
func LookupWithContext(ctx context.Context, ipStr string) string {
	ipStr = strings.TrimSpace(ipStr)
	if ipStr == "" {
		return "Unknown"
	}

	// Remove port if present
	if host, _, err := net.SplitHostPort(ipStr); err == nil {
		ipStr = host
	}

	ip := net.ParseIP(ipStr)
	if ip == nil {
		return "Unknown"
	}

	if ip.IsLoopback() {
		return "Localhost"
	}
	if ip.IsPrivate() {
		return "LAN / Private"
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return "Link-Local"
	}

	now := time.Now().UTC()

	// Check in-memory cache
	if cached, ok := getCached(ipStr, now); ok {
		return cached
	}

	// 1. Try ipwho.is (HTTPS, fast worldwide)
	req1, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://ipwho.is/"+ipStr, nil)
	if err == nil {
		if resp, err := httpClient.Do(req1); err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var data ipwhoisResponse
				if err := json.NewDecoder(resp.Body).Decode(&data); err == nil && data.Success && data.Country != "" {
					result := data.Country
					if data.CountryCode != "" {
						result = fmt.Sprintf("%s (%s)", data.Country, data.CountryCode)
					}
					setCached(ipStr, result, successTTL, now)
					return result
				}
			}
		}
	}

	// 2. Fallback to ip-api.com
	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://ip-api.com/json/"+ipStr+"?fields=status,country,countryCode", nil)
	if err == nil {
		if resp, err := httpClient.Do(req2); err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var data ipapiResponse
				if err := json.NewDecoder(resp.Body).Decode(&data); err == nil && data.Status == "success" && data.Country != "" {
					result := data.Country
					if data.CountryCode != "" {
						result = fmt.Sprintf("%s (%s)", data.Country, data.CountryCode)
					}
					setCached(ipStr, result, successTTL, now)
					return result
				}
			}
		}
	}

	// Cache negative lookup for failureTTL to prevent hammering external APIs
	setCached(ipStr, "Unknown", failureTTL, now)
	return "Unknown"
}
