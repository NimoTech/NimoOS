package upload

import (
	"os"
	"path/filepath"
	"testing"

	commonUpload "github.com/NimoTech/NimoOS-Common/upload"
)

// pinRootOnlyMounts 把挂载表钉成只有根:孤儿判定永不触发,使不关心孤儿兜底的
// 扫描器测试与宿主机真实挂载布局解耦(fixtures 的 /DATA/Media 在别的机器上未必存在)。
func pinRootOnlyMounts(t *testing.T) {
	t.Helper()
	orig := listMountPoints
	listMountPoints = func() []string { return []string{"/"} }
	t.Cleanup(func() { listMountPoints = orig })
}

// 扫描器规则:超时判中断 / 中断满宽限清 staging / 孤儿放弃 / 终态删除。
// TaskStore 部分传 nil 会 panic,所以测试里也建真实 TaskStore(空表即可)。
func TestSweepBatchesIdleToInterrupted(t *testing.T) {
	pinRootOnlyMounts(t)
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

// TestTargetOrphaned 孤儿判定:目标缺失且所在卷确实在挂载状态才判孤儿;
// 卷未挂载(USB 拔了/RAID 未装配/冷启动枚举慢)时不判死。
func TestTargetOrphaned(t *testing.T) {
	vol := t.TempDir() // 模拟已挂载的卷根
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
		{"目标存在:不判", exists, mounts, false},
		{"卷已挂载且目标缺失:孤儿", filepath.Join(vol, "deleted"), mounts, true},
		{"覆盖挂载点只有根:不判(卷可能没挂)", "/no-such-volume/deleted", []string{"/"}, false},
		{"无任何覆盖挂载点:不判", filepath.Join(vol, "deleted"), []string{"/"}, false},
	}
	for _, c := range cases {
		if got := targetOrphaned(c.target, c.mounts); got != c.want {
			t.Errorf("%s: targetOrphaned(%q)=%v want %v", c.name, c.target, got, c.want)
		}
	}
}

// TestSweepBatchesOrphanAutoAbandoned 孤儿兜底端到端:目标目录被删的 interrupted
// 批次自动放弃并在同一轮被终态回收删行;目标健在/卷未挂载的批次不受影响。
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
