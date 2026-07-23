package route

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// loopbackOnly 是仅放行回环地址(127.0.0.1/::1)请求的中间件。
//
// 背景:/v1/nimoos 前缀整体注册到了网关(main.go),对外统一暴露。若该前缀下
// 的 _internal/root-grants/* 写端点只靠 v1 既有的 JWT 中间件保护,任何持有
// 有效 token 的外部用户都能改写 o_root_grants 授权表——等同于提权。这些端点
// 只应由本机的 Wiki/其它内部服务调用,绝不应经网关暴露给外部用户,哪怕带着
// 合法 token 也不行。
//
// 判断方式沿用本文件同包内 InitV1Router/InitV2Router 的 JWTConfig.Skipper 里
// 已有的回环判断惯例(v1.go / v2.go 中的 `c.RealIP() == "::1" || "127.0.0.1"`),
// 抽成独立中间件复用,而不是发明新的判断口径。
func loopbackOnly(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		ip := c.RealIP()
		if ip == "::1" || ip == "127.0.0.1" {
			return next(c)
		}
		return echo.NewHTTPError(http.StatusForbidden, "internal endpoint: loopback only")
	}
}
