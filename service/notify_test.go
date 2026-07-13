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
func TestBuildFileNotifyTask_CarriesParkedPath(t *testing.T) {
	op := model2.FileOperate{
		Type:          "move",
		To:            "/dst",
		TotalSize:     100,
		ProcessedSize: 40,
		Item: []model2.FileItem{
			{From: "/src/a", Size: 100, ProcessedSize: 40, ParkedPath: "/dst/a.nimoos-replacing-abcd1234"},
		},
	}

	task, finished := buildFileNotifyTask("task-1", op)

	require.False(t, finished)
	require.Equal(t, "/dst/a.nimoos-replacing-abcd1234", task.ParkedPath)
	require.Equal(t, "task-1", task.Id)
	require.Equal(t, "/src/a", task.ProcessingPath)
	require.Equal(t, "PROCESSING", task.Status)
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

// TestBuildFileNotifyTask_FinishedSkipsItemScan asserts the finished branch
// returns early (mirroring the pre-refactor inline behavior) without
// inspecting Item, since a finished task's items are no longer meaningful.
func TestBuildFileNotifyTask_FinishedSkipsItemScan(t *testing.T) {
	op := model2.FileOperate{
		Type:          "move",
		To:            "/dst",
		TotalSize:     100,
		ProcessedSize: 100,
		Finished:      true,
		Item: []model2.FileItem{
			{From: "/src/a", Size: 100, ProcessedSize: 100, ParkedPath: "/dst/a.nimoos-replacing-deadbeef"},
		},
	}

	task, finished := buildFileNotifyTask("task-3", op)

	require.True(t, finished)
	require.Equal(t, "FINISHED", task.Status)
	require.Empty(t, task.ParkedPath)
}
