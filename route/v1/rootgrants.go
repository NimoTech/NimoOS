/*
 * @Description: Decoupling of the grant source — read endpoint for the search side
 * to fetch "enabled search roots". Real multi-user ACL is a separate requirement;
 * during the MVP phase all users share the same enabled list. user_id is read but
 * unused (a placeholder, so per-user filtering can be added later without breaking
 * the caller contract).
 */
package v1

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"go.uber.org/zap"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS/service"
	"github.com/NimoTech/NimoOS/service/model"
)

// RootGrantHandler holds RootGrantRepo, used by search-roots and other read-only endpoints.
type RootGrantHandler struct {
	repo service.RootGrantRepo
}

// NewRootGrantHandler constructs a RootGrantHandler.
func NewRootGrantHandler(repo service.RootGrantRepo) *RootGrantHandler {
	return &RootGrantHandler{repo: repo}
}

// SearchRoots returns the root_id list of all currently enabled=true entries.
// The user_id query param is read but currently ignored — the MVP phase does no
// per-user ACL filtering; real multi-user permissions are a separate requirement
// left for a later task.
//
// This is a public read endpoint exposed through the gateway (unlike the other
// three endpoints in this file, which only mount under the loopback-only group as
// _internal write endpoints). Raw DB errors may carry path/SQL fragments or other
// internal detail, which must not leak to external callers — on error we log and
// the response body is fixed to {"error":"internal error"}.
func (h *RootGrantHandler) SearchRoots(c echo.Context) error {
	_ = c.QueryParam("user_id") // MVP: read but ignored, real multi-user ACL is a separate requirement

	grants, err := h.repo.EnabledRoots()
	if err != nil {
		logger.Error("SearchRoots: EnabledRoots failed", zap.Error(err))
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "internal error"})
	}

	// root_ids is the original contract (kept for older Search builds); roots
	// adds the path so Search can scope its filename index by grant path.
	ids := make([]string, 0, len(grants))
	roots := make([]searchRoot, 0, len(grants))
	for _, g := range grants {
		ids = append(ids, g.RootID)
		roots = append(roots, searchRoot{RootID: g.RootID, Path: g.Path})
	}
	return c.JSON(http.StatusOK, searchRootsResponse{RootIDs: ids, Roots: roots})
}

type searchRoot struct {
	RootID string `json:"root_id"`
	Path   string `json:"path"`
}

type searchRootsResponse struct {
	RootIDs []string     `json:"root_ids"`
	Roots   []searchRoot `json:"roots"`
}

// upsertGrantBody is the request body for PUT /_internal/root-grants/:root_id.
type upsertGrantBody struct {
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`
}

// UpsertGrant upserts one grant record incrementally, with source hardcoded to
// "wiki" — this write endpoint only serves the Wiki-side node-tree reconciliation
// scenario and does not accept a caller-supplied source.
// Note: this endpoint only mounts under the loopback-only group (see route/loopback.go),
// because the /v1/nimoos prefix as a whole is registered with the gateway, so JWT
// validation alone isn't enough to prevent external privilege escalation.
func (h *RootGrantHandler) UpsertGrant(c echo.Context) error {
	rootID := c.Param("root_id")

	var body upsertGrantBody
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if err := h.repo.UpsertGrant(rootID, body.Path, body.Enabled, "wiki"); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

// DeleteGrant deletes one grant record. Same as UpsertGrant, only mounted under the loopback-only group.
func (h *RootGrantHandler) DeleteGrant(c echo.Context) error {
	rootID := c.Param("root_id")

	if err := h.repo.DeleteGrant(rootID); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

// reconcileGrantsBody is the request body for POST /_internal/root-grants/reconcile.
type reconcileGrantsBody struct {
	Grants []struct {
		RootID  string `json:"root_id"`
		Path    string `json:"path"`
		Enabled bool   `json:"enabled"`
	} `json:"grants"`
}

// ReconcileGrants fully reconciles source="wiki" grant records against the request
// body (delegates to repo.ReconcileWiki, which hardcodes source on the repo side,
// see service/rootgrants.go). Same as UpsertGrant, only mounted under the loopback-only group.
func (h *RootGrantHandler) ReconcileGrants(c echo.Context) error {
	var body reconcileGrantsBody
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	grants := make([]model.RootGrant, 0, len(body.Grants))
	for _, g := range body.Grants {
		grants = append(grants, model.RootGrant{
			RootID:  g.RootID,
			Path:    g.Path,
			Enabled: g.Enabled,
		})
	}

	if err := h.repo.ReconcileWiki(grants); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]bool{"ok": true})
}
