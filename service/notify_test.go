package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	model2 "github.com/NimoTech/NimoOS/model"
	"github.com/NimoTech/NimoOS/pkg/utils/file"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestBuildFileNotifyTask_CarriesParkedPath asserts that when a FileOperate's
// item has ParkedPath set (conflict-replace rollback-restore itself failed,
// see service.replaceConflict / model.FileItem.ParkedPath), the notify.File
// DTO handed to SendFileOperateNotify's MessageBus/WS publish carries that
// path too, so it is externally discoverable instead of log-only.
//
// The FileOperate shape here mirrors production: FileOperate.ParkedPath is
// only ever set inside FileOperate's per-item loop in service/file.go, and
// temp.Finished=true is stored atomically together with it right after that
// loop. So every real queue entry carrying a ParkedPath also has
// Finished==true (and/or ProcessedSize>=TotalSize) — the finished branch is
// the only notification actually sent for that task id before it's deleted
// from the queue, so that's the branch that must carry ParkedPath.
func TestBuildFileNotifyTask_CarriesParkedPath(t *testing.T) {
	op := model2.FileOperate{
		Type:          "move",
		To:            "/dst",
		TotalSize:     100,
		ProcessedSize: 100,
		Finished:      true,
		Item: []model2.FileItem{
			{From: "/src/a", Size: 100, ProcessedSize: 100, ParkedPath: "/dst/a.nimoos-replacing-abcd1234"},
		},
	}

	task, finished := buildFileNotifyTask("task-1", op)

	require.True(t, finished)
	require.Equal(t, "FINISHED", task.Status)
	require.Equal(t, "task-1", task.Id)
	require.Equal(t, "/dst/a.nimoos-replacing-abcd1234", task.ParkedPath)
}

// TestBuildFileNotifyTask_NoParkedPath asserts the field stays empty (and so
// omitted via json's omitempty) for the ordinary, non-parked case, matching
// the existing consumer contract.
func TestBuildFileNotifyTask_NoParkedPath(t *testing.T) {
	op := model2.FileOperate{
		Type:          "move",
		To:            "/dst",
		TotalSize:     100,
		ProcessedSize: 0,
		Item: []model2.FileItem{
			{From: "/src/a", Size: 100, ProcessedSize: 0},
		},
	}

	task, finished := buildFileNotifyTask("task-2", op)

	require.False(t, finished)
	require.Empty(t, task.ParkedPath)
	require.Equal(t, "STARTING", task.Status)
}

// --- zero-byte task regression (2026-08-13 field report) ---
//
// A move/copy batch whose sources contain no regular files at all — a folder
// tree of nothing but empty directories, the shape the user reported — has
// TotalSize == 0, because file.GetFileOrDirSize only sums non-directory
// entries. The queue entry therefore starts life at ProcessedSize(0) >=
// TotalSize(0), which the notify poller used to read as "already done".
//
// That is not a cosmetic mislabel: SendFileOperateNotify's finished branch
// (service/notify.go) does FileQueue.Delete + DequeueOp on it. The route
// dispatches the worker (go ExecOpFile -> go FileOperate) and the poller
// (go SendFileOperateNotify) at the same moment, so whichever wins decides
// the outcome — on the reporter's device the poller won 6 times out of 10,
// retiring the task before FileOperate ever loaded it. Result: HTTP 200,
// nothing moved, no error anywhere.

// zeroByteMoveFixture builds a real on-disk source tree containing only
// empty directories (0 bytes, matching the field report) plus an empty
// destination directory, and returns the FileOperate the route would have
// built for it — including the TotalSize/Size values PostOperateFileOrDir
// computes via file.GetFileOrDirSize, which are 0 here.
func zeroByteMoveFixture(t *testing.T) (op model2.FileOperate, srcItem, dstItem string) {
	t.Helper()
	logger.LogInitConsoleOnly()

	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))

	srcItem = filepath.Join(srcParent, "aasd")
	for _, sub := range []string{"d1", "d2", "d3"} {
		require.NoError(t, os.MkdirAll(filepath.Join(srcItem, sub), 0o755))
	}

	size, err := file.GetFileOrDirSize(srcItem)
	require.NoError(t, err)
	require.Zero(t, size, "fixture must really be 0 bytes, otherwise it doesn't reproduce anything")

	op = model2.FileOperate{
		Type:  "move",
		To:    dstDir,
		Style: "overwrite", // what NimoOS-UI's paste() actually sends
		Item:  []model2.FileItem{{From: srcItem, Size: size}},
		// PostOperateFileOrDir sets both of these explicitly.
		TotalSize:     size,
		ProcessedSize: 0,
	}
	return op, srcItem, filepath.Join(dstDir, "aasd")
}

// TestBuildFileNotifyTask_ZeroByteTaskNotFinishedBeforeWorkerRuns pins the
// predicate itself: a freshly-enqueued 0-byte task has done no work yet, so
// it must not be reported (and hence retired) as finished.
func TestBuildFileNotifyTask_ZeroByteTaskNotFinishedBeforeWorkerRuns(t *testing.T) {
	op := model2.FileOperate{
		Type:          "move",
		To:            "/dst",
		TotalSize:     0,
		ProcessedSize: 0,
		Item:          []model2.FileItem{{From: "/src/emptydirs", Size: 0}},
	}

	_, finished := buildFileNotifyTask("task-zero", op)

	require.False(t, finished,
		"a 0-byte task that has not run yet must not be treated as finished — "+
			"the notify loop deletes it from FileQueue and dequeues it on `finished`, "+
			"which is what silently cancels the move before FileOperate ever loads it")
}

// TestZeroByteMove_NotRetiredBeforeWorkerRuns is the end-to-end reproduction
// on a real filesystem, using the real queue functions in the real order the
// route uses them, with the poller deliberately scheduled first (the losing
// race the reporter hit). The data must still move.
func TestZeroByteMove_NotRetiredBeforeWorkerRuns(t *testing.T) {
	ClearOps()
	FileQueue.Clear()
	defer func() { ClearOps(); FileQueue.Clear() }()

	op, srcItem, dstItem := zeroByteMoveFixture(t)

	id := "zero-byte-" + uuid.NewString()
	isFirst, dup := EnqueueOp(id, op)
	require.True(t, isFirst)
	require.False(t, dup)

	// One sweep of SendFileOperateNotify's task loop, inlined: the MessageBus
	// publish around it needs a live MyService, but the retirement decision
	// under test is exactly these four lines.
	for _, v := range PeekOps() {
		item, ok := FileQueue.Load(v)
		require.True(t, ok)
		_, finished := buildFileNotifyTask(v, item.(model2.FileOperate))
		if finished {
			FileQueue.Delete(v)
			DequeueOp(v)
		}
	}

	// The worker goroutine the route already dispatched now gets scheduled.
	FileOperate(id)

	require.DirExists(t, dstItem, "the empty-directory tree must have been moved to the destination")
	require.DirExists(t, filepath.Join(dstItem, "d1"))
	require.True(t, file.CheckNotExist(srcItem), "the source must be gone after a move")
}

// TestZeroByteMove_RetiredAfterWorkerCompletes is the other half of the
// guard: not treating a 0-byte task as pre-finished must not leave it stuck
// in the queue forever. Once FileOperate has actually run, the poller must
// see it as finished and retire it.
func TestZeroByteMove_RetiredAfterWorkerCompletes(t *testing.T) {
	ClearOps()
	FileQueue.Clear()
	defer func() { ClearOps(); FileQueue.Clear() }()

	op, _, dstItem := zeroByteMoveFixture(t)

	id := "zero-byte-done-" + uuid.NewString()
	_, dup := EnqueueOp(id, op)
	require.False(t, dup)

	FileOperate(id)
	require.DirExists(t, dstItem)

	item, ok := FileQueue.Load(id)
	require.True(t, ok, "the task must still be in the queue for the poller to retire")
	_, finished := buildFileNotifyTask(id, item.(model2.FileOperate))
	require.True(t, finished,
		"a completed 0-byte task must be retired by the notify poller — "+
			"otherwise the fix trades a silent no-op for a task wedged in the queue forever, "+
			"blocking every later task behind it (ExecOpFile only ever starts PeekOps()[0])")
}

// TestZeroByteMove_RetiredEvenIfProgressPollerClobbersFinished covers the
// way the previous test's guarantee can be lost in production.
//
// CheckFileStatus (service/file.go) runs concurrently on its own 3s timer.
// Each pass does FileQueue.Load -> walk the destination for sizes ->
// FileQueue.Store of that pre-walk snapshot. If FileOperate stores its
// terminal state inside that window, the snapshot write-back rolls
// Finished back to false. For a task with TotalSize > 0 the
// ProcessedSize >= TotalSize fallback still retires it, but for a 0-byte
// task there is no fallback left — so completion has to be recorded
// somewhere CheckFileStatus cannot overwrite.
func TestZeroByteMove_RetiredEvenIfProgressPollerClobbersFinished(t *testing.T) {
	ClearOps()
	FileQueue.Clear()
	defer func() { ClearOps(); FileQueue.Clear() }()

	op, _, dstItem := zeroByteMoveFixture(t)

	id := "zero-byte-clobber-" + uuid.NewString()
	_, dup := EnqueueOp(id, op)
	require.False(t, dup)

	// The snapshot CheckFileStatus loaded before its size walk.
	snapshot, ok := FileQueue.Load(id)
	require.True(t, ok)

	FileOperate(id)
	require.DirExists(t, dstItem)

	// ... and its write-back, landing after the worker stored Finished=true.
	FileQueue.Store(id, snapshot.(model2.FileOperate))

	item, ok := FileQueue.Load(id)
	require.True(t, ok)
	require.False(t, item.(model2.FileOperate).Finished,
		"precondition: the stale write-back really did clear the terminal flag")

	_, finished := buildFileNotifyTask(id, item.(model2.FileOperate))
	require.True(t, finished,
		"the task ran to completion, so it must still be retired even though "+
			"CheckFileStatus's stale snapshot clobbered Finished in FileQueue")
}

// TestStoreFileProgress_DoesNotResurrectRetiredTask guards the other
// direction of the same window: once the notify poller has retired a task
// (FileQueue.Delete + DequeueOp), CheckFileStatus's in-flight write-back must
// not put a ghost entry back into FileQueue. A ghost is never in
// opQueue.ids, so no poller pass can ever retire it again, and
// SendFileOperateNotify's "len == 0" exit condition (which counts FileQueue,
// not the id queue) never becomes true — the never-terminating notify loop
// from the 2026-07 incident.
func TestStoreFileProgress_DoesNotResurrectRetiredTask(t *testing.T) {
	ClearOps()
	FileQueue.Clear()
	defer func() { ClearOps(); FileQueue.Clear() }()

	op, _, _ := zeroByteMoveFixture(t)

	id := "ghost-" + uuid.NewString()
	_, dup := EnqueueOp(id, op)
	require.False(t, dup)

	// CheckFileStatus's pre-walk Load ...
	snapshot, ok := FileQueue.Load(id)
	require.True(t, ok)

	// ... the task completing and being retired while the walk ran ...
	FileOperate(id)
	FileQueue.Delete(id)
	DequeueOp(id)

	// ... and the write-back arriving afterwards.
	storeFileProgress(id, snapshot.(model2.FileOperate))

	_, resurrected := FileQueue.Load(id)
	require.False(t, resurrected,
		"a retired task must not be resurrected in FileQueue by a late progress write-back")
}

// TestStoreFileProgress_DropsWriteOverTerminalState is the same guard for the
// case where the task is still queued but FileOperate has already stored its
// terminal state: the pre-walk snapshot must not roll Finished back.
func TestStoreFileProgress_DropsWriteOverTerminalState(t *testing.T) {
	ClearOps()
	FileQueue.Clear()
	defer func() { ClearOps(); FileQueue.Clear() }()

	op, _, _ := zeroByteMoveFixture(t)

	id := "clobber-guard-" + uuid.NewString()
	_, dup := EnqueueOp(id, op)
	require.False(t, dup)

	snapshot, ok := FileQueue.Load(id)
	require.True(t, ok)

	FileOperate(id)

	storeFileProgress(id, snapshot.(model2.FileOperate))

	cur, ok := FileQueue.Load(id)
	require.True(t, ok)
	require.True(t, cur.(model2.FileOperate).Finished,
		"a stale progress write-back must not clear the terminal Finished flag")
}
