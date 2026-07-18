package upload

import (
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
	if err := SweepBatches(s, tasks, t.TempDir(), 1000+BatchIdleInterruptSeconds+1); err != nil {
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
	if err := SweepBatches(s, tasks, t.TempDir(), 5000+BatchStagingGraceSeconds+1); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("b1")
	if !got.StagingCleaned {
		t.Fatal("staging should be cleaned after grace")
	}
}
