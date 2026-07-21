package upload

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func openBatchTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "test.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&UploadBatch{}, &UploadBatchItem{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func newTestBatch(t *testing.T, s *BatchStore, id string, rels ...string) {
	t.Helper()
	items := make([]UploadBatchItem, 0, len(rels))
	for _, r := range rels {
		items = append(items, UploadBatchItem{BatchID: id, RelativePath: r, Size: 100})
	}
	b := &UploadBatch{ID: id, OwnerUserID: "u1", TargetPath: "/DATA/Media",
		Status: BatchStatusActive, Total: len(rels), ExpiresAt: 9999999999}
	if err := s.Create(b, items); err != nil {
		t.Fatalf("create: %v", err)
	}
}

func TestBatchCreateIdempotent(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	newTestBatch(t, s, "b1", "a.jpg", "sub/b.jpg")
	// 同 id 再建:不报错、不重复插 items
	b := &UploadBatch{ID: "b1", OwnerUserID: "u1", TargetPath: "/DATA/Media",
		Status: BatchStatusActive, Total: 2, ExpiresAt: 9999999999}
	if err := s.Create(b, []UploadBatchItem{{BatchID: "b1", RelativePath: "a.jpg", Size: 100}}); err != nil {
		t.Fatalf("second create should be idempotent: %v", err)
	}
	missing, _ := s.MissingItems("b1")
	if len(missing) != 2 {
		t.Fatalf("want 2 missing, got %d", len(missing))
	}
}

func TestMarkItemDoneCompletesBatch(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	newTestBatch(t, s, "b1", "a.jpg", "b.jpg")
	if err := s.MarkItemDone("b1", "a.jpg", 1000); err != nil {
		t.Fatal(err)
	}
	// 重复标记同一文件不重复计数
	_ = s.MarkItemDone("b1", "a.jpg", 1001)
	got, _ := s.Get("b1")
	if got.Done != 1 || got.Status != BatchStatusActive {
		t.Fatalf("after one done: done=%d status=%s", got.Done, got.Status)
	}
	if err := s.MarkItemDone("b1", "b.jpg", 1002); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get("b1")
	if got.Done != 2 || got.Status != BatchStatusCompleted {
		t.Fatalf("after all done: done=%d status=%s", got.Done, got.Status)
	}
}

// TestMarkItemDoneRepeatedDoesNotDoubleCount 防回归:同一 item 重复 MarkItemDone
// 多次(模拟前端重试/重复上报),done 计数只应自增一次,不能被 gorm.Expr("done + 1")
// 在 RowsAffected==0 时重复触发。
func TestMarkItemDoneRepeatedDoesNotDoubleCount(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	newTestBatch(t, s, "b1", "a.jpg", "b.jpg", "c.jpg")
	for i := 0; i < 5; i++ {
		if err := s.MarkItemDone("b1", "a.jpg", int64(1000+i)); err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
	}
	got, _ := s.Get("b1")
	if got.Done != 1 {
		t.Fatalf("done should stay 1 after 5 repeated MarkItemDone calls, got %d", got.Done)
	}
	if got.Status != BatchStatusActive {
		t.Fatalf("status should stay active, got %s", got.Status)
	}
}

func TestInterruptedRevertsToActiveOnProgress(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	newTestBatch(t, s, "b1", "a.jpg")
	if err := s.SetInterrupted("b1", 2000); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchProgress("b1", 3000); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("b1")
	if got.Status != BatchStatusActive || got.LastProgressAt != 3000 {
		t.Fatalf("want active/3000, got %s/%d", got.Status, got.LastProgressAt)
	}
}

func TestMarkItemDoneRevertsInterruptedToActive(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	newTestBatch(t, s, "b1", "a.jpg", "b.jpg")
	if err := s.SetInterrupted("b1", 2000); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkItemDone("b1", "a.jpg", 3000); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("b1")
	if got.Status != BatchStatusActive || got.Done != 1 {
		t.Fatalf("want active/done=1, got %s/%d", got.Status, got.Done)
	}
}

func TestBrokenChildren(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	newTestBatch(t, s, "b1", "备份/2024/x.jpg", "备份/2025/y.jpg", "loose.jpg")
	_ = s.MarkItemDone("b1", "备份/2024/x.jpg", 1000) // 2024 已传全
	_ = s.SetInterrupted("b1", 2000)

	// /DATA/Media 下:子条目「备份」(缺 2025/y.jpg)与「loose.jpg」命中
	m, err := s.BrokenChildren("/DATA/Media")
	if err != nil {
		t.Fatal(err)
	}
	if m["备份"] != "b1" || m["loose.jpg"] != "b1" || len(m) != 2 {
		t.Fatalf("unexpected map: %#v", m)
	}
	// 钻进一层:/DATA/Media/备份 下只有「2025」命中,2024 不命中
	m, _ = s.BrokenChildren("/DATA/Media/备份")
	if m["2025"] != "b1" || len(m) != 1 {
		t.Fatalf("unexpected map: %#v", m)
	}
	// active(未中断)批次不产生角标
	_ = s.TouchProgress("b1", 3000)
	m, _ = s.BrokenChildren("/DATA/Media")
	if len(m) != 0 {
		t.Fatalf("active batch should not badge: %#v", m)
	}
}

// TestMarkItemDoneAcrossBatchesClearsOldInterruptedBatch 覆盖普通补传场景:
// 旧批次 interrupted 缺 2 项,新批次(不同 id)完成同名 2 项后,旧批次应隐式
// 销账并转 completed(角标随之消失)。
func TestMarkItemDoneAcrossBatchesClearsOldInterruptedBatch(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	newTestBatch(t, s, "old", "a.jpg", "b.jpg", "c.jpg")
	_ = s.MarkItemDone("old", "a.jpg", 1000) // 旧批次已传完 1/3
	_ = s.SetInterrupted("old", 2000)        // 旧批次挂起,缺 b.jpg / c.jpg

	newTestBatch(t, s, "new", "b.jpg", "c.jpg") // 用户不经弹窗直接重传缺失的两个文件

	if err := s.MarkItemDoneAcrossBatches("/DATA/Media", "b.jpg", 3000); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkItemDoneAcrossBatches("/DATA/Media", "c.jpg", 3001); err != nil {
		t.Fatal(err)
	}

	old, _ := s.Get("old")
	if old.Status != BatchStatusCompleted || old.Done != 3 {
		t.Fatalf("old batch want completed/done=3, got %s/%d", old.Status, old.Done)
	}
	newB, _ := s.Get("new")
	if newB.Status != BatchStatusCompleted || newB.Done != 2 {
		t.Fatalf("new batch want completed/done=2, got %s/%d", newB.Status, newB.Done)
	}
}

// TestMarkItemDoneAcrossBatchesIgnoresOtherTargetPath 不同 targetPath 的同名
// 批次不应被误销账。
func TestMarkItemDoneAcrossBatchesIgnoresOtherTargetPath(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	newTestBatch(t, s, "old", "a.jpg")
	_ = s.SetInterrupted("old", 2000)

	other := &UploadBatch{ID: "other", OwnerUserID: "u1", TargetPath: "/DATA/Other",
		Status: BatchStatusInterrupted, Total: 1, ExpiresAt: 9999999999}
	if err := s.Create(other, []UploadBatchItem{{BatchID: "other", RelativePath: "a.jpg", Size: 100}}); err != nil {
		t.Fatal(err)
	}

	if err := s.MarkItemDoneAcrossBatches("/DATA/Media", "a.jpg", 3000); err != nil {
		t.Fatal(err)
	}

	got, _ := s.Get("other")
	if got.Status != BatchStatusInterrupted || got.Done != 0 {
		t.Fatalf("other targetPath batch should be untouched, got %s/%d", got.Status, got.Done)
	}
	oldB, _ := s.Get("old")
	if oldB.Status != BatchStatusCompleted || oldB.Done != 1 {
		t.Fatalf("matching batch should be cleared, got %s/%d", oldB.Status, oldB.Done)
	}
}

// TestMarkItemDoneAcrossBatchesIdempotent 旧批次里该项已 done 时重复调用应
// 保持幂等,不重复计数、不报错。
func TestMarkItemDoneAcrossBatchesIdempotent(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	newTestBatch(t, s, "old", "a.jpg", "b.jpg")
	_ = s.MarkItemDone("old", "a.jpg", 1000)
	_ = s.SetInterrupted("old", 2000)

	for i := 0; i < 3; i++ {
		if err := s.MarkItemDoneAcrossBatches("/DATA/Media", "a.jpg", int64(3000+i)); err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
	}
	got, _ := s.Get("old")
	if got.Done != 1 || got.Status != BatchStatusInterrupted {
		t.Fatalf("already-done item should stay idempotent (no-op), got done=%d status=%s", got.Done, got.Status)
	}
}

func TestDeleteExpired(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	newTestBatch(t, s, "b1", "a.jpg")
	s.db.Model(&UploadBatch{}).Where("id = ?", "b1").Update("expires_at", 100)
	n, err := s.DeleteExpired(200)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, err := s.Get("b1"); err == nil {
		t.Fatal("batch should be gone")
	}
	var cnt int64
	s.db.Model(&UploadBatchItem{}).Where("batch_id = ?", "b1").Count(&cnt)
	if cnt != 0 {
		t.Fatal("items should be gone")
	}
}
