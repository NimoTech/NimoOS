package upload

import (
	"github.com/NimoTech/NimoOS/common"
	commonUpload "github.com/NimoTech/NimoOS-Common/upload"
)

// DefaultGCConfig 返回 NimoOS Files 服务使用的 GC 配置:
// StagingDir 取自 common 常量,TTL 沿用阶段一定义的值。
func DefaultGCConfig() commonUpload.GCConfig {
	return commonUpload.GCConfig{
		StagingDir:     common.FileUploadStagingDir,
		PausedTTL:      common.UploadPausedTTLSeconds,
		GCIntervalSecs: common.UploadGCIntervalSeconds,
	}
}
