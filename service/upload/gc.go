package upload

import (
	"os"
	"path/filepath"
	"time"

	"github.com/NimoTech/NimoOS/common"
	"github.com/NimoTech/NimoOS/service/model"
	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"go.uber.org/zap"
)

// GCConfig 控制分级清理。生产用 DefaultGCConfig 从 common 常量构造。
type GCConfig struct {
	StagingDir  string
	IdleTimeout int64 // uploading 无进展降级 paused
	PausedTTL   int64 // paused staging 保留
	CanceledTTL int64 // canceled/failed 清理
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
	var due []model.UploadTaskDBModel
	if e := s.db.Where("expires_at > 0 AND expires_at <= ?", now.Unix()).Find(&due).Error; e != nil {
		return 0, 0, e
	}
	for _, t := range due {
		switch t.Status {
		case model.UploadStatusUploading:
			// 僵死上传:降级 paused,保留 staging,刷新过期。
			if e := s.SetStatus(t.ID, model.UploadStatusPaused, now.Unix()+cfg.PausedTTL); e != nil {
				return transitioned, deleted, e
			}
			transitioned++
		case model.UploadStatusPaused, model.UploadStatusFailed, model.UploadStatusCanceled:
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
