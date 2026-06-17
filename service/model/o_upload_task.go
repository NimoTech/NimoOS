package model

// 上传任务状态常量。
const (
	UploadStatusUploading = "uploading"
	UploadStatusPaused    = "paused"
	UploadStatusFailed    = "failed"
	UploadStatusCompleted = "completed"
	UploadStatusCanceled  = "canceled"
)

// UploadTaskDBModel 是可恢复上传引擎的服务端真相源。每个未完成上传一行,
// 完成后保留行做审计(staging 已清)。
type UploadTaskDBModel struct {
	ID           string `gorm:"column:id;primaryKey" json:"id"` // = tus uploadID
	OwnerUserID  string `gorm:"column:owner_user_id;index" json:"owner_user_id"`
	Filename     string `gorm:"column:filename" json:"filename"`
	TargetPath   string `gorm:"column:target_path" json:"target_path"`
	RelativePath string `gorm:"column:relative_path" json:"relative_path"`
	Size         int64  `gorm:"column:size" json:"size"`
	Mime         string `gorm:"column:mime" json:"mime"`
	Fingerprint  string `gorm:"column:fingerprint;index" json:"fingerprint"`
	ContentHash  string `gorm:"column:content_hash;index" json:"content_hash"`
	UploadURL    string `gorm:"column:upload_url" json:"upload_url"`
	Offset       int64  `gorm:"column:offset" json:"offset"`
	Status       string `gorm:"column:status;index" json:"status"`
	RetryCount   int    `gorm:"column:retry_count" json:"retry_count"`
	Error        string `gorm:"column:error" json:"error"`
	LastErrorAt  int64  `gorm:"column:last_error_at" json:"last_error_at"`
	BatchID      string `gorm:"column:batch_id;index" json:"batch_id"`
	ClientID     string `gorm:"column:client_id" json:"client_id"`
	ClientMeta   string `gorm:"column:client_meta" json:"client_meta"` // JSON: UA/IP
	CreatedAt    int64  `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt    int64  `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
	ExpiresAt    int64  `gorm:"column:expires_at;index" json:"expires_at"` // unix 秒;<=0 表示不参与 GC
}

func (UploadTaskDBModel) TableName() string { return "o_upload_tasks" }
