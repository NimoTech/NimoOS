package v2

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	commonUpload "github.com/NimoTech/NimoOS-Common/upload"
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
	if err := db.AutoMigrate(&commonUpload.UploadTask{}); err != nil {
		t.Fatal(err)
	}
	return upload.NewTaskStore(db)
}

func stagingDirForTest() string {
	d := filepath.Join(os.TempDir(), "nimoos-cancel-test")
	cancelStagingDirsFn = func() []string { return []string{d} }
	return d
}

// A task routed to a per-volume staging dir (ID carries a volume prefix, see
// tus_routing_store.go) must still get its staging file cleaned up on cancel:
// cancelStagingDirsFn now returns every known staging dir, and cancel must try
// them all rather than assuming a single legacy directory.
func TestCancelUploadCleansAcrossMultipleStagingDirs(t *testing.T) {
	s := setupTaskStore(t)
	SetTaskStore(s)
	id := "hexroot~subid2"
	_ = s.Create(&commonUpload.UploadTask{ID: id, OwnerUserID: "1", Status: commonUpload.UploadStatusUploading})

	legacyDir := filepath.Join(os.TempDir(), "nimoos-cancel-test-legacy")
	volumeDir := filepath.Join(os.TempDir(), "nimoos-cancel-test-volume")
	_ = os.MkdirAll(legacyDir, 0700)
	_ = os.MkdirAll(volumeDir, 0700)
	defer func() {
		_ = os.RemoveAll(legacyDir)
		_ = os.RemoveAll(volumeDir)
	}()
	cancelStagingDirsFn = func() []string { return []string{legacyDir, volumeDir} }
	_ = os.WriteFile(filepath.Join(volumeDir, id), []byte("d"), 0600)
	_ = os.WriteFile(filepath.Join(volumeDir, id+".info"), []byte("{}"), 0600)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v2/nimoos/file/uploads/"+id+"/cancel", nil)
	req.Header.Set("user_id", "1")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(id)
	if err := CancelUpload(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 {
		t.Fatalf("code=%d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(volumeDir, id)); !os.IsNotExist(err) {
		t.Fatal("staged file on the volume dir should have been removed")
	}
	if _, err := os.Stat(filepath.Join(volumeDir, id+".info")); !os.IsNotExist(err) {
		t.Fatal("staged .info file on the volume dir should have been removed")
	}
}

func TestListUploadsFiltersByOwner(t *testing.T) {
	s := setupTaskStore(t)
	SetTaskStore(s)
	_ = s.Create(&commonUpload.UploadTask{ID: "a", OwnerUserID: "1", Status: commonUpload.UploadStatusUploading})
	_ = s.Create(&commonUpload.UploadTask{ID: "b", OwnerUserID: "2", Status: commonUpload.UploadStatusUploading})

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
		Tasks []commonUpload.UploadTask `json:"tasks"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Tasks) != 1 || body.Tasks[0].ID != "a" {
		t.Fatalf("owner filter failed: %+v", body.Tasks)
	}
}

func TestCancelUploadIdempotentAndCleansStaging(t *testing.T) {
	s := setupTaskStore(t)
	SetTaskStore(s)
	_ = s.Create(&commonUpload.UploadTask{ID: "x", OwnerUserID: "1", Status: commonUpload.UploadStatusUploading})
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
	if got.Status != commonUpload.UploadStatusCanceled {
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
	_ = s.Create(&commonUpload.UploadTask{ID: "z", OwnerUserID: "1", Status: commonUpload.UploadStatusUploading})

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
	if got.Status != commonUpload.UploadStatusUploading {
		t.Fatalf("task status should still be uploading, got %s", got.Status)
	}
}

// TestCancelUploadIDOR 确保 owner="2" 无法取消 owner="1" 的任务。
func TestCancelUploadIDOR(t *testing.T) {
	s := setupTaskStore(t)
	SetTaskStore(s)
	_ = s.Create(&commonUpload.UploadTask{ID: "y", OwnerUserID: "1", Status: commonUpload.UploadStatusUploading})

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
	if got.Status != commonUpload.UploadStatusUploading {
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
