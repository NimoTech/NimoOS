package upload

import (
	"github.com/NimoTech/NimoOS/common"
	commonUpload "github.com/NimoTech/NimoOS-Common/upload"
)

// DefaultGCConfig 返回 NimoOS Files 服务使用的 GC 配置:
// StagingDir 取自 common 常量,TTL 沿用阶段一定义的值。
func DefaultGCConfig() commonUpload.GCConfig {
	return commonUpload.GCConfig{
		StagingDir: common.FileUploadStagingDir,
		// 任务残片可能被路由到 /media/<卷>/.system_data/file-tus-staging,
		// 与批次清扫(batch_sweeper)同源枚举,消除 RAID 上永久残留的半截上传。
		StagingDirs: func() []string {
			return StagingDirs(common.FileUploadStagingDir, "/media")
		},
		PausedTTL:      common.UploadPausedTTLSeconds,
		GCIntervalSecs: common.UploadGCIntervalSeconds,
	}
}
