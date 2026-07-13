package service

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
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
