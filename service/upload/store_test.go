package upload

import (
	"errors"
	"testing"

	upload "github.com/NimoTech/NimoOS-Common/upload"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newTestStore(t *testing.T) *TaskStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&upload.UploadTask{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewTaskStore(db)
}

func TestCreateGetAndListActive(t *testing.T) {
	s := newTestStore(t)
	mustCreate := func(id, owner, status string) {
		if err := s.Create(&upload.UploadTask{ID: id, OwnerUserID: owner, Status: status}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	mustCreate("a", "1", upload.UploadStatusUploading)
	mustCreate("b", "1", upload.UploadStatusPaused)
	mustCreate("c", "1", upload.UploadStatusCompleted) // doesn't count as active
	mustCreate("d", "2", upload.UploadStatusFailed)    // different owner

	got, err := s.Get("a")
	if err != nil || got.OwnerUserID != "1" {
		t.Fatalf("get a: %+v err=%v", got, err)
	}
	if _, err := s.Get("zzz"); !errors.Is(err, upload.ErrNotFound) {
		t.Fatalf("expected upload.ErrNotFound, got %v", err)
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
	_ = s.Create(&upload.UploadTask{ID: "u", OwnerUserID: "1", Status: upload.UploadStatusUploading})
	_ = s.Create(&upload.UploadTask{ID: "done", OwnerUserID: "1", Status: upload.UploadStatusCompleted})

	ok, err := upload.Cancel(s, "u", 100)
	if err != nil || !ok {
		t.Fatalf("first cancel: ok=%v err=%v", ok, err)
	}
	got, _ := s.Get("u")
	if got.Status != upload.UploadStatusCanceled || got.ExpiresAt != 100 {
		t.Fatalf("after cancel: %+v", got)
	}
	// Canceling the same one again: idempotent, returns false, nil
	ok, err = upload.Cancel(s, "u", 200)
	if err != nil || ok {
		t.Fatalf("second cancel should be no-op: ok=%v err=%v", ok, err)
	}
	// Canceling one that doesn't exist: idempotent
	ok, err = upload.Cancel(s, "nope", 0)
	if err != nil || ok {
		t.Fatalf("cancel missing should be no-op: ok=%v err=%v", ok, err)
	}
	// Canceling one that's already completed: leave it alone
	ok, err = upload.Cancel(s, "done", 0)
	if err != nil || ok {
		t.Fatalf("cancel completed should be no-op: ok=%v err=%v", ok, err)
	}
	d, _ := s.Get("done")
	if d.Status != upload.UploadStatusCompleted {
		t.Fatalf("completed must stay completed, got %s", d.Status)
	}
}

func TestListDueForGC(t *testing.T) {
	s := newTestStore(t)
	_ = s.Create(&upload.UploadTask{ID: "due1", Status: upload.UploadStatusUploading, ExpiresAt: 100})
	_ = s.Create(&upload.UploadTask{ID: "due2", Status: upload.UploadStatusPaused, ExpiresAt: 50})
	_ = s.Create(&upload.UploadTask{ID: "notdue", Status: upload.UploadStatusUploading, ExpiresAt: 9999})
	_ = s.Create(&upload.UploadTask{ID: "noexpiry", Status: upload.UploadStatusUploading, ExpiresAt: 0})

	due, err := s.ListDueForGC(200)
	if err != nil {
		t.Fatalf("ListDueForGC: %v", err)
	}
	if len(due) != 2 {
		t.Fatalf("want 2 due tasks, got %d", len(due))
	}
	ids := map[string]bool{}
	for _, d := range due {
		ids[d.ID] = true
	}
	if !ids["due1"] || !ids["due2"] {
		t.Fatalf("unexpected due tasks: %v", ids)
	}
}
