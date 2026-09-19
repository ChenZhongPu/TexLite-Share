package cache

import (
	"container/list"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	ErrPathInvalid   = errors.New("invalid asset path")
	ErrFileTooLarge  = errors.New("asset exceeds maximum allowed file size")
	ErrCacheDisabled = errors.New("asset cache is disabled")
)

const maxSingleFileSize = 15 * 1024 * 1024 // 15 MB

type cacheEntry struct {
	key        string // e.g. "/assets/index-p1NHkGGG.js"
	relPath    string // e.g. "assets/index-p1NHkGGG.js"
	size       int64
	lastAccess time.Time
}

// AssetCache provides an on-disk LRU cache specifically for content-hashed immutable static assets.
type AssetCache struct {
	mu           sync.Mutex
	dir          string
	maxBytes     int64
	currentBytes int64
	entries      map[string]*list.Element
	lruList      *list.List
}

// New creates and initializes an AssetCache in the specified directory with maxMB capacity.
// If maxMB <= 0 or dir is empty, returns nil indicating cache is disabled.
func New(dir string, maxMB int) (*AssetCache, error) {
	if maxMB <= 0 || dir == "" {
		return nil, nil
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to determine absolute path for cache dir %q: %w", dir, err)
	}

	if err := os.MkdirAll(absDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cache directory %q: %w", absDir, err)
	}

	c := &AssetCache{
		dir:      absDir,
		maxBytes: int64(maxMB) * 1024 * 1024,
		entries:  make(map[string]*list.Element),
		lruList:  list.New(),
	}

	// Index existing files on startup
	if err := c.indexExistingFiles(); err != nil {
		slog.Warn("error indexing existing asset cache files", "dir", absDir, "error", err)
	}

	slog.Info("asset cache initialized",
		"dir", absDir,
		"max_mb", maxMB,
		"indexed_files", c.lruList.Len(),
		"current_bytes", c.currentBytes,
	)

	return c, nil
}

func (c *AssetCache) indexExistingFiles() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return filepath.Walk(c.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}

		// Skip temporary files
		if strings.HasSuffix(info.Name(), ".tmp") {
			_ = os.Remove(path)
			return nil
		}

		rel, err := filepath.Rel(c.dir, path)
		if err != nil {
			return nil
		}

		key := "/" + filepath.ToSlash(rel)
		entry := &cacheEntry{
			key:        key,
			relPath:    rel,
			size:       info.Size(),
			lastAccess: info.ModTime(),
		}

		elem := c.lruList.PushFront(entry)
		c.entries[key] = elem
		c.currentBytes += info.Size()

		return nil
	})
}

// sanitizeKey verifies and converts a URL request path into a safe, normalized cache key.
func sanitizeKey(path string) (string, error) {
	if strings.Contains(path, "..") || strings.Contains(path, "\x00") {
		return "", ErrPathInvalid
	}

	cleaned := filepath.ToSlash(filepath.Clean("/" + strings.TrimPrefix(path, "/")))
	if !strings.HasPrefix(cleaned, "/assets/") &&
		cleaned != "/logo.svg" &&
		cleaned != "/favicon.ico" &&
		cleaned != "/pdf-download.svg" &&
		cleaned != "/pdf-file.svg" &&
		cleaned != "/tex-file.svg" {
		return "", ErrPathInvalid
	}

	return cleaned, nil
}

// Get returns the absolute filesystem path for a cached asset if present.
func (c *AssetCache) Get(assetPath string) (string, bool) {
	if c == nil {
		return "", false
	}

	key, err := sanitizeKey(assetPath)
	if err != nil {
		return "", false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	elem, exists := c.entries[key]
	if !exists {
		return "", false
	}

	entry := elem.Value.(*cacheEntry)
	fullPath := filepath.Join(c.dir, entry.relPath)

	// Verify file still exists on disk
	if _, err := os.Stat(fullPath); err != nil {
		c.removeElement(elem)
		return "", false
	}

	// Promote to most recently used
	entry.lastAccess = time.Now()
	c.lruList.MoveToFront(elem)

	return fullPath, true
}

// Put writes asset data to the cache using atomic write and updates the LRU registry.
func (c *AssetCache) Put(assetPath string, data []byte) error {
	if c == nil {
		return ErrCacheDisabled
	}

	if int64(len(data)) > maxSingleFileSize {
		return ErrFileTooLarge
	}

	key, err := sanitizeKey(assetPath)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// If already in cache, update access and return
	if elem, exists := c.entries[key]; exists {
		entry := elem.Value.(*cacheEntry)
		entry.lastAccess = time.Now()
		c.lruList.MoveToFront(elem)
		return nil
	}

	// Evict older entries if space needed
	needBytes := int64(len(data))
	for c.currentBytes+needBytes > c.maxBytes && c.lruList.Len() > 0 {
		oldest := c.lruList.Back()
		if oldest == nil {
			break
		}
		c.removeElement(oldest)
	}

	relPath := strings.TrimPrefix(key, "/")
	targetPath := filepath.Join(c.dir, filepath.FromSlash(relPath))

	// Ensure target directory is inside c.dir (defense in depth)
	if !strings.HasPrefix(targetPath, c.dir) {
		return ErrPathInvalid
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return fmt.Errorf("failed to create directory for cached asset: %w", err)
	}

	// Write atomically to temporary file first
	tmpFile, err := os.CreateTemp(filepath.Dir(targetPath), ".asset-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp cache file: %w", err)
	}
	tmpName := tmpFile.Name()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed writing cache temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}

	if err := os.Rename(tmpName, targetPath); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed committing cached asset file: %w", err)
	}

	entry := &cacheEntry{
		key:        key,
		relPath:    relPath,
		size:       needBytes,
		lastAccess: time.Now(),
	}

	elem := c.lruList.PushFront(entry)
	c.entries[key] = elem
	c.currentBytes += needBytes

	return nil
}

func (c *AssetCache) removeElement(elem *list.Element) {
	entry := elem.Value.(*cacheEntry)
	delete(c.entries, entry.key)
	c.lruList.Remove(elem)
	c.currentBytes -= entry.size

	fullPath := filepath.Join(c.dir, entry.relPath)
	_ = os.Remove(fullPath)
}

// CurrentSize returns the total bytes occupied by cached assets.
func (c *AssetCache) CurrentSize() int64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.currentBytes
}

// ItemCount returns the number of cached items in the registry.
func (c *AssetCache) ItemCount() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lruList.Len()
}
