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

// ---------- statPrecheck unit tests ----------

func TestStatPrecheck_ExistsMatchingSize(t *testing.T) {
	dir := t.TempDir()
	content := []byte("hello world")
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	exists, sizeMatch, isDir := statPrecheck(dir, "a.txt", int64(len(content)))
	if !exists {
		t.Error("expected exists=true for existing file with matching size")
	}
	if !sizeMatch {
		t.Error("expected size_match=true for existing file with matching size")
	}
	if isDir {
		t.Error("expected is_dir=false for a regular file")
	}
}

// 新语义的核心断言:同名但大小不同 -> exists 仍为 true(同名即冲突,不论大小),
// 只有 size_match 为 false。旧实现下 fileExistsWithSize 会因大小不同直接判定
// 「不存在」(exists=false),这条断言在旧实现下必须失败(红)。
func TestStatPrecheck_SameNameDifferentSize(t *testing.T) {
	dir := t.TempDir()
	content := []byte("hello world")
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	exists, sizeMatch, isDir := statPrecheck(dir, "a.txt", int64(len(content))+1)
	if !exists {
		t.Error("expected exists=true for same-name file with different size (同名即冲突)")
	}
	if sizeMatch {
		t.Error("expected size_match=false for same-name file with different size")
	}
	if isDir {
		t.Error("expected is_dir=false for a regular file")
	}
}

func TestStatPrecheck_NotExists(t *testing.T) {
	dir := t.TempDir()
	exists, sizeMatch, isDir := statPrecheck(dir, "notexist.txt", 100)
	if exists || sizeMatch || isDir {
		t.Errorf("expected all false for non-existent file, got exists=%v size_match=%v is_dir=%v", exists, sizeMatch, isDir)
	}
}

func TestStatPrecheck_IsDirectory(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatal(err)
	}
	exists, sizeMatch, isDir := statPrecheck(dir, "subdir", 0)
	if !exists {
		t.Error("expected exists=true for a same-name directory")
	}
	if sizeMatch {
		t.Error("expected size_match=false for a directory (not a regular file)")
	}
	if !isDir {
		t.Error("expected is_dir=true for a directory")
	}
}

func TestStatPrecheck_RelativePathWithDotDot(t *testing.T) {
	dir := t.TempDir()
	// Create a file outside dir to make sure we don't accidentally stat it.
	parent := filepath.Dir(dir)
	outside := filepath.Join(parent, "outside.txt")
	_ = os.WriteFile(outside, []byte("x"), 0644)
	defer os.Remove(outside)

	exists, sizeMatch, isDir := statPrecheck(dir, "../outside.txt", 1)
	if exists || sizeMatch || isDir {
		t.Errorf("expected all false for relativePath containing '..', got exists=%v size_match=%v is_dir=%v", exists, sizeMatch, isDir)
	}
}

func TestStatPrecheck_AbsoluteRelativePath(t *testing.T) {
	dir := t.TempDir()
	content := []byte("data")
	absPath := filepath.Join(dir, "abs.txt")
	if err := os.WriteFile(absPath, content, 0644); err != nil {
		t.Fatal(err)
	}
	// Pass the absolute path as relativePath — must be rejected.
	exists, sizeMatch, isDir := statPrecheck(dir, absPath, int64(len(content)))
	if exists || sizeMatch || isDir {
		t.Errorf("expected all false for absolute relativePath, got exists=%v size_match=%v is_dir=%v", exists, sizeMatch, isDir)
	}
}

func TestStatPrecheck_PathTraversalEscape(t *testing.T) {
	dir := t.TempDir()
	// "../../etc/passwd" style traversal
	exists, sizeMatch, isDir := statPrecheck(dir, "../../etc/passwd", 0)
	if exists || sizeMatch || isDir {
		t.Errorf("expected all false for path traversal escape, got exists=%v size_match=%v is_dir=%v", exists, sizeMatch, isDir)
	}
}

func TestStatPrecheck_ProtectedNameInRelativePath(t *testing.T) {
	dir := t.TempDir()
	// Create the file so the only rejection reason is the protected name.
	sub := filepath.Join(dir, "AppData")
	_ = os.Mkdir(sub, 0755)
	f := filepath.Join(sub, "x.txt")
	_ = os.WriteFile(f, []byte("hi"), 0644)

	exists, sizeMatch, isDir := statPrecheck(dir, "AppData/x.txt", 2)
	if exists || sizeMatch || isDir {
		t.Errorf("expected all false for relativePath containing protected name 'AppData', got exists=%v size_match=%v is_dir=%v", exists, sizeMatch, isDir)
	}
}

func TestStatPrecheck_NestedFile(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "backup", "2024")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	content := []byte("nested content")
	if err := os.WriteFile(filepath.Join(sub, "file.tar"), content, 0644); err != nil {
		t.Fatal(err)
	}
	exists, sizeMatch, isDir := statPrecheck(dir, "backup/2024/file.tar", int64(len(content)))
	if !exists || !sizeMatch || isDir {
		t.Errorf("expected exists=true size_match=true is_dir=false for nested file with matching size, got exists=%v size_match=%v is_dir=%v", exists, sizeMatch, isDir)
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
	if !resp.Results[0].SizeMatch {
		t.Errorf("expected existing.jpg to have size_match=true")
	}
	if resp.Results[0].IsDir {
		t.Errorf("expected existing.jpg to have is_dir=false")
	}
	if resp.Results[1].Exists {
		t.Errorf("expected missing.jpg to have exists=false")
	}
	if resp.Results[1].SizeMatch {
		t.Errorf("expected missing.jpg to have size_match=false")
	}
	if resp.Results[1].IsDir {
		t.Errorf("expected missing.jpg to have is_dir=false")
	}
}

// 同名但大小不同 -> exists=true/size_match=false 的 HTTP 层断言(新语义的核心场景,
// 冲突弹窗只关心「是否同名」,不关心大小)。
func TestFileUploadPrecheck_Handler_SameNameDifferentSize(t *testing.T) {
	dir := t.TempDir()
	content := []byte("test data for precheck")
	_ = os.WriteFile(filepath.Join(dir, "existing.jpg"), content, 0644)

	reqBody := precheckRequest{
		TargetPath: dir,
		Files: []precheckFileItem{
			{RelativePath: "existing.jpg", Size: int64(len(content)) + 1},
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

	var resp precheckResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Results))
	}
	if !resp.Results[0].Exists {
		t.Errorf("expected exists=true for same-name file with different size")
	}
	if resp.Results[0].SizeMatch {
		t.Errorf("expected size_match=false for same-name file with different size")
	}
}

// 同名但为目录 -> exists=true, is_dir=true 的 HTTP 层断言。
func TestFileUploadPrecheck_Handler_SameNameIsDir(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "existing-folder")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatal(err)
	}

	reqBody := precheckRequest{
		TargetPath: dir,
		Files: []precheckFileItem{
			{RelativePath: "existing-folder", Size: 0},
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

	var resp precheckResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Results))
	}
	if !resp.Results[0].Exists {
		t.Errorf("expected exists=true for same-name directory")
	}
	if !resp.Results[0].IsDir {
		t.Errorf("expected is_dir=true for same-name directory")
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
