package upload

import (
	"errors"
	"path/filepath"
	"strings"

	commonUpload "github.com/NimoTech/NimoOS-Common/upload"
	"gorm.io/gorm"
)

// BatchStore wraps read/write and status transitions for o_upload_batches / o_upload_batch_items.
type BatchStore struct{ db *gorm.DB }

func NewBatchStore(db *gorm.DB) *BatchStore { return &BatchStore{db: db} }

// Create idempotently creates a batch: if the same id already exists, it just
// returns nil (safe against frontend retries/duplicate submits).
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

// MarkItemDone sets (batchID, relativePath) to done and recomputes the count;
// once everything is done → completed. If the batch was previously judged
// interrupted (timeout misjudgment/resumed upload), receiving a completion
// brings it back to active.
// If the item was already done (duplicate upload/overwrite), it isn't counted
// again. If the batch or item doesn't exist, silently returns nil (a manifest
// report failure must not block the main upload flow).
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
		// done count increments atomically (the item's false→true happens exactly
		// once, guaranteed by RowsAffected above).
		if err := tx.Model(&UploadBatch{}).Where("id = ?", batchID).
			Updates(map[string]interface{}{
				"done":             gorm.Expr("done + 1"),
				"last_progress_at": now,
			}).Error; err != nil {
			return err
		}
		// Everything done → completed (either active or interrupted can transition).
		if err := tx.Model(&UploadBatch{}).
			Where("id = ? AND done >= total AND status IN ?", batchID,
				[]string{BatchStatusActive, BatchStatusInterrupted}).
			Update("status", BatchStatusCompleted).Error; err != nil {
			return err
		}
		// Not done yet, but the batch was previously judged interrupted → back to
		// active. ONLY for timeout interrupts: that judgment can be wrong and new
		// progress disproves it. A signal interrupt is definitive (the page is
		// confirmed gone) and late completions racing the signal must not hide
		// the badge by reviving the batch (see BatchInterruptSource* in batch.go).
		return tx.Model(&UploadBatch{}).
			Where("id = ? AND status = ? AND done < total AND interrupt_source <> ?",
				batchID, BatchStatusInterrupted, BatchInterruptSourceSignal).
			Updates(map[string]interface{}{"status": BatchStatusActive, "interrupt_source": ""}).Error
	})
}

// MarkItemDoneAcrossBatches, matching by absolute-path equivalence, accounts
// for any matching unfinished item across all active/interrupted batches (not
// scoped to a single batchID, and not requiring the targetPath pair to match exactly).
// completedAbs = filepath.Join(targetPath, relativePath); for each candidate
// batch, if completedAbs falls under batch.TargetPath (prefix match, split on
// "/" to avoid mistakenly matching string-prefix-similar-but-different
// directories like /DATA/Media vs /DATA/Medias), the remainder after stripping
// the batch's root path is taken as the relativePath in that batch's coordinate
// system and passed to MarkItemDone.
// Typical scenario: the user uploads a whole folder (batch targetPath=/X,
// item=backup/a.jpg); when manually resuming, they navigate into the
// subdirectory and upload directly (targetPath=/X/backup, relativePath=a.jpg)
// — the pair differs but the absolute path is the same, which used to miss and
// now hits; conversely, if the batch is rooted at a subdirectory and the resume
// happens at the parent directory with a sub-path, that also hits. The
// original exact-path match (same targetPath, same relativePath) is naturally
// covered as a special case.
func (s *BatchStore) MarkItemDoneAcrossBatches(targetPath, relativePath string, now int64) error {
	completedAbs := filepath.Clean(filepath.Join(targetPath, relativePath))

	var candidates []UploadBatch
	if err := s.db.Where("status IN ?", []string{BatchStatusActive, BatchStatusInterrupted}).
		Find(&candidates).Error; err != nil {
		return err
	}

	for _, b := range candidates {
		root := strings.TrimRight(filepath.Clean(b.TargetPath), "/")
		prefix := root + "/"
		if !strings.HasPrefix(completedAbs, prefix) {
			continue
		}
		rel := strings.TrimPrefix(completedAbs, prefix)
		if rel == "" {
			continue
		}
		if err := s.MarkItemDone(b.ID, rel, now); err != nil {
			return err
		}
	}
	return nil
}

// TouchProgress records that the batch still has upload progress; an
// interrupted batch uses this to go back to active (timeout misjudgment is
// reversible). Signal interrupts are excluded for the same reason as in
// MarkItemDone: a chunk racing the window-close signal is not evidence the
// page is alive.
func (s *BatchStore) TouchProgress(batchID string, now int64) error {
	return s.db.Model(&UploadBatch{}).
		Where("id = ? AND (status = ? OR (status = ? AND interrupt_source <> ?))",
			batchID, BatchStatusActive, BatchStatusInterrupted, BatchInterruptSourceSignal).
		Updates(map[string]interface{}{
			"status": BatchStatusActive, "last_progress_at": now, "interrupt_source": "",
		}).Error
}

func (s *BatchStore) SetStatus(id, status string) error {
	return s.db.Model(&UploadBatch{}).Where("id = ?", id).Update("status", status).Error
}

// SetInterrupted sets interrupted and records the timestamp (staging's delayed
// cleanup is timed from this) and the source — signal (window close, definitive)
// or timeout (sweeper judgment, reversible on new progress). Only active can
// transition.
func (s *BatchStore) SetInterrupted(id string, now int64, source string) error {
	return s.db.Model(&UploadBatch{}).
		Where("id = ? AND status = ?", id, BatchStatusActive).
		Updates(map[string]interface{}{
			"status": BatchStatusInterrupted, "interrupted_at": now,
			"staging_cleaned": false, "interrupt_source": source,
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

// BrokenChildren returns a map of dir's direct child entry names → batchID: any
// missing file from one of the owner's own interrupted batches that falls
// under that child entry's path counts as a hit. The list-directory endpoint
// uses this to inject a "broken" badge onto folders; it's naturally recursive
// — wherever the user drills down, ancestor/child folders missing content will
// each be hit by the call at their own level.
// Only looks at the owner's own batches: this matches the owner check
// (getOwnedBatch) used by the batch-detail/abandon endpoints, otherwise
// someone else's batch badge would be visible but unclickable (GET/abandon
// 404), and could never be cleared.
func (s *BatchStore) BrokenChildren(dir, owner string) (map[string]string, error) {
	out := map[string]string{}
	var batches []UploadBatch
	if err := s.db.Where("status = ? AND owner_user_id = ?",
		BatchStatusInterrupted, owner).Find(&batches).Error; err != nil {
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

// AbandonInterruptedUnder abandons every one of the owner's interrupted
// batches that still has a missing item at or under path (the badged entry's
// absolute path). Same hit test as BrokenChildren: several interrupted batches
// can stack on one folder (each canceled retry leaves one), while the badge
// only carries a single batch id — abandoning by id would make the badge
// reappear with the next batch's id on every listing. Returns the abandoned
// ids so the caller can clear their staging.
func (s *BatchStore) AbandonInterruptedUnder(path, owner string) ([]string, error) {
	var batches []UploadBatch
	if err := s.db.Where("status = ? AND owner_user_id = ?",
		BatchStatusInterrupted, owner).Find(&batches).Error; err != nil {
		return nil, err
	}
	clean := filepath.Clean(path)
	prefix := clean + "/"
	ids := []string{}
	for _, b := range batches {
		items, err := s.MissingItems(b.ID)
		if err != nil {
			return nil, err
		}
		hit := false
		for _, it := range items {
			full := filepath.Clean(filepath.Join(b.TargetPath, it.RelativePath))
			if full == clean || strings.HasPrefix(full, prefix) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		if err := s.SetStatus(b.ID, BatchStatusAbandoned); err != nil {
			return nil, err
		}
		ids = append(ids, b.ID)
	}
	return ids, nil
}

// RemoveItems drops the given not-yet-done items from the batch manifest and
// shrinks Total accordingly: the user canceled those files, so the batch must
// stop waiting for them — a stale manifest leaves the batch permanently
// incomplete and the sweeper later hangs a broken badge on a folder the user
// deliberately trimmed. Done items are accounting history and are ignored, as
// are unknown paths. When everything left on the manifest is done, the batch
// completes (and the sweeper can collect it).
func (s *BatchStore) RemoveItems(batchID string, relativePaths []string) error {
	// Chunked to stay under SQLite's bound-variable limit on large selections.
	for start := 0; start < len(relativePaths); start += 500 {
		end := start + 500
		if end > len(relativePaths) {
			end = len(relativePaths)
		}
		chunk := relativePaths[start:end]
		err := s.db.Transaction(func(tx *gorm.DB) error {
			res := tx.Where("batch_id = ? AND done = ? AND relative_path IN ?",
				batchID, false, chunk).Delete(&UploadBatchItem{})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return nil
			}
			if err := tx.Model(&UploadBatch{}).Where("id = ?", batchID).
				Update("total", gorm.Expr("total - ?", res.RowsAffected)).Error; err != nil {
				return err
			}
			return tx.Model(&UploadBatch{}).
				Where("id = ? AND done >= total AND status IN ?", batchID,
					[]string{BatchStatusActive, BatchStatusInterrupted}).
				Update("status", BatchStatusCompleted).Error
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// DeleteTerminal deletes terminal-state (completed/abandoned) batches and their
// items, returning the number of batches deleted.
// Terminal-state batches produce no badge and no longer participate in
// reconciliation; keeping them around would only let items accumulate without
// bound (a single batch can reach thousands of rows).
// active/interrupted are never auto-cleared — the warning badge can only be
// resolved by the user manually abandoning it or completing the resumed upload.
func (s *BatchStore) DeleteTerminal() (int, error) {
	var ids []string
	if err := s.db.Model(&UploadBatch{}).
		Where("status IN ?", []string{BatchStatusCompleted, BatchStatusAbandoned}).
		Pluck("id", &ids).Error; err != nil {
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
