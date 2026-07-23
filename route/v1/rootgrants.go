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
