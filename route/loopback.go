package route

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// loopbackOnly is middleware that only lets through requests from the loopback
// address (127.0.0.1/::1).
//
// Background: the /v1/nimoos prefix as a whole is registered with the gateway
// (main.go), which exposes it uniformly to the outside. If the _internal/root-grants/*
// write endpoints under that prefix were only protected by v1's existing JWT
// middleware, any external user holding a valid token could rewrite the
// o_root_grants grant table — equivalent to privilege escalation. These endpoints
// should only ever be called by the local Wiki/other internal services, and must
// never be reachable by external users through the gateway, even with a legitimate token.
//
// The detection method follows the existing loopback-check convention already
// used in this package's InitV1Router/InitV2Router JWTConfig.Skipper (the
// `c.RealIP() == "::1" || "127.0.0.1"` check in v1.go / v2.go), extracted into a
// reusable standalone middleware rather than inventing a new detection method.
//
// ⚠️ Security precondition (based on c.RealIP(), which trusts the
// X-Forwarded-For/X-Real-IP headers a request carries itself — this is not by
// itself a security boundary; treating "RealIP is loopback" as "the caller is
// really on this machine" depends on the following three cross-repo
// preconditions all holding together:
//
//  1. This service (NimoOS core) only listens on 127.0.0.1, never on 0.0.0.0
//     (in main.go, `net.Listen("tcp", net.JoinHostPort(LOCALHOST, "0"))`, where
//     LOCALHOST is hardcoded to "127.0.0.1"). This means no external network
//     request can connect directly to this service at all — the only entry
//     points are the gateway's reverse proxy or a direct local-process connection.
//  2. The gateway (NimoOS-Gateway route/gateway_route.go) returns a blanket 404
//     for any request path containing `/_internal/` and does not proxy it —
//     external requests are never forwarded by the gateway to these endpoints,
//     regardless of whether they carry a valid JWT.
//  3. The gateway's rewriteRequestSourceIP (same file) first strips any
//     X-Forwarded-For / X-Real-IP an external request carries (to prevent
//     attacker forgery), then httputil.ReverseProxy appends a trustworthy
//     X-Forwarded-For based on the real peer IP — so even if the gateway itself
//     did forward some internal endpoint, what c.RealIP() resolves to would be
//     the external client's real IP, not 127.0.0.1/::1, and would not be
//     misjudged as loopback.
//
// If any of the three is missing, this middleware is no longer an independent
// line of defense: e.g. if the listen address were changed to 0.0.0.0, an
// external request could bypass the gateway and connect directly to this
// service with a forged X-Forwarded-For: 127.0.0.1, which RealIP() would trust
// outright, letting loopbackOnly through. Before changing this service's listen
// behavior or any of the above NimoOS-Gateway behaviors, the security
// assumptions here must be reevaluated.
func loopbackOnly(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		ip := c.RealIP()
		if ip == "::1" || ip == "127.0.0.1" {
			return next(c)
		}
		return echo.NewHTTPError(http.StatusForbidden, "internal endpoint: loopback only")
	}
}
