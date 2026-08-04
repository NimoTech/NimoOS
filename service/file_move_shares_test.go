package service

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/NimoTech/NimoOS-Common/external"
	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS/codegen/message_bus"
	"github.com/NimoTech/NimoOS/model"
	"github.com/NimoTech/NimoOS/pkg/utils/file"
	smodel "github.com/NimoTech/NimoOS/service/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// fakeMoveSharesRecorder only records the call arguments to
// RewriteSharePathPrefix, for use by the assertion in FileOperate's move
// branch that "share paths are only rewritten once the move has actually
// landed"; the remaining SharesService methods are no-op implementations
// that just satisfy the interface.
type fakeMoveSharesRecorder struct {
	mu    sync.Mutex
	calls [][2]string
}

func (f *fakeMoveSharesRecorder) GetSharesList() []smodel.SharesDBModel         { return nil }
func (f *fakeMoveSharesRecorder) GetSharesByPath(string) []smodel.SharesDBModel { return nil }
func (f *fakeMoveSharesRecorder) GetSharesByName(string) []smodel.SharesDBModel { return nil }
func (f *fakeMoveSharesRecorder) CreateShare(smodel.SharesDBModel)              {}
func (f *fakeMoveSharesRecorder) DeleteShare(string)                            {}
func (f *fakeMoveSharesRecorder) UpdateConfigFile()                             {}
func (f *fakeMoveSharesRecorder) InitSambaConfig()                              {}
func (f *fakeMoveSharesRecorder) DeleteShareByPath(string)                      {}
func (f *fakeMoveSharesRecorder) RewriteSharePathPrefix(oldPath, newPath string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, [2]string{oldPath, newPath})
	return 1
}

func (f *fakeMoveSharesRecorder) snapshot() [][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][2]string, len(f.calls))
	copy(out, f.calls)
	return out
}

// fakeMoveRepository is the minimum service.Repository fake needed to exercise
// FileOperate's post-move shares rewiring: only Shares() is meaningful, every
// other member is unused by FileOperate and returns nil.
type fakeMoveRepository struct{ shares SharesService }

func (f fakeMoveRepository) Casa() CasaService                            { return nil }
func (f fakeMoveRepository) Connections() ConnectionsService              { return nil }
func (f fakeMoveRepository) Gateway() external.ManagementService          { return nil }
func (f fakeMoveRepository) Health() HealthService                        { return nil }
func (f fakeMoveRepository) Notify() NotifyServer                         { return nil }
func (f fakeMoveRepository) Rely() RelyService                            { return nil }
func (f fakeMoveRepository) RootGrants() RootGrantRepo                    { return nil }
func (f fakeMoveRepository) Shares() SharesService                        { return f.shares }
func (f fakeMoveRepository) System() SystemService                        { return nil }
func (f fakeMoveRepository) Storage() StorageService                      { return nil }
func (f fakeMoveRepository) MessageBus() *message_bus.ClientWithResponses { return nil }
func (f fakeMoveRepository) Peer() PeerService                            { return nil }
func (f fakeMoveRepository) Other() OtherService                          { return nil }
func (f fakeMoveRepository) User() UserService                            { return nil }

// withFakeMoveShares installs a recording fake Repository as MyService for the
// duration of the test and restores the previous (normally nil, per this
// package's other FileOperate tests) value afterwards — MyService is a
// package global, so leaving it non-nil would leak into unrelated tests.
func withFakeMoveShares(t *testing.T) *fakeMoveSharesRecorder {
	t.Helper()
	rec := &fakeMoveSharesRecorder{}
	prev := MyService
	MyService = fakeMoveRepository{shares: rec}
	t.Cleanup(func() { MyService = prev })
	return rec
}

// TestFileOperateMove_NoConflict_RewritesSharePath covers the plain
// no-conflict move fast path (line ~761's createdPaths append site): a
// successful landing must rewrite the share path from the source to the
// actual destination.
func TestFileOperateMove_NoConflict_RewritesSharePath(t *testing.T) {
	logger.LogInitConsoleOnly()
	rec := withFakeMoveShares(t)

	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))

	itemDir := filepath.Join(srcParent, "shared-folder")
	require.NoError(t, os.MkdirAll(itemDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(itemDir, "f.txt"), []byte("hi"), 0o644))

	size, err := file.GetFileOrDirSize(itemDir)
	require.NoError(t, err)

	op := model.FileOperate{
		Type: "move",
		To:   dstDir,
		Item: []model.FileItem{{From: itemDir, Size: size}},
	}
	k := "move-shares-noconflict-" + uuid.NewString()
	FileQueue.Store(k, op)
	FileOperate(k)

	wantDst := filepath.Join(dstDir, "shared-folder")
	require.Equal(t, [][2]string{{itemDir, wantDst}}, rec.snapshot())
}

// TestFileOperateMove_ConflictReplace_RewritesSharePath covers the
// overwrite/replace conflict branch (line ~700's createdPaths append site):
// the landed path is the original conflicting dst.
func TestFileOperateMove_ConflictReplace_RewritesSharePath(t *testing.T) {
	logger.LogInitConsoleOnly()
	rec := withFakeMoveShares(t)

	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))

	itemDir := filepath.Join(srcParent, "folderR")
	require.NoError(t, os.MkdirAll(itemDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(itemDir, "new.txt"), []byte("new-data"), 0o644))

	conflictDir := filepath.Join(dstDir, "folderR")
	require.NoError(t, os.MkdirAll(conflictDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(conflictDir, "old.txt"), []byte("old-data"), 0o644))

	size, err := file.GetFileOrDirSize(itemDir)
	require.NoError(t, err)

	op := model.FileOperate{
		Type:  "move",
		To:    dstDir,
		Style: "overwrite",
		Item:  []model.FileItem{{From: itemDir, Size: size}},
	}
	k := "move-shares-replace-" + uuid.NewString()
	FileQueue.Store(k, op)
	FileOperate(k)

	require.Equal(t, [][2]string{{itemDir, conflictDir}}, rec.snapshot())
}

// TestFileOperateMove_ConflictRename_RewritesToRenameDst is the case the task
// brief specifically calls out: the "rename" (keep-both) conflict style lands
// at a de-conflicted sibling name (renameDst), NOT the original conflicting
// dst. The share-path rewrite must follow the actual landed path.
func TestFileOperateMove_ConflictRename_RewritesToRenameDst(t *testing.T) {
	logger.LogInitConsoleOnly()
	rec := withFakeMoveShares(t)

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
	k := "move-shares-rename-" + uuid.NewString()
	FileQueue.Store(k, op)
	FileOperate(k)

	wantRenameDst := filepath.Join(dstDir, "report(1).docx")
	require.NotEqual(t, conflictFile, wantRenameDst, "sanity: renameDst must differ from the original conflicting dst")
	require.Equal(t, [][2]string{{itemFile, wantRenameDst}}, rec.snapshot())
}

// TestFileOperateMove_ConflictSkip_NoRewrite: skip means the item never
// actually moved (source and destination both left alone) — no share rewrite
// must fire.
func TestFileOperateMove_ConflictSkip_NoRewrite(t *testing.T) {
	logger.LogInitConsoleOnly()
	rec := withFakeMoveShares(t)

	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))

	itemDir := filepath.Join(srcParent, "folderSkip")
	require.NoError(t, os.MkdirAll(itemDir, 0o755))
	conflictDir := filepath.Join(dstDir, "folderSkip")
	require.NoError(t, os.MkdirAll(conflictDir, 0o755))

	size, err := file.GetFileOrDirSize(itemDir)
	require.NoError(t, err)

	op := model.FileOperate{
		Type:  "move",
		To:    dstDir,
		Style: "skip",
		Item:  []model.FileItem{{From: itemDir, Size: size}},
	}
	k := "move-shares-skip-" + uuid.NewString()
	FileQueue.Store(k, op)
	FileOperate(k)

	require.Empty(t, rec.snapshot(), "skip must not move anything, so no share path should be rewritten")
}

// TestFileOperateCopy_NoRewrite: copy leaves the source in place, so nothing
// is hanging — the copy branch must never touch shares.
func TestFileOperateCopy_NoRewrite(t *testing.T) {
	logger.LogInitConsoleOnly()
	rec := withFakeMoveShares(t)

	root := t.TempDir()
	srcParent := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(srcParent, 0o755))
	require.NoError(t, os.MkdirAll(dstDir, 0o755))

	itemDir := filepath.Join(srcParent, "folderCopy")
	require.NoError(t, os.MkdirAll(itemDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(itemDir, "f.txt"), []byte("hi"), 0o644))

	size, err := file.GetFileOrDirSize(itemDir)
	require.NoError(t, err)

	op := model.FileOperate{
		Type: "copy",
		To:   dstDir,
		Item: []model.FileItem{{From: itemDir, Size: size}},
	}
	k := "copy-shares-noop-" + uuid.NewString()
	FileQueue.Store(k, op)
	FileOperate(k)

	require.Empty(t, rec.snapshot(), "copy must never rewrite share paths — the source still exists")
}
