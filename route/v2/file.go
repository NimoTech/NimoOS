package v2

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

// Path: route/v2/file.go

func (s *NimoOS) GetFileTest(ctx echo.Context) error {

	//http.ServeFile(w, r, r.URL.Path[1:])
	http.ServeFile(ctx.Response().Writer, ctx.Request(), "/DATA/test.img")

	return ctx.String(200, "pong")
}

func isProtectedName(name string) bool {
	protected := []string{"AppData", "Documents", "Downloads", "Gallery", "Media"}
	for _, p := range protected {
		if name == p {
			return true
		}
	}
	return false
}

func containsProtectedName(path string) (bool, string) {
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if isProtectedName(part) {
			return true, part
		}
	}
	return false, ""
}
