package upload

import (
	"errors"
	"testing"

	"github.com/NimoTech/NimoOS/service/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newTestStore(t *testing.T) *TaskStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.UploadTaskDBModel{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewTaskStore(db)
}

func TestCreateGetAndListActive(t *testing.T) {
	s := newTestStore(t)
	mustCreate := func(id, owner, status string) {
		if err := s.Create(&model.UploadTaskDBModel{ID: id, OwnerUserID: owner, Status: status}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	mustCreate("a", "1", model.UploadStatusUploading)
	mustCreate("b", "1", model.UploadStatusPaused)
	mustCreate("c", "1", model.UploadStatusCompleted) // 不算 active
	mustCreate("d", "2", model.UploadStatusFailed)    // 别的 owner

	got, err := s.Get("a")
	if err != nil || got.OwnerUserID != "1" {
		t.Fatalf("get a: %+v err=%v", got, err)
	}
	if _, err := s.Get("zzz"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}

	active, err := s.ListActiveByOwner("1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("want 2 active for owner 1, got %d", len(active))
	}
}

func TestCancelIdempotent(t *testing.T) {
	s := newTestStore(t)
	_ = s.Create(&model.UploadTaskDBModel{ID: "u", OwnerUserID: "1", Status: model.UploadStatusUploading})
	_ = s.Create(&model.UploadTaskDBModel{ID: "done", OwnerUserID: "1", Status: model.UploadStatusCompleted})

	ok, err := s.Cancel("u", 100)
	if err != nil || !ok {
		t.Fatalf("first cancel: ok=%v err=%v", ok, err)
	}
	got, _ := s.Get("u")
	if got.Status != model.UploadStatusCanceled || got.ExpiresAt != 100 {
		t.Fatalf("after cancel: %+v", got)
	}
	// 再次取消同一个:幂等,返回 false,nil
	ok, err = s.Cancel("u", 200)
	if err != nil || ok {
		t.Fatalf("second cancel should be no-op: ok=%v err=%v", ok, err)
	}
	// 取消不存在的:幂等
	ok, err = s.Cancel("nope", 0)
	if err != nil || ok {
		t.Fatalf("cancel missing should be no-op: ok=%v err=%v", ok, err)
	}
	// 取消已完成的:不动它
	ok, err = s.Cancel("done", 0)
	if err != nil || ok {
		t.Fatalf("cancel completed should be no-op: ok=%v err=%v", ok, err)
	}
	d, _ := s.Get("done")
	if d.Status != model.UploadStatusCompleted {
		t.Fatalf("completed must stay completed, got %s", d.Status)
	}
}
