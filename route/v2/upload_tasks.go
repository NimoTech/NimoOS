package v2

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/NimoTech/NimoOS/common"
	"github.com/NimoTech/NimoOS/service/upload"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

// taskStore 由 route/v2.go 在 InitV2Router 时注入。
var taskStore *upload.TaskStore

// cancelStagingDir 是取消时清理 staging 的目录(测试可改写)。
var cancelStagingDir = common.FileUploadStagingDir

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
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && t.OwnerUserID != owner) {
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
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && t.OwnerUserID != owner) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	expires := time.Now().Unix() + common.UploadCanceledTTLSeconds
	canceled, err := taskStore.Cancel(id, expires)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if canceled {
		os.Remove(filepath.Join(cancelStagingDir, id))          //nolint:errcheck
		os.Remove(filepath.Join(cancelStagingDir, id+".info")) //nolint:errcheck
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"canceled": canceled})
}
