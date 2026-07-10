package v2

import (
	"net/http"

	"github.com/NimoTech/NimoOS-Common/model"
	"github.com/NimoTech/NimoOS-Common/pkg/network"
	"github.com/labstack/echo/v4"
)

// GetNetworkInterfaces handles GET /network/interfaces
func (n *NimoOS) GetNetworkInterfaces(c echo.Context) error {
	configs, err := network.GetAllInterfaceConfigs()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": err.Error(),
		})
	}
	return c.JSON(http.StatusOK, configs)
}

// UpdateNetworkInterface handles PUT /network/interfaces
func (n *NimoOS) UpdateNetworkInterface(c echo.Context) error {
	var req model.NetworkInterface
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error": "invalid request body",
		})
	}

	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error": "interface name is required",
		})
	}

	if err := network.WriteInterfaceConfig(req); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": err.Error(),
		})
	}

	// Apply runtime wireless mode switching (iw, wpa_supplicant, virtual AP interfaces)
	if req.Wireless != nil {
		if err := network.ApplyWirelessMode(req.Name, req.Wireless); err != nil {
			c.Logger().Errorf("failed to apply wireless mode for %s: %v", req.Name, err)
		}
	}

	// Apply IP configuration — skip if not needed:
	// - client: wpa_supplicant handles connection, dhclient/udhcpc would interfere.
	// - concurrent: IP is set on the virtual AP interface by ApplyApConfig.
	// - AP: needs static IP, call ApplyInterfaceIP.
	// - Ethernet: only call if IPv4 config actually changed (zone-only changes must
	//   not trigger ip addr flush, which disconnects the current management session).
	if req.Wireless == nil || req.Wireless.Mode == "ap" || req.Wireless.Mode == "manual" {
		needApplyIP := true
		if req.Wireless == nil && req.IPv4 != nil {
			// For Ethernet: skip if config hasn't changed (e.g. zone-only change)
			cur, _ := network.GetInterfaceConfig(req.Name)
			if cur != nil && cur.IPv4 != nil &&
				cur.IPv4.Method == req.IPv4.Method &&
				cur.IPv4.Address == req.IPv4.Address &&
				cur.IPv4.Netmask == req.IPv4.Netmask &&
				cur.IPv4.Gateway == req.IPv4.Gateway {
				needApplyIP = false
			}
		}
		if needApplyIP {
			if err := network.ApplyInterfaceIP(req.Name, req.IPv4); err != nil {
				// Log but don't fail the request
				c.Logger().Errorf("failed to apply IP to %s: %v", req.Name, err)
			}
		}
	}

	// If this is an AP interface, apply the hostapd config
	if req.Wireless != nil && (req.Wireless.Mode == "ap" || req.Wireless.Mode == "concurrent") {
		// Try to apply, ignore error if hostapd not installed
		_ = network.ApplyApConfig(req.Name, *req.Wireless)
	}

	// Apply DHCP and NAT rules based on Zones
	if err := network.ApplyGatewayConfig(); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to apply gateway rules: " + err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]string{
		"message": "success",
	})
}

// GetWifiScanResults handles GET /network/wifi/scan
func (n *NimoOS) GetWifiScanResults(c echo.Context) error {
	iface := c.QueryParam("iface")
	if iface == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error": "iface parameter is required",
		})
	}

	results, err := network.ScanWifi(iface)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, results)
}
