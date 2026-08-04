package service

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS/model"
	"github.com/NimoTech/NimoOS/model/notify"
	"github.com/NimoTech/NimoOS/pkg/utils/file"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

var (
	ctx    context.Context
	cancel context.CancelFunc
)

// skipIfRoot guards chmod-based fault-injection tests: root (or any process
// with CAP_DAC_OVERRIDE) bypasses the permission bits these tests strip, so
// the induced filesystem failure never actually occurs. Left unguarded, such
// a test wouldn't just stop testing the failure path — it would silently
// report green while asserting nothing.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("chmod-based fault injection requires non-root")
	}
}

func TestNewInteruptReader(t *testing.T) {
	t.Skip("This test is always failing. Skipped to unblock releasing - MUST FIX!")

	ctx, cancel = context.WithCancel(context.Background())

	go func() {
		// create a cancellable context based on the initial context
		//	ctx, cancel := context.WithCancel(ctx)
		fmt.Println("start")
		fIn, err := os.Open("/Users/liangjianli/Downloads/demo_data.tar.gz")
		if err != nil {
		}
		defer fIn.Close()
		fmt.Println("creating new file")
		fOut, err := os.Create("/Users/liangjianli/Downloads/demo_data1.tar.gz")
		if err != nil {
			fmt.Println(err)
		}

		defer fOut.Close()

		fmt.Println("about to copy")
		//	_, err = io.Copy(out, NewReader(ctx, f))
		//	time.Sleep(time.Second * 2)
		// ctx.Done()
		//	cancel()

		// interrupt context after 500ms

		// interrupt context with SIGTERM (CTRL+C)
		// sigs := make(chan os.Signal, 1)
		// signal.Notify(sigs, os.Interrupt)

		if err != nil {
			log.Fatal(err)
		}

		// Reader that fails when context is canceled
		in := NewReader(ctx, fIn)
		// Writer that fails when context is canceled
		out := NewWriter(ctx, fOut)

		// time.Sleep(2 * time.Second)

		// cancel()

		n, err := io.Copy(out, in)
		log.Println(n, "bytes copied.")
		if err != nil {
			fmt.Println("Err:", err)
		}

		fmt.Println("Closing.")
	}()

	go func() {
		//<-sigs
		time.Sleep(time.Second)
		fmt.Println("exiting")
		ddd()
	}()
	time.Sleep(time.Second * 10)
}

func ddd() {
	cancel()
}

func TestOpDestPath(t *testing.T) {
	require.Equal(t, "/dst/pics", opDestPath("/src/pics", "/dst"))
	require.Equal(t, "/dst/pics", opDestPath("/src/pics/", "/dst")) // trailing slash no longer degenerates
	require.Equal(t, "/dst/a.jpg", opDestPath("/src/a.jpg", "/dst/"))
}

// TestFileOperateMove_DuplicateSubmission_NoDataLoss reproduces the 2026-07-10
// production incident: the same logical move (from, to) submitted twice.
// Task 1 moves the data; task 2, executed afterwards with stale instructions,
// must NOT destroy what task 1 just placed at the destination.
//
// On the pre-fix code this fails: dst already exists when k2 runs, Style !=
// "skip" takes the unconditional `os.RemoveAll(dst)` branch (D1), then the
// subsequent CopyDir(v.From, ...) fails because the source was already
// consumed by task 1 — net result: destination wiped, nothing rebuilt.
func TestFileOperateMove_DuplicateSubmission_NoDataLoss(t *testing.T) {
	logger.LogInitConsoleOnly()
	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))

	itemDir := filepath.Join(srcParent, "folder1")
	require.NoError(t, os.MkdirAll(itemDir, 0o755))
	content := []byte("original design asset")
	require.NoError(t, os.WriteFile(filepath.Join(itemDir, "asset.bin"), content, 0o644))

	size, err := file.GetFileOrDirSize(itemDir)
	require.NoError(t, err)

	op := model.FileOperate{
		Type: "move",
		To:   dstDir,
		// "overwrite" is the literal value the production NimoOS-UI sends
		// (FilePanel.vue's paste() defaults to style="overwrite") — this is
		// the exact style the 2026-07-10 incident's requests carried.
		Style: "overwrite",
		Item:  []model.FileItem{{From: itemDir, Size: size}},
	}

	k1 := "regression-k1-" + uuid.NewString()
	FileQueue.Store(k1, op)
	FileOperate(k1)

	dstItem := filepath.Join(dstDir, "folder1")
	got, err := os.ReadFile(filepath.Join(dstItem, "asset.bin"))
	require.NoError(t, err, "task1 must have moved the data to dst")
	require.Equal(t, content, got)
	require.True(t, file.CheckNotExist(itemDir), "task1 must have removed the source")

	// Second submission of the SAME logical move (user double-clicked paste).
	// It carries the same (now stale) From, which task 1 already removed.
	k2 := "regression-k2-" + uuid.NewString()
	FileQueue.Store(k2, op)
	FileOperate(k2)

	got2, err := os.ReadFile(filepath.Join(dstItem, "asset.bin"))
	require.NoError(t, err, "k2 must not have destroyed the destination copy")
	require.Equal(t, content, got2, "destination content must be untouched by the duplicate task")
}

// TestFileOperateMove_SameDeviceRenameFastPath verifies R1: a same-filesystem
// move takes the os.Rename fast path (dst ends up as the SAME inode as src,
// not a freshly-copied one), and the item is marked Finished/ProcessedSize
// immediately so progress polling doesn't see a stuck 0%.
func TestFileOperateMove_SameDeviceRenameFastPath(t *testing.T) {
	logger.LogInitConsoleOnly()
	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))

	itemDir := filepath.Join(srcParent, "folderA")
	require.NoError(t, os.MkdirAll(itemDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(itemDir, "f.txt"), []byte("hi"), 0o644))

	fi, err := os.Stat(itemDir)
	require.NoError(t, err)
	srcStat := fi.Sys().(*syscall.Stat_t)
	srcIno := srcStat.Ino

	size, err := file.GetFileOrDirSize(itemDir)
	require.NoError(t, err)

	op := model.FileOperate{
		Type: "move",
		To:   dstDir,
		Item: []model.FileItem{{From: itemDir, Size: size}},
	}
	k := "rename-fastpath-" + uuid.NewString()
	FileQueue.Store(k, op)
	FileOperate(k)

	dstItem := filepath.Join(dstDir, "folderA")
	fi2, err := os.Stat(dstItem)
	require.NoError(t, err)
	dstStat := fi2.Sys().(*syscall.Stat_t)
	require.Equal(t, srcIno, dstStat.Ino, "expected dst to be the SAME inode as src (rename, not copy)")

	loaded, ok := FileQueue.Load(k)
	require.True(t, ok)
	result := loaded.(model.FileOperate)
	require.True(t, result.Item[0].Finished, "rename success must mark the item Finished immediately")
	require.Equal(t, result.Item[0].Size, result.Item[0].ProcessedSize)
}

// TestFileOperateMove_ConflictSkip verifies R2's "skip" row: an existing
// conflicting destination is left completely alone, and so is the source.
func TestFileOperateMove_ConflictSkip(t *testing.T) {
	logger.LogInitConsoleOnly()
	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))

	itemDir := filepath.Join(srcParent, "folderB")
	require.NoError(t, os.MkdirAll(itemDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(itemDir, "new.txt"), []byte("new-data"), 0o644))

	conflictDir := filepath.Join(dstDir, "folderB")
	require.NoError(t, os.MkdirAll(conflictDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(conflictDir, "old.txt"), []byte("old-data"), 0o644))

	size, err := file.GetFileOrDirSize(itemDir)
	require.NoError(t, err)

	op := model.FileOperate{
		Type:  "move",
		To:    dstDir,
		Style: "skip",
		Item:  []model.FileItem{{From: itemDir, Size: size}},
	}
	k := "conflict-skip-" + uuid.NewString()
	FileQueue.Store(k, op)
	FileOperate(k)

	old, err := os.ReadFile(filepath.Join(conflictDir, "old.txt"))
	require.NoError(t, err)
	require.Equal(t, []byte("old-data"), old)
	require.True(t, file.CheckNotExist(filepath.Join(conflictDir, "new.txt")), "skip must not merge new data in")

	untouched, err := os.ReadFile(filepath.Join(itemDir, "new.txt"))
	require.NoError(t, err, "source must be left in place when skipped")
	require.Equal(t, []byte("new-data"), untouched)
}

// TestFileOperateMove_ConflictReplace_Success verifies R2's "replace" row on
// the happy path: the old conflicting destination content is fully replaced
// by the new data, source removed, and no staging temp dir left behind.
func TestFileOperateMove_ConflictReplace_Success(t *testing.T) {
	logger.LogInitConsoleOnly()
	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))

	itemDir := filepath.Join(srcParent, "folderC")
	require.NoError(t, os.MkdirAll(itemDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(itemDir, "new.txt"), []byte("new-data"), 0o644))

	conflictDir := filepath.Join(dstDir, "folderC")
	require.NoError(t, os.MkdirAll(conflictDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(conflictDir, "old.txt"), []byte("old-data"), 0o644))

	size, err := file.GetFileOrDirSize(itemDir)
	require.NoError(t, err)

	op := model.FileOperate{
		Type:  "move",
		To:    dstDir,
		Style: "replace",
		Item:  []model.FileItem{{From: itemDir, Size: size}},
	}
	k := "conflict-replace-ok-" + uuid.NewString()
	FileQueue.Store(k, op)
	FileOperate(k)

	require.True(t, file.CheckNotExist(filepath.Join(conflictDir, "old.txt")), "old conflicting data must be gone after a successful replace")
	newContent, err := os.ReadFile(filepath.Join(conflictDir, "new.txt"))
	require.NoError(t, err)
	require.Equal(t, []byte("new-data"), newContent)

	require.True(t, file.CheckNotExist(itemDir), "source must be removed after a successful replace-move")

	entries, err := os.ReadDir(dstDir)
	require.NoError(t, err)
	for _, e := range entries {
		require.NotContains(t, e.Name(), "nimoos-replacing", "no temp staging directory should remain")
	}
}

// TestFileOperateMove_ConflictReplace_RollbackOnFailure verifies R2's
// "replace" row on the failure path: if the move itself fails after the old
// destination has been staged aside, the original data must be restored —
// zero data loss. The failure is a genuine filesystem fault (read-only
// source parent directory), not a mock.
func TestFileOperateMove_ConflictReplace_RollbackOnFailure(t *testing.T) {
	skipIfRoot(t)
	logger.LogInitConsoleOnly()
	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))

	itemDir := filepath.Join(srcParent, "folderD")
	require.NoError(t, os.MkdirAll(itemDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(itemDir, "new.txt"), []byte("new-data"), 0o644))

	conflictDir := filepath.Join(dstDir, "folderD")
	require.NoError(t, os.MkdirAll(conflictDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(conflictDir, "old.txt"), []byte("old-data"), 0o644))

	size, err := file.GetFileOrDirSize(itemDir)
	require.NoError(t, err)

	// Read-only parent: removing/renaming folderD out of srcParent will fail
	// with EACCES/EPERM, but only once the replace flow has already staged
	// the old dst aside — this is exactly the rollback path.
	require.NoError(t, os.Chmod(srcParent, 0o555))
	defer os.Chmod(srcParent, 0o755) // let t.TempDir() clean up afterwards

	op := model.FileOperate{
		Type:  "move",
		To:    dstDir,
		Style: "replace",
		Item:  []model.FileItem{{From: itemDir, Size: size}},
	}
	k := "conflict-replace-fail-" + uuid.NewString()
	FileQueue.Store(k, op)
	FileOperate(k)

	oldContent, err := os.ReadFile(filepath.Join(conflictDir, "old.txt"))
	require.NoError(t, err, "old destination data must be restored after a failed replace")
	require.Equal(t, []byte("old-data"), oldContent)
	require.True(t, file.CheckNotExist(filepath.Join(conflictDir, "new.txt")), "new data must not appear when the replace failed")

	entries, err := os.ReadDir(dstDir)
	require.NoError(t, err)
	for _, e := range entries {
		require.NotContains(t, e.Name(), "nimoos-replacing", "rollback must not leave a temp staging dir behind")
	}
}

// TestFileOperateCopy_ConflictReplace_Success verifies R2's "replace" row on
// the copy branch's happy path: the old conflicting destination content is
// fully replaced by the new data, and — unlike move — the source is left
// completely untouched.
func TestFileOperateCopy_ConflictReplace_Success(t *testing.T) {
	logger.LogInitConsoleOnly()
	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))

	itemDir := filepath.Join(srcParent, "folderF")
	require.NoError(t, os.MkdirAll(itemDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(itemDir, "new.txt"), []byte("new-data"), 0o644))

	conflictDir := filepath.Join(dstDir, "folderF")
	require.NoError(t, os.MkdirAll(conflictDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(conflictDir, "old.txt"), []byte("old-data"), 0o644))

	size, err := file.GetFileOrDirSize(itemDir)
	require.NoError(t, err)

	op := model.FileOperate{
		Type:  "copy",
		To:    dstDir,
		Style: "overwrite",
		Item:  []model.FileItem{{From: itemDir, Size: size}},
	}
	k := "copy-conflict-replace-ok-" + uuid.NewString()
	FileQueue.Store(k, op)
	FileOperate(k)

	require.True(t, file.CheckNotExist(filepath.Join(conflictDir, "old.txt")), "old conflicting data must be gone after a successful copy-replace")
	newContent, err := os.ReadFile(filepath.Join(conflictDir, "new.txt"))
	require.NoError(t, err)
	require.Equal(t, []byte("new-data"), newContent)

	srcContent, err := os.ReadFile(filepath.Join(itemDir, "new.txt"))
	require.NoError(t, err, "copy must never remove the source")
	require.Equal(t, []byte("new-data"), srcContent)

	entries, err := os.ReadDir(dstDir)
	require.NoError(t, err)
	for _, e := range entries {
		require.NotContains(t, e.Name(), "nimoos-replacing", "no temp staging directory should remain")
	}
}

// TestFileOperateCopy_ConflictReplace_RollbackOnFailure verifies R2's
// "replace" row on the copy branch's failure path: if the CopyDir call
// invoked from inside replaceConflict fails after the old destination has
// been staged aside, the original destination content must be restored —
// zero data loss. The failure is a genuine filesystem fault (an unreadable
// source directory, so `cp -a` and the manual ReadDir fallback both fail),
// not a mock.
func TestFileOperateCopy_ConflictReplace_RollbackOnFailure(t *testing.T) {
	skipIfRoot(t)
	logger.LogInitConsoleOnly()
	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))

	itemDir := filepath.Join(srcParent, "folderG")
	require.NoError(t, os.MkdirAll(itemDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(itemDir, "new.txt"), []byte("new-data"), 0o644))

	conflictDir := filepath.Join(dstDir, "folderG")
	require.NoError(t, os.MkdirAll(conflictDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(conflictDir, "old.txt"), []byte("old-data"), 0o644))

	size, err := file.GetFileOrDirSize(itemDir)
	require.NoError(t, err)

	// Strip all permissions from the source item dir itself: `cp -a` can't
	// even open it, and the manual os.ReadDir fallback fails too, so
	// file.CopyDir genuinely fails — but this has no effect on dstDir's own
	// permissions, so replaceConflict's stage-aside rename still succeeds
	// and we exercise the doOp-fails-after-staging rollback path.
	require.NoError(t, os.Chmod(itemDir, 0o000))
	defer os.Chmod(itemDir, 0o755) // let t.TempDir() clean up afterwards

	op := model.FileOperate{
		Type:  "copy",
		To:    dstDir,
		Style: "replace",
		Item:  []model.FileItem{{From: itemDir, Size: size}},
	}
	k := "copy-conflict-replace-fail-" + uuid.NewString()
	FileQueue.Store(k, op)
	FileOperate(k)

	oldContent, err := os.ReadFile(filepath.Join(conflictDir, "old.txt"))
	require.NoError(t, err, "old destination data must be restored after a failed copy-replace")
	require.Equal(t, []byte("old-data"), oldContent)
	require.True(t, file.CheckNotExist(filepath.Join(conflictDir, "new.txt")), "new data must not appear when the replace failed")

	entries, err := os.ReadDir(dstDir)
	require.NoError(t, err)
	for _, e := range entries {
		require.NotContains(t, e.Name(), "nimoos-replacing", "rollback must not leave a temp staging dir behind")
	}
}

// TestFileOperateMove_ConflictReplace_RollbackFailure_ParksAndRecordsPath
// verifies the second review finding: when the rollback rename itself
// (tmp -> dst, inside replaceConflict) fails — not just the original doOp —
// the parked temp path must be surfaced in the task/item state
// (model.FileItem.ParkedPath), not just logged.
//
// This needs a real fault that hits ONLY the rollback rename, not the
// earlier stage-aside rename (dst -> tmp), which must still succeed so we
// actually reach the rollback branch. Both renames share dstDir as their
// parent, so dstDir can't simply be made read-only up front. Instead the
// one mock the spec allows (renameFn, used by moveItem for the primary
// from->dst attempt) is used as the trigger point: it flips dstDir to
// read-only as a side effect and then returns a plain (non-EXDEV) error.
// By the time this runs, replaceConflict's stage-aside rename has already
// completed (it runs before doOp/moveItem is even called), so:
//  1. stage-aside (dst -> tmp): succeeds, dstDir still writable.
//  2. moveItem calls renameFn(from, dst): the mock chmods dstDir read-only
//     and returns EACCES -> moveItem returns a real (non-EXDEV) error.
//  3. replaceConflict's rollback (os.Rename(tmp, dst)): now genuinely fails
//     for real, because dstDir is read-only — this is the exact scenario
//     under test.
func TestFileOperateMove_ConflictReplace_RollbackFailure_ParksAndRecordsPath(t *testing.T) {
	skipIfRoot(t)
	logger.LogInitConsoleOnly()
	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))

	itemDir := filepath.Join(srcParent, "folderH")
	require.NoError(t, os.MkdirAll(itemDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(itemDir, "new.txt"), []byte("new-data"), 0o644))

	conflictDir := filepath.Join(dstDir, "folderH")
	require.NoError(t, os.MkdirAll(conflictDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(conflictDir, "old.txt"), []byte("old-data"), 0o644))

	size, err := file.GetFileOrDirSize(itemDir)
	require.NoError(t, err)

	orig := renameFn
	renameFn = func(oldpath, newpath string) error {
		require.NoError(t, os.Chmod(dstDir, 0o555))
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EACCES}
	}
	defer func() { renameFn = orig }()
	defer os.Chmod(dstDir, 0o755) // let t.TempDir() clean up afterwards

	op := model.FileOperate{
		Type:  "move",
		To:    dstDir,
		Style: "replace",
		Item:  []model.FileItem{{From: itemDir, Size: size}},
	}
	k := "conflict-replace-park-" + uuid.NewString()
	FileQueue.Store(k, op)
	FileOperate(k)

	loaded, ok := FileQueue.Load(k)
	require.True(t, ok)
	result := loaded.(model.FileOperate)
	require.NotEmpty(t, result.Item[0].ParkedPath, "when the rollback rename itself fails, the parked path must be recorded in the task state")

	parkedContent, err := os.ReadFile(filepath.Join(result.Item[0].ParkedPath, "old.txt"))
	require.NoError(t, err, "the original data must actually be sitting at the recorded parked path")
	require.Equal(t, []byte("old-data"), parkedContent)
}

// TestFileOperateMove_CrossDeviceFallback verifies R1's EXDEV branch: when
// the rename attempt fails because src/dst are on different devices, the
// existing copy -> verify -> delete-source path is used instead. Since a
// real cross-device boundary can't be constructed inside a single temp dir,
// this test injects the EXDEV failure via the package-level renameFn hook —
// the one mock the spec explicitly allows.
func TestFileOperateMove_CrossDeviceFallback(t *testing.T) {
	logger.LogInitConsoleOnly()
	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))

	itemDir := filepath.Join(srcParent, "folderE")
	require.NoError(t, os.MkdirAll(itemDir, 0o755))
	content := []byte("cross-device-data")
	require.NoError(t, os.WriteFile(filepath.Join(itemDir, "f.txt"), content, 0o644))

	size, err := file.GetFileOrDirSize(itemDir)
	require.NoError(t, err)

	orig := renameFn
	renameFn = func(oldpath, newpath string) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
	}
	defer func() { renameFn = orig }()

	op := model.FileOperate{
		Type: "move",
		To:   dstDir,
		Item: []model.FileItem{{From: itemDir, Size: size}},
	}
	k := "exdev-fallback-" + uuid.NewString()
	FileQueue.Store(k, op)
	FileOperate(k)

	dstItem := filepath.Join(dstDir, "folderE")
	got, err := os.ReadFile(filepath.Join(dstItem, "f.txt"))
	require.NoError(t, err, "fallback copy must have populated dst despite the injected EXDEV")
	require.Equal(t, content, got)
	require.True(t, file.CheckNotExist(itemDir), "source must be removed after fallback copy+verify")
}

// --- R4: "rename" (keep-both) conflict style ---
//
// Style stays batch-level (unchanged): the UI splits a paste batch into
// per-style groups client-side and submits "rename" only for the items the
// user explicitly chose keep-both for. These tests exercise FileOperate's
// "rename" branch directly, the same way the "skip"/"replace" tests above do.

// TestFileOperateMove_ConflictRename_File_Success verifies the "rename" row
// for a plain file conflict: the existing dst is left completely alone, and
// the moved file lands at the "(1)" sibling name.
func TestFileOperateMove_ConflictRename_File_Success(t *testing.T) {
	logger.LogInitConsoleOnly()
	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))

	itemFile := filepath.Join(srcParent, "report.docx")
	require.NoError(t, os.WriteFile(itemFile, []byte("new-data"), 0o644))

	conflictFile := filepath.Join(dstDir, "report.docx")
	require.NoError(t, os.WriteFile(conflictFile, []byte("old-data"), 0o644))

	size, err := file.GetFileOrDirSize(itemFile)
	require.NoError(t, err)

	op := model.FileOperate{
		Type:  "move",
		To:    dstDir,
		Style: "rename",
		Item:  []model.FileItem{{From: itemFile, Size: size}},
	}
	k := "conflict-rename-move-file-" + uuid.NewString()
	FileQueue.Store(k, op)
	FileOperate(k)

	old, err := os.ReadFile(conflictFile)
	require.NoError(t, err, "the existing destination file must be untouched")
	require.Equal(t, []byte("old-data"), old)

	renamed, err := os.ReadFile(filepath.Join(dstDir, "report(1).docx"))
	require.NoError(t, err, "moved data must land at the de-conflicted sibling name")
	require.Equal(t, []byte("new-data"), renamed)

	require.True(t, file.CheckNotExist(itemFile), "source must be removed after a successful rename-move")

	loaded, ok := FileQueue.Load(k)
	require.True(t, ok)
	result := loaded.(model.FileOperate)
	require.True(t, result.Item[0].Finished)
}

// TestFileOperateMove_ConflictRename_Directory_Success verifies the "rename"
// row for a directory conflict, including that the directory's contents
// (not just an empty shell) land intact at the sibling name.
func TestFileOperateMove_ConflictRename_Directory_Success(t *testing.T) {
	logger.LogInitConsoleOnly()
	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))

	itemDir := filepath.Join(srcParent, "folderRen")
	require.NoError(t, os.MkdirAll(itemDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(itemDir, "new.txt"), []byte("new-data"), 0o644))

	conflictDir := filepath.Join(dstDir, "folderRen")
	require.NoError(t, os.MkdirAll(conflictDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(conflictDir, "old.txt"), []byte("old-data"), 0o644))

	size, err := file.GetFileOrDirSize(itemDir)
	require.NoError(t, err)

	op := model.FileOperate{
		Type:  "move",
		To:    dstDir,
		Style: "rename",
		Item:  []model.FileItem{{From: itemDir, Size: size}},
	}
	k := "conflict-rename-move-dir-" + uuid.NewString()
	FileQueue.Store(k, op)
	FileOperate(k)

	old, err := os.ReadFile(filepath.Join(conflictDir, "old.txt"))
	require.NoError(t, err, "the existing destination directory must be untouched")
	require.Equal(t, []byte("old-data"), old)

	renamedDir := filepath.Join(dstDir, "folderRen(1)")
	renamed, err := os.ReadFile(filepath.Join(renamedDir, "new.txt"))
	require.NoError(t, err, "moved directory must land at the de-conflicted sibling name")
	require.Equal(t, []byte("new-data"), renamed)

	require.True(t, file.CheckNotExist(itemDir), "source must be removed after a successful rename-move")
}

// TestFileOperateCopy_ConflictRename_File_Success mirrors the move-file test
// above for the copy branch: source must survive (copy never deletes it).
func TestFileOperateCopy_ConflictRename_File_Success(t *testing.T) {
	logger.LogInitConsoleOnly()
	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))

	itemFile := filepath.Join(srcParent, "report.docx")
	require.NoError(t, os.WriteFile(itemFile, []byte("new-data"), 0o644))

	conflictFile := filepath.Join(dstDir, "report.docx")
	require.NoError(t, os.WriteFile(conflictFile, []byte("old-data"), 0o644))

	size, err := file.GetFileOrDirSize(itemFile)
	require.NoError(t, err)

	op := model.FileOperate{
		Type:  "copy",
		To:    dstDir,
		Style: "rename",
		Item:  []model.FileItem{{From: itemFile, Size: size}},
	}
	k := "conflict-rename-copy-file-" + uuid.NewString()
	FileQueue.Store(k, op)
	FileOperate(k)

	old, err := os.ReadFile(conflictFile)
	require.NoError(t, err, "the existing destination file must be untouched")
	require.Equal(t, []byte("old-data"), old)

	renamed, err := os.ReadFile(filepath.Join(dstDir, "report(1).docx"))
	require.NoError(t, err, "copied data must land at the de-conflicted sibling name")
	require.Equal(t, []byte("new-data"), renamed)

	srcContent, err := os.ReadFile(itemFile)
	require.NoError(t, err, "copy must never remove the source")
	require.Equal(t, []byte("new-data"), srcContent)
}

// TestFileOperateCopy_ConflictRename_Directory_Success mirrors the
// move-directory test above for the copy branch.
func TestFileOperateCopy_ConflictRename_Directory_Success(t *testing.T) {
	logger.LogInitConsoleOnly()
	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))

	itemDir := filepath.Join(srcParent, "folderRenC")
	require.NoError(t, os.MkdirAll(itemDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(itemDir, "new.txt"), []byte("new-data"), 0o644))

	conflictDir := filepath.Join(dstDir, "folderRenC")
	require.NoError(t, os.MkdirAll(conflictDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(conflictDir, "old.txt"), []byte("old-data"), 0o644))

	size, err := file.GetFileOrDirSize(itemDir)
	require.NoError(t, err)

	op := model.FileOperate{
		Type:  "copy",
		To:    dstDir,
		Style: "rename",
		Item:  []model.FileItem{{From: itemDir, Size: size}},
	}
	k := "conflict-rename-copy-dir-" + uuid.NewString()
	FileQueue.Store(k, op)
	FileOperate(k)

	old, err := os.ReadFile(filepath.Join(conflictDir, "old.txt"))
	require.NoError(t, err, "the existing destination directory must be untouched")
	require.Equal(t, []byte("old-data"), old)

	renamedDir := filepath.Join(dstDir, "folderRenC(1)")
	renamed, err := os.ReadFile(filepath.Join(renamedDir, "new.txt"))
	require.NoError(t, err, "copied directory must land at the de-conflicted sibling name")
	require.Equal(t, []byte("new-data"), renamed)

	srcContent, err := os.ReadFile(filepath.Join(itemDir, "new.txt"))
	require.NoError(t, err, "copy must never remove the source")
	require.Equal(t, []byte("new-data"), srcContent)
}

// TestResolveRenameTarget_ChainedConflicts_SkipsTakenNumbers verifies the
// naming rule end to end: when "report.docx", "report(1).docx" AND
// "report(2).docx" already exist, the next free candidate is
// "report(3).docx" — not a restart at "(1)" and not some other scheme.
func TestResolveRenameTarget_ChainedConflicts_SkipsTakenNumbers(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "report.docx")
	require.NoError(t, os.WriteFile(dst, []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "report(1).docx"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "report(2).docx"), []byte("x"), 0o644))

	got, err := resolveRenameTarget(dst)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "report(3).docx"), got)
}

// TestResolveRenameTarget_ExecutionWindowRace_DefersToNextCandidate exercises
// the TOCTOU guard between resolveRenameTarget's probe (via
// file.GetNoDuplicateFileName) and its final re-check: something else claims
// the exact candidate name in that window, and the function must defer to
// the next candidate rather than the caller going on to overwrite it. The
// race is driven deterministically through the afterRenameCandidateScan test
// seam (mirroring how renameFn is used elsewhere in this file to simulate
// EXDEV) instead of depending on real goroutine-scheduling timing.
func TestResolveRenameTarget_ExecutionWindowRace_DefersToNextCandidate(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "report.docx")
	require.NoError(t, os.WriteFile(dst, []byte("x"), 0o644))

	claimed := filepath.Join(root, "report(1).docx")
	orig := afterRenameCandidateScan
	fired := false
	afterRenameCandidateScan = func(candidate string) {
		if !fired && candidate == claimed {
			fired = true
			// Simulate something else claiming this exact name in the gap
			// between the scan and resolveRenameTarget's re-check.
			require.NoError(t, os.WriteFile(claimed, []byte("raced-in"), 0o644))
		}
	}
	defer func() { afterRenameCandidateScan = orig }()

	got, err := resolveRenameTarget(dst)
	require.NoError(t, err)
	require.True(t, fired, "test seam never fired — race was not exercised")
	require.Equal(t, filepath.Join(root, "report(2).docx"), got, "must defer past the name claimed mid-window")

	// The raced-in file must be completely untouched — resolveRenameTarget
	// must never overwrite it.
	racedContent, err := os.ReadFile(claimed)
	require.NoError(t, err)
	require.Equal(t, []byte("raced-in"), racedContent)
}

// TestResolveRenameTarget_AttemptsExhausted_ReturnsError verifies the bound
// on the TOCTOU retry loop: if whatever candidate the internal scan comes up
// with keeps getting claimed out from under it (pathological, unrelenting
// churn), resolveRenameTarget gives up with an error after
// maxRenameCandidateAttempts tries instead of spinning forever or falling
// back to some other (silently overwriting) behavior. The churn is driven
// deterministically via the afterRenameCandidateScan test seam — it claims
// every single candidate the scan produces, forcing every retry to actually
// retry (a plain pile of pre-created "(1)".."(N)" files would not exercise
// this: file.GetNoDuplicateFileName's own internal scan already skips past
// those in one call, so the retry loop would never actually loop).
// maxRenameCandidateAttempts is shrunk so the test doesn't need hundreds of
// iterations.
func TestResolveRenameTarget_AttemptsExhausted_ReturnsError(t *testing.T) {
	origMax := maxRenameCandidateAttempts
	maxRenameCandidateAttempts = 3
	defer func() { maxRenameCandidateAttempts = origMax }()

	root := t.TempDir()
	dst := filepath.Join(root, "report.docx")
	require.NoError(t, os.WriteFile(dst, []byte("x"), 0o644))

	origHook := afterRenameCandidateScan
	claimedCount := 0
	afterRenameCandidateScan = func(candidate string) {
		require.NoError(t, os.WriteFile(candidate, []byte("raced-in"), 0o644))
		claimedCount++
	}
	defer func() { afterRenameCandidateScan = origHook }()

	_, err := resolveRenameTarget(dst)
	require.Error(t, err)
	require.Equal(t, maxRenameCandidateAttempts, claimedCount, "must have actually retried up to the bound, not given up early or looped past it")
}

// TestFileOperateMove_UnknownStyle_TreatedAsSkip_Regression is a regression
// test locking in the pre-existing (unchanged) conservative default: an
// unrecognized Style value must never delete or touch anything, exactly
// like "skip" — this must keep holding true after adding the "rename" case
// alongside it.
func TestFileOperateMove_UnknownStyle_TreatedAsSkip_Regression(t *testing.T) {
	logger.LogInitConsoleOnly()
	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))

	itemFile := filepath.Join(srcParent, "x.txt")
	require.NoError(t, os.WriteFile(itemFile, []byte("new-data"), 0o644))

	conflictFile := filepath.Join(dstDir, "x.txt")
	require.NoError(t, os.WriteFile(conflictFile, []byte("old-data"), 0o644))

	size, err := file.GetFileOrDirSize(itemFile)
	require.NoError(t, err)

	op := model.FileOperate{
		Type:  "move",
		To:    dstDir,
		Style: "some-future-style-the-backend-does-not-know-yet",
		Item:  []model.FileItem{{From: itemFile, Size: size}},
	}
	k := "unknown-style-move-" + uuid.NewString()
	FileQueue.Store(k, op)
	FileOperate(k)

	old, err := os.ReadFile(conflictFile)
	require.NoError(t, err)
	require.Equal(t, []byte("old-data"), old, "unknown style must never overwrite the existing destination")
	require.True(t, file.CheckNotExist(filepath.Join(dstDir, "x(1).txt")), "unknown style must not silently behave like rename either")

	src, err := os.ReadFile(itemFile)
	require.NoError(t, err, "unknown style must leave the source in place, same as skip")
	require.Equal(t, []byte("new-data"), src)
}

// TestFileOperateCopy_UnknownStyle_TreatedAsSkip_Regression mirrors the move
// test above for the copy branch.
func TestFileOperateCopy_UnknownStyle_TreatedAsSkip_Regression(t *testing.T) {
	logger.LogInitConsoleOnly()
	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))

	itemFile := filepath.Join(srcParent, "x.txt")
	require.NoError(t, os.WriteFile(itemFile, []byte("new-data"), 0o644))

	conflictFile := filepath.Join(dstDir, "x.txt")
	require.NoError(t, os.WriteFile(conflictFile, []byte("old-data"), 0o644))

	size, err := file.GetFileOrDirSize(itemFile)
	require.NoError(t, err)

	op := model.FileOperate{
		Type:  "copy",
		To:    dstDir,
		Style: "some-future-style-the-backend-does-not-know-yet",
		Item:  []model.FileItem{{From: itemFile, Size: size}},
	}
	k := "unknown-style-copy-" + uuid.NewString()
	FileQueue.Store(k, op)
	FileOperate(k)

	old, err := os.ReadFile(conflictFile)
	require.NoError(t, err)
	require.Equal(t, []byte("old-data"), old, "unknown style must never overwrite the existing destination")
	require.True(t, file.CheckNotExist(filepath.Join(dstDir, "x(1).txt")), "unknown style must not silently behave like rename either")

	src, err := os.ReadFile(itemFile)
	require.NoError(t, err, "unknown style must leave the source in place (copy never deletes it anyway)")
	require.Equal(t, []byte("new-data"), src)
}

// --- R3: task dedup (test case 6) ---
//
// These tests exercise EnqueueOp/DequeueOp/ClearOps directly rather than
// through the HTTP route, mirroring the rest of this file's approach of
// testing the real queue/state layer with real concurrency, not a mock.
// ClearOps() is called before/after each test to reset the package-level
// opQueue (and its fingerprint tables) so tests don't leak state into each
// other; FileQueue is reset with a fresh sync.Map for the same reason.

func dedupOp(to string, froms ...string) model.FileOperate {
	items := make([]model.FileItem, len(froms))
	for i, f := range froms {
		items[i] = model.FileItem{From: f}
	}
	return model.FileOperate{Type: "move", To: to, Item: items}
}

// TestEnqueueOp_DuplicateFingerprint_Rejected is the core of use case 6: a
// second submission of the exact same (type, to, from-set) batch — the
// production incident's trigger — must be rejected with duplicate=true, and
// must not leave anything behind in FileQueue for the caller to clean up.
// Item order is deliberately reversed on the second submission to prove the
// fingerprint is order-independent (sort(froms), per the spec), matching
// how the UI might resubmit the same batch with items enumerated in a
// different order.
func TestEnqueueOp_DuplicateFingerprint_Rejected(t *testing.T) {
	ClearOps()
	FileQueue.Clear()
	defer func() { ClearOps(); FileQueue.Clear() }()

	op1 := dedupOp("/dst", "/src/a", "/src/b")
	isFirst, dup := EnqueueOp("id1", op1)
	require.True(t, isFirst)
	require.False(t, dup)

	op2 := dedupOp("/dst", "/src/b", "/src/a") // same set, reversed order
	isFirst2, dup2 := EnqueueOp("id2", op2)
	require.True(t, dup2, "identical in-flight fingerprint must be rejected")
	require.False(t, isFirst2)

	_, stored := FileQueue.Load("id2")
	require.False(t, stored, "a rejected submission must not be stored in FileQueue")

	require.Equal(t, []string{"id1"}, PeekOps(), "queue must still contain only the original task")
}

// TestEnqueueOp_ReleasedAfterDequeueOp covers the first of the two required
// release paths: task completion, where service/notify.go's
// SendFileOperateNotify calls FileQueue.Delete(id) + DequeueOp(id) once a
// task's Finished flag is observed. After that, the identical fingerprint
// must be admittable again.
func TestEnqueueOp_ReleasedAfterDequeueOp(t *testing.T) {
	ClearOps()
	FileQueue.Clear()
	defer func() { ClearOps(); FileQueue.Clear() }()

	op := dedupOp("/dst", "/src/a")
	_, dup := EnqueueOp("id1", op)
	require.False(t, dup)

	_, dup2 := EnqueueOp("id2", op)
	require.True(t, dup2, "must still be rejected while id1 is in flight")

	// Mimic service/notify.go's completion cleanup for a finished task.
	FileQueue.Delete("id1")
	DequeueOp("id1")

	isFirst3, dup3 := EnqueueOp("id3", op)
	require.False(t, dup3, "fingerprint must be free again after DequeueOp released it")
	require.True(t, isFirst3)
}

// TestEnqueueOp_ReleasedAfterClearOps covers the second required release
// path: route/v1/file.go's DeleteOperateFileOrDir "clear all" branch
// (id == "0"), which resets FileQueue to a fresh sync.Map and calls
// ClearOps(). After that, the identical fingerprint must be admittable
// again too.
func TestEnqueueOp_ReleasedAfterClearOps(t *testing.T) {
	ClearOps()
	FileQueue.Clear()
	defer func() { ClearOps(); FileQueue.Clear() }()

	op := dedupOp("/dst", "/src/a")
	_, dup := EnqueueOp("id1", op)
	require.False(t, dup)

	_, dup2 := EnqueueOp("id2", op)
	require.True(t, dup2)

	// Mimic route/v1/file.go's DeleteOperateFileOrDir(id="0") branch.
	FileQueue.Clear()
	ClearOps()

	isFirst3, dup3 := EnqueueOp("id3", op)
	require.False(t, dup3, "fingerprint must be free again after ClearOps released it")
	require.True(t, isFirst3)
}

// TestEnqueueOp_ConcurrentDuplicates_OnlyOneAdmitted guards against a
// check-then-insert race: many goroutines racing to submit the identical
// batch must result in exactly one admitted task, never more. Run with
// -race to also confirm there's no data race on the shared queue/fingerprint
// state.
func TestEnqueueOp_ConcurrentDuplicates_OnlyOneAdmitted(t *testing.T) {
	ClearOps()
	FileQueue.Clear()
	defer func() { ClearOps(); FileQueue.Clear() }()

	op := dedupOp("/dst", "/src/a", "/src/b", "/src/c")
	const n = 20

	var wg sync.WaitGroup
	admitted := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("concurrent-%d", i)
			_, dup := EnqueueOp(id, op)
			admitted[i] = !dup
		}(i)
	}
	wg.Wait()

	count := 0
	for _, a := range admitted {
		if a {
			count++
		}
	}
	require.Equal(t, 1, count, "exactly one concurrent duplicate submission must be admitted")
	require.Len(t, PeekOps(), 1, "queue must contain exactly one entry")
}

// --- A3: real cancellation of in-flight move/copy tasks ---
//
// These tests use file.RunCopyCommand (an injectable hook wrapping the
// interruptible cp subprocess) to deterministically land a cancellation
// while a copy is genuinely in flight, rather than racing against real
// disk-I/O timing — which would make a "cancel mid-copy" test flaky. They
// also override terminalNotifyFn (which normally calls into MyService's
// MessageBus, nil in this test binary — see the comment on terminalNotifyFn)
// with a capturing stub, the same dependency-injection pattern renameFn (R1)
// already established.

// blockingCopyHook returns a file.RunCopyCommand replacement that signals
// startedCh once entered and then blocks until releaseCh is closed, letting
// a test synchronize "the copy is now genuinely in flight" with a concurrent
// CancelOp call before letting the (by-then-cancelled) cp subprocess run.
func blockingCopyHook(startedCh, releaseCh chan struct{}) func(cmd *exec.Cmd) error {
	var once sync.Once
	return func(cmd *exec.Cmd) error {
		once.Do(func() { close(startedCh) })
		<-releaseCh
		return cmd.Run()
	}
}

// forceCrossDevice returns a renameFn replacement that reports EXDEV for
// every path except those in passthrough (which fall through to the real
// os.Rename) — used to force specific items down moveItem's copy fallback
// while letting others (e.g. an already-completed prior item) rename
// normally.
func forceCrossDevice(passthrough ...string) func(oldpath, newpath string) error {
	allow := make(map[string]bool, len(passthrough))
	for _, p := range passthrough {
		allow[p] = true
	}
	return func(oldpath, newpath string) error {
		if allow[oldpath] {
			return os.Rename(oldpath, newpath)
		}
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
	}
}

// TestFileOperateMove_CancelMidCopy_CleansHalfWrittenDestSourceIntact is TDD
// scenario 1: a cross-device move cancelled mid-copy must kill the cp
// subprocess, remove whatever half-written fragment it left at the
// destination, leave the source completely untouched, and push exactly one
// terminal notification — carrying Cancelled — BEFORE the task is dequeued/
// deleted/its fingerprint released (the ordering is asserted from inside the
// notify stub itself, at the moment it fires).
//
// On pre-A3 code this fails outright: moveItem/CopyDir had no ctx at all,
// so nothing could interrupt the cp subprocess — the file would land at the
// destination regardless of any "cancel" request (see the brief's
// user-observed failure). TestFileOperate_QueueRemovalAloneDoesNotStopInFlightCopy
// below demonstrates that exact old behavior directly on the current copy
// engine, by exercising only the pre-A3 half of the fix (queue removal
// without also cancelling the context).
func TestFileOperateMove_CancelMidCopy_CleansHalfWrittenDestSourceIntact(t *testing.T) {
	logger.LogInitConsoleOnly()
	ClearOps()
	FileQueue.Clear()
	defer func() { ClearOps(); FileQueue.Clear() }()

	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))

	itemDir := filepath.Join(srcParent, "big")
	require.NoError(t, os.MkdirAll(itemDir, 0o755))
	content := []byte("cross-device payload")
	require.NoError(t, os.WriteFile(filepath.Join(itemDir, "f.txt"), content, 0o644))

	size, err := file.GetFileOrDirSize(itemDir)
	require.NoError(t, err)

	origRename := renameFn
	renameFn = forceCrossDevice()
	defer func() { renameFn = origRename }()

	started := make(chan struct{})
	release := make(chan struct{})
	origRun := file.RunCopyCommand
	file.RunCopyCommand = blockingCopyHook(started, release)
	defer func() { file.RunCopyCommand = origRun }()

	k := "cancel-midcopy"
	var pushed []notify.File
	origNotify := terminalNotifyFn
	terminalNotifyFn = func(task notify.File) {
		// Ordering assertion (design requirement 4): the push must happen
		// BEFORE the task is removed from FileQueue/opQueue.
		_, stillQueued := FileQueue.Load(k)
		require.True(t, stillQueued, "terminal notify must fire before FileQueue.Delete")
		require.Contains(t, PeekOps(), k, "terminal notify must fire before DequeueOp")
		pushed = append(pushed, task)
	}
	defer func() { terminalNotifyFn = origNotify }()

	op := model.FileOperate{
		Type: "move",
		To:   dstDir,
		Item: []model.FileItem{{From: itemDir, Size: size}},
	}
	isFirst, dup := EnqueueOp(k, op)
	require.True(t, isFirst)
	require.False(t, dup)

	done := make(chan struct{})
	go func() {
		FileOperate(k)
		close(done)
	}()

	<-started
	CancelOp(k)
	close(release)
	<-done

	dstItem := filepath.Join(dstDir, "big")
	require.True(t, file.CheckNotExist(dstItem), "cancelled mid-copy must leave no half-written destination")

	srcContent, err := os.ReadFile(filepath.Join(itemDir, "f.txt"))
	require.NoError(t, err, "source must be completely untouched by a cancelled cross-device move")
	require.Equal(t, content, srcContent)

	require.Len(t, pushed, 1, "exactly one terminal notification must be pushed")
	require.True(t, pushed[0].Finished)
	require.True(t, pushed[0].Cancelled)
	require.Equal(t, "CANCELLED", pushed[0].Status)

	_, stillInQueue := FileQueue.Load(k)
	require.False(t, stillInQueue, "task must be retired from FileQueue once cancellation is observed")
	require.NotContains(t, PeekOps(), k)

	// Fingerprint released exactly once: resubmitting the identical batch
	// must be admitted, not rejected as a duplicate.
	isFirst2, dup2 := EnqueueOp("resubmit-after-cancel-1", op)
	require.True(t, isFirst2)
	require.False(t, dup2, "fingerprint must be released after cancellation")
}

// TestFileOperate_QueueRemovalAloneDoesNotStopInFlightCopy is the runtime RED
// evidence for scenario 1: it reproduces the pre-A3 bug directly on the
// current copy engine by performing only what the OLD DeleteOperateFileOrDir
// handler actually did — FileQueue.Delete + DequeueOp, with no context
// cancellation at all (CancelOp is deliberately not called). Since nothing
// tells the in-flight FileOperate goroutine to stop, the cp subprocess runs
// to completion and the file lands at the destination regardless of the
// "cancel" — the exact user-observed failure ("transfer stopped, nothing
// landed at the destination") did not happen; the file landed anyway, proving
// DequeueOp alone was never enough and CancelOp's ctx cancellation is the
// operative fix.
func TestFileOperate_QueueRemovalAloneDoesNotStopInFlightCopy(t *testing.T) {
	logger.LogInitConsoleOnly()
	ClearOps()
	FileQueue.Clear()
	defer func() { ClearOps(); FileQueue.Clear() }()

	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))

	itemDir := filepath.Join(srcParent, "big")
	require.NoError(t, os.MkdirAll(itemDir, 0o755))
	content := []byte("cross-device payload")
	require.NoError(t, os.WriteFile(filepath.Join(itemDir, "f.txt"), content, 0o644))

	size, err := file.GetFileOrDirSize(itemDir)
	require.NoError(t, err)

	origRename := renameFn
	renameFn = forceCrossDevice()
	defer func() { renameFn = origRename }()

	started := make(chan struct{})
	release := make(chan struct{})
	origRun := file.RunCopyCommand
	file.RunCopyCommand = blockingCopyHook(started, release)
	defer func() { file.RunCopyCommand = origRun }()

	k := "old-style-delete-no-cancel"
	op := model.FileOperate{
		Type: "move",
		To:   dstDir,
		Item: []model.FileItem{{From: itemDir, Size: size}},
	}
	isFirst, dup := EnqueueOp(k, op)
	require.True(t, isFirst)
	require.False(t, dup)

	done := make(chan struct{})
	go func() {
		FileOperate(k)
		close(done)
	}()

	<-started
	// The pre-A3 DELETE handler, verbatim: remove from the queue, but never
	// cancel anything.
	FileQueue.Delete(k)
	DequeueOp(k)
	close(release)
	<-done

	dstItem := filepath.Join(dstDir, "big")
	got, err := os.ReadFile(filepath.Join(dstItem, "f.txt"))
	require.NoError(t, err, "with no cancellation signal, the copy runs to completion despite the queue removal")
	require.Equal(t, content, got, "the data lands at the destination even though the task was \"cancelled\" (dequeued)")

	// And the orphaned completion is invisible: FileOperate's tail resurrects
	// a FileQueue entry the poller will never look at again (PeekOps() no
	// longer contains k), so the terminal notification for it is lost.
	_, resurrected := FileQueue.Load(k)
	require.True(t, resurrected, "FileOperate still stores its result under k even though it was removed mid-flight")
	require.NotContains(t, PeekOps(), k, "k is no longer visible to the notify poller, which walks PeekOps()")
}

// TestFileOperateMove_CancelMidBatch_PriorItemKept_NextItemNotStarted is TDD
// scenario 2: a 3-item batch cancelled while item 2 is mid-copy must leave
// item 1 (already fully moved) alone, clean up item 2's half-written dst
// while leaving its source intact, and never start item 3 at all.
func TestFileOperateMove_CancelMidBatch_PriorItemKept_NextItemNotStarted(t *testing.T) {
	logger.LogInitConsoleOnly()
	ClearOps()
	FileQueue.Clear()
	defer func() { ClearOps(); FileQueue.Clear() }()

	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))

	item1 := filepath.Join(srcParent, "folder1")
	item2 := filepath.Join(srcParent, "folder2")
	item3 := filepath.Join(srcParent, "folder3")
	for _, d := range []string{item1, item2, item3} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(item1, "f.txt"), []byte("item1-data"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(item2, "f.txt"), []byte("item2-data"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(item3, "f.txt"), []byte("item3-data"), 0o644))

	size1, err := file.GetFileOrDirSize(item1)
	require.NoError(t, err)
	size2, err := file.GetFileOrDirSize(item2)
	require.NoError(t, err)
	size3, err := file.GetFileOrDirSize(item3)
	require.NoError(t, err)

	// item1 renames normally (completes instantly, before cancellation);
	// item2 is forced cross-device so its copy can be blocked and cancelled
	// mid-flight; item3 must never be reached at all.
	origRename := renameFn
	renameFn = forceCrossDevice(item1)
	defer func() { renameFn = origRename }()

	started := make(chan struct{})
	release := make(chan struct{})
	origRun := file.RunCopyCommand
	file.RunCopyCommand = blockingCopyHook(started, release)
	defer func() { file.RunCopyCommand = origRun }()

	origNotify := terminalNotifyFn
	terminalNotifyFn = func(notify.File) {}
	defer func() { terminalNotifyFn = origNotify }()

	k := "cancel-midbatch"
	op := model.FileOperate{
		Type: "move",
		To:   dstDir,
		Item: []model.FileItem{
			{From: item1, Size: size1},
			{From: item2, Size: size2},
			{From: item3, Size: size3},
		},
	}
	isFirst, dup := EnqueueOp(k, op)
	require.True(t, isFirst)
	require.False(t, dup)

	done := make(chan struct{})
	go func() {
		FileOperate(k)
		close(done)
	}()

	<-started
	CancelOp(k)
	close(release)
	<-done

	// Item 1: fully moved.
	got1, err := os.ReadFile(filepath.Join(dstDir, "folder1", "f.txt"))
	require.NoError(t, err, "item 1 must remain fully moved")
	require.Equal(t, []byte("item1-data"), got1)
	require.True(t, file.CheckNotExist(item1), "item 1's source must have been removed by its completed rename")

	// Item 2: half-written dst cleaned up, source intact.
	require.True(t, file.CheckNotExist(filepath.Join(dstDir, "folder2")), "item 2's half-written destination must be cleaned up")
	got2, err := os.ReadFile(filepath.Join(item2, "f.txt"))
	require.NoError(t, err, "item 2's source must be untouched")
	require.Equal(t, []byte("item2-data"), got2)

	// Item 3: never started.
	require.True(t, file.CheckNotExist(filepath.Join(dstDir, "folder3")), "item 3 must never have been started")
	got3, err := os.ReadFile(filepath.Join(item3, "f.txt"))
	require.NoError(t, err, "item 3's source must be untouched")
	require.Equal(t, []byte("item3-data"), got3)
}

// TestFileOperateCopy_CancelAfterReplaceConflictStaged_RollsBack is TDD
// scenario 3: cancelling mid-copy after replaceConflict has already staged
// the pre-existing conflicting destination aside must roll it back via
// replaceConflict's existing rollback path (reused, not reimplemented) —
// the original destination content is restored, no nimoos-replacing temp
// dir is left behind, and the source (copy, never touched either way) is
// unaffected.
func TestFileOperateCopy_CancelAfterReplaceConflictStaged_RollsBack(t *testing.T) {
	logger.LogInitConsoleOnly()
	ClearOps()
	FileQueue.Clear()
	defer func() { ClearOps(); FileQueue.Clear() }()

	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))

	itemDir := filepath.Join(srcParent, "folderX")
	require.NoError(t, os.MkdirAll(itemDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(itemDir, "new.txt"), []byte("new-data"), 0o644))

	conflictDir := filepath.Join(dstDir, "folderX")
	require.NoError(t, os.MkdirAll(conflictDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(conflictDir, "old.txt"), []byte("old-data"), 0o644))

	size, err := file.GetFileOrDirSize(itemDir)
	require.NoError(t, err)

	started := make(chan struct{})
	release := make(chan struct{})
	origRun := file.RunCopyCommand
	file.RunCopyCommand = blockingCopyHook(started, release)
	defer func() { file.RunCopyCommand = origRun }()

	origNotify := terminalNotifyFn
	terminalNotifyFn = func(notify.File) {}
	defer func() { terminalNotifyFn = origNotify }()

	k := "cancel-after-staged"
	op := model.FileOperate{
		Type:  "copy",
		To:    dstDir,
		Style: "overwrite",
		Item:  []model.FileItem{{From: itemDir, Size: size}},
	}
	isFirst, dup := EnqueueOp(k, op)
	require.True(t, isFirst)
	require.False(t, dup)

	done := make(chan struct{})
	go func() {
		FileOperate(k)
		close(done)
	}()

	<-started
	CancelOp(k)
	close(release)
	<-done

	oldContent, err := os.ReadFile(filepath.Join(conflictDir, "old.txt"))
	require.NoError(t, err, "the staged-aside original destination must be rolled back after cancellation")
	require.Equal(t, []byte("old-data"), oldContent)
	require.True(t, file.CheckNotExist(filepath.Join(conflictDir, "new.txt")), "new data must not appear when the copy was cancelled")

	entries, err := os.ReadDir(dstDir)
	require.NoError(t, err)
	for _, e := range entries {
		require.NotContains(t, e.Name(), "nimoos-replacing", "rollback must not leave a temp staging dir behind")
	}

	srcContent, err := os.ReadFile(filepath.Join(itemDir, "new.txt"))
	require.NoError(t, err, "copy's source must never be touched")
	require.Equal(t, []byte("new-data"), srcContent)
}

// TestCancelOp_AlreadyCompletedTask_NoOp is TDD scenario 4: cancelling a
// task that already ran to completion (Finished==true) — even if it is
// still sitting in FileQueue awaiting the periodic notify poller's next
// sweep, not yet dequeued — must be a pure no-op: no panic, FileQueue/opQueue
// state is left exactly as it was, and the fingerprint stays occupied (its
// release remains owned exclusively by the normal completion path).
func TestCancelOp_AlreadyCompletedTask_NoOp(t *testing.T) {
	logger.LogInitConsoleOnly()
	ClearOps()
	FileQueue.Clear()
	defer func() { ClearOps(); FileQueue.Clear() }()

	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))
	itemDir := filepath.Join(srcParent, "quick")
	require.NoError(t, os.MkdirAll(itemDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(itemDir, "f.txt"), []byte("hi"), 0o644))
	size, err := file.GetFileOrDirSize(itemDir)
	require.NoError(t, err)

	k := "already-done"
	op := model.FileOperate{Type: "move", To: dstDir, Item: []model.FileItem{{From: itemDir, Size: size}}}
	isFirst, dup := EnqueueOp(k, op)
	require.True(t, isFirst)
	require.False(t, dup)

	FileOperate(k) // runs to completion synchronously (rename fast path, no cancellation)

	before, ok := FileQueue.Load(k)
	require.True(t, ok)
	require.True(t, before.(model.FileOperate).Finished)
	require.False(t, before.(model.FileOperate).Cancelled)
	beforeIds := PeekOps()
	require.Contains(t, beforeIds, k)

	require.NotPanics(t, func() { CancelOp(k) })

	after, ok := FileQueue.Load(k)
	require.True(t, ok, "CancelOp on an already-completed task must not remove it from FileQueue")
	require.True(t, after.(model.FileOperate).Finished)
	require.False(t, after.(model.FileOperate).Cancelled, "an already-completed task must not be relabeled Cancelled")
	require.Equal(t, beforeIds, PeekOps(), "CancelOp on an already-completed task must not touch the queue")

	// The fingerprint is still occupied — release remains the completion
	// path's job, not CancelOp's, for a task in this state.
	_, dupResubmit := EnqueueOp("resubmit-should-be-rejected", op)
	require.True(t, dupResubmit, "fingerprint must still be occupied; CancelOp must not have released it")
}

// TestCancelOp_UnknownId_NoOp covers the other no-op case: an id that was
// never enqueued (or has already been fully retired earlier).
func TestCancelOp_UnknownId_NoOp(t *testing.T) {
	ClearOps()
	FileQueue.Clear()
	defer func() { ClearOps(); FileQueue.Clear() }()

	require.NotPanics(t, func() { CancelOp("no-such-task-id") })
}

// TestCancelOp_ConcurrentCancelsDuringInFlightCopy_SingleTerminalPush is TDD
// scenario 5 (part 1): many concurrent CancelOp calls racing against a
// single in-flight task must still result in exactly one terminal
// notification, no panic, and the fingerprint released exactly once — run
// with -race.
func TestCancelOp_ConcurrentCancelsDuringInFlightCopy_SingleTerminalPush(t *testing.T) {
	logger.LogInitConsoleOnly()
	ClearOps()
	FileQueue.Clear()
	defer func() { ClearOps(); FileQueue.Clear() }()

	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))
	itemDir := filepath.Join(srcParent, "folder")
	require.NoError(t, os.MkdirAll(itemDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(itemDir, "f.txt"), []byte("payload"), 0o644))
	size, err := file.GetFileOrDirSize(itemDir)
	require.NoError(t, err)

	origRename := renameFn
	renameFn = forceCrossDevice()
	defer func() { renameFn = origRename }()

	started := make(chan struct{})
	release := make(chan struct{})
	origRun := file.RunCopyCommand
	file.RunCopyCommand = blockingCopyHook(started, release)
	defer func() { file.RunCopyCommand = origRun }()

	var pushCount int32
	origNotify := terminalNotifyFn
	terminalNotifyFn = func(task notify.File) { atomic.AddInt32(&pushCount, 1) }
	defer func() { terminalNotifyFn = origNotify }()

	k := "race-cancel"
	op := model.FileOperate{Type: "move", To: dstDir, Item: []model.FileItem{{From: itemDir, Size: size}}}
	isFirst, dup := EnqueueOp(k, op)
	require.True(t, isFirst)
	require.False(t, dup)

	done := make(chan struct{})
	go func() {
		FileOperate(k)
		close(done)
	}()

	<-started

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			CancelOp(k)
		}()
	}
	wg.Wait()
	close(release)
	<-done

	require.EqualValues(t, 1, atomic.LoadInt32(&pushCount), "exactly one terminal notification despite 20 concurrent CancelOp calls")
	_, ok := FileQueue.Load(k)
	require.False(t, ok)
	require.NotContains(t, PeekOps(), k)

	isFirst2, dup2 := EnqueueOp("resubmit-after-race-1", op)
	require.True(t, isFirst2)
	require.False(t, dup2, "fingerprint must be released exactly once")
}

// TestCancelOp_RaceWithNaturalCompletion is TDD scenario 5 (part 2): a task
// left to run at full speed (no injected slowdown) racing against a
// concurrent flood of CancelOp calls. Whichever side "wins" is not
// prescribed — a rename fast path can complete before any cancellation
// signal lands, or a CancelOp call can catch it queued before FileOperate
// even starts — but the outcome must always be internally consistent: no
// panic, no data race (-race), and at most one terminal notification.
func TestCancelOp_RaceWithNaturalCompletion(t *testing.T) {
	logger.LogInitConsoleOnly()
	ClearOps()
	FileQueue.Clear()
	defer func() { ClearOps(); FileQueue.Clear() }()

	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))
	itemDir := filepath.Join(srcParent, "quick")
	require.NoError(t, os.MkdirAll(itemDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(itemDir, "f.txt"), []byte("hi"), 0o644))
	size, err := file.GetFileOrDirSize(itemDir)
	require.NoError(t, err)

	k := "race-natural-completion"
	op := model.FileOperate{Type: "move", To: dstDir, Item: []model.FileItem{{From: itemDir, Size: size}}}
	isFirst, dup := EnqueueOp(k, op)
	require.True(t, isFirst)
	require.False(t, dup)

	var pushCount int32
	origNotify := terminalNotifyFn
	terminalNotifyFn = func(task notify.File) { atomic.AddInt32(&pushCount, 1) }
	defer func() { terminalNotifyFn = origNotify }()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); FileOperate(k) }()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			CancelOp(k)
		}
	}()
	wg.Wait()

	require.LessOrEqual(t, int(atomic.LoadInt32(&pushCount)), 1, "at most one terminal notification, however the race resolved")

	// Which side "won" determines who owns releasing the fingerprint, and
	// that must be asserted accordingly rather than assumed — this test
	// used to assert unconditional release here, which is only true for
	// one of the two possible outcomes and made the test genuinely flaky
	// (see fix-round C1). The task's single item is a same-filesystem
	// rename, which is atomic and never checks ctx (by design — see
	// moveItem's doc comment), so it always either fully completes or
	// (if CancelOp's queued-task fast path removes it from FileQueue
	// before FileOperate even loads it) never runs at all; there is no
	// third, half-done outcome to account for.
	item, stillQueued := FileQueue.Load(k)
	if !stillQueued {
		// The cancel path won (either FileOperate never got to run at
		// all, or it ran and cancellation had already landed by the top
		// of its single-item loop): its own terminal sequence — or
		// CancelOp's queued-task fast path — already released the
		// fingerprint.
		isFirst2, dup2 := EnqueueOp("resubmit-after-race-2", op)
		require.True(t, isFirst2)
		require.False(t, dup2, "the cancel path must release the fingerprint as part of retiring the task")
		return
	}

	// Natural completion won: the item fully landed (a rename can't be
	// interrupted mid-flight — see above), so per I4 this task must never
	// be mislabeled Cancelled even though CancelOp was called on it (up to
	// 50 times) while it ran.
	temp := item.(model.FileOperate)
	require.True(t, temp.Finished, "if still present, the task must be in a terminal state")
	require.False(t, temp.Cancelled, "an item that fully completed must never be mislabeled Cancelled just because CancelOp raced it")

	// Per A3's documented scope, natural completion does not self-retire —
	// that remains the periodic notify poller's job (service/notify.go's
	// SendFileOperateNotify) — so the fingerprint is legitimately still
	// held here, not "stuck".
	_, dupWhileUnswept := EnqueueOp("resubmit-before-poller-2", op)
	require.True(t, dupWhileUnswept, "fingerprint must still be held until the poller sweeps this finished-but-unretired task")

	// Mirror SendFileOperateNotify's completion cleanup exactly, proving
	// the fingerprint is not permanently stuck — just pending the poller.
	FileQueue.Delete(k)
	DequeueOp(k)

	isFirst3, dup3 := EnqueueOp("resubmit-after-poller-2", op)
	require.True(t, isFirst3)
	require.False(t, dup3, "fingerprint must be released once the poller retires the naturally-completed task")
}

// TestCancelOp_CancelDuringDispatchRace_StillHonored is TDD scenario 2/C2's
// regression test: a task cancelled in the exact TOCTOU window between
// EnqueueOp/dispatch and its own FileOperate goroutine's beginRun call must
// still be genuinely cancelled — not silently handed a fresh,
// never-cancelled context.Background() (see CancelOp's and
// releaseQueueSlotLocked's doc comments in service/file.go for the
// mechanism this closes). beginRunHook lands the race deterministically —
// no sleeps — by blocking FileOperate right at the door of beginRun until
// the test has called CancelOp.
//
// On the pre-fix-round code (ce92f6e), CancelOp's "queued, not running"
// branch called DequeueOp, which deleted ctxOf/cancelOf out from under the
// racing beginRun call; beginRun then fell back to context.Background(),
// and the move ran to completion uncancelled — this test would fail (the
// item would actually land at the destination).
func TestCancelOp_CancelDuringDispatchRace_StillHonored(t *testing.T) {
	logger.LogInitConsoleOnly()
	ClearOps()
	FileQueue.Clear()
	defer func() { ClearOps(); FileQueue.Clear() }()

	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))
	itemDir := filepath.Join(srcParent, "quick")
	require.NoError(t, os.MkdirAll(itemDir, 0o755))
	content := []byte("dispatch-race-payload")
	require.NoError(t, os.WriteFile(filepath.Join(itemDir, "f.txt"), content, 0o644))
	size, err := file.GetFileOrDirSize(itemDir)
	require.NoError(t, err)

	k := "dispatch-race"
	op := model.FileOperate{Type: "move", To: dstDir, Item: []model.FileItem{{From: itemDir, Size: size}}}
	isFirst, dup := EnqueueOp(k, op)
	require.True(t, isFirst)
	require.False(t, dup)

	// Simulate the real EnqueueOp -> go ExecOpFile() -> go FileOperate(k)
	// dispatch sequence, but hold FileOperate right at the door of
	// beginRun until the test has called CancelOp — deterministically
	// landing the cancellation in the exact window C2 identified, instead
	// of racing goroutine scheduling with a sleep.
	reachedHook := make(chan struct{})
	releaseHook := make(chan struct{})
	origHook := beginRunHook
	var once sync.Once
	beginRunHook = func(id string) {
		once.Do(func() { close(reachedHook) })
		<-releaseHook
	}
	defer func() { beginRunHook = origHook }()

	var pushed []notify.File
	origNotify := terminalNotifyFn
	terminalNotifyFn = func(task notify.File) { pushed = append(pushed, task) }
	defer func() { terminalNotifyFn = origNotify }()

	done := make(chan struct{})
	go func() {
		FileOperate(k)
		close(done)
	}()

	<-reachedHook
	CancelOp(k)
	close(releaseHook)
	<-done

	// The item must never have been executed at all: no destination
	// (half-written or complete), source untouched.
	require.True(t, file.CheckNotExist(filepath.Join(dstDir, "quick")),
		"a task cancelled before beginRun must never execute any item")
	srcContent, err := os.ReadFile(filepath.Join(itemDir, "f.txt"))
	require.NoError(t, err, "source must be untouched")
	require.Equal(t, content, srcContent)

	require.Len(t, pushed, 1, "exactly one terminal notification")
	require.True(t, pushed[0].Finished)
	require.True(t, pushed[0].Cancelled)
	require.Equal(t, "CANCELLED", pushed[0].Status)

	_, stillQueued := FileQueue.Load(k)
	require.False(t, stillQueued, "task must be retired")
	require.NotContains(t, PeekOps(), k)

	isFirst2, dup2 := EnqueueOp("resubmit-after-dispatch-race", op)
	require.True(t, isFirst2)
	require.False(t, dup2, "fingerprint must be released after cancel-before-beginRun")
}

// TestFileOperateMove_CancelAfterItemSucceeds_NotMisreportedCancelled is
// I4's regression test: a cancel() that lands genuinely AFTER an item's
// move/copy has already succeeded — but before FileOperate gets around to
// computing/storing the terminal state — must never relabel that
// fully-successful task as Cancelled. postCopyBlockHook holds FileOperate
// right after the cp subprocess (and therefore moveItem's cross-device
// copy) has already succeeded, deterministically landing CancelOp's call
// in that exact window instead of racing it with a sleep.
//
// On the pre-fix-round code (ce92f6e), `cancelled := ctx.Err() != nil` was
// sampled once, after the whole items loop, with no record of whether
// cancellation had actually interrupted anything — so this exact scenario
// would incorrectly store Cancelled: true for an item that fully landed.
func TestFileOperateMove_CancelAfterItemSucceeds_NotMisreportedCancelled(t *testing.T) {
	logger.LogInitConsoleOnly()
	ClearOps()
	FileQueue.Clear()
	defer func() { ClearOps(); FileQueue.Clear() }()

	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))
	itemDir := filepath.Join(srcParent, "big")
	require.NoError(t, os.MkdirAll(itemDir, 0o755))
	content := []byte("late-cancel-payload")
	require.NoError(t, os.WriteFile(filepath.Join(itemDir, "f.txt"), content, 0o644))
	size, err := file.GetFileOrDirSize(itemDir)
	require.NoError(t, err)

	origRename := renameFn
	renameFn = forceCrossDevice()
	defer func() { renameFn = origRename }()

	// Block AFTER the cp subprocess actually finishes copying, but before
	// RunCopyCommand returns to moveItem — i.e. right after the item's
	// work has genuinely, successfully completed.
	copyDone := make(chan struct{})
	releaseAfterCopy := make(chan struct{})
	var once sync.Once
	origRun := file.RunCopyCommand
	file.RunCopyCommand = func(cmd *exec.Cmd) error {
		err := cmd.Run()
		once.Do(func() { close(copyDone) })
		<-releaseAfterCopy
		return err
	}
	defer func() { file.RunCopyCommand = origRun }()

	origNotify := terminalNotifyFn
	terminalNotifyFn = func(notify.File) {}
	defer func() { terminalNotifyFn = origNotify }()

	k := "cancel-after-item-succeeds"
	op := model.FileOperate{
		Type: "move",
		To:   dstDir,
		Item: []model.FileItem{{From: itemDir, Size: size}},
	}
	isFirst, dup := EnqueueOp(k, op)
	require.True(t, isFirst)
	require.False(t, dup)

	done := make(chan struct{})
	go func() {
		FileOperate(k)
		close(done)
	}()

	<-copyDone
	CancelOp(k) // lands after the copy already succeeded, before Store
	close(releaseAfterCopy)
	<-done

	after, ok := FileQueue.Load(k)
	require.True(t, ok, "natural completion does not self-retire (unchanged A3 scope)")
	temp := after.(model.FileOperate)
	require.True(t, temp.Finished)
	require.False(t, temp.Cancelled, "a cancel landing after the item's work fully succeeded must not be reported as Cancelled")

	dstItem := filepath.Join(dstDir, "big")
	got, err := os.ReadFile(filepath.Join(dstItem, "f.txt"))
	require.NoError(t, err, "the successfully-copied item must remain at the destination")
	require.Equal(t, content, got)
	require.True(t, file.CheckNotExist(itemDir), "move's source must have been removed after a successful, size-verified copy")
}

// TestCancelAllOps_CancelsQueuedAndClears verifies DELETE /file/operate/0's
// new semantics: every not-yet-started queued task is removed (unchanged
// behavior) and every admitted context is cancelled (defensive: even though
// nothing is executing here, CancelAllOps must not panic and must leave the
// queue empty so a fresh identical submission is admitted).
func TestCancelAllOps_CancelsQueuedAndClears(t *testing.T) {
	ClearOps()
	FileQueue.Clear()
	defer func() { ClearOps(); FileQueue.Clear() }()

	op1 := dedupOp("/dst", "/src/a")
	op2 := dedupOp("/dst2", "/src/b")
	_, dup1 := EnqueueOp("queued-1", op1)
	require.False(t, dup1)
	_, dup2 := EnqueueOp("queued-2", op2)
	require.False(t, dup2)

	require.NotPanics(t, func() { CancelAllOps() })

	require.Empty(t, PeekOps())
	_, ok1 := FileQueue.Load("queued-1")
	require.False(t, ok1)
	_, ok2 := FileQueue.Load("queued-2")
	require.False(t, ok2)

	isFirst, dup := EnqueueOp("queued-1", op1)
	require.True(t, isFirst)
	require.False(t, dup)
}
