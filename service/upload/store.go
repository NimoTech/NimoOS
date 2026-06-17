package upload

import (
	"errors"

	"github.com/NimoTech/NimoOS/service/model"
	"gorm.io/gorm"
)

// TaskStore 封装 o_upload_tasks 的读写与状态迁移。
type TaskStore struct{ db *gorm.DB }

func NewTaskStore(db *gorm.DB) *TaskStore { return &TaskStore{db: db} }

func (s *TaskStore) Create(t *model.UploadTaskDBModel) error { return s.db.Create(t).Error }

func (s *TaskStore) Get(id string) (*model.UploadTaskDBModel, error) {
	var t model.UploadTaskDBModel
	if err := s.db.Where("id = ?", id).First(&t).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *TaskStore) ListActiveByOwner(owner string) ([]model.UploadTaskDBModel, error) {
	var ts []model.UploadTaskDBModel
	err := s.db.
		Where("owner_user_id = ? AND status IN ?", owner,
			[]string{model.UploadStatusUploading, model.UploadStatusPaused, model.UploadStatusFailed}).
		Order("created_at desc").
		Find(&ts).Error
	return ts, err
}

func (s *TaskStore) UpdateOffset(id string, offset, expiresAt int64) error {
	return s.db.Model(&model.UploadTaskDBModel{}).Where("id = ?", id).
		Updates(map[string]interface{}{"uploaded_offset": offset, "expires_at": expiresAt}).Error
}

func (s *TaskStore) SetStatus(id, status string, expiresAt int64) error {
	return s.db.Model(&model.UploadTaskDBModel{}).Where("id = ?", id).
		Updates(map[string]interface{}{"status": status, "expires_at": expiresAt}).Error
}

func (s *TaskStore) SetFailed(id, errMsg string, lastErrorAt, expiresAt int64) error {
	return s.db.Model(&model.UploadTaskDBModel{}).Where("id = ?", id).
		Updates(map[string]interface{}{
			"status": model.UploadStatusFailed, "error": errMsg,
			"last_error_at": lastErrorAt, "expires_at": expiresAt,
		}).Error
}

// Cancel 幂等地取消任意非终态任务。不存在 / 已 completed / 已 canceled → (false, nil)。
func (s *TaskStore) Cancel(id string, expiresAt int64) (bool, error) {
	t, err := s.Get(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	switch t.Status {
	case model.UploadStatusCompleted, model.UploadStatusCanceled:
		return false, nil
	}
	if err := s.SetStatus(id, model.UploadStatusCanceled, expiresAt); err != nil {
		return false, err
	}
	return true, nil
}

func (s *TaskStore) Delete(id string) error {
	return s.db.Where("id = ?", id).Delete(&model.UploadTaskDBModel{}).Error
}
