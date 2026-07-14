package service

import (
	"testing"

	model2 "github.com/NimoTech/NimoOS/model"
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
