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
