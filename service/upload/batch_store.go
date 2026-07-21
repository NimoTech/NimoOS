package upload

import (
	"errors"
	"path/filepath"
	"strings"

	commonUpload "github.com/NimoTech/NimoOS-Common/upload"
	"gorm.io/gorm"
)

// BatchStore 封装 o_upload_batches / o_upload_batch_items 的读写与状态迁移。
type BatchStore struct{ db *gorm.DB }

func NewBatchStore(db *gorm.DB) *BatchStore { return &BatchStore{db: db} }

// Create 幂等创建批次:同 id 已存在则直接返回 nil(前端重试/重复提交安全)。
func (s *BatchStore) Create(b *UploadBatch, items []UploadBatchItem) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var cnt int64
		if err := tx.Model(&UploadBatch{}).Where("id = ?", b.ID).Count(&cnt).Error; err != nil {
			return err
		}
		if cnt > 0 {
			return nil
		}
		if err := tx.Create(b).Error; err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		return tx.CreateInBatches(items, 500).Error
	})
}

func (s *BatchStore) Get(id string) (*UploadBatch, error) {
	var b UploadBatch
	if err := s.db.Where("id = ?", id).First(&b).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, commonUpload.ErrNotFound
		}
		return nil, err
	}
	return &b, nil
}

func (s *BatchStore) MissingItems(batchID string) ([]UploadBatchItem, error) {
	var items []UploadBatchItem
	err := s.db.Where("batch_id = ? AND done = ?", batchID, false).
		Order("relative_path asc").Find(&items).Error
	return items, err
}

// MarkItemDone 把 (batchID, relativePath) 置 done 并重算计数;全部完成 → completed;
// 若批次此前被判 interrupted(超时误判/补传),收到完成即回 active。
// item 原本已 done(重复上传/覆盖)则不重复计数。批次或 item 不存在时静默返回 nil
// (清单未上报成功时不阻塞上传主流程)。
func (s *BatchStore) MarkItemDone(batchID, relativePath string, now int64) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&UploadBatchItem{}).
			Where("batch_id = ? AND relative_path = ? AND done = ?", batchID, relativePath, false).
			Update("done", true)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		// done 计数原子自增(item 的 false→true 恰好一次,由上面 RowsAffected 保证)。
		if err := tx.Model(&UploadBatch{}).Where("id = ?", batchID).
			Updates(map[string]interface{}{
				"done":             gorm.Expr("done + 1"),
				"last_progress_at": now,
			}).Error; err != nil {
			return err
		}
		// 全部完成 → completed(active/interrupted 均可迁移)。
		if err := tx.Model(&UploadBatch{}).
			Where("id = ? AND done >= total AND status IN ?", batchID,
				[]string{BatchStatusActive, BatchStatusInterrupted}).
			Update("status", BatchStatusCompleted).Error; err != nil {
			return err
		}
		// 未完成但批次此前被判中断 → 回 active(超时误判/补传可逆)。
		return tx.Model(&UploadBatch{}).
			Where("id = ? AND status = ? AND done < total", batchID, BatchStatusInterrupted).
			Update("status", BatchStatusActive).Error
	})
}

// MarkItemDoneAcrossBatches 把 (targetPath, relativePath) 在所有 active/
// interrupted 批次中的同名未完成项都记账(不限 batchID)。普通重传(新批次)
// 补齐文件时,旧中断批次的对应项隐式销账,数量对齐即自动 completed、角标消失。
func (s *BatchStore) MarkItemDoneAcrossBatches(targetPath, relativePath string, now int64) error {
	var ids []string
	if err := s.db.Model(&UploadBatch{}).
		Where("target_path = ? AND status IN ?", targetPath, []string{BatchStatusActive, BatchStatusInterrupted}).
		Pluck("id", &ids).Error; err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.MarkItemDone(id, relativePath, now); err != nil {
			return err
		}
	}
	return nil
}

// TouchProgress 记录批次仍有上传进度;interrupted 批次借此回 active(超时误判可逆)。
func (s *BatchStore) TouchProgress(batchID string, now int64) error {
	return s.db.Model(&UploadBatch{}).
		Where("id = ? AND status IN ?", batchID, []string{BatchStatusActive, BatchStatusInterrupted}).
		Updates(map[string]interface{}{"status": BatchStatusActive, "last_progress_at": now}).Error
}

func (s *BatchStore) SetStatus(id, status string) error {
	return s.db.Model(&UploadBatch{}).Where("id = ?", id).Update("status", status).Error
}

// SetInterrupted 置 interrupted 并记录时刻(staging 延迟清理以此起算)。仅 active 可迁移。
func (s *BatchStore) SetInterrupted(id string, now int64) error {
	return s.db.Model(&UploadBatch{}).
		Where("id = ? AND status = ?", id, BatchStatusActive).
		Updates(map[string]interface{}{
			"status": BatchStatusInterrupted, "interrupted_at": now, "staging_cleaned": false,
		}).Error
}

func (s *BatchStore) MarkStagingCleaned(id string) error {
	return s.db.Model(&UploadBatch{}).Where("id = ?", id).Update("staging_cleaned", true).Error
}

func (s *BatchStore) ListByStatus(status string) ([]UploadBatch, error) {
	var bs []UploadBatch
	err := s.db.Where("status = ?", status).Find(&bs).Error
	return bs, err
}

// BrokenChildren 返回 dir 的直接子条目名 → batchID:凡存在 interrupted 批次的缺失文件
// 落在该子条目路径下,即命中。列目录接口据此给文件夹注入「裂开」角标;天然递归——
// 用户钻到任何一层,缺内容的祖先/子文件夹都会被各自层级的调用命中。
func (s *BatchStore) BrokenChildren(dir string) (map[string]string, error) {
	out := map[string]string{}
	batches, err := s.ListByStatus(BatchStatusInterrupted)
	if err != nil {
		return nil, err
	}
	if len(batches) == 0 {
		return out, nil
	}
	cleanDir := filepath.Clean(dir)
	prefix := cleanDir + "/"
	if cleanDir == "/" {
		prefix = "/"
	}
	for _, b := range batches {
		items, err := s.MissingItems(b.ID)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			full := filepath.Clean(filepath.Join(b.TargetPath, it.RelativePath))
			if !strings.HasPrefix(full, prefix) {
				continue
			}
			rest := strings.TrimPrefix(full, prefix)
			child := rest
			if i := strings.Index(rest, "/"); i >= 0 {
				child = rest[:i]
			}
			if child == "" {
				continue
			}
			if _, ok := out[child]; !ok {
				out[child] = b.ID
			}
		}
	}
	return out, nil
}

// DeleteExpired 删除过期批次及其 items,返回删除的批次数。
func (s *BatchStore) DeleteExpired(now int64) (int, error) {
	var ids []string
	if err := s.db.Model(&UploadBatch{}).
		Where("expires_at > 0 AND expires_at <= ?", now).Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	for _, id := range ids {
		err := s.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("batch_id = ?", id).Delete(&UploadBatchItem{}).Error; err != nil {
				return err
			}
			return tx.Where("id = ?", id).Delete(&UploadBatch{}).Error
		})
		if err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}
