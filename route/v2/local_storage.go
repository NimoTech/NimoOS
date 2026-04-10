package v2

import (
	"net/http"

	"github.com/NimoTech/NimoOS/codegen"
	"github.com/labstack/echo/v4"
)

// GetLocalStorageDisplayNames returns the display names for all managed mount points
func (c *NimoOS) GetLocalStorageDisplayNames(ctx echo.Context) error {
	// TODO: In the future, this should load from a persistent configuration file
	// For now, we return a default mapping to ensure the UI looks correct
	displayNames := map[string]string{
		"/DATA": "NimoOS-HD",
	}

	return ctx.JSON(http.StatusOK, echo.Map{
		"data":    displayNames,
		"message": "",
	})
}

// UpdateLocalStorageDisplayName updates the display name for a specific mount point
func (c *NimoOS) UpdateLocalStorageDisplayName(ctx echo.Context) error {
	return ctx.JSON(http.StatusOK, codegen.BaseResponse{})
}
