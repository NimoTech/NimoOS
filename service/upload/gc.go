package upload

import (
	"github.com/NimoTech/NimoOS/common"
	commonUpload "github.com/NimoTech/NimoOS-Common/upload"
)

// DefaultGCConfig returns the GC config used by the NimoOS Files service:
// StagingDir comes from a common constant, TTL reuses the value defined in phase one.
func DefaultGCConfig() commonUpload.GCConfig {
	return commonUpload.GCConfig{
		StagingDir: common.FileUploadStagingDir,
		// Task fragments may be routed to /media/<volume>/.system_data/file-tus-staging;
		// enumerated from the same source as the batch sweep (batch_sweeper), to
		// eliminate half-finished uploads permanently stranded on RAID.
		StagingDirs: func() []string {
			return StagingDirs(common.FileUploadStagingDir, "/media")
		},
		PausedTTL:      common.UploadPausedTTLSeconds,
		GCIntervalSecs: common.UploadGCIntervalSeconds,
	}
}
