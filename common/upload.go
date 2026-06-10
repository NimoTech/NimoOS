package common

// Files 文件管理器 tus 上传相关常量。与 Photos 的 photos-tus-staging 分开，
// 互不干扰。
const (
	// FileUploadStagingDir 是 tusd 落临时分片的目录。上传中的所有半成品都在
	// 这里，绝不在用户目录留 .part/.tmp。
	FileUploadStagingDir = "/DATA/.system_data/file-tus-staging"

	// FileUploadMaxSize 单文件上限 20 GB（与 Photos 对齐）。
	FileUploadMaxSize = int64(20 * 1024 * 1024 * 1024)

	// FileUploadStagingTTLSeconds 是 staging 中未完成上传的过期秒数（7 天），
	// 供 GC 清理。
	FileUploadStagingTTLSeconds = int64(7 * 24 * 60 * 60)
)
