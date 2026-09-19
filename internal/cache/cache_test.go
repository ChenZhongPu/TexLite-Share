package cache_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"texlite-share/internal/cache"
)

func TestAssetCache_BasicPutGet(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "texlite-cache-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	c, err := cache.New(tempDir, 10) // 10 MB
	if err != nil {
		t.Fatalf("failed to initialize cache: %v", err)
	}

	data := []byte("console.log('hello texlite');")
	path := "/assets/index-test1234.js"

	// Initial Get should be false
	if _, found := c.Get(path); found {
		t.Fatal("expected cache miss for unwritten key")
	}

	// Put
	if err := c.Put(path, data); err != nil {
		t.Fatalf("failed to put asset: %v", err)
	}

	// Get
	filePath, found := c.Get(path)
	if !found {
		t.Fatal("expected cache hit after Put")
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed reading cached file: %v", err)
	}
	if string(content) != string(data) {
		t.Fatalf("expected content %q, got %q", data, content)
	}

	if c.ItemCount() != 1 {
		t.Fatalf("expected 1 item, got %d", c.ItemCount())
	}
	if c.CurrentSize() != int64(len(data)) {
		t.Fatalf("expected size %d, got %d", len(data), c.CurrentSize())
	}
}

func TestAssetCache_LRUEviction(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "texlite-cache-lru-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// Create a cache of only 1 MB
	c, err := cache.New(tempDir, 1) // 1 MB = 1048576 bytes
	if err != nil {
		t.Fatal(err)
	}

	chunkSize := 400 * 1024 // 400 KB
	chunk1 := make([]byte, chunkSize)
	chunk2 := make([]byte, chunkSize)
	chunk3 := make([]byte, chunkSize)

	// Put 1 (400 KB)
	if err := c.Put("/assets/chunk1.js", chunk1); err != nil {
		t.Fatal(err)
	}
	// Put 2 (400 KB) -> total 800 KB <= 1 MB
	if err := c.Put("/assets/chunk2.js", chunk2); err != nil {
		t.Fatal(err)
	}

	if c.ItemCount() != 2 {
		t.Fatalf("expected 2 items, got %d", c.ItemCount())
	}

	// Put 3 (400 KB) -> total 1200 KB > 1 MB, must evict chunk1!
	if err := c.Put("/assets/chunk3.js", chunk3); err != nil {
		t.Fatal(err)
	}

	// chunk1 should have been evicted
	if _, found := c.Get("/assets/chunk1.js"); found {
		t.Fatal("expected chunk1 to have been evicted")
	}

	// chunk2 and chunk3 should be present
	if _, found := c.Get("/assets/chunk2.js"); !found {
		t.Fatal("expected chunk2 to still be cached")
	}
	if _, found := c.Get("/assets/chunk3.js"); !found {
		t.Fatal("expected chunk3 to still be cached")
	}

	if c.ItemCount() != 2 {
		t.Fatalf("expected 2 items after eviction, got %d", c.ItemCount())
	}
}

func TestAssetCache_PathTraversalProtection(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "texlite-cache-traversal-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	c, err := cache.New(tempDir, 10)
	if err != nil {
		t.Fatal(err)
	}

	maliciousPaths := []string{
		"/assets/../../etc/passwd",
		"/assets/../secret.js",
		"/api/documents",
		"/index.html",
		"/admin",
		"assets/test.js\x00.png",
	}

	data := []byte("dummy")
	for _, p := range maliciousPaths {
		if err := c.Put(p, data); err == nil {
			t.Errorf("expected error putting malicious path %q, got nil", p)
		}
		if _, found := c.Get(p); found {
			t.Errorf("expected Get to reject malicious path %q, got found", p)
		}
	}
}

func TestAssetCache_StartupReindexing(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "texlite-cache-reindex-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// Pre-create cache files on disk
	assetsDir := filepath.Join(tempDir, "assets")
	_ = os.MkdirAll(assetsDir, 0755)
	_ = os.WriteFile(filepath.Join(assetsDir, "pre-existing.js"), []byte("pre-existing content"), 0644)

	c, err := cache.New(tempDir, 10)
	if err != nil {
		t.Fatal(err)
	}

	if c.ItemCount() != 1 {
		t.Fatalf("expected 1 pre-indexed file, got %d", c.ItemCount())
	}

	if _, found := c.Get("/assets/pre-existing.js"); !found {
		t.Fatal("expected pre-existing file to be found in cache")
	}
}

func TestAssetCache_Concurrency(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "texlite-cache-conc-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	c, err := cache.New(tempDir, 50)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	workers := 10
	iterations := 20

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				path := "/assets/asset-concurrent.js"
				data := []byte("test concurrent data")
				_ = c.Put(path, data)
				_, _ = c.Get(path)
			}
		}(i)
	}

	wg.Wait()

	if _, found := c.Get("/assets/asset-concurrent.js"); !found {
		t.Fatal("expected asset-concurrent.js to be found")
	}
}
