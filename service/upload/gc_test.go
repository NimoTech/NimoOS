package upload

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NimoTech/NimoOS/common"
	commonUpload "github.com/NimoTech/NimoOS-Common/upload"
)

func writeStagingFile(t *testing.T, dir, id string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, id), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".info"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSweepTasksTieredCleanup(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	now := time.Unix(10_000, 0)
	cfg := commonUpload.GCConfig{StagingDir: dir, PausedTTL: 1000}

	// canceled, already expired → delete staging + delete row
	_ = s.Create(&commonUpload.UploadTask{ID: "cancel1", Status: commonUpload.UploadStatusCanceled, ExpiresAt: 9_000})
	writeStagingFile(t, dir, "cancel1")
	// uploading, already expired (stalled) → downgrade to paused, don't delete staging
	_ = s.Create(&commonUpload.UploadTask{ID: "idle1", Status: commonUpload.UploadStatusUploading, ExpiresAt: 9_000})
	writeStagingFile(t, dir, "idle1")
	// paused, not expired → leave alone
	_ = s.Create(&commonUpload.UploadTask{ID: "keep1", Status: commonUpload.UploadStatusPaused, ExpiresAt: 99_999})
	writeStagingFile(t, dir, "keep1")
	// completed, expires=0 → never enters the candidate set
	_ = s.Create(&commonUpload.UploadTask{ID: "done1", Status: commonUpload.UploadStatusCompleted, ExpiresAt: 0})

	transitioned, deleted, err := commonUpload.SweepTasks(s, cfg, now)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if transitioned != 1 || deleted != 1 {
		t.Fatalf("want transitioned=1 deleted=1, got %d/%d", transitioned, deleted)
	}
	// cancel1 row deleted, staging cleared
	if _, err := s.Get("cancel1"); err == nil {
		t.Fatal("cancel1 row should be deleted")
	}
	if _, err := os.Stat(filepath.Join(dir, "cancel1")); !os.IsNotExist(err) {
		t.Fatal("cancel1 staging should be gone")
	}
	// idle1 downgraded to paused, staging still there, expires reset
	idle, _ := s.Get("idle1")
	if idle.Status != commonUpload.UploadStatusPaused || idle.ExpiresAt != now.Unix()+cfg.PausedTTL {
		t.Fatalf("idle1 should become paused with refreshed expires: %+v", idle)
	}
	if _, err := os.Stat(filepath.Join(dir, "idle1")); err != nil {
		t.Fatal("idle1 staging must remain")
	}
	// keep1 unchanged
	if k, _ := s.Get("keep1"); k.Status != commonUpload.UploadStatusPaused {
		t.Fatal("keep1 must stay paused")
	}
}

// TestDefaultGCConfigStagingDirsWiring pins down DefaultGCConfig's multi-directory
// wiring: StagingDirs must already be wired up (non-nil), and the first element
// must always be the legacy directory (per the enumeration convention in staging_dirs.go).
func TestDefaultGCConfigStagingDirsWiring(t *testing.T) {
	cfg := DefaultGCConfig()
	if cfg.StagingDirs == nil {
		t.Fatal("DefaultGCConfig().StagingDirs must not be nil")
	}
	dirs := cfg.StagingDirs()
	if len(dirs) == 0 || dirs[0] != common.FileUploadStagingDir {
		t.Fatalf("StagingDirs()[0] want legacy dir %q, got %+v", common.FileUploadStagingDir, dirs)
	}
}
