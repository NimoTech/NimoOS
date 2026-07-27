package route

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestLoopbackOnly_AllowsIPv4Loopback(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	called := false
	h := loopbackOnly(func(c echo.Context) error {
		called = true
		return c.NoContent(http.StatusOK)
	})
	if err := h(c); err != nil {
		t.Fatal(err)
	}
	if !called || rec.Code != http.StatusOK {
		t.Fatalf("127.0.0.1 应放行: called=%v code=%d", called, rec.Code)
	}
}

func TestLoopbackOnly_AllowsIPv6Loopback(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "[::1]:12345"
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	called := false
	h := loopbackOnly(func(c echo.Context) error {
		called = true
		return c.NoContent(http.StatusOK)
	})
	if err := h(c); err != nil {
		t.Fatal(err)
	}
	if !called || rec.Code != http.StatusOK {
		t.Fatalf("::1 应放行: called=%v code=%d", called, rec.Code)
	}
}

func TestLoopbackOnly_RejectsExternal(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.5:54321"
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	called := false
	h := loopbackOnly(func(c echo.Context) error {
		called = true
		return c.NoContent(http.StatusOK)
	})
	err := h(c)
	if called {
		t.Fatal("外部请求不应放行")
	}
	he, ok := err.(*echo.HTTPError)
	if !ok || he.Code != http.StatusForbidden {
		t.Fatalf("期望 403 HTTPError,got %v", err)
	}
}
