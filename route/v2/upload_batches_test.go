package v2

import (
	"bytes"
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

func setupBatchStore(t *testing.T) *upload.BatchStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&upload.UploadBatch{}, &upload.UploadBatchItem{}); err != nil {
		t.Fatal(err)
	}
	return upload.NewBatchStore(db)
}

func doJSON(e *echo.Echo, method, target string, body interface{}, userID string) (*httptest.ResponseRecorder, echo.Context) {
	var reader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("user_id", userID)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	return rec, c
}

func TestUploadBatchLifecycle(t *testing.T) {
	bs := setupBatchStore(t)
	SetBatchStore(bs)
	ts := setupTaskStore(t)
	SetTaskStore(ts)
	dir := stagingDirForTest()
	_ = os.MkdirAll(dir, 0700)

	e := echo.New()

	// 1) create
	createReq := createBatchReq{
		ID:         "b1",
		TargetPath: "/DATA/Media",
		Items: []createBatchItem{
			{RelativePath: "a.jpg", Size: 100},
			{RelativePath: "sub/b.jpg", Size: 200},
		},
	}
	rec, c := doJSON(e, http.MethodPost, "/v2/nimoos/file/upload-batches", createReq, "u1")
	if err := CreateUploadBatch(c); err != nil {
		t.Fatalf("create err: %v", err)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("create code=%d body=%s", rec.Code, rec.Body.String())
	}

	// 2) get by owner u1
	rec, c = doJSON(e, http.MethodGet, "/v2/nimoos/file/upload-batches/b1", nil, "u1")
	c.SetParamNames("id")
	c.SetParamValues("b1")
	if err := GetUploadBatch(c); err != nil {
		t.Fatalf("get err: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("get code=%d body=%s", rec.Code, rec.Body.String())
	}
	var getBody struct {
		Batch   upload.UploadBatch       `json:"batch"`
		Missing []upload.UploadBatchItem `json:"missing"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &getBody); err != nil {
		t.Fatal(err)
	}
	if len(getBody.Missing) != 2 {
		t.Fatalf("missing len=%d", len(getBody.Missing))
	}
	if getBody.Batch.Total != 2 {
		t.Fatalf("batch.total=%d", getBody.Batch.Total)
	}

	// 3) get by non-owner u2 -> 404
	rec, c = doJSON(e, http.MethodGet, "/v2/nimoos/file/upload-batches/b1", nil, "u2")
	c.SetParamNames("id")
	c.SetParamValues("b1")
	err := GetUploadBatch(c)
	if err == nil {
		t.Fatal("expected 404 error for non-owner get")
	}
	he, ok := err.(*echo.HTTPError)
	if !ok || he.Code != http.StatusNotFound {
		t.Fatalf("expected 404 HTTPError, got %v", err)
	}

	// 4) Create an uploading task row for this batch + a fake staging file
	taskID := "t1"
	if err := ts.Create(&commonUpload.UploadTask{
		ID: taskID, OwnerUserID: "u1", BatchID: "b1", Status: commonUpload.UploadStatusUploading,
	}); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, taskID), []byte("d"), 0600)
	_ = os.WriteFile(filepath.Join(dir, taskID+".info"), []byte("{}"), 0600)

	rec, c = doJSON(e, http.MethodPost, "/v2/nimoos/file/upload-batches/b1/interrupt", nil, "u1")
	c.SetParamNames("id")
	c.SetParamValues("b1")
	if err := InterruptUploadBatch(c); err != nil {
		t.Fatalf("interrupt err: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("interrupt code=%d body=%s", rec.Code, rec.Body.String())
	}
	var interruptBody struct {
		Interrupted bool `json:"interrupted"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &interruptBody)
	if !interruptBody.Interrupted {
		t.Fatal("expected interrupted=true on first call")
	}

	b, err := bs.Get("b1")
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != upload.BatchStatusInterrupted {
		t.Fatalf("batch status=%s", b.Status)
	}
	if _, statErr := os.Stat(filepath.Join(dir, taskID)); !os.IsNotExist(statErr) {
		t.Fatal("staging file should be removed after interrupt")
	}
	gotTask, err := ts.Get(taskID)
	if err != nil {
		t.Fatal(err)
	}
	if gotTask.Status != commonUpload.UploadStatusCanceled {
		t.Fatalf("task status=%s", gotTask.Status)
	}

	// 5) interrupt again -> idempotent
	rec, c = doJSON(e, http.MethodPost, "/v2/nimoos/file/upload-batches/b1/interrupt", nil, "u1")
	c.SetParamNames("id")
	c.SetParamValues("b1")
	if err := InterruptUploadBatch(c); err != nil {
		t.Fatalf("second interrupt err: %v", err)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &interruptBody)
	if interruptBody.Interrupted {
		t.Fatal("expected interrupted=false on second call (idempotent)")
	}

	// 6) abandon
	rec, c = doJSON(e, http.MethodPost, "/v2/nimoos/file/upload-batches/b1/abandon", nil, "u1")
	c.SetParamNames("id")
	c.SetParamValues("b1")
	if err := AbandonUploadBatch(c); err != nil {
		t.Fatalf("abandon err: %v", err)
	}
	var abandonBody struct {
		Abandoned bool `json:"abandoned"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &abandonBody)
	if !abandonBody.Abandoned {
		t.Fatal("expected abandoned=true")
	}
	b, err = bs.Get("b1")
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != upload.BatchStatusAbandoned {
		t.Fatalf("batch status after abandon=%s", b.Status)
	}
}

func TestCreateBatchValidation(t *testing.T) {
	bs := setupBatchStore(t)
	SetBatchStore(bs)

	e := echo.New()

	cases := []struct {
		name string
		req  createBatchReq
	}{
		{
			name: "missing id",
			req: createBatchReq{
				TargetPath: "/DATA/Media",
				Items:      []createBatchItem{{RelativePath: "a.jpg", Size: 1}},
			},
		},
		{
			name: "missing targetPath",
			req: createBatchReq{
				ID:    "v1",
				Items: []createBatchItem{{RelativePath: "a.jpg", Size: 1}},
			},
		},
		{
			name: "empty items",
			req: createBatchReq{
				ID:         "v2",
				TargetPath: "/DATA/Media",
				Items:      []createBatchItem{},
			},
		},
		{
			name: "relativePath traversal",
			req: createBatchReq{
				ID:         "v3",
				TargetPath: "/DATA/Media",
				Items:      []createBatchItem{{RelativePath: "../a.jpg", Size: 1}},
			},
		},
		{
			name: "relativePath absolute",
			req: createBatchReq{
				ID:         "v4",
				TargetPath: "/DATA/Media",
				Items:      []createBatchItem{{RelativePath: "/a.jpg", Size: 1}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, c := doJSON(e, http.MethodPost, "/v2/nimoos/file/upload-batches", tc.req, "u1")
			err := CreateUploadBatch(c)
			if err == nil {
				t.Fatalf("expected error, code=%d body=%s", rec.Code, rec.Body.String())
			}
			he, ok := err.(*echo.HTTPError)
			if !ok || he.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 HTTPError, got %v", err)
			}
		})
	}
}
