package route

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/NimoTech/NimoOS/service/model"
)

// wiringFakeRepo 是最小的 service.RootGrantRepo 假实现,只用于验证真实路由
// wiring(分组嵌套 + 中间件挂载),不关心具体业务逻辑,方法全部无副作用返回。
type wiringFakeRepo struct{}

func (wiringFakeRepo) EnabledRootIDs() ([]string, error)              { return nil, nil }
func (wiringFakeRepo) UpsertGrant(string, string, bool, string) error { return nil }
func (wiringFakeRepo) DeleteGrant(string) error                       { return nil }
func (wiringFakeRepo) SeedVirtual(string) error                       { return nil }
func (wiringFakeRepo) ReconcileWiki([]model.RootGrant) error          { return nil }

// TestRootGrantRoutes_InternalWriteEndpoints_LoopbackOnly 端到端验证
// 「loopbackOnly 确实挂在了 _internal 写端点分组上」:走真实的 echo 路由树 +
// ServeHTTP 发请求,而不是直接调用 loopbackOnly 函数本身、也不是直调 handler
// 方法。这是本任务防提权安全点的落地证据——如果日后有人重构 registerRootGrantRoutes
// 把 handler 挪出 _internal 分组、或忘记挂中间件,这条测试必须挂红。
func TestRootGrantRoutes_InternalWriteEndpoints_LoopbackOnly(t *testing.T) {
	e := echo.New()
	nimoosGroup := e.Group("/v1/nimoos")
	registerRootGrantRoutes(nimoosGroup, wiringFakeRepo{})

	body := `{"path":"/a","enabled":true}`

	t.Run("外部非回环请求被 403 拒绝", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/v1/nimoos/_internal/root-grants/xxx", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		// 非回环地址,且不带任何会被 echo RealIP() 优先采信的伪造头
		// (X-Forwarded-For / X-Real-IP),模拟真实的外部攻击者请求。
		req.RemoteAddr = "203.0.113.5:12345"
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("外部请求应被 loopbackOnly 拒绝,code=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("回环请求能穿过中间件到达handler", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/v1/nimoos/_internal/root-grants/xxx", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "127.0.0.1:12345"
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("回环请求应放行并到达 handler,code=%d body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"ok":true`) {
			t.Fatalf("回环请求应正常返回 handler 结果,body=%s", rec.Body.String())
		}
	})
}
