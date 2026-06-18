package upload

import (
	"errors"

	upload "github.com/NimoTech/NimoOS-Common/upload"
	"gorm.io/gorm"
)

// TaskStore 封装 o_upload_tasks 的读写与状态迁移,实现 upload.Store 接口。
type TaskStore struct{ db *gorm.DB }

func NewTaskStore(db *gorm.DB) *TaskStore { return &TaskStore{db: db} }

func (s *TaskStore) Create(t *upload.UploadTask) error { return s.db.Create(t).Error }

func (s *TaskStore) Get(id string) (*upload.UploadTask, error) {
	var t upload.UploadTask
	if err := s.db.Where("id = ?", id).First(&t).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, upload.ErrNotFound
		}
		return nil, err
	}
	return &t, nil
}

func (s *TaskStore) ListActiveByOwner(owner string) ([]upload.UploadTask, error) {
	var ts []upload.UploadTask
	err := s.db.
		Where("owner_user_id = ? AND status IN ?", owner,
			[]string{upload.UploadStatusUploading, upload.UploadStatusPaused, upload.UploadStatusFailed}).
		Order("created_at desc").
		Find(&ts).Error
	return ts, err
}

func (s *TaskStore) ListDueForGC(now int64) ([]upload.UploadTask, error) {
	var ts []upload.UploadTask
	err := s.db.Where("expires_at > 0 AND expires_at <= ?", now).Find(&ts).Error
	return ts, err
}

func (s *TaskStore) UpdateOffset(id string, offset, expiresAt int64) error {
	return s.db.Model(&upload.UploadTask{}).Where("id = ?", id).
		Updates(map[string]interface{}{"uploaded_offset": offset, "expires_at": expiresAt}).Error
}

func (s *TaskStore) SetStatus(id, status string, expiresAt int64) error {
	return s.db.Model(&upload.UploadTask{}).Where("id = ?", id).
		Updates(map[string]interface{}{"status": status, "expires_at": expiresAt}).Error
}

func (s *TaskStore) SetFailed(id, errMsg string, lastErrorAt, expiresAt int64) error {
	return s.db.Model(&upload.UploadTask{}).Where("id = ?", id).
		Updates(map[string]interface{}{
			"status": upload.UploadStatusFailed, "error": errMsg,
			"last_error_at": lastErrorAt, "expires_at": expiresAt,
		}).Error
}

func (s *TaskStore) Delete(id string) error {
	return s.db.Where("id = ?", id).Delete(&upload.UploadTask{}).Error
}
