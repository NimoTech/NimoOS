package upload

import (
	"os"
	"path/filepath"
	"strings"
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

// listMountPoints 枚举当前挂载点(读 /proc/self/mounts);包级变量便于测试注入。
var listMountPoints = func() []string {
	data, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 {
			// mounts 里空格转义为 \040(如带空格的卷标)。
			out = append(out, strings.ReplaceAll(f[1], `\040`, " "))
		}
	}
	return out
}

// targetOrphaned 判定批次目标目录是否「确定性缺失」:目标 stat 不到,且被某个
// 非根挂载点覆盖(即所在卷确实处于挂载状态)。卷未挂载时(USB 拔了/RAID 未装配/
// 冷启动枚举慢)目标同样 stat 不到,但覆盖它的挂载点也不在——不判死,卷回来
// 角标就回来。根 "/" 不算覆盖:卷缺席时路径落回根文件系统视角(如 /media/X
// 残留空目录),凭根挂载无法区分「删了」和「没挂」。代价是数据直接放根文件系统
// 的形态享受不到孤儿兜底(NimoOS 数据卷均为独立挂载,实际不受影响)。
func targetOrphaned(target string, mountPoints []string) bool {
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		return false // 存在,或权限等其它错误:都不判孤儿
	}
	clean := filepath.Clean(target)
	for _, m := range mountPoints {
		mm := filepath.Clean(m)
		if mm == "/" {
			continue
		}
		if clean == mm || strings.HasPrefix(clean, mm+"/") {
			return true
		}
	}
	return false
}

// cancelUnfinished 终止批次未完成的 tus 任务并清各暂存目录残件。
func cancelUnfinished(tasks *TaskStore, batchID string, stagingDirs []string, now int64) error {
	list, err := tasks.ListUnfinishedByBatch(batchID)
	if err != nil {
		return err
	}
	expires := now + common.UploadCanceledTTLSeconds
	for _, t := range list {
		_, _ = commonUpload.Cancel(tasks, t.ID, expires)
		for _, dir := range stagingDirs {
			os.Remove(filepath.Join(dir, t.ID))         //nolint:errcheck
			os.Remove(filepath.Join(dir, t.ID+".info")) //nolint:errcheck
		}
	}
	return nil
}

// SweepBatches 执行一轮批次扫描:
//  1. active 且 (now - last_progress_at) > 120s → interrupted(角标出现)
//  2. interrupted 且未清 staging 且 (now - interrupted_at) > 600s → 终止任务 + 清 staging
//  3. 孤儿兜底:目标目录已被删除(targetOrphaned)的 interrupted 批次 → 自动放弃
//  4. 终态(completed/abandoned)→ 删除批次与 items
//
// interrupted 批次不自动过期:角标一直挂着,直到用户手动放弃或补传完成。
// 唯一例外是第 3 步的孤儿——角标只挂在列表里实际存在的条目上,目标目录没了,
// 角标永不显示、用户没有手动入口,不清会永久滞留(真机事故:/media/RAID_0/homer
// 删除后 4 条批次 7k+ items 无人可清)。
//
// stagingDirs 是当前所有在用的暂存目录(见 StagingDirs):任务 ID 现在可能带卷前缀、
// 落在不同卷的暂存目录里,不能再假设单一目录,清理时逐个尝试(不存在则 os.Remove
// 静默失败,忽略即可)。
func SweepBatches(batches *BatchStore, tasks *TaskStore, stagingDirs []string, now int64) error {
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
		if err := cancelUnfinished(tasks, b.ID, stagingDirs, now); err != nil {
			return err
		}
		if err := batches.MarkStagingCleaned(b.ID); err != nil {
			return err
		}
	}
	mounts := listMountPoints()
	for _, b := range interrupted {
		if !targetOrphaned(b.TargetPath, mounts) {
			continue
		}
		if err := cancelUnfinished(tasks, b.ID, stagingDirs, now); err != nil {
			return err
		}
		if err := batches.SetStatus(b.ID, BatchStatusAbandoned); err != nil {
			return err
		}
	}
	if _, err := batches.DeleteTerminal(); err != nil {
		return err
	}
	return nil
}

// StartBatchSweeper 在独立 goroutine 中按固定间隔扫描(与任务 GC 并存,职责不同:
// 任务 GC 管 o_upload_tasks 生命周期,本扫描器管批次状态机与延迟清理)。
// stagingDirsFn 每轮重新枚举当前在用的暂存目录(见 StagingDirs),而非固定传入一次
// ——这样运行期间新挂载的卷一旦产生了暂存目录,下一轮扫描就能覆盖到。
func StartBatchSweeper(batches *BatchStore, tasks *TaskStore, stagingDirsFn func() []string) {
	go func() {
		ticker := time.NewTicker(time.Duration(BatchSweepIntervalSeconds) * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if err := SweepBatches(batches, tasks, stagingDirsFn(), time.Now().Unix()); err != nil {
				logger.Error("upload batch sweep failed", zap.Error(err))
			}
		}
	}()
}
