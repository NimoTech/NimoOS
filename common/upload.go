package common

// Files 文件管理器 tus 上传相关常量。与 Photos 的 photos-tus-staging 分开，
// 互不干扰。
const (
	// FileUploadStagingDir 是 tusd 落临时分片的目录。上传中的所有半成品都在
	// 这里，绝不在用户目录留 .part/.tmp。
	FileUploadStagingDir = "/DATA/.system_data/file-tus-staging"

	// FileUploadMaxSize 单文件上限 20 GB（与 Photos 对齐）。
	FileUploadMaxSize = int64(20 * 1024 * 1024 * 1024)

)

// 分级缓存清理 TTL(秒)。可后续提升为配置项;当前以常量提供默认值。
const (
	// UploadIdleTimeoutSeconds: uploading 任务多久无进展降级为 paused(6 小时)。
	UploadIdleTimeoutSeconds = int64(6 * 60 * 60)
	// UploadPausedTTLSeconds: paused(待重传)staging 保留窗口(3 天)。
	UploadPausedTTLSeconds = int64(3 * 24 * 60 * 60)
	// UploadCanceledTTLSeconds: canceled/failed 多久清理(1 小时)。
	UploadCanceledTTLSeconds = int64(60 * 60)
	// UploadGCIntervalSeconds: GC 协程运行间隔(1 小时)。
	UploadGCIntervalSeconds = int64(60 * 60)
)
