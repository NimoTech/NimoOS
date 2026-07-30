package service

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
)

// statfsFree returns the raw Bavail*Bsize free-byte count for path, for use as a
// test oracle independent of checkTargetFreeSpace's own implementation.
func statfsFree(t *testing.T, path string) int64 {
	t.Helper()
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		t.Fatalf("statfs(%s): %v", path, err)
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

// need=0 must always pass, regardless of the probe path's actual free space.
func TestCheckTargetFreeSpace_NeedZeroAlwaysPasses(t *testing.T) {
	tmpDir := t.TempDir()
	if err := checkTargetFreeSpace(tmpDir, 0); err != nil {
		t.Fatalf("need=0 should always pass, got error: %v", err)
	}
}

// need = free*2 must fail with an "insufficient space" error that reports both the
// free and required byte counts (two numbers).
func TestCheckTargetFreeSpace_InsufficientSpace(t *testing.T) {
	tmpDir := t.TempDir()
	free := statfsFree(t, tmpDir)

	err := checkTargetFreeSpace(tmpDir, free*2)
	if err == nil {
		t.Fatalf("expected error when need (%d) far exceeds free space (%d), got nil", free*2, free)
	}
	if !strings.Contains(err.Error(), "insufficient space") {
		t.Fatalf("expected error to mention insufficient space, got: %v", err)
	}
	numbers := regexp.MustCompile(`[0-9]+`).FindAllString(err.Error(), -1)
	if len(numbers) < 2 {
		t.Fatalf("expected error message to contain at least two numbers, got: %v (message: %s)", numbers, err.Error())
	}
}

// When probePath does not exist, checkTargetFreeSpace must walk up to the nearest
// existing ancestor directory and statfs that instead of failing outright.
func TestCheckTargetFreeSpace_ClimbsToNearestExistingParent(t *testing.T) {
	tmpDir := t.TempDir()
	nested := filepath.Join(tmpDir, "not", "created", "yet")

	// A modest need, comfortably inside the real free space of tmpDir (checked
	// dynamically so this test isn't flaky on constrained disks), so that a
	// successful climb-and-statfs is the only way this passes.
	free := statfsFree(t, tmpDir)
	need := free / 4

	if err := checkTargetFreeSpace(nested, need); err != nil {
		t.Fatalf("expected climb to nearest existing parent (%s) to succeed, got error: %v", tmpDir, err)
	}

	// Sanity check: probing tmpDir directly with the same need must agree.
	if err := checkTargetFreeSpace(tmpDir, need); err != nil {
		t.Fatalf("expected direct probe on existing dir to succeed, got error: %v", err)
	}
}

// When probePath is itself a symlink (as migration anchors like /DATA/AppData
// are), probeDirForStatfs must not trust it — os.Stat/syscall.Statfs would
// silently follow the symlink to whatever it points at (e.g. the external
// source disk during a system-restore), statfs-ing the wrong filesystem.
// Instead it must climb to the symlink's parent directory, which is the real
// directory that the sibling "anchor+.migrating" staging path is actually
// written under.
func TestProbeDirForStatfs_DoesNotTrustSymlink(t *testing.T) {
	tmpDir := t.TempDir()

	realDir := filepath.Join(tmpDir, "A")
	if err := os.Mkdir(realDir, 0755); err != nil {
		t.Fatalf("failed to create real dir: %v", err)
	}

	link := filepath.Join(tmpDir, "L")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	got := probeDirForStatfs(link)
	want := tmpDir // link's parent directory
	if got != want {
		t.Fatalf("probeDirForStatfs(%s) = %s, want %s (link's parent, not the symlink itself or its target %s)",
			link, got, want, realDir)
	}
}
