package upload

import (
	"os"
	"path/filepath"
	"testing"

	commonUpload "github.com/NimoTech/NimoOS-Common/upload"
)

// pinRootOnlyMounts pins the mount table to just root: orphan detection never
// fires, decoupling sweeper tests that don't care about the orphan fallback
// from the host's real mount layout (fixtures' /DATA/Media may not exist on another machine).
func pinRootOnlyMounts(t *testing.T) {
	t.Helper()
	orig := listMountPoints
	listMountPoints = func() []string { return []string{"/"} }
	t.Cleanup(func() { listMountPoints = orig })
}

// Sweeper rules: timeout → interrupted / interrupted past grace → clear
// staging / orphan → abandoned / terminal state → deleted.
// Passing nil for the TaskStore part would panic, so the test also builds a real TaskStore (an empty table is fine).
func TestSweepBatchesIdleToInterrupted(t *testing.T) {
	pinRootOnlyMounts(t)
	db := openBatchTestDB(t)
	if err := db.AutoMigrate(&commonUpload.UploadTask{}); err != nil {
		t.Fatal(err)
	}
	s := NewBatchStore(db)
	tasks := NewTaskStore(db)
	newTestBatch(t, s, "b1", "a.jpg")
	// last_progress_at=1000, more than 120s without progress → interrupted
	_ = s.TouchProgress("b1", 1000)
	if err := SweepBatches(s, tasks, []string{t.TempDir()}, 1000+BatchIdleInterruptSeconds+1); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("b1")
	if got.Status != BatchStatusInterrupted {
		t.Fatalf("want interrupted, got %s", got.Status)
	}
	// Grace period not yet reached: staging_cleaned is still false
	if got.StagingCleaned {
		t.Fatal("staging must not be cleaned before grace")
	}
}

func TestSweepBatchesGraceCleansStaging(t *testing.T) {
	pinRootOnlyMounts(t)
	db := openBatchTestDB(t)
	_ = db.AutoMigrate(&commonUpload.UploadTask{})
	s := NewBatchStore(db)
	tasks := NewTaskStore(db)
	newTestBatch(t, s, "b1", "a.jpg")
	_ = s.SetInterrupted("b1", 5000)
	if err := SweepBatches(s, tasks, []string{t.TempDir()}, 5000+BatchStagingGraceSeconds+1); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("b1")
	if !got.StagingCleaned {
		t.Fatal("staging should be cleaned after grace")
	}
}

// TestTargetOrphaned orphan detection: only judged orphaned when the target is
// missing AND the volume it's on is actually mounted; not judged dead when the
// volume isn't mounted (USB unplugged / RAID not yet assembled / slow cold-boot enumeration).
func TestTargetOrphaned(t *testing.T) {
	vol := t.TempDir() // simulates the root of a mounted volume
	exists := filepath.Join(vol, "still-here")
	if err := os.MkdirAll(exists, 0755); err != nil {
		t.Fatal(err)
	}
	mounts := []string{"/", vol}

	cases := []struct {
		name   string
		target string
		mounts []string
		want   bool
	}{
		{"target exists: not judged", exists, mounts, false},
		{"volume mounted and target missing: orphan", filepath.Join(vol, "deleted"), mounts, true},
		{"only root covers it: not judged (volume may not be mounted)", "/no-such-volume/deleted", []string{"/"}, false},
		{"no covering mount point at all: not judged", filepath.Join(vol, "deleted"), []string{"/"}, false},
	}
	for _, c := range cases {
		if got := targetOrphaned(c.target, c.mounts); got != c.want {
			t.Errorf("%s: targetOrphaned(%q)=%v want %v", c.name, c.target, got, c.want)
		}
	}
}

// TestSweepBatchesOrphanAutoAbandoned tests the orphan fallback end-to-end: an
// interrupted batch whose target directory was deleted is auto-abandoned and
// its row reclaimed as terminal state in the same round; batches whose target
// is still alive or whose volume isn't mounted are unaffected.
func TestSweepBatchesOrphanAutoAbandoned(t *testing.T) {
	db := openBatchTestDB(t)
	_ = db.AutoMigrate(&commonUpload.UploadTask{})
	s := NewBatchStore(db)
	tasks := NewTaskStore(db)

	vol := t.TempDir()
	orig := listMountPoints
	listMountPoints = func() []string { return []string{"/", vol} }
	t.Cleanup(func() { listMountPoints = orig })

	mk := func(id, target string) {
		b := &UploadBatch{ID: id, OwnerUserID: "u1", TargetPath: target,
			Status: BatchStatusActive, Total: 1}
		if err := s.Create(b, []UploadBatchItem{{BatchID: id, RelativePath: "a.jpg", Size: 1}}); err != nil {
			t.Fatal(err)
		}
		_ = s.SetInterrupted(id, 5000)
	}
	alive := filepath.Join(vol, "alive")
	if err := os.MkdirAll(alive, 0755); err != nil {
		t.Fatal(err)
	}
	mk("orphan", filepath.Join(vol, "deleted-dir"))
	mk("alive", alive)
	mk("unmounted", "/no-such-volume/dir")

	if err := SweepBatches(s, tasks, []string{t.TempDir()}, 5001); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("orphan"); err == nil {
		t.Fatal("orphan batch should be auto-abandoned and deleted")
	}
	if b, err := s.Get("alive"); err != nil || b.Status != BatchStatusInterrupted {
		t.Fatalf("alive batch must survive: %v", err)
	}
	if b, err := s.Get("unmounted"); err != nil || b.Status != BatchStatusInterrupted {
		t.Fatalf("unmounted-volume batch must survive: %v", err)
	}
}

// Uploads routed to a per-volume staging dir (task ID carries a volume prefix,
// see route/v2/tus_routing_store.go) must still get their staging files
// cleaned up. SweepBatches now tries every known staging directory rather
// than assuming a single one, so this must work even when the actual file
// lives in the *second* directory of the list.
func TestSweepBatchesCleansAcrossMultipleStagingDirs(t *testing.T) {
	pinRootOnlyMounts(t)
	db := openBatchTestDB(t)
	_ = db.AutoMigrate(&commonUpload.UploadTask{})
	s := NewBatchStore(db)
	tasks := NewTaskStore(db)
	newTestBatch(t, s, "b1", "a.jpg")
	taskID := "hexroot~subid1"
	if err := tasks.Create(&commonUpload.UploadTask{
		ID: taskID, OwnerUserID: "u1", BatchID: "b1", Status: commonUpload.UploadStatusUploading,
	}); err != nil {
		t.Fatal(err)
	}

	legacyDir := t.TempDir()
	volumeDir := t.TempDir()
	// The task's staged file actually lives on the volume dir, not the legacy one.
	if err := os.WriteFile(filepath.Join(volumeDir, taskID), []byte("d"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(volumeDir, taskID+".info"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	_ = s.SetInterrupted("b1", 5000)
	if err := SweepBatches(s, tasks, []string{legacyDir, volumeDir}, 5000+BatchStagingGraceSeconds+1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(volumeDir, taskID)); !os.IsNotExist(err) {
		t.Fatal("staged file on the volume dir should have been removed")
	}
	if _, err := os.Stat(filepath.Join(volumeDir, taskID+".info")); !os.IsNotExist(err) {
		t.Fatal("staged .info file on the volume dir should have been removed")
	}
}
