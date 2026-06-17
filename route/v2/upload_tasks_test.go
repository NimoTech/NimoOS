package v2

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/NimoTech/NimoOS/service/model"
	"github.com/NimoTech/NimoOS/service/upload"
	"github.com/glebarez/sqlite"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

func setupTaskStore(t *testing.T) *upload.TaskStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.UploadTaskDBModel{}); err != nil {
		t.Fatal(err)
	}
	return upload.NewTaskStore(db)
}

func stagingDirForTest() string {
	d := filepath.Join(os.TempDir(), "nimoos-cancel-test")
	cancelStagingDir = d
	return d
}

func TestListUploadsFiltersByOwner(t *testing.T) {
	s := setupTaskStore(t)
	SetTaskStore(s)
	_ = s.Create(&model.UploadTaskDBModel{ID: "a", OwnerUserID: "1", Status: model.UploadStatusUploading})
	_ = s.Create(&model.UploadTaskDBModel{ID: "b", OwnerUserID: "2", Status: model.UploadStatusUploading})

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v2/nimoos/file/uploads?status=active", nil)
	req.Header.Set("user_id", "1")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if err := ListUploads(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 {
		t.Fatalf("code=%d", rec.Code)
	}
	var body struct {
		Tasks []model.UploadTaskDBModel `json:"tasks"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Tasks) != 1 || body.Tasks[0].ID != "a" {
		t.Fatalf("owner filter failed: %+v", body.Tasks)
	}
}

func TestCancelUploadIdempotentAndCleansStaging(t *testing.T) {
	s := setupTaskStore(t)
	SetTaskStore(s)
	_ = s.Create(&model.UploadTaskDBModel{ID: "x", OwnerUserID: "1", Status: model.UploadStatusUploading})
	// 造 staging 文件
	dir := stagingDirForTest() // helper 见下
	_ = os.MkdirAll(dir, 0700)
	_ = os.WriteFile(filepath.Join(dir, "x"), []byte("d"), 0600)
	_ = os.WriteFile(filepath.Join(dir, "x.info"), []byte("{}"), 0600)

	e := echo.New()
	call := func() int {
		req := httptest.NewRequest(http.MethodPost, "/v2/nimoos/file/uploads/x/cancel", nil)
		req.Header.Set("user_id", "1")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues("x")
		_ = CancelUpload(c)
		return rec.Code
	}
	if code := call(); code != 200 {
		t.Fatalf("first cancel code=%d", code)
	}
	got, _ := s.Get("x")
	if got.Status != model.UploadStatusCanceled {
		t.Fatalf("status=%s", got.Status)
	}
	if _, err := os.Stat(filepath.Join(dir, "x")); !os.IsNotExist(err) {
		t.Fatal("staging should be removed")
	}
	if code := call(); code != 200 { // 幂等
		t.Fatalf("second cancel code=%d", code)
	}
}

// TestGetUploadIDOR 确保 owner="2" 无法读取 owner="1" 的任务(GetUpload 越权应返回 404)。
func TestGetUploadIDOR(t *testing.T) {
	s := setupTaskStore(t)
	SetTaskStore(s)
	_ = s.Create(&model.UploadTaskDBModel{ID: "z", OwnerUserID: "1", Status: model.UploadStatusUploading})

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v2/nimoos/file/uploads/z", nil)
	req.Header.Set("user_id", "2") // 不同 owner
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("z")

	err := GetUpload(c)

	// 应返回 404
	if err != nil {
		he, ok := err.(*echo.HTTPError)
		if !ok || he.Code != http.StatusNotFound {
			t.Fatalf("expected 404 HTTPError, got %v", err)
		}
	} else if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}

	// 任务状态未被修改,owner="1" 仍可查
	got, getErr := s.Get("z")
	if getErr != nil {
		t.Fatalf("Get task failed: %v", getErr)
	}
	if got.Status != model.UploadStatusUploading {
		t.Fatalf("task status should still be uploading, got %s", got.Status)
	}
}

// TestCancelUploadIDOR 确保 owner="2" 无法取消 owner="1" 的任务。
func TestCancelUploadIDOR(t *testing.T) {
	s := setupTaskStore(t)
	SetTaskStore(s)
	_ = s.Create(&model.UploadTaskDBModel{ID: "y", OwnerUserID: "1", Status: model.UploadStatusUploading})

	// 造 staging 文件，用于断言文件未被删除
	dir := stagingDirForTest()
	_ = os.MkdirAll(dir, 0700)
	stagingFile := filepath.Join(dir, "y")
	infoFile := filepath.Join(dir, "y.info")
	_ = os.WriteFile(stagingFile, []byte("d"), 0600)
	_ = os.WriteFile(infoFile, []byte("{}"), 0600)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v2/nimoos/file/uploads/y/cancel", nil)
	req.Header.Set("user_id", "2") // 不同 owner
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("y")

	err := CancelUpload(c)

	// 应返回 404(通过 echo.HTTPError 或 handler 直接写入)
	if err != nil {
		he, ok := err.(*echo.HTTPError)
		if !ok || he.Code != http.StatusNotFound {
			t.Fatalf("expected 404 HTTPError, got %v", err)
		}
	} else if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}

	// 任务状态仍为 uploading，未被取消
	got, getErr := s.Get("y")
	if getErr != nil {
		t.Fatalf("Get task failed: %v", getErr)
	}
	if got.Status != model.UploadStatusUploading {
		t.Fatalf("task status should still be uploading, got %s", got.Status)
	}

	// staging 文件未被删除
	if _, statErr := os.Stat(stagingFile); os.IsNotExist(statErr) {
		t.Fatal("staging file should NOT have been removed by unauthorized cancel")
	}
	if _, statErr := os.Stat(infoFile); os.IsNotExist(statErr) {
		t.Fatal("staging .info file should NOT have been removed by unauthorized cancel")
	}
}
