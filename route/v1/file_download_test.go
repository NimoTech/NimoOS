package v1

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/labstack/echo/v4"
)

func TestMain(m *testing.M) {
	// checkPathAccess logs via the package-level zap logger, which is only
	// wired up by main.init() in the real binary. Initialize a console-only
	// logger here so handler-level tests don't nil-pointer-panic.
	logger.LogInitConsoleOnly()
	os.Exit(m.Run())
}

func TestDownloadZipName(t *testing.T) {
	cases := []struct {
		name string
		list []string
		want string
	}{
		{
			name: "single folder",
			list: []string{"/DATA/photos"},
			want: "photos.zip",
		},
		{
			name: "single folder trailing slash normalized",
			list: []string{"/DATA/photos/"},
			want: "photos.zip",
		},
		{
			name: "multi files same parent dir",
			list: []string{"/DATA/photos/a.jpg", "/DATA/photos/b.jpg", "/DATA/photos/c.jpg"},
			want: "photos.zip",
		},
		{
			name: "multi paths common prefix is root",
			list: []string{"/DATA/photos", "/mnt/other"},
			want: "NimoOS.zip",
		},
		{
			name: "chinese folder name preserved",
			list: []string{"/DATA/照片"},
			want: "照片.zip",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := downloadArchiveName(c.list, ".zip")
			if got != c.want {
				t.Fatalf("downloadArchiveName(%v, \".zip\") = %q, want %q", c.list, got, c.want)
			}
		})
	}
}

// TestGetDownloadFileZipContentDisposition drives GetDownloadFile end-to-end
// through httptest for the multi-file (zip) branch. No auth headers are set,
// so checkPathAccess takes the localhost-bypass path and no service.MyService
// dependency is touched.
func TestGetDownloadFileZipContentDisposition(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "photos")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f1 := filepath.Join(parent, "a.jpg")
	f2 := filepath.Join(parent, "b.jpg")
	if err := os.WriteFile(f1, []byte("aaa"), 0o644); err != nil {
		t.Fatalf("write f1: %v", err)
	}
	if err := os.WriteFile(f2, []byte("bbb"), 0o644); err != nil {
		t.Fatalf("write f2: %v", err)
	}

	files := strings.Join([]string{f1, f2}, ",")
	reqURL := "/v1/file/download?format=zip&files=" + url.QueryEscape(files)
	req := httptest.NewRequest(http.MethodGet, reqURL, nil)
	rec := httptest.NewRecorder()

	e := echo.New()
	ctx := e.NewContext(req, rec)

	if err := GetDownloadFile(ctx); err != nil {
		t.Fatalf("GetDownloadFile returned error: %v", err)
	}

	cd := rec.Header().Get("Content-Disposition")
	if cd == "" {
		t.Fatalf("Content-Disposition header missing on response")
	}
	wantEscaped := url.PathEscape("photos.zip")
	if !strings.Contains(cd, wantEscaped) {
		t.Fatalf("Content-Disposition = %q, want it to contain %q", cd, wantEscaped)
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "application/zip" {
		t.Fatalf("Content-Type = %q, want application/zip", ct)
	}

	if rec.Body.Len() == 0 {
		t.Fatalf("expected non-empty zip body")
	}
}

func TestDownloadArchiveNameFollowsFormatExtension(t *testing.T) {
	if got := downloadArchiveName([]string{"/DATA/photos"}, ".tar.gz"); got != "photos.tar.gz" {
		t.Fatalf("got %q, want photos.tar.gz", got)
	}
	// 空扩展名兜底 .zip(GetCompressionAlgorithm 对 "" 也返回 .zip,双保险)
	if got := downloadArchiveName([]string{"/DATA/photos"}, ""); got != "photos.zip" {
		t.Fatalf("got %q, want photos.zip", got)
	}
}
