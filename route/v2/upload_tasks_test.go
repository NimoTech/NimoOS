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
