package file

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withTempThumbCacheDir points ThumbCacheDir at a fresh temp dir for the
// duration of the test and restores it afterward.
func withTempThumbCacheDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := ThumbCacheDir
	ThumbCacheDir = dir
	t.Cleanup(func() { ThumbCacheDir = old })
	return dir
}

func TestThumbCacheKey_StableForSameInput(t *testing.T) {
	mtime := time.Unix(1700000000, 0)
	k1 := ThumbCacheKey("/DATA/photo.jpg", mtime, 723_000)
	k2 := ThumbCacheKey("/DATA/photo.jpg", mtime, 723_000)
	if k1 != k2 {
		t.Errorf("expected identical keys for identical input, got %q vs %q", k1, k2)
	}
}

func TestThumbCacheKey_DiffersByPath(t *testing.T) {
	mtime := time.Unix(1700000000, 0)
	k1 := ThumbCacheKey("/DATA/a.jpg", mtime, 100)
	k2 := ThumbCacheKey("/DATA/b.jpg", mtime, 100)
	if k1 == k2 {
		t.Error("expected different keys for different paths")
	}
}

func TestThumbCacheKey_DiffersByMtimeAndSize(t *testing.T) {
	path := "/DATA/a.jpg"
	base := time.Unix(1700000000, 0)
	k1 := ThumbCacheKey(path, base, 100)
	k2 := ThumbCacheKey(path, base.Add(time.Second), 100)
	k3 := ThumbCacheKey(path, base, 200)
	if k1 == k2 {
		t.Error("expected different keys when mtime changes (file overwritten)")
	}
	if k1 == k3 {
		t.Error("expected different keys when size changes")
	}
}

// TestGetOrCreateThumbnailCached_MissThenHit verifies a first call generates
// and writes a cache entry, and a second call for the same source file hits
// the cache and returns the same path WITHOUT rewriting the cached file's
// content.
//
// 判别信号是内容比较,不是 mtime:命中分支会 Chtimes 刷新 mtime 作为"最近
// 使用"标记(见 TestCacheHitRefreshesMtime),所以「mtime 不回退」这种断言对
// 命中与误重新生成都成立,起不到区分作用。这里第一次生成后,故意往缓存文件
// 里塞一段已知的哨兵字节(不是合法 JPEG),再触发第二次调用;命中分支只应
// Chtimes,不应重写文件,所以哨兵字节必须原样保留。如果命中分支被误改成会
// 重新生成/重写缓存文件,这里读到的就会是重新编码出的 JPEG 字节而不是哨兵
// 字节,从而让本测试真正 FAIL。
func TestGetOrCreateThumbnailCached_MissThenHit(t *testing.T) {
	withTempThumbCacheDir(t)
	srcPath, _ := makeLargeJPEG(t, 800, 600)

	cachedPath1, ok := GetOrCreateThumbnailCached(srcPath)
	if !ok {
		t.Fatal("expected cache miss path to succeed (generate + store)")
	}

	sentinel := []byte("SENTINEL-CONTENT-NOT-A-REAL-JPEG")
	if err := os.WriteFile(cachedPath1, sentinel, 0644); err != nil {
		t.Fatalf("overwrite cache file with sentinel content: %v", err)
	}

	cachedPath2, ok := GetOrCreateThumbnailCached(srcPath)
	if !ok {
		t.Fatal("expected cache hit path to succeed")
	}
	if cachedPath1 != cachedPath2 {
		t.Errorf("expected same cache path across calls, got %q vs %q", cachedPath1, cachedPath2)
	}

	got, err := os.ReadFile(cachedPath2)
	if err != nil {
		t.Fatalf("expected cache file to still exist: %v", err)
	}
	if !bytes.Equal(got, sentinel) {
		t.Error("expected cache hit to leave the cached file's content untouched (no regeneration/rewrite on hit)")
	}
}

// TestGetOrCreateThumbnailCached_InvalidatesOnContentChange verifies that
// overwriting the source file (new mtime/size) produces a different cache
// entry rather than serving stale pixels.
func TestGetOrCreateThumbnailCached_InvalidatesOnContentChange(t *testing.T) {
	withTempThumbCacheDir(t)
	srcPath, _ := makeLargeJPEG(t, 800, 600)

	cachedPath1, ok := GetOrCreateThumbnailCached(srcPath)
	if !ok {
		t.Fatal("expected first generation to succeed")
	}

	// Overwrite with different content/size and bump mtime so the cache key
	// changes even on filesystems with coarse mtime resolution.
	newPath, _ := makeLargeJPEG(t, 400, 300)
	newData, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("read replacement file: %v", err)
	}
	if err := os.WriteFile(srcPath, newData, 0644); err != nil {
		t.Fatalf("overwrite source: %v", err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(srcPath, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	cachedPath2, ok := GetOrCreateThumbnailCached(srcPath)
	if !ok {
		t.Fatal("expected regeneration after content change to succeed")
	}
	if cachedPath1 == cachedPath2 {
		t.Error("expected a different cache path after the source file's mtime/size changed")
	}
}

// TestGetOrCreateThumbnailCached_NegativeCachesFailures verifies that a
// source file which cannot be decoded as an image produces a fast negative
// result (ok=false) without panicking, and that a follow-up call within the
// negative-cache TTL does not re-attempt decoding (checked indirectly by
// confirming the negative marker file exists after the first call).
func TestGetOrCreateThumbnailCached_NegativeCachesFailures(t *testing.T) {
	dir := withTempThumbCacheDir(t)
	f, err := os.CreateTemp(t.TempDir(), "bad_*.heic")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	f.Write([]byte("not an image"))
	f.Close()

	if _, ok := GetOrCreateThumbnailCached(f.Name()); ok {
		t.Fatal("expected undecodable source to fail thumbnail generation")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read cache dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected a negative-cache marker file to be written after a failed generation")
	}

	// Second call within the TTL should still report failure (fast path via
	// the negative-cache marker) rather than erroring differently.
	if _, ok := GetOrCreateThumbnailCached(f.Name()); ok {
		t.Fatal("expected negative-cache hit to still report failure")
	}
}

func TestThumbCachePath_IsUnderThumbCacheDir(t *testing.T) {
	dir := withTempThumbCacheDir(t)
	key := ThumbCacheKey("/DATA/x.jpg", time.Unix(1, 0), 1)
	p := thumbCachePath(key)
	if filepath.Dir(p) != filepath.Clean(dir) {
		t.Errorf("expected cache path to live under ThumbCacheDir %q, got %q", dir, p)
	}
}

// TestCacheHitRefreshesMtime verifies a cache hit (GetOrCreateThumbnailCached
// on an already-cached source) refreshes the cached file's mtime to "now",
// so it reads as recently-used and PruneThumbCache's age-based sweep won't
// reap a still-in-use entry just because it was generated long ago.
func TestCacheHitRefreshesMtime(t *testing.T) {
	withTempThumbCacheDir(t)
	srcPath, _ := makeLargeJPEG(t, 800, 600)

	cachedPath, ok := GetOrCreateThumbnailCached(srcPath)
	if !ok {
		t.Fatal("expected cache miss path to succeed (generate + store)")
	}

	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(cachedPath, past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if _, ok := GetOrCreateThumbnailCached(srcPath); !ok {
		t.Fatal("expected cache hit to succeed")
	}

	info, err := os.Stat(cachedPath)
	if err != nil {
		t.Fatalf("stat cached file: %v", err)
	}
	if time.Since(info.ModTime()) > time.Minute {
		t.Errorf("expected cache hit to refresh mtime to now, got mtime %v", info.ModTime())
	}
}

// TestPruneThumbCacheRemovesOldEntries verifies PruneThumbCache removes
// entries (both .jpg and .neg) whose mtime is older than maxAge, and leaves
// fresh entries untouched.
func TestPruneThumbCacheRemovesOldEntries(t *testing.T) {
	dir := t.TempDir()
	old := ThumbCacheDir
	ThumbCacheDir = dir
	defer func() { ThumbCacheDir = old }()

	fresh := filepath.Join(dir, "fresh.jpg")
	stale := filepath.Join(dir, "stale.jpg")
	staleNeg := filepath.Join(dir, "stale.neg")
	for _, p := range []string{fresh, stale, staleNeg} {
		os.WriteFile(p, []byte("x"), 0644)
	}
	past := time.Now().Add(-31 * 24 * time.Hour)
	os.Chtimes(stale, past, past)
	os.Chtimes(staleNeg, past, past)

	n, err := PruneThumbCache(30 * 24 * time.Hour)
	if err != nil || n != 2 {
		t.Fatalf("want removed=2, got %d err=%v", n, err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh 不应被删")
	}
	if _, err := os.Stat(stale); err == nil {
		t.Fatal("stale 应被删")
	}
}

// TestPruneThumbCache_MissingDirIsNoop verifies pruning a ThumbCacheDir that
// doesn't exist yet (e.g. before any thumbnail was ever generated) returns
// (0, nil) rather than an error.
func TestPruneThumbCache_MissingDirIsNoop(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist")
	old := ThumbCacheDir
	ThumbCacheDir = missing
	defer func() { ThumbCacheDir = old }()

	n, err := PruneThumbCache(30 * 24 * time.Hour)
	if err != nil || n != 0 {
		t.Fatalf("want removed=0, err=nil, got %d err=%v", n, err)
	}
}
