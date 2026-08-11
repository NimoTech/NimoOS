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
		Status: BatchStatusActive, Total: len(rels)}
	if err := s.Create(b, items); err != nil {
		t.Fatalf("create: %v", err)
	}
}

func TestBatchCreateIdempotent(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	newTestBatch(t, s, "b1", "a.jpg", "sub/b.jpg")
	// Creating again with the same id: no error, items not inserted twice
	b := &UploadBatch{ID: "b1", OwnerUserID: "u1", TargetPath: "/DATA/Media",
		Status: BatchStatusActive, Total: 2}
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
	// Marking the same file done again doesn't count twice
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

// TestMarkItemDoneRepeatedDoesNotDoubleCount guards against a regression: when
// MarkItemDone is called repeatedly for the same item (simulating
// frontend retries/duplicate reports), the done count should only increment
// once, and must not be triggered repeatedly by gorm.Expr("done + 1") when RowsAffected==0.
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
	if err := s.SetInterrupted("b1", 2000, BatchInterruptSourceTimeout); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchProgress("b1", 3000); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("b1")
	if got.Status != BatchStatusActive || got.LastProgressAt != 3000 {
		t.Fatalf("want active/3000, got %s/%d", got.Status, got.LastProgressAt)
	}
	if got.InterruptSource != "" {
		t.Fatalf("revival should clear interrupt_source, got %q", got.InterruptSource)
	}
}

func TestMarkItemDoneRevertsInterruptedToActive(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	newTestBatch(t, s, "b1", "a.jpg", "b.jpg")
	if err := s.SetInterrupted("b1", 2000, BatchInterruptSourceTimeout); err != nil {
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

// TestSignalInterruptNotRevivedByLateCompletion guards the race found in
// real-device acceptance (2026-08-11): the pagehide interrupt signal landed
// and marked the batch interrupted, but an already-received upload finished in
// the same second and MarkItemDone's "timeout misjudgment is reversible"
// clause flipped the batch straight back to active — so the badge vanished
// until the idle sweeper re-judged it 120s+ later. A signal interrupt means
// the page is confirmed gone; late completions must count as done but must
// not revive the batch.
func TestSignalInterruptNotRevivedByLateCompletion(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	newTestBatch(t, s, "b1", "a.jpg", "b.jpg", "c.jpg")
	if err := s.SetInterrupted("b1", 2000, BatchInterruptSourceSignal); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkItemDone("b1", "a.jpg", 2000); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("b1")
	if got.Status != BatchStatusInterrupted || got.Done != 1 {
		t.Fatalf("late completion must count but not revive: want interrupted/done=1, got %s/%d", got.Status, got.Done)
	}
	// Completing everything must still finish the batch (refill flow) even
	// though the intermediate revival is blocked.
	_ = s.MarkItemDone("b1", "b.jpg", 2001)
	if err := s.MarkItemDone("b1", "c.jpg", 2002); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get("b1")
	if got.Status != BatchStatusCompleted || got.Done != 3 {
		t.Fatalf("full completion must still transition: want completed/done=3, got %s/%d", got.Status, got.Done)
	}
}

// TestSignalInterruptNotRevivedByTouchProgress: same rule for the chunk-progress
// path — an in-flight tus PATCH racing the window-close signal must not bring
// the batch back to active either.
func TestSignalInterruptNotRevivedByTouchProgress(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	newTestBatch(t, s, "b1", "a.jpg")
	if err := s.SetInterrupted("b1", 2000, BatchInterruptSourceSignal); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchProgress("b1", 3000); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("b1")
	if got.Status != BatchStatusInterrupted {
		t.Fatalf("signal interrupt must stick through TouchProgress, got %s", got.Status)
	}
}

func TestBrokenChildren(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	newTestBatch(t, s, "b1", "备份/2024/x.jpg", "备份/2025/y.jpg", "loose.jpg")
	_ = s.MarkItemDone("b1", "备份/2024/x.jpg", 1000) // 2024 fully uploaded
	_ = s.SetInterrupted("b1", 2000, BatchInterruptSourceTimeout)

	// Under /DATA/Media: child entries "备份" (missing 2025/y.jpg) and "loose.jpg" hit
	m, err := s.BrokenChildren("/DATA/Media", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if m["备份"] != "b1" || m["loose.jpg"] != "b1" || len(m) != 2 {
		t.Fatalf("unexpected map: %#v", m)
	}
	// Drilling in one level: under /DATA/Media/备份 only "2025" hits, 2024 doesn't
	m, _ = s.BrokenChildren("/DATA/Media/备份", "u1")
	if m["2025"] != "b1" || len(m) != 1 {
		t.Fatalf("unexpected map: %#v", m)
	}
	// an active (non-interrupted) batch produces no badge
	_ = s.TouchProgress("b1", 3000)
	m, _ = s.BrokenChildren("/DATA/Media", "u1")
	if len(m) != 0 {
		t.Fatalf("active batch should not badge: %#v", m)
	}
}

// TestBrokenChildrenOwnerScoped guards against a regression (real-machine
// incident): badge visibility must match the owner check used by the
// batch-detail/abandon endpoints. An interrupted batch belonging to someone
// else (or a legacy test batch with an empty owner) must not inject a badge
// for the current user — otherwise it's visible but unclickable, GET/abandon
// both 404, the frontend falsely reports a server error, and the badge can never be cleared.
func TestBrokenChildrenOwnerScoped(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	newTestBatch(t, s, "b1", "loose.jpg") // owner=u1
	_ = s.SetInterrupted("b1", 2000, BatchInterruptSourceTimeout)

	// another user cannot see u1's badge
	m, err := s.BrokenChildren("/DATA/Media", "u2")
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 0 {
		t.Fatalf("other owner should see no badge: %#v", m)
	}
	// empty owner (localhost bypasses JWT) also cannot see u1's badge
	m, _ = s.BrokenChildren("/DATA/Media", "")
	if len(m) != 0 {
		t.Fatalf("empty owner should see no badge: %#v", m)
	}
	// the owner can still see their own
	m, _ = s.BrokenChildren("/DATA/Media", "u1")
	if m["loose.jpg"] != "b1" || len(m) != 1 {
		t.Fatalf("owner should see own badge: %#v", m)
	}
}

// TestMarkItemDoneAcrossBatchesClearsOldInterruptedBatch covers the plain
// resume scenario: the old batch is interrupted, missing 2 items; once a new
// batch (a different id) completes 2 same-named items, the old batch should be
// implicitly reconciled and transition to completed (the badge disappears with it).
func TestMarkItemDoneAcrossBatchesClearsOldInterruptedBatch(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	newTestBatch(t, s, "old", "a.jpg", "b.jpg", "c.jpg")
	_ = s.MarkItemDone("old", "a.jpg", 1000) // old batch has uploaded 1/3
	_ = s.SetInterrupted("old", 2000, BatchInterruptSourceTimeout)        // old batch suspended, missing b.jpg / c.jpg

	newTestBatch(t, s, "new", "b.jpg", "c.jpg") // user directly re-uploads the two missing files, no dialog

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

// TestMarkItemDoneAcrossBatchesIgnoresOtherTargetPath: a same-named batch with
// a different targetPath must not be reconciled by mistake.
func TestMarkItemDoneAcrossBatchesIgnoresOtherTargetPath(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	newTestBatch(t, s, "old", "a.jpg")
	_ = s.SetInterrupted("old", 2000, BatchInterruptSourceTimeout)

	other := &UploadBatch{ID: "other", OwnerUserID: "u1", TargetPath: "/DATA/Other",
		Status: BatchStatusInterrupted, Total: 1}
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

// TestMarkItemDoneAcrossBatchesIdempotent: when the item in the old batch is
// already done, repeated calls should stay idempotent — no double counting, no error.
func TestMarkItemDoneAcrossBatchesIdempotent(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	newTestBatch(t, s, "old", "a.jpg", "b.jpg")
	_ = s.MarkItemDone("old", "a.jpg", 1000)
	_ = s.SetInterrupted("old", 2000, BatchInterruptSourceTimeout)

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

// TestMarkItemDoneAcrossBatchesAbsolutePathParentBatchChildUpload covers a
// real-machine screenshot scenario: the user originally uploaded a whole
// folder (batch targetPath=/DATA/Media, item=backup/a.jpg); when manually
// resuming, they navigate into the subdirectory and upload directly (completed
// upload: targetPath=/DATA/Media/backup, relativePath=a.jpg) — the absolute
// path is the same but the pair differs, and it should be reconciled by absolute-path equivalence.
func TestMarkItemDoneAcrossBatchesAbsolutePathParentBatchChildUpload(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	newTestBatch(t, s, "old", "backup/a.jpg", "backup/b.jpg")
	_ = s.SetInterrupted("old", 2000, BatchInterruptSourceTimeout) // missing backup/a.jpg, backup/b.jpg

	// Direct upload into the subdirectory: targetPath=/DATA/Media/backup, relativePath=a.jpg
	if err := s.MarkItemDoneAcrossBatches("/DATA/Media/backup", "a.jpg", 3000); err != nil {
		t.Fatal(err)
	}

	got, _ := s.Get("old")
	if got.Done != 1 || got.Status != BatchStatusActive {
		t.Fatalf("want done=1/active (interrupted->active on progress), got done=%d status=%s", got.Done, got.Status)
	}
}

// TestMarkItemDoneAcrossBatchesAbsolutePathChildBatchParentUpload covers the
// reverse scenario: the batch is rooted at a subdirectory
// (targetPath=/DATA/Media/backup, item=a.jpg), but the resume uploads directly
// at the parent directory with a sub-path (targetPath=/DATA/Media,
// relativePath=backup/a.jpg) — this should also hit.
func TestMarkItemDoneAcrossBatchesAbsolutePathChildBatchParentUpload(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	b := &UploadBatch{ID: "old", OwnerUserID: "u1", TargetPath: "/DATA/Media/backup",
		Status: BatchStatusActive, Total: 1}
	if err := s.Create(b, []UploadBatchItem{{BatchID: "old", RelativePath: "a.jpg", Size: 100}}); err != nil {
		t.Fatal(err)
	}
	_ = s.SetInterrupted("old", 2000, BatchInterruptSourceTimeout)

	if err := s.MarkItemDoneAcrossBatches("/DATA/Media", "backup/a.jpg", 3000); err != nil {
		t.Fatal(err)
	}

	got, _ := s.Get("old")
	if got.Done != 1 || got.Status != BatchStatusCompleted {
		t.Fatalf("want done=1/completed, got done=%d status=%s", got.Done, got.Status)
	}
}

// TestMarkItemDoneAcrossBatchesUnrelatedDirNotCleared: unrelated directories
// (merely string-prefix-similar, like /DATA/Media vs /DATA/Medias) must not be
// reconciled by mistake.
func TestMarkItemDoneAcrossBatchesUnrelatedDirNotCleared(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	b := &UploadBatch{ID: "sibling", OwnerUserID: "u1", TargetPath: "/DATA/Medias",
		Status: BatchStatusInterrupted, Total: 1}
	if err := s.Create(b, []UploadBatchItem{{BatchID: "sibling", RelativePath: "a.jpg", Size: 100}}); err != nil {
		t.Fatal(err)
	}

	if err := s.MarkItemDoneAcrossBatches("/DATA/Media", "a.jpg", 3000); err != nil {
		t.Fatal(err)
	}

	got, _ := s.Get("sibling")
	if got.Status != BatchStatusInterrupted || got.Done != 0 {
		t.Fatalf("string-prefix-similar sibling dir should be untouched, got %s/%d", got.Status, got.Done)
	}
}

// TestMarkItemDoneAcrossBatchesTrailingSlashNormalized: a batch targetPath with
// a trailing slash should still be normalized and match correctly.
func TestMarkItemDoneAcrossBatchesTrailingSlashNormalized(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	b := &UploadBatch{ID: "old", OwnerUserID: "u1", TargetPath: "/DATA/Media/backup/",
		Status: BatchStatusActive, Total: 1}
	if err := s.Create(b, []UploadBatchItem{{BatchID: "old", RelativePath: "a.jpg", Size: 100}}); err != nil {
		t.Fatal(err)
	}
	_ = s.SetInterrupted("old", 2000, BatchInterruptSourceTimeout)

	if err := s.MarkItemDoneAcrossBatches("/DATA/Media/backup", "a.jpg", 3000); err != nil {
		t.Fatal(err)
	}

	got, _ := s.Get("old")
	if got.Done != 1 || got.Status != BatchStatusCompleted {
		t.Fatalf("want done=1/completed, got done=%d status=%s", got.Done, got.Status)
	}
}

// TestDeleteTerminal: terminal-state (completed/abandoned) batches are deleted
// along with their items; active/interrupted are never auto-cleared — the
// warning badge can only be resolved by the user manually abandoning or resuming.
func TestDeleteTerminal(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	newTestBatch(t, s, "done", "a.jpg")
	_ = s.MarkItemDone("done", "a.jpg", 1000) // → completed
	newTestBatch(t, s, "quit", "a.jpg")
	_ = s.SetStatus("quit", BatchStatusAbandoned)
	newTestBatch(t, s, "stuck", "a.jpg")
	_ = s.SetInterrupted("stuck", 2000, BatchInterruptSourceTimeout)
	newTestBatch(t, s, "live", "a.jpg") // active

	n, err := s.DeleteTerminal()
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	for _, id := range []string{"done", "quit"} {
		if _, err := s.Get(id); err == nil {
			t.Fatalf("terminal batch %s should be gone", id)
		}
		var cnt int64
		s.db.Model(&UploadBatchItem{}).Where("batch_id = ?", id).Count(&cnt)
		if cnt != 0 {
			t.Fatalf("items of %s should be gone", id)
		}
	}
	if b, err := s.Get("stuck"); err != nil || b.Status != BatchStatusInterrupted {
		t.Fatalf("interrupted batch must survive forever: %v", err)
	}
	if b, err := s.Get("live"); err != nil || b.Status != BatchStatusActive {
		t.Fatalf("active batch must survive: %v", err)
	}
}

// TestAbandonInterruptedUnder: one click on the badge must clear EVERY
// interrupted batch whose missing files sit under the badged entry — the badge
// only carries one batch id, but several interrupted batches can be stacked on
// the same folder (each retry that got canceled leaves one), and abandoning
// them one listing at a time reads as a badge that refuses to die.
func TestAbandonInterruptedUnder(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	// Two interrupted batches missing files under /DATA/Media/Trip
	newTestBatch(t, s, "trip1", "Trip/a.jpg", "Trip/b.jpg")
	_ = s.SetInterrupted("trip1", 1000, BatchInterruptSourceTimeout)
	newTestBatch(t, s, "trip2", "Trip/c.jpg")
	_ = s.SetInterrupted("trip2", 1000, BatchInterruptSourceSignal)
	// Interrupted, but under a different folder
	newTestBatch(t, s, "other", "Docs/x.pdf")
	_ = s.SetInterrupted("other", 1000, BatchInterruptSourceTimeout)
	// Under Trip but still active — a running upload must never be abandoned
	newTestBatch(t, s, "running", "Trip/d.jpg")
	// Under Trip, interrupted, but owned by someone else
	ob := &UploadBatch{ID: "foreign", OwnerUserID: "u2", TargetPath: "/DATA/Media",
		Status: BatchStatusActive, Total: 1}
	if err := s.Create(ob, []UploadBatchItem{{BatchID: "foreign", RelativePath: "Trip/e.jpg", Size: 1}}); err != nil {
		t.Fatal(err)
	}
	_ = s.SetInterrupted("foreign", 1000, BatchInterruptSourceTimeout)

	ids, err := s.AbandonInterruptedUnder("/DATA/Media/Trip", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("want trip1+trip2 abandoned, got %v", ids)
	}
	for _, id := range []string{"trip1", "trip2"} {
		if b, _ := s.Get(id); b.Status != BatchStatusAbandoned {
			t.Fatalf("%s should be abandoned, got %s", id, b.Status)
		}
	}
	for id, want := range map[string]string{
		"other": BatchStatusInterrupted, "running": BatchStatusActive, "foreign": BatchStatusInterrupted,
	} {
		if b, _ := s.Get(id); b.Status != want {
			t.Fatalf("%s should stay %s, got %s", id, want, b.Status)
		}
	}
}

// TestAbandonInterruptedUnderExactFile: the badged entry can be a plain file
// (flat upload with one finished sibling), so an exact path match — not just
// prefix-with-slash — must count as a hit.
func TestAbandonInterruptedUnderExactFile(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	newTestBatch(t, s, "flat", "report.pdf")
	_ = s.SetInterrupted("flat", 1000, BatchInterruptSourceTimeout)
	ids, err := s.AbandonInterruptedUnder("/DATA/Media/report.pdf", "u1")
	if err != nil || len(ids) != 1 {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	if b, _ := s.Get("flat"); b.Status != BatchStatusAbandoned {
		t.Fatalf("got %s", b.Status)
	}
}

// TestRemoveItems: canceling individual files must shrink the manifest, or the
// batch can never complete and the sweeper later hangs a broken badge on a
// folder the user deliberately trimmed.
func TestRemoveItems(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	newTestBatch(t, s, "b1", "a.jpg", "b.jpg", "c.jpg")
	_ = s.MarkItemDone("b1", "a.jpg", 1000)

	if err := s.RemoveItems("b1", []string{"b.jpg", "c.jpg"}); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Get("b1")
	if b.Total != 1 || b.Done != 1 {
		t.Fatalf("want total=1 done=1, got total=%d done=%d", b.Total, b.Done)
	}
	// Everything still on the manifest is done → the batch completes and the
	// sweeper can collect it; nothing is left to badge.
	if b.Status != BatchStatusCompleted {
		t.Fatalf("want completed, got %s", b.Status)
	}
	missing, _ := s.MissingItems("b1")
	if len(missing) != 0 {
		t.Fatalf("want no missing items, got %d", len(missing))
	}
}

// TestRemoveItemsKeepsDoneAndUnknown: done items are accounting history and
// must not be deletable; unknown paths are ignored; a batch with work left
// stays active.
func TestRemoveItemsKeepsDoneAndUnknown(t *testing.T) {
	s := NewBatchStore(openBatchTestDB(t))
	newTestBatch(t, s, "b1", "a.jpg", "b.jpg", "c.jpg")
	_ = s.MarkItemDone("b1", "a.jpg", 1000)

	if err := s.RemoveItems("b1", []string{"a.jpg", "nope.jpg", "b.jpg"}); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Get("b1")
	// Only b.jpg (not done) was removable: total 3→2, done stays 1.
	if b.Total != 2 || b.Done != 1 || b.Status != BatchStatusActive {
		t.Fatalf("got total=%d done=%d status=%s", b.Total, b.Done, b.Status)
	}
}
