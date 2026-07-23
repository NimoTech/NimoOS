/*
 * @Description: 授权源解耦——检索侧读取「已启用检索根」的读端点。
 * 真实的多用户 ACL 是独立需求,MVP 阶段所有用户共享同一份 enabled 列表,
 * user_id 只读不用(占位,便于未来接入按用户过滤而不破坏调用方约定)。
 */
package v1

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/NimoTech/NimoOS/service"
	"github.com/NimoTech/NimoOS/service/model"
)

// RootGrantHandler 持有 RootGrantRepo,供 search-roots 等只读端点使用。
type RootGrantHandler struct {
	repo service.RootGrantRepo
}

// NewRootGrantHandler 构造 RootGrantHandler。
func NewRootGrantHandler(repo service.RootGrantRepo) *RootGrantHandler {
	return &RootGrantHandler{repo: repo}
}

// SearchRoots 返回当前所有 enabled=true 的 root_id 列表。
// user_id query 参数当前读了即忽略——MVP 阶段不做按用户的 ACL 过滤,
// 真正的多用户权限是独立需求,留待后续任务。
func (h *RootGrantHandler) SearchRoots(c echo.Context) error {
	_ = c.QueryParam("user_id") // MVP:读了即忽略,真实多用户 ACL 是独立需求

	ids, err := h.repo.EnabledRootIDs()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string][]string{"root_ids": ids})
}

// upsertGrantBody 是 PUT /_internal/root-grants/:root_id 的请求体。
type upsertGrantBody struct {
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`
}

// UpsertGrant 增量 upsert 一条授权记录,source 固定写死为 "wiki"——
// 这个写端点只服务于 Wiki 侧节点树对账场景,不接受调用方自带 source。
// 注意:本端点只挂在 loopback-only 分组下(见 route/loopback.go),
// 因为 /v1/nimoos 前缀整体注册到了网关,单靠 JWT 校验不足以防止外部提权。
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

// DeleteGrant 删除一条授权记录。同 UpsertGrant,只挂在 loopback-only 分组下。
func (h *RootGrantHandler) DeleteGrant(c echo.Context) error {
	rootID := c.Param("root_id")

	if err := h.repo.DeleteGrant(rootID); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

// reconcileGrantsBody 是 POST /_internal/root-grants/reconcile 的请求体。
type reconcileGrantsBody struct {
	Grants []struct {
		RootID  string `json:"root_id"`
		Path    string `json:"path"`
		Enabled bool   `json:"enabled"`
	} `json:"grants"`
}

// ReconcileGrants 以请求体为准全量对账 source="wiki" 的授权记录(转调
// repo.ReconcileWiki,source 由 repo 侧固定写死,详见 service/rootgrants.go)。
// 同 UpsertGrant,只挂在 loopback-only 分组下。
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
