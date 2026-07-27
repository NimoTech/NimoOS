package route

import (
	v1 "github.com/NimoTech/NimoOS/route/v1"
	"github.com/NimoTech/NimoOS/service"
	"github.com/labstack/echo/v4"
)

// registerRootGrantRoutes 把 root-grant 相关路由挂到 nimoosGroup 上:只读的
// search-roots,以及 _internal 下三个写端点(loopback-only 保护)。
//
// 抽成独立函数是为了留一个测试接缝(test seam):InitV1Router 里调用
// service.MyService.RootGrants() 依赖完整初始化的全局 Repository,难以在单测
// 里构造;抽出来后,测试可以传入一个假的 service.RootGrantRepo,走真实的
// echo 路由树 + ServeHTTP 验证 loopbackOnly 确实挂在了 _internal 分组上——
// 而不只是测 loopbackOnly 函数本身或直调 handler 方法。见 rootgrants_wiring_test.go。
func registerRootGrantRoutes(nimoosGroup *echo.Group, repo service.RootGrantRepo) {
	rootGrantHandler := v1.NewRootGrantHandler(repo)
	nimoosGroup.GET("/search-roots", rootGrantHandler.SearchRoots)

	// _internal 写端点只服务于本机的 Wiki 等内部调用方,必须 loopback-only:
	// /v1/nimoos 前缀整体注册到了网关,若只靠上面的 JWT 中间件,任何持有效
	// token 的外部用户都能改写授权表(等同提权),见 route/loopback.go 注释。
	v1InternalGroup := nimoosGroup.Group("/_internal", loopbackOnly)
	{
		v1InternalGroup.PUT("/root-grants/:root_id", rootGrantHandler.UpsertGrant)
		v1InternalGroup.DELETE("/root-grants/:root_id", rootGrantHandler.DeleteGrant)
		v1InternalGroup.POST("/root-grants/reconcile", rootGrantHandler.ReconcileGrants)
	}
}
