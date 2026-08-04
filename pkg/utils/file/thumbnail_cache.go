package file

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sync/singleflight"
)

// ThumbCacheDir is the on-disk root for cached thumbnails, keyed by a hash
// of (path, mtime, size) so a changed/replaced source file naturally misses
// the old entry instead of serving stale pixels. It mirrors the existing
// "<volume root or /DATA>/.system_data/<name>" convention already used for tus
// staging (see common.FileUploadStagingDir) — a fixed /DATA root, not a
// true per-mount-point root; see task report for the tradeoff. It is a var
// (not a const) so tests can redirect it to a temp dir.
var ThumbCacheDir = "/DATA/.system_data/thumb-cache"

// negativeCacheTTL bounds how long a failed generation (unsupported format,
// decode error, etc.) is remembered so a client hammering the same broken
// file doesn't re-attempt an expensive decode on every request.
const negativeCacheTTL = 30 * time.Second

const (
	thumbCacheExt    = ".jpg"
	thumbNegativeExt = ".neg"
)

// thumbGroup de-dupes concurrent cache-miss generations for the same key
// (thundering-herd protection when many requests race in for the same
// not-yet-cached photo).
var thumbGroup singleflight.Group

// ThumbCacheKey derives a deterministic cache key from a file's path,
// modification time and size. Any change to the underlying file (new
// content -> new mtime and/or size) yields a different key, so stale
// entries are simply orphaned rather than served.
func ThumbCacheKey(path string, modTime time.Time, size int64) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%d|%d", path, modTime.UnixNano(), size)
	return hex.EncodeToString(h.Sum(nil))
}

func thumbCachePath(key string) string {
	return filepath.Join(ThumbCacheDir, key+thumbCacheExt)
}

func thumbNegativePath(key string) string {
	return filepath.Join(ThumbCacheDir, key+thumbNegativeExt)
}

// GetOrCreateThumbnailCached returns the on-disk path of a cached thumbnail
// JPEG for the image at path, generating and persisting it on a cache miss.
// ok is false when the thumbnail could not be produced (unsupported/corrupt
// image, or a recent failed attempt still within the negative-cache TTL);
// callers should fall back to serving the original file or a generic icon
// in that case.
func GetOrCreateThumbnailCached(path string) (cachedPath string, ok bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	key := ThumbCacheKey(path, fi.ModTime(), fi.Size())
	cp := thumbCachePath(key)

	if _, err := os.Stat(cp); err == nil {
		// Refresh mtime as a "recently used" marker; PruneThumbCache uses this
		// for an approximate LRU. The cache key is derived from the source
		// file's mtime, which is unrelated to this refresh, so it doesn't
		// affect hit detection. Ignoring a Chtimes failure is an intentional
		// degraded path: the worst case is this entry gets mistaken for
		// long-unused and reaped early on the next PruneThumbCache pass,
		// triggering a regeneration — not a correctness issue (never returns
		// an error result or corrupts the cache).
		now := time.Now()
		_ = os.Chtimes(cp, now, now)
		return cp, true
	}

	if negInfo, err := os.Stat(thumbNegativePath(key)); err == nil {
		if time.Since(negInfo.ModTime()) < negativeCacheTTL {
			return "", false
		}
	}

	// singleflight collapses concurrent misses for the same key into one
	// generation; everyone else waits for that result.
	_, _, _ = thumbGroup.Do(key, func() (interface{}, error) {
		generateAndCacheThumbnail(path, key, cp)
		return nil, nil
	})

	if _, err := os.Stat(cp); err == nil {
		return cp, true
	}
	return "", false
}

// generateAndCacheThumbnail does the actual decode/resize/encode and writes
// either the resulting JPEG or a negative marker to ThumbCacheDir. Errors
// are swallowed here by design: the negative-cache marker (or the absence
// of a positive cache file) is itself the error signal to the caller.
func generateAndCacheThumbnail(path, key, cachePath string) {
	if err := os.MkdirAll(ThumbCacheDir, 0o755); err != nil {
		return
	}

	data, err := GenerateThumbnail(path)
	if err != nil {
		// Negative-cache the failure so repeated requests for a
		// known-unsupported file don't re-attempt a full decode each time.
		_ = os.WriteFile(thumbNegativePath(key), []byte{}, 0o644)
		return
	}

	// Write-then-rename for atomicity: a concurrent reader never observes a
	// partially-written cache file.
	tmp := cachePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, cachePath); err != nil {
		_ = os.Remove(tmp)
	}
}

// PurgeThumbCacheEntry should be called right before a source file is
// deleted/moved: it computes the key from the current stat and removes the
// corresponding cache entries (.jpg and .neg). Best-effort and silent — a
// missing entry, a failed removal, etc. never return an error; the worst
// case is just falling back to the 30-day LRU sweep (PruneThumbCache),
// which doesn't affect correctness. When path is not a regular file
// (directory, doesn't exist, stat failed, etc.) it does nothing: deleting a
// directory does not recurse into computing and purging a key for every
// child file (cost tradeoff) — that's left to the LRU backstop.
//
// Call timing is a precondition for this function's correctness: it must be
// called before the source file is actually deleted/renamed/overwritten —
// the cache key includes mtime and size, and once the source file no longer
// exists or has already been rewritten, there's no way to recompute the key
// that was used when the cache entry was originally generated.
func PurgeThumbCacheEntry(path string) {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() || !fi.Mode().IsRegular() {
		return
	}
	key := ThumbCacheKey(path, fi.ModTime(), fi.Size())
	_ = os.Remove(thumbCachePath(key))
	_ = os.Remove(thumbNegativePath(key))
}

// PruneThumbCache sweeps ThumbCacheDir for all regular files (.jpg/.neg and
// orphaned .tmp, etc.) with mtime older than maxAge, with no extension
// filtering. When a source file is deleted/changed/moved, the old entry
// just becomes orphaned (the key embeds path+mtime+size) with no linked
// cleanup — this function is the only reclamation path; combined with the
// mtime refresh on hit, the effect is approximately recency-based eviction.
// Returns (0, nil) if the directory doesn't exist.
func PruneThumbCache(maxAge time.Duration) (int, error) {
	entries, err := os.ReadDir(ThumbCacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fi, ierr := e.Info()
		if ierr != nil || !fi.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(ThumbCacheDir, e.Name())); err == nil {
			removed++
		}
	}
	return removed, nil
}
