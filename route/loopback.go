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
//
// ⚠️ 安全前提(基于 c.RealIP() 判断,而 RealIP 会信任请求自带的
// X-Forwarded-For/X-Real-IP 头——这本身不是安全边界,它依赖以下三层
// 跨仓库前提共同成立,才能把「RealIP 是回环地址」当作「调用方确实在本机」:
//
//  1. 本服务(NimoOS 核心)只监听 127.0.0.1,不监听 0.0.0.0(main.go 里
//     `net.Listen("tcp", net.JoinHostPort(LOCALHOST, "0"))`,LOCALHOST 硬编码
//     为 "127.0.0.1")。这意味着任何外部网络请求根本无法直接连到本服务,
//     唯一入口是网关反向代理或本机进程直连。
//  2. 网关(NimoOS-Gateway route/gateway_route.go)对路径含 `/_internal/`
//     的请求整体返回 404、不做代理转发——外部请求永远不会被网关转发到这些
//     端点,不管带不带合法 JWT。
//  3. 网关的 rewriteRequestSourceIP(同文件)会先清洗掉外部请求自带的
//     X-Forwarded-For / X-Real-IP(防止攻击者伪造),再由 httputil.ReverseProxy
//     按真实对端 IP 追加一条可信的 X-Forwarded-For,所以即便网关本身把某个
//     内部端点转发出去,c.RealIP() 解析出的也会是外部客户端的真实 IP 而非
//     127.0.0.1/::1,不会误判为回环。
//
// 三者缺一,本中间件就不再是独立防线:例如若把监听地址改成 0.0.0.0,外部
// 请求可以绕过网关直连本服务、自带伪造的 X-Forwarded-For: 127.0.0.1,
// RealIP() 会直接采信从而被 loopbackOnly 放行。改动本服务监听方式或
// NimoOS-Gateway 的上述任一行为前,必须重新评估这里的安全假设。
func loopbackOnly(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		ip := c.RealIP()
		if ip == "::1" || ip == "127.0.0.1" {
			return next(c)
		}
		return echo.NewHTTPError(http.StatusForbidden, "internal endpoint: loopback only")
	}
}
