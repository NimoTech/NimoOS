package upload

// 上传批次状态机:active → interrupted(关窗信号/超时)→ active(收到新进度,可逆)
// active/interrupted → completed(done==total)/ abandoned(用户放弃)。
const (
	BatchStatusActive      = "active"
	BatchStatusInterrupted = "interrupted"
	BatchStatusCompleted   = "completed"
	BatchStatusAbandoned   = "abandoned"
)

// BatchExpireSeconds: 批次(含 items)保留 30 天后由扫描器删除。
const BatchExpireSeconds = int64(30 * 24 * 60 * 60)

// UploadBatch 是一次「用户选择并开始上传」的对账单头:总数/完成数/状态。
// 前端在开传前用完整清单 POST 创建;tus 完成事件逐条回填 done。
type UploadBatch struct {
	ID             string `gorm:"column:id;primaryKey" json:"id"`
	OwnerUserID    string `gorm:"column:owner_user_id;index" json:"owner_user_id"`
	TargetPath     string `gorm:"column:target_path" json:"target_path"`
	Status         string `gorm:"column:status;index" json:"status"`
	Total          int    `gorm:"column:total" json:"total"`
	Done           int    `gorm:"column:done" json:"done"`
	LastProgressAt int64  `gorm:"column:last_progress_at" json:"last_progress_at"`
	InterruptedAt  int64  `gorm:"column:interrupted_at" json:"interrupted_at"`
	StagingCleaned bool   `gorm:"column:staging_cleaned" json:"-"`
	CreatedAt      int64  `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt      int64  `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
	ExpiresAt      int64  `gorm:"column:expires_at;index" json:"expires_at"`
}

func (UploadBatch) TableName() string { return "o_upload_batches" }

// UploadBatchItem 是清单里的一个文件(相对 target_path 的路径 + 大小)。
// 用行表而非 JSON blob:大批次逐条置 done 可控,且 (batch_id, done) 可走索引。
type UploadBatchItem struct {
	ID           uint   `gorm:"column:id;primaryKey;autoIncrement" json:"-"`
	BatchID      string `gorm:"column:batch_id;uniqueIndex:uq_batch_rel;index:idx_batch_done" json:"batch_id"`
	RelativePath string `gorm:"column:relative_path;uniqueIndex:uq_batch_rel" json:"relative_path"`
	Size         int64  `gorm:"column:size" json:"size"`
	Done         bool   `gorm:"column:done;index:idx_batch_done" json:"done"`
}

func (UploadBatchItem) TableName() string { return "o_upload_batch_items" }
