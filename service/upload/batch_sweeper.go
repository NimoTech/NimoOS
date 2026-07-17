package upload

import (
	"os"
	"path/filepath"
	"time"

	commonUpload "github.com/NimoTech/NimoOS-Common/upload"
	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS/common"
	"go.uber.org/zap"
)

const (
	// BatchIdleInterruptSeconds: active 批次多久无进度自动判中断(关窗信号丢失的兜底)。
	BatchIdleInterruptSeconds = int64(120)
	// BatchStagingGraceSeconds: 超时中断后再等多久才清 staging——期间 tus 会话内
	// 自动重连(最长约 4 分钟)仍可从 offset 续上,清早了会把整文件打断。
	BatchStagingGraceSeconds = int64(600)
	// BatchSweepIntervalSeconds: 扫描间隔。
	BatchSweepIntervalSeconds = int64(30)
)

// SweepBatches 执行一轮批次扫描:
//  1. active 且 (now - last_progress_at) > 120s → interrupted(角标出现)
//  2. interrupted 且未清 staging 且 (now - interrupted_at) > 600s → 终止任务 + 清 staging
//  3. expires_at 到期 → 删除批次与 items
func SweepBatches(batches *BatchStore, tasks *TaskStore, stagingDir string, now int64) error {
	actives, err := batches.ListByStatus(BatchStatusActive)
	if err != nil {
		return err
	}
	for _, b := range actives {
		last := b.LastProgressAt
		if last == 0 {
			last = b.CreatedAt
		}
		if now-last > BatchIdleInterruptSeconds {
			if err := batches.SetInterrupted(b.ID, now); err != nil {
				return err
			}
		}
	}
	interrupted, err := batches.ListByStatus(BatchStatusInterrupted)
	if err != nil {
		return err
	}
	for _, b := range interrupted {
		if b.StagingCleaned || now-b.InterruptedAt <= BatchStagingGraceSeconds {
			continue
		}
		list, lerr := tasks.ListUnfinishedByBatch(b.ID)
		if lerr != nil {
			return lerr
		}
		expires := now + common.UploadCanceledTTLSeconds
		for _, t := range list {
			_, _ = commonUpload.Cancel(tasks, t.ID, expires)
			os.Remove(filepath.Join(stagingDir, t.ID))         //nolint:errcheck
			os.Remove(filepath.Join(stagingDir, t.ID+".info")) //nolint:errcheck
		}
		if err := batches.MarkStagingCleaned(b.ID); err != nil {
			return err
		}
	}
	if _, err := batches.DeleteExpired(now); err != nil {
		return err
	}
	return nil
}

// StartBatchSweeper 在独立 goroutine 中按固定间隔扫描(与任务 GC 并存,职责不同:
// 任务 GC 管 o_upload_tasks 生命周期,本扫描器管批次状态机与延迟清理)。
func StartBatchSweeper(batches *BatchStore, tasks *TaskStore, stagingDir string) {
	go func() {
		ticker := time.NewTicker(time.Duration(BatchSweepIntervalSeconds) * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if err := SweepBatches(batches, tasks, stagingDir, time.Now().Unix()); err != nil {
				logger.Error("upload batch sweep failed", zap.Error(err))
			}
		}
	}()
}
