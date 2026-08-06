package v1

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS/pkg/utils/common_err"
	"github.com/labstack/echo/v4"
)

func init() {
	// checkPathAccess (route/v1/file.go) logs via the package-global zap logger,
	// which is nil until initialized — required so GetFilePreview tests below
	// (which exercise checkPathAccess) don't panic on a nil logger.
	logger.LogInitConsoleOnly()
}

func TestIsConvertibleOffice(t *testing.T) {
	for _, ext := range []string{".doc", ".DOC", ".wps", ".xls", ".ppt", ".pptx"} {
		if !isConvertibleOffice(ext) {
			t.Errorf("expected %s convertible", ext)
		}
	}
	for _, ext := range []string{".docx", ".xlsx", ".csv", ".pdf", ".txt", ""} {
		if isConvertibleOffice(ext) {
			t.Errorf("expected %s NOT convertible", ext)
		}
	}
}

func TestSofficeArgs(t *testing.T) {
	args := sofficeArgs("/tmp/out/lo-profile", "/tmp/out", "/DATA/a.doc")
	joined := strings.Join(args, " ")
	for _, want := range []string{"--headless", "--convert-to pdf", "--outdir /tmp/out", "/DATA/a.doc", "-env:UserInstallation=file:///tmp/out/lo-profile"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q; got %q", want, joined)
		}
	}
}

func TestGetFilePreview_MissingPath(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/file/preview", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	_ = GetFilePreview(c)
	if rec.Code != common_err.CLIENT_ERROR {
		t.Fatalf("missing path: want %d got %d", common_err.CLIENT_ERROR, rec.Code)
	}
}

func TestGetFilePreview_UnsupportedExt(t *testing.T) {
	e := echo.New()
	// 无 user_id/user_role header → checkPathAccess 视为本地内部调用,放行,进入 ext 校验
	req := httptest.NewRequest(http.MethodGet, "/v1/file/preview?path=/DATA/a.txt", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	_ = GetFilePreview(c)
	if rec.Code != common_err.CLIENT_ERROR {
		t.Fatalf("bad ext: want %d got %d", common_err.CLIENT_ERROR, rec.Code)
	}
}
