package route

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/NimoTech/NimoOS/service/model"
)

// wiringFakeRepo is the minimal fake implementation of service.RootGrantRepo,
// used only to verify real route wiring (group nesting + middleware mounting);
// it doesn't care about business logic, and all methods return with no side effects.
type wiringFakeRepo struct{}

func (wiringFakeRepo) EnabledRootIDs() ([]string, error)              { return nil, nil }
func (wiringFakeRepo) EnabledRoots() ([]model.RootGrant, error)       { return nil, nil }
func (wiringFakeRepo) UpsertGrant(string, string, bool, string) error { return nil }
func (wiringFakeRepo) DeleteGrant(string) error                       { return nil }
func (wiringFakeRepo) SeedVirtual(string) error                       { return nil }
func (wiringFakeRepo) ReconcileWiki([]model.RootGrant) error          { return nil }

// TestRootGrantRoutes_InternalWriteEndpoints_LoopbackOnly verifies end-to-end
// that "loopbackOnly really is mounted on the _internal write-endpoint group":
// it sends requests through the real echo route tree + ServeHTTP, rather than
// calling the loopbackOnly function directly or calling the handler methods
// directly. This is the concrete evidence for the privilege-escalation defense
// in this task — if someone later refactors registerRootGrantRoutes and moves a
// handler out of the _internal group, or forgets to mount the middleware, this
// test must fail.
func TestRootGrantRoutes_InternalWriteEndpoints_LoopbackOnly(t *testing.T) {
	e := echo.New()
	nimoosGroup := e.Group("/v1/nimoos")
	registerRootGrantRoutes(nimoosGroup, wiringFakeRepo{})

	body := `{"path":"/a","enabled":true}`

	t.Run("external non-loopback request is rejected with 403", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/v1/nimoos/_internal/root-grants/xxx", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		// Non-loopback address, and no forged headers that echo's RealIP() would
		// prefer to trust (X-Forwarded-For / X-Real-IP), simulating a real
		// external attacker's request.
		req.RemoteAddr = "203.0.113.5:12345"
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("external request should be rejected by loopbackOnly, code=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("loopback request passes through the middleware to the handler", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/v1/nimoos/_internal/root-grants/xxx", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "127.0.0.1:12345"
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("loopback request should be let through to the handler, code=%d body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"ok":true`) {
			t.Fatalf("loopback request should return the handler's normal result, body=%s", rec.Body.String())
		}
	})
}
