package upload

import (
	"os"
	"path/filepath"
	"testing"
)

// StagingDirs must enumerate the legacy dir plus any per-volume staging dirs
// that already exist one level under mediaRoot (no recursion), for the
// batch sweeper / GC to try cleaning up across every volume that ever staged
// an upload — not just /DATA.
func TestStagingDirs(t *testing.T) {
	legacy := t.TempDir()
	media := t.TempDir()

	// Volume with an existing staging dir -> included.
	stagingA := filepath.Join(media, "RAID_0", ".system_data", "file-tus-staging")
	if err := os.MkdirAll(stagingA, 0700); err != nil {
		t.Fatal(err)
	}

	// Volume that exists but never staged anything -> its staging dir does
	// not exist -> excluded.
	if err := os.MkdirAll(filepath.Join(media, "Empty"), 0755); err != nil {
		t.Fatal(err)
	}

	// A plain file directly under mediaRoot must not trip up the scan.
	if err := os.WriteFile(filepath.Join(media, "not-a-dir"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	// A staging dir nested two levels down must NOT be picked up (readdir
	// mediaRoot is one level only, no recursion).
	nested := filepath.Join(media, "RAID_0", "sub", ".system_data", "file-tus-staging")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}

	got := StagingDirs(legacy, media)

	want := map[string]bool{legacy: true, stagingA: true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want dirs matching %v", got, want)
	}
	for _, d := range got {
		if !want[d] {
			t.Fatalf("unexpected dir in result: %s (full: %v)", d, got)
		}
	}
}

func TestStagingDirsMediaRootMissing(t *testing.T) {
	legacy := t.TempDir()
	got := StagingDirs(legacy, filepath.Join(legacy, "no-such-media-root"))
	if len(got) != 1 || got[0] != legacy {
		t.Fatalf("got %v, want [%s]", got, legacy)
	}
}
