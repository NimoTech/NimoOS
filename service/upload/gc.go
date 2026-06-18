package upload

import (
	"os"
	"path/filepath"
	"time"

	"github.com/NimoTech/NimoOS/common"
	commonUpload "github.com/NimoTech/NimoOS-Common/upload"
	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"go.uber.org/zap"
)

// GCConfig 控制分级清理。生产用 DefaultGCConfig 从 common 常量构造。
//
// 注意:SweepTasks 实际只读取 StagingDir 与 PausedTTL。
// IdleTimeout 和 CanceledTTL 不被 SweepTasks 使用——它们由"写侧"
// 在设置 expires_at 时直接取 common 常量:
//   - 取消/失败时用 common.UploadCanceledTTLSeconds
//   - 进度更新时用 common.UploadIdleTimeoutSeconds
//
// 因此修改 cfg.CanceledTTL / cfg.IdleTimeout **不会**影响 GC 行为;
// 如需调整这两项 TTL,请修改对应的 common 常量。
// 字段保留以备未来统一化改造。
type GCConfig struct {
	StagingDir  string
	IdleTimeout int64 // uploading 无进展降级 paused(仅由写侧用,SweepTasks 不读)
	PausedTTL   int64 // paused staging 保留(SweepTasks 读取)
	CanceledTTL int64 // canceled/failed 清理(仅由写侧用,SweepTasks 不读)
}

func DefaultGCConfig() GCConfig {
	return GCConfig{
		StagingDir:  common.FileUploadStagingDir,
		IdleTimeout: common.UploadIdleTimeoutSeconds,
		PausedTTL:   common.UploadPausedTTLSeconds,
		CanceledTTL: common.UploadCanceledTTLSeconds,
	}
}

func removeStaging(dir, id string) {
	os.Remove(filepath.Join(dir, id))         //nolint:errcheck
	os.Remove(filepath.Join(dir, id+".info")) //nolint:errcheck
}

// SweepTasks 对所有 expires_at>0 且 <=now 的任务执行分级清理。
func SweepTasks(s *TaskStore, cfg GCConfig, now time.Time) (transitioned, deleted int, err error) {
	due, e := s.ListDueForGC(now.Unix())
	if e != nil {
		return 0, 0, e
	}
	for _, t := range due {
		switch t.Status {
		case commonUpload.UploadStatusUploading:
			// 僵死上传:降级 paused,保留 staging,刷新过期。
			if e := s.SetStatus(t.ID, commonUpload.UploadStatusPaused, now.Unix()+cfg.PausedTTL); e != nil {
				return transitioned, deleted, e
			}
			transitioned++
		case commonUpload.UploadStatusPaused, commonUpload.UploadStatusFailed, commonUpload.UploadStatusCanceled:
			removeStaging(cfg.StagingDir, t.ID)
			if e := s.Delete(t.ID); e != nil {
				return transitioned, deleted, e
			}
			deleted++
		}
	}
	return transitioned, deleted, nil
}

// StartGC 启动后台分级 GC 协程(每 UploadGCIntervalSeconds 跑一次)。
func StartGC(s *TaskStore) {
	go func() {
		ticker := time.NewTicker(time.Duration(common.UploadGCIntervalSeconds) * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if tr, del, e := SweepTasks(s, DefaultGCConfig(), time.Now()); e != nil {
				logger.Error("upload GC sweep failed", zap.Error(e))
			} else if tr+del > 0 {
				logger.Info("upload GC", zap.Int("transitioned", tr), zap.Int("deleted", del))
			}
		}
	}()
}
