package v2

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	commonUpload "github.com/NimoTech/NimoOS-Common/upload"
	"github.com/NimoTech/NimoOS/common"
	"github.com/NimoTech/NimoOS/service/upload"
	"github.com/labstack/echo/v4"
)

// batchStore is injected by route/v2.go in InitV2Router (same way as taskStore).
var batchStore *upload.BatchStore

func SetBatchStore(s *upload.BatchStore) { batchStore = s }

type createBatchItem struct {
	RelativePath string `json:"relativePath"`
	Size         int64  `json:"size"`
}

type createBatchReq struct {
	ID         string            `json:"id"`
	TargetPath string            `json:"targetPath"`
	Items      []createBatchItem `json:"items"`
}

// CreateUploadBatch: POST /v2/nimoos/file/upload-batches — registers a
// reconciliation manifest before the upload starts.
// Idempotent: resubmitting the same id returns 201 without creating a duplicate.
func CreateUploadBatch(c echo.Context) error {
	var req createBatchReq
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid body")
	}
	if req.ID == "" || req.TargetPath == "" || len(req.Items) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "id/targetPath/items required")
	}
	if !strings.HasPrefix(req.TargetPath, "/") {
		return echo.NewHTTPError(http.StatusBadRequest, "targetPath must be absolute")
	}
	items := make([]upload.UploadBatchItem, 0, len(req.Items))
	for _, it := range req.Items {
		// Same criteria as tus metadata validation: relativePath must not be a
		// traversal or absolute path.
		if it.RelativePath == "" || strings.Contains(it.RelativePath, "..") ||
			strings.HasPrefix(it.RelativePath, "/") {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid relativePath")
		}
		items = append(items, upload.UploadBatchItem{
			BatchID: req.ID, RelativePath: it.RelativePath, Size: it.Size,
		})
	}
	b := &upload.UploadBatch{
		ID: req.ID, OwnerUserID: c.Request().Header.Get("user_id"),
		TargetPath: filepath.Clean(req.TargetPath),
		Status:     upload.BatchStatusActive, Total: len(items),
		LastProgressAt: time.Now().Unix(),
	}
	if err := batchStore.Create(b, items); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{"id": b.ID})
}

// getOwnedBatch fetches the batch by id and checks ownership; errors are
// already wrapped as echo.HTTPError.
func getOwnedBatch(c echo.Context) (*upload.UploadBatch, error) {
	owner := c.Request().Header.Get("user_id")
	b, err := batchStore.Get(c.Param("id"))
	if errors.Is(err, commonUpload.ErrNotFound) || (err == nil && b.OwnerUserID != owner) {
		return nil, echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return b, nil
}

// GetUploadBatch: GET /v2/nimoos/file/upload-batches/:id — reconciliation
// manifest (missing items).
func GetUploadBatch(c echo.Context) error {
	b, err := getOwnedBatch(c)
	if err != nil {
		return err
	}
	missing, merr := batchStore.MissingItems(b.ID)
	if merr != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, merr.Error())
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"batch": b, "missing": missing})
}

// cancelBatchTasks terminates a batch's unfinished tus tasks and clears staging
// (executed immediately on a signal interrupt/abandon; a timeout interrupt is
// executed by the sweeper after a grace delay). Reused by B3's sweeper.
func cancelBatchTasks(tasks *upload.TaskStore, batchID string) {
	list, err := tasks.ListUnfinishedByBatch(batchID)
	if err != nil {
		return
	}
	expires := time.Now().Unix() + common.UploadCanceledTTLSeconds
	for _, t := range list {
		_, _ = commonUpload.Cancel(tasks, t.ID, expires)
		for _, dir := range cancelStagingDirsFn() {
			os.Remove(filepath.Join(dir, t.ID))         //nolint:errcheck
			os.Remove(filepath.Join(dir, t.ID+".info")) //nolint:errcheck
		}
	}
}

// InterruptUploadBatch: POST .../:id/interrupt — window-close signal.
// Idempotent: only an active batch transitions.
// A signal interrupt means the page is confirmed closed, so tasks are
// terminated and staging cleared immediately (no resume window).
func InterruptUploadBatch(c echo.Context) error {
	b, err := getOwnedBatch(c)
	if err != nil {
		return err
	}
	if b.Status != upload.BatchStatusActive {
		return c.JSON(http.StatusOK, map[string]interface{}{"interrupted": false})
	}
	if serr := batchStore.SetInterrupted(b.ID, time.Now().Unix()); serr != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, serr.Error())
	}
	cancelBatchTasks(taskStore, b.ID)
	_ = batchStore.MarkStagingCleaned(b.ID)
	return c.JSON(http.StatusOK, map[string]interface{}{"interrupted": true})
}

// AbandonUploadBatch: POST .../:id/abandon — user abandons the batch, the
// badge disappears immediately. Idempotent.
func AbandonUploadBatch(c echo.Context) error {
	b, err := getOwnedBatch(c)
	if err != nil {
		return err
	}
	if b.Status == upload.BatchStatusAbandoned || b.Status == upload.BatchStatusCompleted {
		return c.JSON(http.StatusOK, map[string]interface{}{"abandoned": false})
	}
	if serr := batchStore.SetStatus(b.ID, upload.BatchStatusAbandoned); serr != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, serr.Error())
	}
	cancelBatchTasks(taskStore, b.ID)
	return c.JSON(http.StatusOK, map[string]interface{}{"abandoned": true})
}
