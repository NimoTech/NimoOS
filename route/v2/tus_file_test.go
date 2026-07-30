package v2

import (
	"os"
	"path/filepath"
	"testing"

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
		// 上传「进入」受保护名的用户数据文件夹现在是允许的(targetPath 不再被拦)。
		{"upload into Documents allowed", map[string]string{"filename": "a.txt", "targetPath": "/DATA/Documents"}, 10, big, false, 0},
		{"upload into Downloads allowed", map[string]string{"filename": "a.txt", "targetPath": "/DATA/admin/Downloads"}, 10, big, false, 0},
		{"empty filename", map[string]string{"filename": "", "targetPath": "/DATA/x"}, 10, big, true, 400},
		{"illegal filename slash", map[string]string{"filename": "a/b.txt", "targetPath": "/DATA/x"}, 10, big, true, 400},
		{"illegal filename dotdot", map[string]string{"filename": "..", "targetPath": "/DATA/x"}, 10, big, true, 400},
		// relativePath 仍拦受保护名:防止「文件夹上传」在根部重建系统特殊文件夹。返回 403。
		{"protected in relpath", map[string]string{"filename": "a.txt", "targetPath": "/DATA/x", "relativePath": "Media/a.txt"}, 10, big, true, 403},
		{"traversal in relpath", map[string]string{"filename": "a.txt", "targetPath": "/DATA/x", "relativePath": "../../etc/a.txt"}, 10, big, true, 400},
		{"empty file", map[string]string{"filename": "a.txt", "targetPath": "/DATA/x"}, 0, big, true, 400},
		// 无人为单文件上限:约 98 GiB 的文件只要磁盘空间足够(×1.05 余量)就放行。
		{"huge file with space allowed", map[string]string{"filename": "a.mov", "targetPath": "/DATA/x"}, 104960272307, uint64(200 * 1024 * 1024 * 1024), false, 0},
		{"insufficient space", map[string]string{"filename": "a.txt", "targetPath": "/DATA/x"}, 1000, 100, true, 413},
		{"huge file insufficient space", map[string]string{"filename": "a.mov", "targetPath": "/DATA/x"}, 104960272307, big, true, 413},
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

func TestStatfsPath(t *testing.T) {
	avail, err := statfsPath(t.TempDir())
	if err != nil {
		t.Fatalf("statfsPath: %v", err)
	}
	if avail == 0 {
		t.Fatal("expected non-zero available bytes for a real filesystem")
	}
	if _, err := statfsPath("/no/such/path/really-should-not-exist"); err == nil {
		t.Fatal("expected error for nonexistent path")
	}
}

// validateFileUploadMetadataForRoot must statfs the *resolved staging root*
// for targetPath, not a hardcoded /DATA — this is the actual fix for the
// cross-volume 413 bug (quota was always checked against /DATA regardless of
// which volume the upload was actually headed to).
func TestValidateFileUploadMetadataForRootUsesResolvedRoot(t *testing.T) {
	volRoot := t.TempDir()
	mounts := []MountEntry{{Mountpoint: volRoot, FSType: "ext4"}}
	mountsFn := func() []MountEntry { return mounts }

	meta := map[string]string{
		"filename":   "a.bin",
		"targetPath": filepath.Join(volRoot, "sub"),
	}
	hook := hookWith(meta, 10)
	_, _, err := validateFileUploadMetadataForRoot(hook, mountsFn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A target path that falls back (no matching mount) must be checked
	// against /DATA — same as the pre-existing statfsDATA behavior. This is a
	// read-only statfs syscall, safe to run for real in tests.
	hookFallback := hookWith(map[string]string{"filename": "a.bin", "targetPath": "/opt/nowhere"}, 10)
	_, _, err = validateFileUploadMetadataForRoot(hookFallback, func() []MountEntry { return nil })
	if err != nil {
		t.Fatalf("unexpected error on fallback path: %v", err)
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

	dest, err := ingestToTarget(staged, target, "sub/a.txt", false)
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
	dest, err := ingestToTarget(staged, target, "a.txt", false)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if filepath.Base(dest) != "a(1).txt" {
		t.Fatalf("dest base = %s want a(1).txt", filepath.Base(dest))
	}
}

// A resumed re-upload of a file that already landed must overwrite the original
// instead of creating a "(1)" duplicate.
func TestIngestToTargetResumedOverwrites(t *testing.T) {
	staging := t.TempDir()
	target := t.TempDir()
	os.WriteFile(filepath.Join(target, "a.txt"), []byte("old"), 0644)
	staged := filepath.Join(staging, "uploadid")
	os.WriteFile(staged, []byte("new"), 0644)
	dest, err := ingestToTarget(staged, target, "a.txt", true)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if filepath.Base(dest) != "a.txt" {
		t.Fatalf("dest base = %s want a.txt (overwrite, no duplicate)", filepath.Base(dest))
	}
	b, _ := os.ReadFile(dest)
	if string(b) != "new" {
		t.Fatalf("content = %q want new", string(b))
	}
	// No "(1)" duplicate must have been created.
	if _, err := os.Stat(filepath.Join(target, "a(1).txt")); !os.IsNotExist(err) {
		t.Fatalf("unexpected duplicate a(1).txt created")
	}
}

func TestIngestToTargetWithPolicy(t *testing.T) {
	mk := func(t *testing.T, dir, name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("skip 已存在则不覆盖", func(t *testing.T) {
		stg := t.TempDir()
		tgt := t.TempDir()
		_ = os.WriteFile(filepath.Join(tgt, "a.txt"), []byte("OLD"), 0644)
		staged := mk(t, stg, "up1", "NEW")
		_ = os.WriteFile(staged+".info", []byte("{}"), 0644)

		dest, skipped, err := ingestToTargetWithPolicy(staged, tgt, "a.txt", "skip")
		if err != nil || !skipped {
			t.Fatalf("want skipped, got dest=%s skipped=%v err=%v", dest, skipped, err)
		}
		b, _ := os.ReadFile(filepath.Join(tgt, "a.txt"))
		if string(b) != "OLD" {
			t.Fatalf("skip must keep old content, got %s", b)
		}
		if _, err := os.Stat(staged); !os.IsNotExist(err) {
			t.Fatal("skip must remove staging file")
		}
	})

	t.Run("overwrite 覆盖同名", func(t *testing.T) {
		stg := t.TempDir()
		tgt := t.TempDir()
		_ = os.WriteFile(filepath.Join(tgt, "a.txt"), []byte("OLD"), 0644)
		staged := mk(t, stg, "up2", "NEW")

		dest, skipped, err := ingestToTargetWithPolicy(staged, tgt, "a.txt", "overwrite")
		if err != nil || skipped {
			t.Fatalf("overwrite err=%v skipped=%v", err, skipped)
		}
		b, _ := os.ReadFile(dest)
		if string(b) != "NEW" {
			t.Fatalf("overwrite must replace, got %s", b)
		}
	})

	t.Run("rename 加序号", func(t *testing.T) {
		stg := t.TempDir()
		tgt := t.TempDir()
		_ = os.WriteFile(filepath.Join(tgt, "a.txt"), []byte("OLD"), 0644)
		staged := mk(t, stg, "up3", "NEW")

		dest, _, err := ingestToTargetWithPolicy(staged, tgt, "a.txt", "rename")
		if err != nil {
			t.Fatalf("rename err=%v", err)
		}
		if filepath.Base(dest) != "a(1).txt" {
			t.Fatalf("rename expected a(1).txt, got %s", filepath.Base(dest))
		}
	})
}

