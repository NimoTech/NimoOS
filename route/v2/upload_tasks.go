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

// taskStore is injected by route/v2.go in InitV2Router.
var taskStore *upload.TaskStore

// cancelStagingDirsFn returns every directory that a cancel should try to
// clean staging from (overridable in tests). Task IDs may now carry a volume
// prefix and land in different volumes' staging directories (see
// tus_routing_store.go), so a single /DATA directory can no longer be
// assumed — every cancel re-enumerates, following the same rule as the batch
// sweeper.
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

// CancelUpload: POST /v2/nimoos/file/uploads/:id/cancel — idempotent, always 200.
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
