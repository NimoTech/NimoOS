package v2

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tus/tusd/v2/pkg/handler"
)

func mockFree(avail uint64) freeBytesFn {
	return func() (uint64, error) { return avail, nil }
}

func hookWith(meta map[string]string, size int64) handler.HookEvent {
	return handler.HookEvent{Upload: handler.FileInfo{MetaData: meta, Size: size}}
}

func TestValidateFileUploadMetadata(t *testing.T) {
	big := uint64(100 * 1024 * 1024 * 1024) // 100 GB free

	cases := []struct {
		name    string
		meta    map[string]string
		size    int64
		free    uint64
		wantErr bool
		wantSC  int
	}{
		{"ok", map[string]string{"filename": "a.txt", "targetPath": "/DATA/Documents-x"}, 10, big, false, 0},
		{"empty filename", map[string]string{"filename": "", "targetPath": "/DATA/x"}, 10, big, true, 0},
		{"illegal filename slash", map[string]string{"filename": "a/b.txt", "targetPath": "/DATA/x"}, 10, big, true, 0},
		{"illegal filename dotdot", map[string]string{"filename": "..", "targetPath": "/DATA/x"}, 10, big, true, 0},
		{"protected folder", map[string]string{"filename": "a.txt", "targetPath": "/DATA/Documents"}, 10, big, true, 0},
		{"protected in relpath", map[string]string{"filename": "a.txt", "targetPath": "/DATA/x", "relativePath": "Media/a.txt"}, 10, big, true, 0},
		{"traversal in relpath", map[string]string{"filename": "a.txt", "targetPath": "/DATA/x", "relativePath": "../../etc/a.txt"}, 10, big, true, 0},
		{"empty file", map[string]string{"filename": "a.txt", "targetPath": "/DATA/x"}, 0, big, true, 0},
		{"too big", map[string]string{"filename": "a.txt", "targetPath": "/DATA/x"}, FileUploadMaxSizeForTest() + 1, big, true, 0},
		{"insufficient space", map[string]string{"filename": "a.txt", "targetPath": "/DATA/x"}, 1000, 100, true, 413},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, _, err := validateFileUploadMetadataWithQuota(hookWith(c.meta, c.size), mockFree(c.free))
			if c.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.wantSC != 0 && resp.StatusCode != c.wantSC {
				t.Fatalf("status = %d, want %d", resp.StatusCode, c.wantSC)
			}
		})
	}
}

func TestUniqueDestPath(t *testing.T) {
	dir := t.TempDir()
	p := uniqueDestPath(filepath.Join(dir, "a.txt"))
	if filepath.Base(p) != "a.txt" {
		t.Fatalf("got %s want a.txt", filepath.Base(p))
	}
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0644)
	p = uniqueDestPath(filepath.Join(dir, "a.txt"))
	if filepath.Base(p) != "a(1).txt" {
		t.Fatalf("got %s want a(1).txt", filepath.Base(p))
	}
	os.WriteFile(filepath.Join(dir, "a(1).txt"), []byte("x"), 0644)
	p = uniqueDestPath(filepath.Join(dir, "a.txt"))
	if filepath.Base(p) != "a(2).txt" {
		t.Fatalf("got %s want a(2).txt", filepath.Base(p))
	}
	os.WriteFile(filepath.Join(dir, "README"), []byte("x"), 0644)
	p = uniqueDestPath(filepath.Join(dir, "README"))
	if filepath.Base(p) != "README(1)" {
		t.Fatalf("got %s want README(1)", filepath.Base(p))
	}
}

func TestIngestToTarget(t *testing.T) {
	staging := t.TempDir()
	target := t.TempDir()
	staged := filepath.Join(staging, "uploadid")
	os.WriteFile(staged, []byte("hello"), 0644)
	os.WriteFile(staged+".info", []byte("{}"), 0644)

	dest, err := ingestToTarget(staged, target, "sub/a.txt")
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if dest != filepath.Join(target, "sub", "a.txt") {
		t.Fatalf("dest = %s", dest)
	}
	b, _ := os.ReadFile(dest)
	if string(b) != "hello" {
		t.Fatalf("content = %q", string(b))
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("staged file not removed")
	}
	if _, err := os.Stat(staged + ".info"); !os.IsNotExist(err) {
		t.Fatalf(".info not removed")
	}
}

func TestIngestToTargetSameName(t *testing.T) {
	staging := t.TempDir()
	target := t.TempDir()
	os.WriteFile(filepath.Join(target, "a.txt"), []byte("old"), 0644)
	staged := filepath.Join(staging, "uploadid")
	os.WriteFile(staged, []byte("new"), 0644)
	dest, err := ingestToTarget(staged, target, "a.txt")
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if filepath.Base(dest) != "a(1).txt" {
		t.Fatalf("dest base = %s want a(1).txt", filepath.Base(dest))
	}
}

func TestSweepStaging(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	oldF := filepath.Join(dir, "old")
	os.WriteFile(oldF, []byte("x"), 0644)
	os.WriteFile(oldF+".info", []byte("{}"), 0644)
	old := now.Add(-8 * 24 * time.Hour)
	os.Chtimes(oldF, old, old)

	newF := filepath.Join(dir, "new")
	os.WriteFile(newF, []byte("x"), 0644)

	removed := sweepStaging(dir, 7*24*60*60, now)
	if removed != 1 {
		t.Fatalf("removed = %d want 1", removed)
	}
	if _, err := os.Stat(oldF); !os.IsNotExist(err) {
		t.Fatalf("old file should be gone")
	}
	if _, err := os.Stat(oldF + ".info"); !os.IsNotExist(err) {
		t.Fatalf("old .info should be gone")
	}
	if _, err := os.Stat(newF); err != nil {
		t.Fatalf("new file should remain")
	}
}
