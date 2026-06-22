package v2

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/labstack/echo/v4"
)

// ---------- fileExistsWithSize unit tests ----------

func TestFileExistsWithSize_ExistsMatchingSize(t *testing.T) {
	dir := t.TempDir()
	content := []byte("hello world")
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	if !fileExistsWithSize(dir, "a.txt", int64(len(content))) {
		t.Error("expected true for existing file with matching size")
	}
}

func TestFileExistsWithSize_SizeMismatch(t *testing.T) {
	dir := t.TempDir()
	content := []byte("hello world")
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	if fileExistsWithSize(dir, "a.txt", int64(len(content))+1) {
		t.Error("expected false for existing file with wrong size")
	}
}

func TestFileExistsWithSize_NotExists(t *testing.T) {
	dir := t.TempDir()
	if fileExistsWithSize(dir, "notexist.txt", 100) {
		t.Error("expected false for non-existent file")
	}
}

func TestFileExistsWithSize_IsDirectory(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatal(err)
	}
	// A directory reports a non-zero size on many filesystems, so we pass 0 to
	// ensure it's rejected purely because it is not a regular file.
	if fileExistsWithSize(dir, "subdir", 0) {
		t.Error("expected false for directory")
	}
}

func TestFileExistsWithSize_RelativePathWithDotDot(t *testing.T) {
	dir := t.TempDir()
	// Create a file outside dir to make sure we don't accidentally stat it.
	parent := filepath.Dir(dir)
	outside := filepath.Join(parent, "outside.txt")
	_ = os.WriteFile(outside, []byte("x"), 0644)
	defer os.Remove(outside)

	if fileExistsWithSize(dir, "../outside.txt", 1) {
		t.Error("expected false for relativePath containing '..'")
	}
}

func TestFileExistsWithSize_AbsoluteRelativePath(t *testing.T) {
	dir := t.TempDir()
	content := []byte("data")
	absPath := filepath.Join(dir, "abs.txt")
	if err := os.WriteFile(absPath, content, 0644); err != nil {
		t.Fatal(err)
	}
	// Pass the absolute path as relativePath — must be rejected.
	if fileExistsWithSize(dir, absPath, int64(len(content))) {
		t.Error("expected false for absolute relativePath")
	}
}

func TestFileExistsWithSize_PathTraversalEscape(t *testing.T) {
	dir := t.TempDir()
	// "../../etc/passwd" style traversal
	if fileExistsWithSize(dir, "../../etc/passwd", 0) {
		t.Error("expected false for path traversal escape")
	}
}

func TestFileExistsWithSize_ProtectedNameInRelativePath(t *testing.T) {
	dir := t.TempDir()
	// Create the file so the only rejection reason is the protected name.
	sub := filepath.Join(dir, "AppData")
	_ = os.Mkdir(sub, 0755)
	f := filepath.Join(sub, "x.txt")
	_ = os.WriteFile(f, []byte("hi"), 0644)

	if fileExistsWithSize(dir, "AppData/x.txt", 2) {
		t.Error("expected false for relativePath containing protected name 'AppData'")
	}
}

func TestFileExistsWithSize_NestedFile(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "backup", "2024")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	content := []byte("nested content")
	if err := os.WriteFile(filepath.Join(sub, "file.tar"), content, 0644); err != nil {
		t.Fatal(err)
	}
	if !fileExistsWithSize(dir, "backup/2024/file.tar", int64(len(content))) {
		t.Error("expected true for nested file with matching size")
	}
}

// ---------- HTTP handler integration test ----------

func TestFileUploadPrecheck_Handler(t *testing.T) {
	dir := t.TempDir()
	content := []byte("test data for precheck")
	_ = os.WriteFile(filepath.Join(dir, "existing.jpg"), content, 0644)

	reqBody := precheckRequest{
		TargetPath: dir,
		Files: []precheckFileItem{
			{RelativePath: "existing.jpg", Size: int64(len(content))},
			{RelativePath: "missing.jpg", Size: 999},
		},
	}
	b, _ := json.Marshal(reqBody)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v2/nimoos/file/upload-precheck", bytes.NewReader(b))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := FileUploadPrecheck(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	var resp precheckResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(resp.Results))
	}
	if !resp.Results[0].Exists {
		t.Errorf("expected existing.jpg to have exists=true")
	}
	if resp.Results[1].Exists {
		t.Errorf("expected missing.jpg to have exists=false")
	}
}

func TestFileUploadPrecheck_EmptyTargetPath(t *testing.T) {
	reqBody := precheckRequest{
		TargetPath: "",
		Files:      []precheckFileItem{{RelativePath: "a.jpg", Size: 100}},
	}
	b, _ := json.Marshal(reqBody)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v2/nimoos/file/upload-precheck", bytes.NewReader(b))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := FileUploadPrecheck(c)
	// Echo handler may return HTTPError or write directly; accept either form.
	if err != nil {
		he, ok := err.(*echo.HTTPError)
		if !ok || he.Code != http.StatusBadRequest {
			t.Errorf("expected 400 HTTPError, got %v", err)
		}
	} else if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 status, got %d", rec.Code)
	}
}

// precheck 不再因 targetPath 落在受保护名文件夹(如 AppData/Documents/Downloads)而拒绝:
// 上传「进入」这些用户数据文件夹是正常用途。应返回 200 并给出每个文件的存在性结果。
func TestFileUploadPrecheck_ProtectedTargetPathAllowed(t *testing.T) {
	reqBody := precheckRequest{
		TargetPath: "/DATA/AppData/SomeApp",
		Files:      []precheckFileItem{{RelativePath: "a.jpg", Size: 100}},
	}
	b, _ := json.Marshal(reqBody)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v2/nimoos/file/upload-precheck", bytes.NewReader(b))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := FileUploadPrecheck(c); err != nil {
		t.Fatalf("expected no error (targetPath 不再受保护拦截), got %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp precheckResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Exists {
		t.Fatalf("expected 1 result, not-exists; got %+v", resp.Results)
	}
}
