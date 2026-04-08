# Changelog

## v0.2.0 — 2026-04-07

### Added

- `Stats(cfg)` — scan cache directory and return total size/file count without mmap
- `Clear(cfg)` — remove all cached files and empty directories
- `Trim(cfg, maxSize, maxFiles, maxAge)` — LRU eviction by size, file count, and/or age
- `OpenIfExists(cfg)` — open handler only if tracking file already exists
- All scan functions use optimized directory walking: `Readdirnames` (cached) for intermediate levels, `ReadDir` + `Info()` at leaf level
- `Clear` and `Trim` cooperatively update the mmap tracking file if it exists (via `OpenIfExists` + `BeforeRemove`), but do not create it
- Empty leaf directories are removed after file deletion (errors ignored for concurrent safety)

## v0.1.0 — 2026-04-05

Initial preliminary release.

- Persistent mmap'd tracking file with lock-free atomic counters
- Configurable directory layout via callbacks (SubdirPath, FilePrefix, IsCachedFile, IsTempFile)
- Automatic LRU eviction: file-count-based target (90% of fair share per partition)
- Cross-process eviction lock with stale PID detection
- Subdirectory presence bitmask to skip empty directories during eviction
- Stale temporary file cleanup during eviction scans
- Hit/miss statistics (Hits, Misses, HitRatio, ResetStats)
- Versioned file format (v1.0) with 1024-byte header and reserved space for future fields
- Counter reconciliation from directory listing during eviction (self-healing drift)
- Platform support: Linux, macOS, Windows
