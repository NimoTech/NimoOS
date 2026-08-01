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

// batchStore 由 route/v2.go 在 InitV2Router 时注入(与 taskStore 同法)。
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

// CreateUploadBatch: POST /v2/nimoos/file/upload-batches —— 上传开始前登记对账单。
// 幂等:同 id 重复提交返回 201 不重复建。
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
		// 与 tus 元数据校验同口径:相对路径不得穿越/绝对。
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

// getOwnedBatch 按 id 取批次并做 owner 校验;错误已包装为 echo.HTTPError。
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

// GetUploadBatch: GET /v2/nimoos/file/upload-batches/:id —— 对账清单(缺失项)。
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

// cancelBatchTasks 终止批次未完成 tus 任务并清 staging(信号中断/放弃时立即执行;
// 超时中断由扫描器延迟 grace 后执行)。B3 的扫描器复用。
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

// InterruptUploadBatch: POST .../:id/interrupt —— 关窗信号。幂等:仅 active 会迁移。
// 信号中断意味着页面确定已关,立即终止任务并清 staging(无续传窗口)。
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

// AbandonUploadBatch: POST .../:id/abandon —— 用户放弃批次,角标立即消失。幂等。
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
