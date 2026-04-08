package cacheevict

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func applyScanDefaults(cfg *Config) {
	if cfg.PrefixCount <= 0 {
		cfg.PrefixCount = DefaultPrefixCount
	}
}

// OpenIfExists opens the eviction handler if the persistent tracking file
// (.cache-sizes) already exists, or returns (nil, nil) if it does not.
// This is useful for cooperatively updating counters when the mmap tracking
// is active, without creating the file on caches that don't use it.
func OpenIfExists(cfg Config) (*Handler, error) {
	path := filepath.Join(cfg.BaseDir, trackingFileName)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}
	return Open(cfg)
}

// Stats scans the cache directory and returns the total size and file count.
// This works without the mmap handler and is suitable for network caches.
//
// Required Config fields: BaseDir, SubdirPath, IsCachedFile.
// PrefixCount defaults to DefaultPrefixCount (65536) if not set.
// All other fields are ignored.
func Stats(cfg Config) (totalSize int64, totalFiles int64, err error) {
	applyScanDefaults(&cfg)
	if cfg.BaseDir == "" || cfg.SubdirPath == nil || cfg.IsCachedFile == nil {
		return 0, 0, fmt.Errorf("cacheevict: Stats requires BaseDir, SubdirPath, and IsCachedFile")
	}

	err = walkCache(cfg, func(path string, size int64, mtime time.Time) {
		totalSize += size
		totalFiles++
	})
	return
}

// Clear removes all cached files from the cache directory. If the mmap
// tracking file exists, counters are updated via BeforeRemove. Empty
// leaf directories are removed (errors ignored for concurrent safety).
//
// Required Config fields: BaseDir, PrefixCount, SubdirPath, IsCachedFile.
// Optional: IsTempFile (to also clean stale temp files).
func Clear(cfg Config) error {
	applyScanDefaults(&cfg)
	if cfg.BaseDir == "" || cfg.SubdirPath == nil || cfg.IsCachedFile == nil {
		return fmt.Errorf("cacheevict: Clear requires BaseDir, SubdirPath, and IsCachedFile")
	}

	h, err := OpenIfExists(cfg)
	if err != nil {
		return fmt.Errorf("cacheevict: open tracking file: %w", err)
	}
	if h != nil {
		defer h.Close()
	}

	var dirs []string

	err = walkCacheAll(cfg, func(path string, size int64, mtime time.Time, isCached bool) {
		if h != nil && isCached {
			h.BeforeRemove(path)
		}
		os.Remove(path)
	}, func(dir string) {
		dirs = append(dirs, dir)
	})

	// Remove empty directories bottom-up.
	removeEmptyDirs(dirs)

	return err
}

// Trim removes the oldest cached files until the cache is within the given
// limits. At least one of maxSize, maxFiles, or maxAge must be non-zero.
// If the mmap tracking file exists, counters are updated via BeforeRemove.
// Empty leaf directories are removed (errors ignored for concurrent safety).
//
// Required Config fields: BaseDir, PrefixCount, SubdirPath, IsCachedFile.
func Trim(cfg Config, maxSize, maxFiles int64, maxAge time.Duration) error {
	applyScanDefaults(&cfg)
	if cfg.BaseDir == "" || cfg.SubdirPath == nil || cfg.IsCachedFile == nil {
		return fmt.Errorf("cacheevict: Trim requires BaseDir, SubdirPath, and IsCachedFile")
	}
	if maxSize <= 0 && maxFiles <= 0 && maxAge <= 0 {
		return fmt.Errorf("cacheevict: Trim requires at least one of maxSize, maxFiles, or maxAge")
	}

	// Collect all cached files with metadata.
	var files []fileEntry
	var totalSize int64
	if err := walkCache(cfg, func(path string, size int64, mtime time.Time) {
		files = append(files, fileEntry{path: path, size: size, mtime: mtime})
		totalSize += size
	}); err != nil {
		return err
	}

	// Sort by mtime ascending (oldest first).
	sort.Slice(files, func(i, j int) bool {
		return files[i].mtime.Before(files[j].mtime)
	})

	// Determine which files to delete.
	cutoff := time.Now().Add(-maxAge)
	totalFiles := int64(len(files))
	var toDelete []string
	for _, f := range files {
		del := false

		// Phase 1: age-based eviction.
		if maxAge > 0 && f.mtime.Before(cutoff) {
			del = true
		}

		// Phase 2: file-count-based eviction.
		if !del && maxFiles > 0 && totalFiles > maxFiles {
			del = true
		}

		// Phase 3: size-based eviction.
		if !del && maxSize > 0 && totalSize > maxSize {
			del = true
		}

		if del {
			toDelete = append(toDelete, f.path)
			totalSize -= f.size
			totalFiles--
		}
	}

	if len(toDelete) == 0 {
		return nil
	}

	// Open tracking file if it exists.
	h, err := OpenIfExists(cfg)
	if err != nil {
		return fmt.Errorf("cacheevict: open tracking file: %w", err)
	}
	if h != nil {
		defer h.Close()
	}

	dirs := make(map[string]struct{})
	for _, path := range toDelete {
		if h != nil {
			h.BeforeRemove(path)
		}
		os.Remove(path)
		dirs[filepath.Dir(path)] = struct{}{}
	}

	// Remove empty directories.
	dirList := make([]string, 0, len(dirs))
	for d := range dirs {
		dirList = append(dirList, d)
	}
	removeEmptyDirs(dirList)

	return nil
}

// walkCache iterates all cached files in the cache directory, calling fn for
// each. It uses Readdirnames for intermediate directory levels for performance,
// and ReadDir + Info() only at the leaf level where file metadata is needed.
func walkCache(cfg Config, fn func(path string, size int64, mtime time.Time)) error {
	return walkCacheAll(cfg, func(path string, size int64, mtime time.Time, isCached bool) {
		if isCached {
			fn(path, size, mtime)
		}
	}, nil)
}

// walkCacheAll iterates all files (cached and temp) in the cache directory.
// For each file, fn is called with isCached=true for cached files and false
// for temp files. If dirFn is non-nil, it is called for each leaf directory.
func walkCacheAll(cfg Config, fn func(path string, size int64, mtime time.Time, isCached bool), dirFn func(dir string)) error {
	// Cache Readdirnames results for intermediate directories to avoid
	// redundant syscalls. Key is the absolute directory path.
	dirCache := make(map[string]map[string]struct{})

	readdirnamesSet := func(dir string) map[string]struct{} {
		if cached, ok := dirCache[dir]; ok {
			return cached
		}
		f, err := os.Open(dir)
		if err != nil {
			dirCache[dir] = nil
			return nil
		}
		names, err := f.Readdirnames(-1)
		f.Close()
		if err != nil {
			dirCache[dir] = nil
			return nil
		}
		set := make(map[string]struct{}, len(names))
		for _, n := range names {
			set[n] = struct{}{}
		}
		dirCache[dir] = set
		return set
	}

	for idx := 0; idx < cfg.PrefixCount; idx++ {
		relPath := cfg.SubdirPath(idx)
		parts := strings.Split(relPath, string(filepath.Separator))

		// Check intermediate levels using cached Readdirnames.
		skip := false
		dir := cfg.BaseDir
		for _, part := range parts[:len(parts)-1] {
			entries := readdirnamesSet(dir)
			if entries == nil {
				skip = true
				break
			}
			if _, ok := entries[part]; !ok {
				skip = true
				break
			}
			dir = filepath.Join(dir, part)
		}
		if skip {
			continue
		}

		// Check the last intermediate level for the leaf directory.
		leafName := parts[len(parts)-1]
		parentEntries := readdirnamesSet(dir)
		if parentEntries == nil {
			continue
		}
		if _, ok := parentEntries[leafName]; !ok {
			continue
		}

		leafDir := filepath.Join(dir, leafName)
		if dirFn != nil {
			dirFn(leafDir)
		}

		entries, err := os.ReadDir(leafDir)
		if err != nil {
			continue
		}

		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()

			isCached := cfg.IsCachedFile(name)
			isTemp := cfg.IsTempFile != nil && cfg.IsTempFile(name)
			if !isCached && !isTemp {
				continue
			}

			info, err := e.Info()
			if err != nil {
				continue
			}

			fn(filepath.Join(leafDir, name), info.Size(), info.ModTime(), isCached)
		}
	}

	return nil
}

// removeEmptyDirs attempts to remove the given directories and their parent
// directories if they become empty. Errors are silently ignored since
// another process may be adding files concurrently.
func removeEmptyDirs(dirs []string) {
	// Sort longest first so we remove deepest dirs before their parents.
	sort.Slice(dirs, func(i, j int) bool {
		return len(dirs[i]) > len(dirs[j])
	})

	removed := make(map[string]struct{})
	for _, d := range dirs {
		if _, ok := removed[d]; ok {
			continue
		}
		// Try to remove the directory (only succeeds if empty).
		if err := os.Remove(d); err == nil {
			removed[d] = struct{}{}
			// Try parent too.
			parent := filepath.Dir(d)
			if _, ok := removed[parent]; !ok {
				if os.Remove(parent) == nil {
					removed[parent] = struct{}{}
				}
			}
		}
	}
}
