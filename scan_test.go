package cacheevict

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStatsLFS(t *testing.T) {
	dir := t.TempDir()
	cfg := lfsConfig(dir, 0, 0, 256)

	// Empty cache.
	size, files, err := Stats(cfg)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, size, 0, "empty size")
	assertEqual(t, files, 0, "empty files")

	// Add some objects.
	storeLFSObjectRaw(t, dir, 100)
	storeLFSObjectRaw(t, dir, 200)
	storeLFSObjectRaw(t, dir, 300)

	size, files, err = Stats(cfg)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, files, 3, "files")
	assertEqual(t, size, 600, "size")
}

func TestStatsDesync(t *testing.T) {
	dir := t.TempDir()
	cfg := desyncConfig(dir, 0, 0, 256)

	storeDesyncChunkRaw(t, dir, 500)
	storeDesyncChunkRaw(t, dir, 500)

	size, files, err := Stats(cfg)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, files, 2, "files")
	assertEqual(t, size, 1000, "size")
}

func TestClear(t *testing.T) {
	dir := t.TempDir()
	cfg := lfsConfig(dir, 0, 0, 256)

	storeLFSObjectRaw(t, dir, 100)
	storeLFSObjectRaw(t, dir, 200)
	storeLFSObjectRaw(t, dir, 300)

	// Verify files exist.
	_, files, _ := Stats(cfg)
	assertEqual(t, files, 3, "before clear")

	if err := Clear(cfg); err != nil {
		t.Fatal(err)
	}

	_, files, _ = Stats(cfg)
	assertEqual(t, files, 0, "after clear")
}

func TestClearWithHandler(t *testing.T) {
	dir := t.TempDir()
	cfg := lfsConfig(dir, 10*1024*1024, 0, 256)

	// Open handler to create mmap file.
	h, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	storeLFSObject(t, h, dir, 100)
	storeLFSObject(t, h, dir, 200)
	assertEqual(t, h.TotalFiles(), 2, "handler files before")
	h.Close()

	// Clear should update the mmap counters.
	if err := Clear(cfg); err != nil {
		t.Fatal(err)
	}

	// Reopen and check counters were updated.
	h, err = Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	assertEqual(t, h.TotalFiles(), 0, "handler files after clear")
	assertEqual(t, h.TotalSize(), 0, "handler size after clear")
}

func TestClearRemovesEmptyDirs(t *testing.T) {
	dir := t.TempDir()
	cfg := lfsConfig(dir, 0, 0, 256)

	path := storeLFSObjectRaw(t, dir, 100)
	leafDir := filepath.Dir(path)

	if err := Clear(cfg); err != nil {
		t.Fatal(err)
	}

	// Leaf directory should be removed.
	if _, err := os.Stat(leafDir); !os.IsNotExist(err) {
		t.Errorf("leaf directory still exists: %s", leafDir)
	}
}

func TestTrimByAge(t *testing.T) {
	dir := t.TempDir()
	cfg := lfsConfig(dir, 0, 0, 256)

	// Create files with old timestamps.
	old := storeLFSObjectRaw(t, dir, 100)
	os.Chtimes(old, time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour))

	recent := storeLFSObjectRaw(t, dir, 200)
	_ = recent

	if err := Trim(cfg, 0, 0, 24*time.Hour); err != nil {
		t.Fatal(err)
	}

	// Old file should be gone, recent should remain.
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("old file should have been removed")
	}
	_, files, _ := Stats(cfg)
	assertEqual(t, files, 1, "remaining files")
}

func TestTrimByMaxFiles(t *testing.T) {
	dir := t.TempDir()
	cfg := lfsConfig(dir, 0, 0, 256)

	paths := make([]string, 5)
	for i := range paths {
		paths[i] = storeLFSObjectRaw(t, dir, 100)
		// Stagger mtimes so ordering is deterministic.
		mtime := time.Now().Add(time.Duration(i) * time.Second)
		os.Chtimes(paths[i], mtime, mtime)
	}

	if err := Trim(cfg, 0, 3, 0); err != nil {
		t.Fatal(err)
	}

	_, files, _ := Stats(cfg)
	assertEqual(t, files, 3, "remaining files")

	// Oldest 2 should be gone.
	for _, p := range paths[:2] {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("old file should have been removed: %s", p)
		}
	}
}

func TestTrimByMaxSize(t *testing.T) {
	dir := t.TempDir()
	cfg := lfsConfig(dir, 0, 0, 256)

	paths := make([]string, 5)
	for i := range paths {
		paths[i] = storeLFSObjectRaw(t, dir, 100)
		mtime := time.Now().Add(time.Duration(i) * time.Second)
		os.Chtimes(paths[i], mtime, mtime)
	}

	// Total is 500 bytes, trim to 300.
	if err := Trim(cfg, 300, 0, 0); err != nil {
		t.Fatal(err)
	}

	size, files, _ := Stats(cfg)
	assertEqual(t, files, 3, "remaining files")
	assertEqual(t, size, 300, "remaining size")
}

func TestTrimWithHandler(t *testing.T) {
	dir := t.TempDir()
	cfg := lfsConfig(dir, 10*1024*1024, 0, 256)

	h, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		path := storeLFSObject(t, h, dir, 100)
		mtime := time.Now().Add(time.Duration(i) * time.Second)
		os.Chtimes(path, mtime, mtime)
	}
	assertEqual(t, h.TotalFiles(), 5, "handler files before trim")
	h.Close()

	if err := Trim(cfg, 0, 3, 0); err != nil {
		t.Fatal(err)
	}

	h, err = Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	assertEqual(t, h.TotalFiles(), 3, "handler files after trim")
}

func TestTrimNoLimits(t *testing.T) {
	dir := t.TempDir()
	cfg := lfsConfig(dir, 0, 0, 256)

	err := Trim(cfg, 0, 0, 0)
	if err == nil {
		t.Error("expected error for no limits")
	}
}

func TestOpenIfExistsNoFile(t *testing.T) {
	dir := t.TempDir()
	cfg := lfsConfig(dir, 0, 0, 256)

	h, err := OpenIfExists(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if h != nil {
		t.Error("expected nil handler when no tracking file exists")
	}
}

func TestOpenIfExistsWithFile(t *testing.T) {
	dir := t.TempDir()
	cfg := lfsConfig(dir, 10*1024*1024, 0, 256)

	// Create the tracking file.
	h, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.Close()

	// OpenIfExists should find it.
	h, err = OpenIfExists(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if h == nil {
		t.Error("expected non-nil handler when tracking file exists")
	}
	h.Close()
}
