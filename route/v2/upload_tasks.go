package v2

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	commonUpload "github.com/NimoTech/NimoOS-Common/upload"
	"github.com/NimoTech/NimoOS/common"
	"github.com/NimoTech/NimoOS/service/upload"
	"github.com/labstack/echo/v4"
)

// taskStore 由 route/v2.go 在 InitV2Router 时注入。
var taskStore *upload.TaskStore

// cancelStagingDirsFn 返回取消时应尝试清理 staging 的所有目录(测试可改写)。
// 任务 ID 现在可能带卷前缀、落在不同卷的暂存目录里(见 tus_routing_store.go),
// 不能再假设单一 /DATA 目录——每次取消都重新枚举,与批次清扫器用同一规则。
var cancelStagingDirsFn = func() []string {
	return upload.StagingDirs(common.FileUploadStagingDir, "/media")
}

func SetTaskStore(s *upload.TaskStore) { taskStore = s }

// ListUploads: GET /v2/nimoos/file/uploads?status=active
func ListUploads(c echo.Context) error {
	owner := c.Request().Header.Get("user_id")
	tasks, err := taskStore.ListActiveByOwner(owner)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"tasks": tasks})
}

// GetUpload: GET /v2/nimoos/file/uploads/:id
func GetUpload(c echo.Context) error {
	owner := c.Request().Header.Get("user_id")
	t, err := taskStore.Get(c.Param("id"))
	if errors.Is(err, commonUpload.ErrNotFound) || (err == nil && t.OwnerUserID != owner) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, t)
}

// CancelUpload: POST /v2/nimoos/file/uploads/:id/cancel —— 幂等,始终 200。
func CancelUpload(c echo.Context) error {
	id := c.Param("id")
	owner := c.Request().Header.Get("user_id")
	t, err := taskStore.Get(id)
	if errors.Is(err, commonUpload.ErrNotFound) || (err == nil && t.OwnerUserID != owner) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	expires := time.Now().Unix() + common.UploadCanceledTTLSeconds
	canceled, err := commonUpload.Cancel(taskStore, id, expires)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if canceled {
		for _, dir := range cancelStagingDirsFn() {
			os.Remove(filepath.Join(dir, id))         //nolint:errcheck
			os.Remove(filepath.Join(dir, id+".info")) //nolint:errcheck
		}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"canceled": canceled})
}
