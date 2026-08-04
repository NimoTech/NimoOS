package upload

// Upload batch state machine: active → interrupted (window-close signal/timeout)
// → active (new progress received, reversible); active/interrupted →
// completed (done==total) / abandoned (user gave up).
const (
	BatchStatusActive      = "active"
	BatchStatusInterrupted = "interrupted"
	BatchStatusCompleted   = "completed"
	BatchStatusAbandoned   = "abandoned"
)

// UploadBatch is the header row for one "user selected and started an upload"
// reconciliation: total/done counts and status. The frontend creates it with a
// POST of the full manifest before starting the transfer; tus completion events
// fill in done one item at a time. Interrupted batches don't auto-expire: the
// warning badge stays up until the user manually abandons it or the upload is
// resumed to completion; once it reaches a terminal state (completed/abandoned)
// the sweeper deletes the row (see DeleteTerminal).
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
}

func (UploadBatch) TableName() string { return "o_upload_batches" }

// UploadBatchItem is one file in the manifest (path relative to target_path + size).
// A row table rather than a JSON blob: for large batches, marking done item-by-item
// is controllable, and (batch_id, done) can use an index.
type UploadBatchItem struct {
	ID           uint   `gorm:"column:id;primaryKey;autoIncrement" json:"-"`
	BatchID      string `gorm:"column:batch_id;uniqueIndex:uq_batch_rel;index:idx_batch_done" json:"batch_id"`
	RelativePath string `gorm:"column:relative_path;uniqueIndex:uq_batch_rel" json:"relative_path"`
	Size         int64  `gorm:"column:size" json:"size"`
	Done         bool   `gorm:"column:done;index:idx_batch_done" json:"done"`
}

func (UploadBatchItem) TableName() string { return "o_upload_batch_items" }
