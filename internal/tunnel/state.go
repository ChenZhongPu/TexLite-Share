package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// SavedShareState holds the persistent metadata of the most recently created share.
type SavedShareState struct {
	ServerURL string    `json:"serverUrl"`
	ShareID   string    `json:"shareId"`
	Token     string    `json:"token"`
	PublicURL string    `json:"publicUrl"`
	LocalAddr string    `json:"localAddr"`
	ExpiresAt time.Time `json:"expiresAt"`
	CreatedAt time.Time `json:"createdAt"`
	Revoked   bool      `json:"revoked"`
}

// DefaultStateFilePath returns the platform-standard config path for saving the last share state.
func DefaultStateFilePath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		homeDir, hErr := os.UserHomeDir()
		if hErr != nil {
			return "", fmt.Errorf("could not determine user config or home directory: %w", err)
		}
		configDir = filepath.Join(homeDir, ".config")
	}
	return filepath.Join(configDir, "texlite-share", "last_share.json"), nil
}

// LoadLastShareState loads the last share state from the given file path.
// If the file does not exist, it returns (nil, nil).
func LoadLastShareState(path string) (*SavedShareState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read share state file %s: %w", path, err)
	}

	var state SavedShareState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse share state file %s: %w", path, err)
	}
	return &state, nil
}

// SaveLastShareState atomically writes the share state to disk with 0600 permissions.
func SaveLastShareState(path string, state *SavedShareState) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize share state: %w", err)
	}

	tmpFile := fmt.Sprintf("%s.tmp.%d", path, time.Now().UnixNano())
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return fmt.Errorf("failed to write temporary share state file: %w", err)
	}

	if err := os.Rename(tmpFile, path); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to commit share state file: %w", err)
	}

	return nil
}

// ProactivelyRevokePreviousShare inspects the state file at statePath.
// If an unrevoked previous share exists, it actively calls the server's DELETE endpoint
// carrying the previous token, marks the previous state as revoked, and saves the updated state.
func ProactivelyRevokePreviousShare(ctx context.Context, statePath string, defaultServerURL string) (*SavedShareState, error) {
	if statePath == "" {
		var err error
		statePath, err = DefaultStateFilePath()
		if err != nil {
			return nil, err
		}
	}

	state, err := LoadLastShareState(statePath)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, nil
	}

	if state.Revoked || state.ShareID == "" || state.Token == "" {
		return state, nil
	}

	targetServerURL := state.ServerURL
	if targetServerURL == "" {
		targetServerURL = defaultServerURL
	}

	revokeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	slog.Info("proactively revoking previous active share on server",
		"share_id", state.ShareID,
		"server_url", targetServerURL,
	)

	revokeErr := AutoRevokeShare(revokeCtx, targetServerURL, state.ShareID, state.Token)
	if revokeErr != nil {
		slog.Warn("attempted to proactively revoke previous share, server returned error",
			"share_id", state.ShareID,
			"error", revokeErr,
		)
	} else {
		slog.Info("successfully revoked previous share", "share_id", state.ShareID)
	}

	// In all cases, mark the previous record as revoked on disk so we don't repeatedly retry
	state.Revoked = true
	if err := SaveLastShareState(statePath, state); err != nil {
		slog.Warn("failed to update state file after proactive revocation", "error", err)
	}

	if revokeErr != nil {
		return state, errors.New("previous share cleanup reported warning")
	}
	return state, nil
}
