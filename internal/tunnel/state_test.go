package tunnel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSaveAndLoadLastShareState(t *testing.T) {
	tempDir := t.TempDir()
	statePath := filepath.Join(tempDir, "state.json")

	// 1. Initial load should return nil, nil
	initial, err := LoadLastShareState(statePath)
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if initial != nil {
		t.Fatalf("expected nil state, got %+v", initial)
	}

	// 2. Save state
	now := time.Now().Truncate(time.Second)
	expires := now.Add(24 * time.Hour)
	saved := &SavedShareState{
		ServerURL: "http://127.0.0.1:9000",
		ShareID:   "testshare123456",
		Token:     "testtokenabcdefg",
		PublicURL: "http://testshare123456.share.local",
		LocalAddr: "127.0.0.1:3000",
		ExpiresAt: expires,
		CreatedAt: now,
		Revoked:   false,
	}

	if err := SaveLastShareState(statePath, saved); err != nil {
		t.Fatalf("SaveLastShareState failed: %v", err)
	}

	// 3. Verify file permissions (must be 0600 on unix)
	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("os.Stat failed: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("expected permissions 0600, got %o", perm)
	}

	// 4. Load state and verify contents
	loaded, err := LoadLastShareState(statePath)
	if err != nil {
		t.Fatalf("LoadLastShareState failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil loaded state")
	}
	if loaded.ShareID != "testshare123456" {
		t.Errorf("expected ShareID testshare123456, got %s", loaded.ShareID)
	}
	if loaded.Token != "testtokenabcdefg" {
		t.Errorf("expected Token testtokenabcdefg, got %s", loaded.Token)
	}
	if loaded.Revoked != false {
		t.Errorf("expected Revoked false, got true")
	}
}

func TestProactivelyRevokePreviousShare(t *testing.T) {
	tempDir := t.TempDir()
	statePath := filepath.Join(tempDir, "state.json")

	var receivedMethod string
	var receivedPath string
	var receivedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedPath = r.URL.Path
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	// Prepare previous unrevoked share state
	saved := &SavedShareState{
		ServerURL: server.URL,
		ShareID:   "oldshare1234567",
		Token:     "secret-token-to-send",
		ExpiresAt: time.Now().Add(1 * time.Hour),
		Revoked:   false,
	}
	if err := SaveLastShareState(statePath, saved); err != nil {
		t.Fatal(err)
	}

	// Run proactive revocation
	old, err := ProactivelyRevokePreviousShare(context.Background(), statePath, server.URL)
	if err != nil {
		t.Fatalf("ProactivelyRevokePreviousShare failed: %v", err)
	}
	if old == nil || old.ShareID != "oldshare1234567" {
		t.Fatalf("expected old share returned, got %+v", old)
	}

	// Verify server received correct DELETE call carrying Bearer token
	if receivedMethod != http.MethodDelete {
		t.Errorf("expected DELETE method, got %s", receivedMethod)
	}
	if !strings.HasSuffix(receivedPath, "/oldshare1234567") {
		t.Errorf("expected path to end with oldshare1234567, got %s", receivedPath)
	}
	if receivedAuth != "Bearer secret-token-to-send" {
		t.Errorf("expected Authorization 'Bearer secret-token-to-send', got %s", receivedAuth)
	}

	// Verify state file on disk is marked Revoked: true
	reloaded, err := LoadLastShareState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Revoked {
		t.Errorf("expected state to be marked Revoked: true after proactive cleanup")
	}

	// Running proactive revocation again should be a no-op (since already marked Revoked)
	receivedMethod = ""
	_, err = ProactivelyRevokePreviousShare(context.Background(), statePath, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if receivedMethod != "" {
		t.Errorf("expected no request sent for already-revoked share, but received %s", receivedMethod)
	}
}
