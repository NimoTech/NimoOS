package route

import (
	v1 "github.com/NimoTech/NimoOS/route/v1"
	"github.com/NimoTech/NimoOS/service"
	"github.com/labstack/echo/v4"
)

// registerRootGrantRoutes mounts the root-grant-related routes onto nimoosGroup:
// the read-only search-roots endpoint, plus the three write endpoints under
// _internal (protected by loopback-only).
//
// Extracted into a standalone function to leave a test seam: InitV1Router calls
// service.MyService.RootGrants(), which depends on a fully-initialized global
// Repository and is hard to construct in a unit test; once extracted, the test
// can pass in a fake service.RootGrantRepo and verify — via the real echo route
// tree + ServeHTTP — that loopbackOnly is actually mounted on the _internal
// group, rather than only testing the loopbackOnly function itself or calling
// the handler methods directly. See rootgrants_wiring_test.go.
func registerRootGrantRoutes(nimoosGroup *echo.Group, repo service.RootGrantRepo) {
	rootGrantHandler := v1.NewRootGrantHandler(repo)
	nimoosGroup.GET("/search-roots", rootGrantHandler.SearchRoots)

	// The _internal write endpoints only serve local callers like Wiki, and must
	// be loopback-only: the /v1/nimoos prefix as a whole is registered with the
	// gateway, so relying only on the JWT middleware above would let any external
	// user with a valid token rewrite the grant table (equivalent to privilege
	// escalation) — see the comment in route/loopback.go.
	v1InternalGroup := nimoosGroup.Group("/_internal", loopbackOnly)
	{
		v1InternalGroup.PUT("/root-grants/:root_id", rootGrantHandler.UpsertGrant)
		v1InternalGroup.DELETE("/root-grants/:root_id", rootGrantHandler.DeleteGrant)
		v1InternalGroup.POST("/root-grants/reconcile", rootGrantHandler.ReconcileGrants)
	}
}
