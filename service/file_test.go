package service

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS/model"
	"github.com/NimoTech/NimoOS/pkg/utils/file"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

var (
	ctx    context.Context
	cancel context.CancelFunc
)

func TestNewInteruptReader(t *testing.T) {
	t.Skip("This test is always failing. Skipped to unblock releasing - MUST FIX!")

	ctx, cancel = context.WithCancel(context.Background())

	go func() {
		// 在初始上下文的基础上创建一个有取消功能的上下文
		//	ctx, cancel := context.WithCancel(ctx)
		fmt.Println("开始")
		fIn, err := os.Open("/Users/liangjianli/Downloads/demo_data.tar.gz")
		if err != nil {
		}
		defer fIn.Close()
		fmt.Println("创建新文件")
		fOut, err := os.Create("/Users/liangjianli/Downloads/demo_data1.tar.gz")
		if err != nil {
			fmt.Println(err)
		}

		defer fOut.Close()

		fmt.Println("准备复制")
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
		fmt.Println("退出")
		ddd()
	}()
	time.Sleep(time.Second * 10)
}

func ddd() {
	cancel()
}

func TestOpDestPath(t *testing.T) {
	require.Equal(t, "/dst/pics", opDestPath("/src/pics", "/dst"))
	require.Equal(t, "/dst/pics", opDestPath("/src/pics/", "/dst")) // 尾斜杠不再退化
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
	FileQueue = sync.Map{}
	defer func() { ClearOps(); FileQueue = sync.Map{} }()

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
	FileQueue = sync.Map{}
	defer func() { ClearOps(); FileQueue = sync.Map{} }()

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
	FileQueue = sync.Map{}
	defer func() { ClearOps(); FileQueue = sync.Map{} }()

	op := dedupOp("/dst", "/src/a")
	_, dup := EnqueueOp("id1", op)
	require.False(t, dup)

	_, dup2 := EnqueueOp("id2", op)
	require.True(t, dup2)

	// Mimic route/v1/file.go's DeleteOperateFileOrDir(id="0") branch.
	FileQueue = sync.Map{}
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
	FileQueue = sync.Map{}
	defer func() { ClearOps(); FileQueue = sync.Map{} }()

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
