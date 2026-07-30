package upload

import (
	"os"
	"path/filepath"
	"testing"

	commonUpload "github.com/NimoTech/NimoOS-Common/upload"
)

// 扫描器三条规则:超时判中断 / 中断满宽限清 staging / 过期删除。
// TaskStore 部分传 nil 会 panic,所以测试里也建真实 TaskStore(空表即可)。
func TestSweepBatchesIdleToInterrupted(t *testing.T) {
	db := openBatchTestDB(t)
	if err := db.AutoMigrate(&commonUpload.UploadTask{}); err != nil {
		t.Fatal(err)
	}
	s := NewBatchStore(db)
	tasks := NewTaskStore(db)
	newTestBatch(t, s, "b1", "a.jpg")
	// last_progress_at=1000,超过 120s 无进度 → interrupted
	_ = s.TouchProgress("b1", 1000)
	if err := SweepBatches(s, tasks, []string{t.TempDir()}, 1000+BatchIdleInterruptSeconds+1); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("b1")
	if got.Status != BatchStatusInterrupted {
		t.Fatalf("want interrupted, got %s", got.Status)
	}
	// 未到宽限期:staging_cleaned 仍 false
	if got.StagingCleaned {
		t.Fatal("staging must not be cleaned before grace")
	}
}

func TestSweepBatchesGraceCleansStaging(t *testing.T) {
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

// Uploads routed to a per-volume staging dir (task ID carries a volume prefix,
// see route/v2/tus_routing_store.go) must still get their staging files
// cleaned up. SweepBatches now tries every known staging directory rather
// than assuming a single one, so this must work even when the actual file
// lives in the *second* directory of the list.
func TestSweepBatchesCleansAcrossMultipleStagingDirs(t *testing.T) {
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
